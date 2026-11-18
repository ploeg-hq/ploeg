package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Held-App index timing: the index of App ids Ploeg holds is reloaded when
// older than heldAppsMaxAge, and a miss reloads it at most once per
// heldAppsMissReload, so a delivery naming an unknown App costs no query.
const (
	heldAppsMaxAge     = 30 * time.Second
	heldAppsMissReload = 5 * time.Second
)

// AppWebhookSecret returns the webhook secret of an App Ploeg holds, by
// GitHub's App id, read once from the store and held in memory. held is
// false for an App Ploeg does not hold. An error means Ploeg cannot tell or
// cannot read the secret; the delivery must then be refused, never verified
// with another secret.
func (s *Service) AppWebhookSecret(ctx context.Context, appID string) (secret string, held bool, err error) {
	if !numericID(appID) {
		return "", false, nil
	}
	s.mu.Lock()
	if secret, ok := s.appSecrets[appID]; ok {
		s.mu.Unlock()
		return secret, true, nil
	}
	s.mu.Unlock()
	if held, err := s.holdsApp(ctx, appID); err != nil || !held {
		return "", false, err
	}
	app, err := s.Store.LiveConnectionAppByAppID(ctx, appID)
	if errors.Is(err, store.ErrConnectionNotFound) {
		s.forgetAppSecret(appID)
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("read App %s: %w", appID, err)
	}
	var refs appSecrets
	if json.Unmarshal(app.Secrets, &refs) != nil || refs.App == nil {
		return "", true, fmt.Errorf("App %s has no secret reference", appID)
	}
	values, err := s.Secrets.Get(ctx, *refs.App)
	if err != nil {
		return "", true, fmt.Errorf("read the webhook secret of App %s: %w", appID, err)
	}
	if values["webhook_secret"] == "" {
		return "", true, fmt.Errorf("App %s has no webhook secret in the store", appID)
	}
	s.mu.Lock()
	if s.appSecrets == nil {
		s.appSecrets = map[string]string{}
	}
	s.appSecrets[appID] = values["webhook_secret"]
	s.mu.Unlock()
	return values["webhook_secret"], true, nil
}

func (s *Service) holdsApp(ctx context.Context, appID string) (bool, error) {
	now := s.now()
	s.mu.Lock()
	known, fresh := s.heldApps[appID], s.heldApps != nil && now.Sub(s.heldAt) < heldAppsMaxAge
	retry := !known && now.Sub(s.heldAt) >= heldAppsMissReload
	s.mu.Unlock()
	if fresh && !retry {
		return known, nil
	}
	ids, err := s.Store.LiveConnectionAppIDs(ctx)
	if err != nil {
		return false, fmt.Errorf("list the Apps Ploeg holds: %w", err)
	}
	held := make(map[string]bool, len(ids))
	for _, id := range ids {
		held[id] = true
	}
	s.mu.Lock()
	s.heldApps, s.heldAt = held, now
	s.mu.Unlock()
	return held[appID], nil
}

func numericID(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Service) forgetAppSecret(appID string) {
	s.mu.Lock()
	delete(s.appSecrets, appID)
	s.heldApps = nil
	s.mu.Unlock()
}

// HandleGrant applies a signed installation delivery to the connections of
// that installation (ADR-0195 section 5, ADR-0193 section 7): deleted and
// suspend make them invalid, created and unsuspend probe them back.
// installation_repositories changes no connection; it is logged for the
// runtime targets that read the grant.
func (s *Service) HandleGrant(ctx context.Context, grant provider.GrantEvent) error {
	if grant.Event == "installation" && grant.Action == "created" {
		if err := s.bindPendingInstallation(ctx, grant); err != nil {
			return err
		}
	}
	rows, err := s.Store.LiveConnectionsByInstallation(ctx, grant.InstallationID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		app, err := s.Store.ConnectionApp(ctx, row.Tenant, row.App)
		if err != nil || app.AppID != grant.AppID {
			continue
		}
		actor := "connection:" + row.ID
		switch {
		case grant.Event == "installation" && grant.Action == "deleted":
			err = s.applyProbe(ctx, row, invalidReport(s.now(), "installation_deleted"), actor, nil)
		case grant.Event == "installation" && grant.Action == "suspend":
			err = s.applyProbe(ctx, row, invalidReport(s.now(), "installation_suspended"), actor, nil)
		case grant.Event == "installation" && (grant.Action == "unsuspend" || grant.Action == "created"):
			err = s.applyProbe(ctx, row, s.probeRow(ctx, row), actor, nil)
		default:
			s.log().Info("installation grant changed", "connection", row.ID, "event", grant.Event, "action", grant.Action,
				"added", len(grant.Added), "removed", len(grant.Removed))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// bindPendingInstallation gives a connection that awaits an organisation
// owner's approval, and so has no installation yet, the installation a
// created delivery signed with its App's own secret reports. The App is
// private, so only its owner's account can install it; an installation on
// any other account binds nothing.
func (s *Service) bindPendingInstallation(ctx context.Context, grant provider.GrantEvent) error {
	if grant.AppID == "" || grant.InstallationID == "" || grant.AccountID == "" {
		return nil
	}
	app, err := s.Store.LiveConnectionAppByAppID(ctx, grant.AppID)
	if errors.Is(err, store.ErrConnectionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var owner provider.ConnectionAccount
	if json.Unmarshal(app.Owner, &owner) != nil || owner.ID == "" || owner.ID != grant.AccountID {
		s.log().Warn("installation created on an account that does not own the App; no pending connection bound",
			"app", app.ID, "installation", grant.InstallationID)
		return nil
	}
	rows, err := s.Store.LiveConnections(ctx, app.Tenant, app.Port)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.App != app.ID || row.InstallationID != "" || row.State != StateAwaitingConsent {
			continue
		}
		if _, err := s.Store.BindPendingInstallation(ctx, row.Tenant, row.ID, grant.InstallationID); err != nil {
			return err
		}
	}
	return nil
}

func invalidReport(at time.Time, reason string) ProbeReport {
	return ProbeReport{At: at.UTC(), Result: "fail", State: StateInvalid, Reason: reason,
		Checks: []Check{{Name: "credential_accepted", Result: "fail", Detail: strings.ReplaceAll(reason, "_", " ")}}}
}

// Reconcile is the hourly pass: it refreshes and probes every store-backed
// connection, deletes the PKCE verifiers of ended flows, and deletes store
// paths under Ploeg's prefix that nothing references and that are older
// than an hour.
func (s *Service) Reconcile(ctx context.Context) {
	if s.Accepting() != nil {
		return
	}
	rows, err := s.Store.LiveConnections(ctx, s.tenant(), "")
	if err != nil {
		s.log().Error("connection reconcile could not list connections", "err", err)
		return
	}
	for _, row := range rows {
		if row.State == StateAwaitingConsent {
			continue
		}
		if err := s.applyProbe(ctx, row, s.probeRow(ctx, row), "connection:"+row.ID, nil); err != nil {
			s.log().Error("connection probe not recorded", "connection", row.ID, "err", err)
		}
	}
	ended, err := s.Store.EndedFlowVerifiers(ctx)
	if err == nil {
		for _, id := range ended {
			_ = s.Secrets.Delete(ctx, "flows/"+id)
		}
	}
	s.sweep(ctx)
}

func (s *Service) sweep(ctx context.Context) {
	referenced, err := s.Store.ReferencedSecretPaths(ctx)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-time.Hour)
	for _, prefix := range []string{"connections/", "apps/", "flows/"} {
		entries, err := s.Secrets.List(ctx, prefix)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !referenced[e.Path] && e.UpdatedAt.Before(cutoff) {
				if err := s.Secrets.Delete(ctx, e.Path); err != nil {
					s.log().Warn("orphan secret not deleted", "path", e.Path, "err", err)
				}
			}
		}
	}
}

// StateCounts counts live connections per kind and state, for
// ploeg_connections{kind,state}.
func (s *Service) StateCounts(ctx context.Context) (map[[2]string]int, error) {
	return s.Store.ConnectionStateCounts(ctx)
}
