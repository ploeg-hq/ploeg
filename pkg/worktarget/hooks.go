package worktarget

import (
	"context"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// HookURL is the URL of the repository webhook a runtime target's
// connection keeps (ADR-0203): the forge route of its kind, naming the
// connection whose secret proves each delivery.
func HookURL(base, kind, connection string) string {
	return strings.TrimRight(base, "/") + "/webhooks/forge/" + kind + "?connection=" + url.QueryEscape(connection)
}

func (s *Service) hookTarget(ctx context.Context, connection string) (provider.RepositoryHooks, provider.ConnectionInstance, provider.ConnectionCredential, ConnectionInfo, bool) {
	if s.Connections == nil || s.PublicWebhookBase == "" {
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, ConnectionInfo{}, false
	}
	info, found, err := s.Connections.ForgeConnection(ctx, connection)
	if err != nil || !found || !info.Runtime {
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, false
	}
	hooks := s.Connectors[info.Kind].Hooks
	if hooks == nil {
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, false
	}
	cred, err := s.Connections.Credential(ctx, connection)
	if err != nil {
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, false
	}
	inst, ok := s.Instances[info.Instance]
	if !ok {
		inst = provider.ConnectionInstance{ID: info.Instance, Kind: info.Kind}
	}
	return hooks, inst, cred, info, true
}

// ensureHook registers the target's repository webhook before the target is
// stored, so a target whose events would never arrive is refused instead.
func (s *Service) ensureHook(ctx context.Context, t store.WorkTarget) *Error {
	info, found, err := s.Connections.ForgeConnection(ctx, t.Connection)
	if err != nil || !found || s.Connectors[info.Kind].Hooks == nil {
		return nil
	}
	hooks, inst, cred, info, ok := s.hookTarget(ctx, t.Connection)
	if !ok {
		return refuse(422, "webhook_failed", "Ploeg could not read the connection to register the repository webhook.")
	}
	secrets, ok := s.Connections.(HookSecrets)
	if !ok {
		return refuse(422, "webhook_failed", "This installation keeps no webhook secrets, so the repository webhook cannot be registered.")
	}
	secret, held, err := secrets.WebhookSecret(ctx, t.Connection)
	if err != nil || !held {
		return refuse(422, "webhook_failed", "The connection holds no webhook secret; reconnect it to get one.")
	}
	if err := hooks.EnsureRepositoryHook(ctx, inst, cred, t.RepositoryID, provider.RepositoryHook{URL: HookURL(s.PublicWebhookBase, info.Kind, t.Connection), Secret: secret}); err != nil {
		s.log().Warn("repository webhook not registered", "connection", t.Connection, "repository", t.Repository, "err", err)
		return refuse(422, "webhook_failed", "The forge refused the repository webhook for "+t.Repository+"; the connection must be a Maintainer of it.")
	}
	return nil
}

// removeHook deletes the target's repository webhook and returns, when it
// could not, the URL of the hook left at the forge.
func (s *Service) removeHook(ctx context.Context, t store.WorkTarget) string {
	hooks, inst, cred, info, ok := s.hookTarget(ctx, t.Connection)
	if !ok {
		return ""
	}
	hookURL := HookURL(s.PublicWebhookBase, info.Kind, t.Connection)
	if err := hooks.RemoveRepositoryHook(ctx, inst, cred, t.RepositoryID, hookURL); err != nil {
		s.log().Warn("repository webhook left at the forge", "connection", t.Connection, "repository", t.Repository, "err", err)
		return hookURL
	}
	return ""
}
