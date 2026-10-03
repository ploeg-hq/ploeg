package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
)

const webhookCheckEvery = time.Hour

type vikunjaWebhookCheck struct {
	provider    *vikunja.Provider
	projects    []config.Project
	expectedURL string
	register    bool
	coverage    *httpapi.WebhookCoverage
	log         *slog.Logger
}

type checkedProject struct{ id, name string }

func newVikunjaWebhookCheck(vik *vikunja.Provider, projects []config.Project, log *slog.Logger) (*vikunjaWebhookCheck, error) {
	if vik.BaseURL == "" || vik.Token == "" || len(projects) == 0 {
		log.Info("vikunja webhook coverage check disabled (no API credentials or no configured projects)")
		return nil, nil
	}
	check := &vikunjaWebhookCheck{
		provider:    vik,
		projects:    projects,
		expectedURL: strings.TrimSpace(os.Getenv("PLOEG_VIKUNJA_WEBHOOK_URL")),
		register:    os.Getenv("PLOEG_VIKUNJA_WEBHOOK_REGISTER") == "true",
		coverage:    &httpapi.WebhookCoverage{},
		log:         log,
	}
	if check.register && (check.expectedURL == "" || vik.Secret == "") {
		return nil, errors.New("PLOEG_VIKUNJA_WEBHOOK_REGISTER=true requires PLOEG_VIKUNJA_WEBHOOK_URL and PLOEG_VIKUNJA_SECRET")
	}
	return check, nil
}

func (c vikunjaWebhookCheck) loop(ctx context.Context) {
	c.run(ctx)
	t := time.NewTicker(webhookCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.run(ctx)
		}
	}
}

func (c vikunjaWebhookCheck) run(ctx context.Context) {
	projects, unresolved := c.resolve(ctx)
	var missing []string
	failed := unresolved
	for _, p := range projects {
		hooks, err := c.provider.ProjectWebhooks(ctx, p.id)
		if err != nil {
			c.log.Error("tracker webhook check failed; assignments on this project may not dispatch", "project", p.name, "project_id", p.id, "err", err)
			failed = append(failed, p.id)
			continue
		}
		if deliversAssignments(hooks, c.expectedURL) {
			continue
		}
		if c.register && c.registerWebhook(ctx, p) {
			continue
		}
		c.log.Warn("tracker project has no webhook delivering assignments to Ploeg; assignments on it will not dispatch", "project", p.name, "project_id", p.id, "event", vikunja.AssignmentEvent)
		missing = append(missing, p.id)
	}
	c.coverage.Record(len(projects), missing, failed, time.Now())
}

func (c vikunjaWebhookCheck) registerWebhook(ctx context.Context, p checkedProject) bool {
	if err := c.provider.RegisterAssignmentWebhook(ctx, p.id, c.expectedURL); err != nil {
		c.log.Error("tracker webhook registration failed", "project", p.name, "project_id", p.id, "err", err)
		return false
	}
	c.log.Info("registered tracker webhook", "project", p.name, "project_id", p.id)
	return true
}

func deliversAssignments(hooks []vikunja.Webhook, expectedURL string) bool {
	for _, h := range hooks {
		if vikunja.DeliversAssignments(h, expectedURL) {
			return true
		}
	}
	return false
}

func (c vikunjaWebhookCheck) resolve(ctx context.Context) ([]checkedProject, []string) {
	var byName map[string]string
	var lookupErr error
	for _, p := range c.projects {
		if p.ID == "" && byName == nil && lookupErr == nil {
			byName, lookupErr = c.provider.ProjectsByName(ctx)
			if lookupErr != nil {
				c.log.Error("tracker webhook check could not resolve project names", "err", lookupErr)
			}
		}
	}
	seen := map[string]bool{}
	var out []checkedProject
	var unresolved []string
	for _, p := range c.projects {
		id := p.ID
		if id == "" {
			id = byName[p.Name]
		}
		if id == "" {
			unresolved = append(unresolved, p.Name)
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, checkedProject{id: id, name: p.Name})
	}
	return out, unresolved
}
