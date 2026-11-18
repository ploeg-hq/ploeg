package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
)

// staticTrackerConnections are the configured trackers as the connections a
// runtime binding may name, by tracker kind until ADR-0183 step 6 gives them
// ids of their own.
func staticTrackerConnections(cfg *config.File, trackers map[string]provider.TrackerProvider) trackerbinding.StaticConnections {
	out := trackerbinding.StaticConnections{}
	for name, tp := range trackers {
		c := trackerbinding.Connection{ID: name, Kind: name, State: trackerbinding.ConnectionActive, Tracker: tp}
		switch name {
		case "clickup":
			settings := clickupWebhookSettings()
			c.Agents, c.WebhookEndpoint, c.RegisterWebhooks = cfg.Trackers.Clickup.Agents, settings.endpoint, settings.register
		case "vikunja":
			c.WebhookEndpoint = strings.TrimSpace(os.Getenv("PLOEG_VIKUNJA_WEBHOOK_URL"))
		}
		out[name] = c
	}
	return out
}

// trackerBindings builds the registry that merges runtime bindings under the
// static rules and loads it once. A store failure leaves the static rules in
// force and is retried by the poll.
func trackerBindings(ctx context.Context, log *slog.Logger, st *store.Store, cfg *config.File, static target.Table,
	resolver *target.MapResolver, readiness *target.Gate, trackers map[string]provider.TrackerProvider, forgeID string) *trackerbinding.Registry {
	reg := &trackerbinding.Registry{Store: st, Config: cfg, Static: static, DefaultForge: forgeID,
		Connections: staticTrackerConnections(cfg, trackers), Readiness: readiness, Live: target.NewLive(resolver), Log: log}
	if cu, ok := trackers["clickup"].(*clickup.Provider); ok {
		cu.ScopeStatuses = reg.ScopeStatuses("clickup")
	}
	if err := reg.Rebuild(ctx); err != nil {
		log.Warn("runtime tracker bindings not loaded; the static routing rules alone are in force until the next poll", "err", err)
	}
	return reg
}
