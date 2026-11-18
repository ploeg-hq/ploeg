package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
)

// trackerWebhookEndpoint is where a webhook Ploeg registers for a tracker
// kind delivers: the tracker's own webhook URL setting, else the public
// webhook base and the tracker's path.
func trackerWebhookEndpoint(kind string) string {
	switch kind {
	case "clickup":
		if url := clickupWebhookSettings().endpoint; url != "" {
			return url
		}
		if base, err := publicWebhookBase(); err == nil && base != "" {
			return base + clickup.WebhookPath
		}
	case "vikunja":
		return strings.TrimSpace(os.Getenv("PLOEG_VIKUNJA_WEBHOOK_URL"))
	}
	return ""
}

// webhookSink keeps a registered webhook's signing secret in the secret
// store, under webhooks/<kind>/<id>, or is nil without a store.
func bindingWebhookSink(secrets secretstore.Store, kind string) provider.WebhookSecretSink {
	if !secretstore.Configured(secrets) {
		return nil
	}
	return secretstore.WebhookSecrets{Store: secrets, Tracker: kind}
}

// staticTrackerConnections are the configured trackers as the connections a
// runtime binding may name, by tracker kind until ADR-0183 step 6 gives them
// ids of their own. With a secret store, Ploeg registers a missing List
// webhook itself and keeps its secret there.
func staticTrackerConnections(cfg *config.File, trackers map[string]provider.TrackerProvider, secrets secretstore.Store) trackerbinding.StaticConnections {
	out := trackerbinding.StaticConnections{}
	for name, tp := range trackers {
		c := trackerbinding.Connection{ID: name, Kind: name, State: trackerbinding.ConnectionActive, Tracker: tp,
			WebhookEndpoint: trackerWebhookEndpoint(name), Secrets: bindingWebhookSink(secrets, name)}
		c.RegisterWebhooks = c.Secrets != nil && c.WebhookEndpoint != ""
		if name == "clickup" {
			c.Agents = cfg.Trackers.Clickup.Agents
		}
		out[name] = c
	}
	return out
}

// runtimeTrackerConnections are the store-backed tracker connections of
// ADR-0195, looked up by id, so "*" and "*:<kind>" in a Team's
// trackerBindings can match them.
type runtimeTrackerConnections struct {
	svc     *connection.Service
	st      *store.Store
	secrets secretstore.Store
}

func (r runtimeTrackerConnections) TrackerConnection(ctx context.Context, id string) (trackerbinding.Connection, bool) {
	if r.svc == nil || r.st == nil {
		return trackerbinding.Connection{}, false
	}
	tenant := r.svc.Tenant
	if tenant == "" {
		tenant = connection.DefaultTenant
	}
	row, err := r.st.Connection(ctx, tenant, id)
	if err != nil || row.RemovedAt != nil || row.Port != connection.PortTracker {
		return trackerbinding.Connection{}, false
	}
	c := trackerbinding.Connection{ID: row.ID, Kind: row.Kind, Runtime: true, State: row.State, HandOverAgent: row.HandOverAgent,
		WebhookEndpoint: trackerWebhookEndpoint(row.Kind), Secrets: bindingWebhookSink(r.secrets, row.Kind)}
	c.RegisterWebhooks = c.Secrets != nil && c.WebhookEndpoint != ""
	if row.State != connection.StateActive {
		return c, true
	}
	cred, err := r.svc.Credential(ctx, id)
	if err != nil {
		return c, true
	}
	if row.Kind == "clickup" {
		var account provider.ConnectionAccount
		if json.Unmarshal(row.Account, &account) == nil && row.HandOverAgent && account.Login != "" {
			c.Agents = []string{account.Login}
		}
		c.Tracker = &clickup.Provider{Token: cred.Token, BaseURL: strings.TrimRight(r.svc.Instances[row.Instance].APIURL, "/")}
	}
	return c, true
}

// bindingUsers lists the live bindings that name a connection, so a
// connection is not removed under them (ADR-0195 section 8).
func bindingUsers(st *store.Store) func(ctx context.Context, id string) ([]connection.Use, error) {
	return func(ctx context.Context, id string) ([]connection.Use, error) {
		live, err := st.LiveTrackerBindings(ctx)
		if err != nil {
			return nil, err
		}
		uses := []connection.Use{}
		for _, b := range live {
			if b.Connection == id {
				uses = append(uses, connection.Use{Kind: "tracker_binding", ID: strconv.FormatInt(b.ID, 10), State: b.State})
			}
		}
		return uses, nil
	}
}

// trackerBindings builds the registry that merges runtime bindings under the
// static rules and loads it once. A store failure leaves the static rules in
// force and is retried by the poll.
func trackerBindings(ctx context.Context, log *slog.Logger, st *store.Store, cfg *config.File, static target.Table,
	resolver *target.MapResolver, readiness *target.Gate, trackers map[string]provider.TrackerProvider, forgeID string,
	secrets secretstore.Store, svc *connection.Service) *trackerbinding.Registry {
	connections := trackerbinding.Chain{staticTrackerConnections(cfg, trackers, secrets), runtimeTrackerConnections{svc: svc, st: st, secrets: secrets}}
	reg := &trackerbinding.Registry{Store: st, Config: cfg, Static: static, DefaultForge: forgeID,
		Connections: connections, Readiness: readiness, Live: target.NewLive(resolver), Log: log}
	if cu, ok := trackers["clickup"].(*clickup.Provider); ok {
		cu.ScopeStatuses = reg.ScopeStatuses("clickup")
		if sink, ok := bindingWebhookSink(secrets, "clickup").(secretstore.WebhookSecrets); ok {
			cu.WebhookSecret = func(ctx context.Context, webhookID string) (string, bool) {
				secret, err := sink.WebhookSecret(ctx, webhookID)
				return secret, err == nil
			}
		}
	}
	if svc != nil {
		svc.Users = bindingUsers(st)
	}
	if err := reg.Rebuild(ctx); err != nil {
		log.Warn("runtime tracker bindings not loaded; the static routing rules alone are in force until the next poll", "err", err)
	}
	return reg
}
