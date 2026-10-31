package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
)

type trackerWebhookCheck struct {
	tracker     string
	checker     provider.WebhookChecker
	scopes      []string
	names       map[string]string
	expectedURL string
	register    bool
	secrets     provider.WebhookSecretSink
	coverage    *httpapi.WebhookCoverage
	connections *httpapi.Connections
	log         *slog.Logger
}

// webhookSettings is how a tracker's webhook check is configured: the exact
// endpoint it looks for, empty for any on the tracker's path, and whether it
// registers one where a board has none.
type webhookSettings struct {
	env      string
	endpoint string
	register bool
}

func clickupWebhookSettings() webhookSettings {
	return webhookSettings{env: "PLOEG_CLICKUP_WEBHOOK_", endpoint: strings.TrimSpace(os.Getenv("PLOEG_CLICKUP_WEBHOOK_URL")),
		register: os.Getenv("PLOEG_CLICKUP_WEBHOOK_REGISTER") == "true"}
}

// newTrackerWebhookCheck checks the webhooks of a tracker whose boards carry
// their ids, through the tracker's WebhookChecker capability.
func newTrackerWebhookCheck(tracker string, tp provider.TrackerProvider, boards []config.Project, settings webhookSettings, secrets provider.WebhookSecretSink, log *slog.Logger) (*trackerWebhookCheck, error) {
	prefix, register, expectedURL := settings.env, settings.register, settings.endpoint
	checker, ok := tp.(provider.WebhookChecker)
	if register {
		if _, can := tp.(provider.WebhookRegistrar); !can || secrets == nil {
			return nil, fmt.Errorf("%sREGISTER=true needs a webhook secret store, and none is configured: the tracker returns a new webhook's signing secret only when it is created", prefix)
		}
		if expectedURL == "" {
			return nil, fmt.Errorf("%sREGISTER=true requires %sURL", prefix, prefix)
		}
	}
	if !ok || len(boards) == 0 {
		log.Info("tracker webhook coverage check disabled (tracker not configured, cannot check webhooks, or has no configured boards)", "tracker", tracker)
		return nil, nil
	}
	check := &trackerWebhookCheck{tracker: tracker, checker: checker, names: map[string]string{}, expectedURL: expectedURL,
		register: register, secrets: secrets, coverage: &httpapi.WebhookCoverage{}, log: log}
	for _, b := range boards {
		if b.ID == "" || check.names[b.ID] != "" {
			continue
		}
		check.scopes = append(check.scopes, b.ID)
		check.names[b.ID] = b.Name
	}
	return check, nil
}

func (c *trackerWebhookCheck) loop(ctx context.Context) {
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

func (c *trackerWebhookCheck) run(ctx context.Context) {
	found := c.checker.CheckWebhooks(ctx, c.scopes, c.expectedURL)
	if c.register && hasMissing(found) && c.registerWebhook(ctx) {
		found = c.checker.CheckWebhooks(ctx, c.scopes, c.expectedURL)
	}
	var missing, failed []string
	wiring := map[string]httpapi.BoardWiring{}
	for _, f := range found {
		name := c.names[f.Scope]
		board := httpapi.BoardWiring{ProjectID: f.Scope, Webhook: f.Webhook, Shared: f.Members}
		switch f.Webhook {
		case provider.WebhookUnreadable:
			c.log.Error("tracker webhook check could not read a board; assignments on it may not dispatch", "tracker", c.tracker, "board", name, "board_id", f.Scope, "err", f.Err)
			failed = append(failed, f.Scope)
			board.Webhook = httpapi.BoardWebhookUnreadable
		case provider.WebhookMissing:
			c.log.Warn("tracker board has no webhook delivering assignments to Ploeg; assignments on it will not dispatch", "tracker", c.tracker, "board", name, "board_id", f.Scope)
			missing = append(missing, f.Scope)
			board.Webhook = httpapi.BoardWebhookMissing
		default:
			board.Webhook = httpapi.BoardWebhookPresent
		}
		wiring["#"+f.Scope] = board
	}
	now := time.Now()
	c.coverage.Record(len(found), missing, failed, now)
	if c.connections != nil {
		c.connections.RecordBoards(c.tracker, wiring, now)
	}
}

func hasMissing(found []provider.ScopeWebhook) bool {
	for _, f := range found {
		if f.Webhook == provider.WebhookMissing {
			return true
		}
	}
	return false
}

func (c *trackerWebhookCheck) registerWebhook(ctx context.Context) bool {
	registrar := c.checker.(provider.WebhookRegistrar)
	id, err := registrar.RegisterWebhook(ctx, c.expectedURL, c.secrets)
	if err != nil {
		c.log.Error("tracker webhook registration failed", "tracker", c.tracker, "err", err)
		return false
	}
	c.log.Info("registered tracker webhook", "tracker", c.tracker, "webhook_id", id)
	return true
}
