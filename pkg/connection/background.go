package connection

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// AppWebhookSecret returns the webhook secret of an App Ploeg holds, by
// GitHub's App id, read once from the store and held in memory.
func (s *Service) AppWebhookSecret(ctx context.Context, appID string) (string, bool) {
	s.mu.Lock()
	if secret, ok := s.appSecrets[appID]; ok {
		s.mu.Unlock()
		return secret, true
	}
	s.mu.Unlock()
	app, err := s.Store.LiveConnectionAppByAppID(ctx, appID)
	if err != nil {
		return "", false
	}
	var refs appSecrets
	if json.Unmarshal(app.Secrets, &refs) != nil || refs.App == nil {
		return "", false
	}
	values, err := s.Secrets.Get(ctx, *refs.App)
	if err != nil || values["webhook_secret"] == "" {
		return "", false
	}
	s.mu.Lock()
	if s.appSecrets == nil {
		s.appSecrets = map[string]string{}
	}
	s.appSecrets[appID] = values["webhook_secret"]
	s.mu.Unlock()
	return values["webhook_secret"], true
}

func (s *Service) forgetAppSecret(appID string) {
	s.mu.Lock()
	delete(s.appSecrets, appID)
	s.mu.Unlock()
}

// HandleGrant applies a signed installation delivery to the connections of
// that installation (ADR-0195 section 5, ADR-0193 section 7): deleted and
// suspend make them invalid, created and unsuspend probe them back.
// installation_repositories changes no connection; it is logged for the
// runtime targets that read the grant.
func (s *Service) HandleGrant(ctx context.Context, grant provider.GrantEvent) error {
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
