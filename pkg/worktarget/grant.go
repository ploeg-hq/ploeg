package worktarget

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func connectionAudit(id string) store.WorkTargetAudit {
	return store.WorkTargetAudit{Actor: "connection:" + id}
}

// finding is what asking the forge about a live target found.
type finding struct {
	state     string
	reason    string
	readiness *store.TargetReadiness
	unknown   bool
}

// check asks the forge, through the target's own connection, whether the
// grant still reaches the repository it was registered for (ADR-0193
// section 7). A rename or transfer is repository_moved and never followed.
func (s *Service) check(ctx context.Context, t store.WorkTarget) finding {
	grants, inst, cred, e := s.grantsOf(ctx, t.Connection)
	if e != nil {
		if e.Code == "capability_missing" || e.Status >= 500 {
			return finding{unknown: true}
		}
		return finding{state: store.WorkTargetInvalid, reason: store.WorkTargetConnectionUnusable}
	}
	grant, err := grants.ProbeRepositoryGrant(ctx, inst, cred, t.Repository)
	switch {
	case errors.Is(err, provider.ErrRepositoryNotGranted):
		return finding{state: store.WorkTargetInvalid, reason: store.WorkTargetGrantRevoked}
	case errors.Is(err, provider.ErrRepositoryNotFound):
		return finding{state: store.WorkTargetInvalid, reason: store.WorkTargetRepositoryGone}
	case errors.Is(err, provider.ErrInstallationSuspended):
		return finding{state: store.WorkTargetInvalid, reason: store.WorkTargetInstallationSuspended}
	case errors.Is(err, provider.ErrCredentialRefused):
		return finding{state: store.WorkTargetInvalid, reason: store.WorkTargetConnectionUnusable}
	case err != nil:
		return finding{unknown: true}
	}
	if grant.ID != t.RepositoryID || !strings.EqualFold(grant.FullName, t.Repository) {
		return finding{state: store.WorkTargetInvalid, reason: store.WorkTargetRepositoryMoved}
	}
	if !grant.CanWrite {
		return finding{state: store.WorkTargetInvalid, reason: store.WorkTargetGrantRevoked}
	}
	readiness, ok := s.inspect(ctx, grants, inst, cred, t.Repository, t.Branch)
	if !ok {
		return finding{state: store.WorkTargetActive, unknown: true}
	}
	return finding{state: store.WorkTargetActive, readiness: &readiness}
}

// apply records what check found for a live target that is not shadowed.
func (s *Service) apply(ctx context.Context, t store.WorkTarget, f finding, audit store.WorkTargetAudit) bool {
	if t.State == store.WorkTargetShadowed || (f.unknown && f.state == "") {
		return false
	}
	switch {
	case f.state == store.WorkTargetActive && t.State == store.WorkTargetActive:
		if f.readiness != nil {
			if err := s.Store.SetWorkTargetReadiness(ctx, t.ID, *f.readiness); err != nil {
				s.log().Error("runtime target readiness not recorded", "target", t.Key, "err", err)
			}
		}
		return false
	case f.state == store.WorkTargetActive:
		if f.unknown {
			return false
		}
		return s.transition(ctx, t, store.WorkTargetStateChange{State: store.WorkTargetActive, Readiness: f.readiness}, store.AuditWorkTargetRestored, audit)
	case t.State == store.WorkTargetInvalid && t.InvalidReason == f.reason:
		return false
	}
	return s.transition(ctx, t, store.WorkTargetStateChange{State: store.WorkTargetInvalid, InvalidReason: f.reason}, store.AuditWorkTargetInvalid, audit)
}

func (s *Service) transition(ctx context.Context, t store.WorkTarget, to store.WorkTargetStateChange, action string, audit store.WorkTargetAudit) bool {
	changed, err := s.Store.SetWorkTargetState(ctx, t.ID, t, to, action, audit, nil)
	if err != nil {
		s.log().Error("runtime target state not recorded", "target", t.Key, "err", err)
		return false
	}
	if changed {
		s.log().Warn("runtime target state changed", "target", t.Key, "repository", t.Repository, "from", t.State, "to", to.State, "reason", to.InvalidReason)
	}
	return changed
}

func (s *Service) invalidate(ctx context.Context, t store.WorkTarget, reason string, audit store.WorkTargetAudit) bool {
	if t.State == store.WorkTargetShadowed || (t.State == store.WorkTargetInvalid && t.InvalidReason == reason) {
		return false
	}
	return s.transition(ctx, t, store.WorkTargetStateChange{State: store.WorkTargetInvalid, InvalidReason: reason}, store.AuditWorkTargetInvalid, audit)
}

// Reconcile asks the forge about every live runtime target, so a grant
// withdrawn without an event, a revoked token, a deleted or moved
// repository or a lost delivery is caught within the hour (ADR-0193
// section 7). It probes each target through its connection.
func (s *Service) Reconcile(ctx context.Context) {
	rows, err := s.Store.LiveWorkTargets(ctx, s.tenant())
	if err != nil {
		s.log().Error("runtime target reconcile could not read the targets", "err", err)
		return
	}
	changed := false
	for _, t := range rows {
		if s.apply(ctx, t, s.check(ctx, t), connectionAudit(t.Connection)) {
			changed = true
		}
	}
	if changed || len(rows) > 0 {
		if err := s.Rebuild(ctx); err != nil {
			s.log().Warn("runtime targets not rebuilt after the reconcile", "err", err)
		}
	}
}

// HandleGrant applies a signed App installation delivery to the runtime
// targets of that installation's connections (ADR-0193 section 7): a
// deletion or removal makes them invalid with grant_revoked, a suspension
// with installation_suspended, and an addition or unsuspension restores a
// target only after its probe passes.
func (s *Service) HandleGrant(ctx context.Context, grant provider.GrantEvent) error {
	if s.Connections == nil {
		return nil
	}
	ids, err := s.Connections.ConnectionsOfInstallation(ctx, grant.AppID, grant.InstallationID)
	if err != nil {
		return err
	}
	changed := false
	for _, id := range ids {
		rows, err := s.Store.LiveWorkTargetsByConnection(ctx, s.tenant(), id)
		if err != nil {
			return err
		}
		audit := connectionAudit(id)
		for _, t := range rows {
			switch {
			case grant.Event == "installation" && grant.Action == "deleted":
				changed = s.invalidate(ctx, t, store.WorkTargetGrantRevoked, audit) || changed
			case grant.Event == "installation" && grant.Action == "suspend":
				changed = s.invalidate(ctx, t, store.WorkTargetInstallationSuspended, audit) || changed
			case grant.Event == "installation_repositories" && grant.Action == "removed" && names(t, grant.Removed, grant.RemovedIDs):
				changed = s.invalidate(ctx, t, store.WorkTargetGrantRevoked, audit) || changed
			case t.State == store.WorkTargetInvalid && restores(grant, t):
				changed = s.apply(ctx, t, s.check(ctx, t), audit) || changed
			}
		}
	}
	if changed {
		return s.Rebuild(ctx)
	}
	return nil
}

func names(t store.WorkTarget, fullNames, ids []string) bool {
	if t.RepositoryID != "" && slices.Contains(ids, t.RepositoryID) {
		return true
	}
	for _, n := range fullNames {
		if strings.EqualFold(n, t.Repository) {
			return true
		}
	}
	return false
}

func restores(grant provider.GrantEvent, t store.WorkTarget) bool {
	switch {
	case grant.Event == "installation" && (grant.Action == "unsuspend" || grant.Action == "created"):
		return true
	case grant.Event == "installation_repositories" && grant.Action == "added":
		return names(t, grant.Added, grant.AddedIDs)
	}
	return false
}

// Claim is what the claim found for a Work Item's target: whether a
// runtime target serves it, and through which connection.
type Claim struct {
	Runtime    bool
	Key        string
	Connection string
	// Refusal names why no Run may start, empty when one may.
	Refusal string
	Reason  string
}

// ClaimTarget reads the runtime target that serves t from the store, never
// from the cache, so a target removed or made invalid through another
// replica refuses the Run before anything is minted (ADR-0193 section 4). A
// target the configuration registers is static and not a runtime target.
func (s *Service) ClaimTarget(ctx context.Context, t work.Target) (Claim, error) {
	forge := t.Forge
	if forge == "" {
		forge = s.DefaultForge
	}
	repository := t.Owner + "/" + t.Repo
	if s.StaticRepositoryConflict(forge, repository) != nil {
		return Claim{}, nil
	}
	row, found, err := s.Store.WorkTargetForRepository(ctx, s.tenant(), forge, repository)
	if err != nil || !found {
		return Claim{}, err
	}
	c := Claim{Runtime: true, Key: row.Key, Connection: row.Connection}
	switch {
	case row.RemovedAt != nil:
		c.Refusal, c.Reason = "target_not_registered", "the runtime target "+row.Key+" was removed"
	case row.State == store.WorkTargetInvalid:
		c.Refusal, c.Reason = "target_invalid", "the runtime target "+row.Key+" is invalid: "+row.InvalidReason
	case row.State != store.WorkTargetActive:
		c.Refusal, c.Reason = "target_not_registered", "the runtime target "+row.Key+" is "+row.State
	}
	return c, nil
}

// MintRefused marks the live runtime target of t invalid with
// grant_revoked after the forge refused a per-Run mint because the
// repository is outside the grant (ADR-0193 section 7).
func (s *Service) MintRefused(ctx context.Context, t work.Target) {
	forge := t.Forge
	if forge == "" {
		forge = s.DefaultForge
	}
	row, found, err := s.Store.WorkTargetForRepository(ctx, s.tenant(), forge, t.Owner+"/"+t.Repo)
	if err != nil || !found || row.RemovedAt != nil {
		return
	}
	if s.invalidate(ctx, row, store.WorkTargetGrantRevoked, connectionAudit(row.Connection)) {
		if err := s.Rebuild(ctx); err != nil {
			s.log().Warn("runtime targets not rebuilt after a refused mint", "err", err)
		}
	}
}

// Use is a runtime target that names a connection.
type Use struct {
	Key   string
	State string
}

// Users returns the live runtime targets that name connection, for the
// connection's usedBy (ADR-0195).
func (s *Service) Users(ctx context.Context, connection string) ([]Use, error) {
	rows, err := s.Store.LiveWorkTargetsByConnection(ctx, s.tenant(), connection)
	if err != nil {
		return nil, err
	}
	out := make([]Use, 0, len(rows))
	for _, t := range rows {
		out = append(out, Use{Key: t.Key, State: t.State})
	}
	return out, nil
}
