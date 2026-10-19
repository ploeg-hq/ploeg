package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const connectionReadinessEvery = time.Hour

func connections(cfg *config.File, forges map[string]provider.ForgeProvider, defaultForge string) *httpapi.Connections {
	c := &httpapi.Connections{}
	gated := map[string]work.Target{}
	keys := make([]string, 0, len(cfg.Targets))
	for key := range cfg.Targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t := connectionTarget(cfg.Targets[key].Repo, cfg.Targets[key].Branch, cfg.Targets[key].Forge, defaultForge)
		c.Repositories = append(c.Repositories, httpapi.ConnectionRepository{Key: key, Label: key, Target: t})
		gated[key] = t
	}
	for _, tr := range []struct {
		name     string
		projects []config.Project
	}{{"vikunja", cfg.Trackers.Vikunja.Projects}, {"clickup", cfg.Trackers.Clickup.Projects}, {"github", cfg.Trackers.Github.Projects}} {
		for _, p := range tr.projects {
			board := httpapi.ConnectionBoard{Tracker: tr.name, Name: p.Name, ID: p.ID, Team: p.Team, Default: p.Default, Allow: p.Allow}
			if tr.name == "github" && board.ID == "" {
				board.ID = config.GitHubScope(p.Name)
			}
			if p.Repo != "" {
				board.Repository = repositoryKey(c, gated, connectionTarget(p.Repo, p.Branch, p.Forge, defaultForge))
			}
			c.Boards = append(c.Boards, board)
		}
	}
	c.Readiness = target.NewGate(gated, repositoryInspectors(forges))
	return c
}

func connectionTarget(repo, branch, forge, defaultForge string) work.Target {
	owner, name, _ := strings.Cut(repo, "/")
	if forge == "" {
		forge = defaultForge
	}
	return work.Target{Forge: forge, Owner: owner, Repo: name, BaseBranch: branch}
}

func repositoryKey(c *httpapi.Connections, gated map[string]work.Target, t work.Target) string {
	for _, repo := range c.Repositories {
		if repo.Target == t {
			return repo.Key
		}
	}
	key := t.Owner + "/" + t.Repo
	if t.BaseBranch != "" {
		key += "@" + t.BaseBranch
	}
	c.Repositories = append(c.Repositories, httpapi.ConnectionRepository{Key: key, Target: t})
	gated[key] = t
	return key
}

func refreshConnections(ctx context.Context, c *httpapi.Connections) {
	c.RefreshReadiness(ctx)
	t := time.NewTicker(connectionReadinessEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.RefreshReadiness(ctx)
		}
	}
}
