package main

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// connectors is the adapter table for runtime connections (ADR-0195): the
// kinds that can be connected at runtime and what each offers.
func connectors() map[string]provider.Connector {
	return map[string]provider.Connector{
		"clickup": {Authorizer: clickup.Connector{}, Prober: clickup.Connector{}, HandOverAgent: true},
		"gitlab":  {Authorizer: gitlab.Connector{}, Prober: gitlab.Connector{}, Repos: gitlab.Connector{}, Grants: gitlab.Connector{}},
		"forgejo": {Authorizer: forgejo.Connector{}, Prober: forgejo.Connector{}, Repos: forgejo.Connector{}, Grants: forgejo.Connector{}},
		"github":  {Prober: github.Connector{}, Manifest: github.Connector{}, Repos: github.Connector{}, Grants: github.Connector{}},
	}
}

func publicWebhookBase() (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(os.Getenv("PLOEG_PUBLIC_WEBHOOK_BASE")), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("PLOEG_PUBLIC_WEBHOOK_BASE must be an https URL without query or fragment")
	}
	return raw, nil
}

// runtimeConnections builds the connection service from the configuration
// file's connections section; nil when the section is absent.
func runtimeConnections(log *slog.Logger, cfg *config.File, st *store.Store, secrets secretstore.Store,
	trackers map[string]provider.TrackerProvider, forges map[string]provider.ForgeProvider) (*connection.Service, error) {
	if cfg.Connections == nil {
		return nil, nil
	}
	instances, err := cfg.Connections.Resolve(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	base, err := publicWebhookBase()
	if err != nil {
		return nil, err
	}
	var static []connection.Static
	for _, name := range slices.Sorted(mapKeys(trackers)) {
		static = append(static, connection.Static{ID: name, Port: connection.PortTracker, Kind: trackers[name].Name(), Instance: name})
	}
	for _, name := range slices.Sorted(mapKeys(forges)) {
		static = append(static, connection.Static{ID: name, Port: connection.PortForge, Kind: forges[name].Name(), Instance: name})
	}
	if cfg.Connections.Enabled && !secretstore.Configured(secrets) {
		log.Warn("connections.enabled is set but no secret store is configured; every connection change answers secret_store_unavailable")
	}
	svc := &connection.Service{Store: st, Secrets: secrets, Config: cfg.Connections, Instances: instances, Connectors: connectors(),
		Static: static, PublicWebhookBase: base, Log: log}
	log.Info("runtime connections configured", "enabled", cfg.Connections.Enabled, "allowed", len(cfg.Connections.Allow))
	return svc, nil
}

// appWebhooks gives every route a runtime App's webhook can reach the App's
// own secret (ADR-0195): the static GitHub forge, when one is configured,
// and a receiver for /webhooks/tracker/github and for /webhooks/forge/github
// without one. Nil when runtime connections are off.
func appWebhooks(log *slog.Logger, svc *connection.Service, forges map[string]provider.ForgeProvider) map[string]httpapi.AppWebhookReceiver {
	if svc == nil {
		return nil
	}
	for _, fp := range forges {
		if gh, ok := fp.(*github.Provider); ok {
			gh.AppSecret = svc.AppWebhookSecret
		}
	}
	return map[string]httpapi.AppWebhookReceiver{"github": &github.Provider{AppSecret: svc.AppWebhookSecret, Log: log}}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func registeredRepository(cfg *config.File, defaultForge string) func(forge, repo string) (string, bool) {
	return func(forge, repo string) (string, bool) {
		for _, key := range slices.Sorted(mapKeys(cfg.Targets)) {
			t := cfg.Targets[key]
			f := t.Forge
			if f == "" {
				f = defaultForge
			}
			if f == forge && strings.EqualFold(t.Repo, repo) {
				return key, true
			}
		}
		return "", false
	}
}

func connectionReconcileLoop(ctx context.Context, svc *connection.Service, every time.Duration) {
	if svc == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			svc.Reconcile(ctx)
		}
	}
}
