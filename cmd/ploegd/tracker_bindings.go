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
		if cu, ok := tp.(*clickup.Provider); ok && cu.ConnectionsOnly {
			continue
		}
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
		c.Tracker = &clickup.Provider{Token: cred.Token, BaseURL: clickup.ConnectionBaseURL(r.svc.Instances[row.Instance])}
	}
	return c, true
}

// clickupConnections are the store-backed ClickUp connections the static
// ClickUp provider hands intake and write-backs to (ADR-0195 section 9):
// whose own account is a hand-over agent, which one a List is bound
// through, and which List a held task is in.
type clickupConnections struct {
	runtime runtimeTrackerConnections
	reg     *trackerbinding.Registry
}

func (c clickupConnections) tenant() string {
	if c.runtime.svc.Tenant != "" {
		return c.runtime.svc.Tenant
	}
	return connection.DefaultTenant
}

func (c clickupConnections) connection(ctx context.Context, row store.ConnectionRow) (clickup.Connection, bool) {
	if row.RemovedAt != nil || row.Port != connection.PortTracker || row.Kind != "clickup" || row.State != connection.StateActive {
		return clickup.Connection{}, false
	}
	cred, err := c.runtime.svc.Credential(ctx, row.ID)
	if err != nil || cred.Token == "" {
		return clickup.Connection{}, false
	}
	out := clickup.Connection{ID: row.ID, Token: cred.Token, BaseURL: clickup.ConnectionBaseURL(c.runtime.svc.Instances[row.Instance]),
		ScopeStatuses: c.reg.ScopeStatuses(row.ID)}
	if row.HandOverAgent {
		out.Agent = row.AccountID
	}
	return out, true
}

func (c clickupConnections) HandOverAgents(ctx context.Context, userID string) []clickup.Connection {
	rows, err := c.runtime.st.LiveConnections(ctx, c.tenant(), connection.PortTracker)
	if err != nil {
		return nil
	}
	var out []clickup.Connection
	for _, row := range rows {
		if !row.HandOverAgent || row.AccountID != userID {
			continue
		}
		if conn, ok := c.connection(ctx, row); ok {
			out = append(out, conn)
		}
	}
	return out
}

func (c clickupConnections) BoundThrough(ctx context.Context, listID string) (clickup.Connection, bool) {
	id, ok := c.reg.RoutedConnection(listID)
	if !ok {
		return clickup.Connection{}, false
	}
	row, err := c.runtime.st.Connection(ctx, c.tenant(), id)
	if err != nil {
		return clickup.Connection{}, false
	}
	return c.connection(ctx, row)
}

func (c clickupConnections) TaskList(ctx context.Context, taskID string) (string, bool) {
	_, item, err := c.runtime.st.TrackerWorkItemID(ctx, "clickup", taskID)
	if err != nil || item.ExternalScope == "" {
		return "", false
	}
	return item.ExternalScope, true
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
	runtime := runtimeTrackerConnections{svc: svc, st: st, secrets: secrets}
	connections := trackerbinding.Chain{staticTrackerConnections(cfg, trackers, secrets), runtime}
	reg := &trackerbinding.Registry{Store: st, Config: cfg, Static: static, DefaultForge: forgeID,
		Connections: connections, Readiness: readiness, Live: target.NewLive(resolver), Log: log}
	if cu, ok := trackers["clickup"].(*clickup.Provider); ok {
		cu.ScopeStatuses = reg.ScopeStatuses("clickup")
		if svc != nil && st != nil {
			cu.Connections = clickupConnections{runtime: runtime, reg: reg}
		}
		if sink, ok := bindingWebhookSink(secrets, "clickup").(secretstore.WebhookSecrets); ok {
			cu.WebhookSecret = func(ctx context.Context, webhookID string) (string, bool) {
				if !reg.RegisteredWebhook(webhookID) {
					return "", false
				}
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
