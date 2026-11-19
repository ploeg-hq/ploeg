// Package teamruntime serves the runtime Teams of ADR-0198: it validates a
// Team against the installation's policy, decides its state with static
// Teams winning, stores it within the Tenant's limits, and keeps the Team
// directory current.
package teamruntime

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

// DefaultTenant is the Tenant runtime Teams belong to while ploegd serves
// one Tenant (ADR-0178).
const DefaultTenant = "default"

// Service is the runtime Team service. Config nil or not enabled refuses
// every change with creation_not_accepted.
type Service struct {
	Store     *store.Store
	Config    *config.TeamCreation
	Pools     team.Pools
	Tenant    string
	Directory *team.Directory
	// Refinement maps each static Team's key to the refinementTeam it
	// names, so a removal sees a static reference.
	Refinement   map[string]string
	TargetExists func(key string) bool
	Now          func() time.Time
	NewID        func() string
	// OnChange runs after a change or a state transition, so bindings can
	// follow their Team.
	OnChange func(ctx context.Context)
	// OnStopped blocks the model keys and revokes the forge credentials of
	// the Runs an expired drain stopped.
	OnStopped func(ctx context.Context, runTokens, forgeTokenIDs []string)
	Log       *slog.Logger
}

// Error is a refusal with its HTTP status and operator error code.
type Error struct {
	Status        int
	Code          string
	Field         string
	Message       string
	ReservedUntil *time.Time
	Headroom      *float64
	Current       *store.RuntimeTeam
	InUse         *store.TeamUse
}

var statusOf = map[string]int{
	"creation_not_accepted":      422,
	team.RefuseFieldNotAllowed:   422,
	team.RefuseInvalidKey:        422,
	team.RefuseNameInvalid:       422,
	team.RefusePlanInvalid:       422,
	team.RefuseHarnessNotOffered: 422,
	team.RefuseHarnessQualified:  422,
	team.RefuseModelNotAllowed:   422,
	team.RefuseUnknownTarget:     422,
	team.RefuseSpendRequired:     422,
	team.RefuseInvalidRequest:    400,
	store.TeamExists:             409,
	store.TeamKeyTaken:           409,
	store.TeamKeyReserved:        409,
	store.TeamConflictsStatic:    409,
	store.TeamLimitReached:       429,
	store.TeamSpendExceeds:       422,
	store.TeamStaleRevision:      412,
	store.TeamDraining:           409,
	store.TeamInUseCode:          409,
	store.TeamUnknown:            404,
	store.TeamTooManyAliases:     422,
	store.TeamAliasInUse:         409,
	"team_is_static":             409,
	"name_set_by_configuration":  409,
}

func refusal(code, field, message string) *Error {
	status, ok := statusOf[code]
	if !ok {
		status = 422
	}
	return &Error{Status: status, Code: code, Field: field, Message: message}
}

func fromStore(err error) (*Error, error) {
	var te *store.TeamError
	if !errors.As(err, &te) {
		return nil, err
	}
	e := refusal(te.Code, "", te.Message)
	e.ReservedUntil, e.Headroom, e.Current, e.InUse = te.ReservedUntil, te.Headroom, te.Current, te.InUse
	return e, nil
}

func (s *Service) tenant() string {
	if s.Tenant != "" {
		return s.Tenant
	}
	return DefaultTenant
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	return team.MustNewID()
}

// Accepting refuses with creation_not_accepted while runtime Teams are off.
func (s *Service) Accepting() *Error {
	if s == nil || !s.Config.Accepting() {
		return refusal("creation_not_accepted", "", "This installation does not accept runtime Teams (teamCreation.enabled).")
	}
	return nil
}

func (s *Service) policy() team.Policy {
	c := s.Config
	return team.Policy{Pools: s.Pools, Tenant: s.tenant(), Models: c.Models, MaxRoleCap: c.RoleCapLimit(),
		MayWrite: c.May.Write, MayComment: c.May.Comment, Egress: c.May.Egress, TargetExists: s.TargetExists}
}

// Installation is what decides a runtime Team's state now.
func (s *Service) Installation() team.Installation {
	in := team.Installation{StaticKeys: s.Directory.StaticKeys(), Pools: s.Pools, TargetExists: s.TargetExists,
		Allowances: map[string]spendcap.Cap{}}
	if s.Config != nil {
		in.Accepting = s.Config.Enabled
		in.Models = s.Config.Models
		for tenant, t := range s.Config.Tenants {
			in.Allowances[tenant] = t.Spend
		}
	}
	return in
}

func (s *Service) limits() store.TeamLimits {
	l := store.TeamLimits{Max: s.Config.Limit(), Reservation: team.AliasReservation, Now: s.now()}
	if t, ok := s.Config.Tenants[s.tenant()]; ok {
		l.Allowance = t.Spend
	}
	return l
}

// DryRun is what a create or replace would store, with every check's
// result and the spend headroom.
type DryRun struct {
	Manifest team.Manifest
	Decision team.Decision
	Headroom float64
}

func (s *Service) prepare(raw []byte) (team.Manifest, *Error) {
	m, r := team.ParseManifest(raw)
	if r != nil {
		return m, refusal(r.Code, r.Field, r.Message)
	}
	if r := m.Validate(s.policy()); r != nil {
		return m, refusal(r.Code, r.Field, r.Message)
	}
	if id, static := s.Directory.StaticKeys()[strings.ToLower(m.Key)]; static {
		e := refusal(store.TeamConflictsStatic, "key", "A static Team in Ploeg's configuration file has key "+m.Key+"; the file wins.")
		e.Current = &store.RuntimeTeam{ID: id, Key: m.Key}
		return m, e
	}
	return m, nil
}

// Create validates and, unless dryRun, stores a runtime Team.
func (s *Service) Create(ctx context.Context, raw []byte, dryRun bool, audit store.TeamAudit) (store.RuntimeTeam, bool, *DryRun, *Error, error) {
	if e := s.Accepting(); e != nil {
		return store.RuntimeTeam{}, false, nil, e, nil
	}
	m, e := s.prepare(raw)
	if e == nil {
		e = sponsoredBy(&m, audit)
	}
	if e != nil {
		return store.RuntimeTeam{}, false, nil, e, nil
	}
	decision := s.Installation().Decide(s.tenant(), m.Key, m)
	limits := s.limits()
	if dryRun {
		until, err := s.Store.ReservedKey(ctx, s.tenant(), m.Key, limits)
		if err != nil {
			return store.RuntimeTeam{}, false, nil, nil, err
		}
		if until != nil {
			e := refusal(store.TeamKeyReserved, "key", "Key "+m.Key+" was released and is still reserved.")
			e.ReservedUntil = until
			return store.RuntimeTeam{}, false, nil, e, nil
		}
		headroom, err := s.Store.SpendHeadroom(ctx, s.tenant(), "", limits.Allowance)
		if err != nil {
			return store.RuntimeTeam{}, false, nil, nil, err
		}
		if m.Spend.Cap.USD > headroom+1e-9 || m.Spend.Cap.Period != limits.Allowance.Period {
			e := refusal(store.TeamSpendExceeds, "spend.cap", "The Team's cap is over what the Tenant's allowance has left.")
			e.Headroom = &headroom
			return store.RuntimeTeam{}, false, nil, e, nil
		}
		return store.RuntimeTeam{}, false, &DryRun{Manifest: m, Decision: decision, Headroom: headroom - m.Spend.Cap.USD}, nil, nil
	}
	out, created, err := s.Store.CreateRuntimeTeam(ctx, s.tenant(), m, decision, limits, audit, s.newID)
	if e, err := fromStore(err); e != nil || err != nil {
		return out, false, nil, e, err
	}
	s.changed(ctx)
	return out, created, nil, nil, nil
}

// Replace replaces a runtime Team's manifest at revision ifMatch.
func (s *Service) Replace(ctx context.Context, id string, ifMatch int64, raw []byte, dryRun bool, audit store.TeamAudit) (store.RuntimeTeam, *DryRun, *Error, error) {
	if e := s.Accepting(); e != nil {
		return store.RuntimeTeam{}, nil, e, nil
	}
	m, e := s.prepare(raw)
	if e == nil {
		e = sponsoredBy(&m, audit)
	}
	if e != nil {
		return store.RuntimeTeam{}, nil, e, nil
	}
	decision := s.Installation().Decide(s.tenant(), m.Key, m)
	if dryRun {
		headroom, err := s.Store.SpendHeadroom(ctx, s.tenant(), id, s.limits().Allowance)
		if err != nil {
			return store.RuntimeTeam{}, nil, nil, err
		}
		return store.RuntimeTeam{}, &DryRun{Manifest: m, Decision: decision, Headroom: headroom - m.Spend.Cap.USD}, nil, nil
	}
	out, err := s.Store.ReplaceRuntimeTeam(ctx, id, ifMatch, m, decision, s.limits(), audit)
	if e, err := fromStore(err); e != nil || err != nil {
		return out, nil, e, err
	}
	s.changed(ctx)
	return out, nil, nil, nil
}

// Rename changes only a runtime Team's display name.
func (s *Service) Rename(ctx context.Context, id string, ifMatch int64, name string, audit store.TeamAudit) (store.RuntimeTeam, *Error, error) {
	if err := team.ValidateName(name); err != nil {
		return store.RuntimeTeam{}, refusal(team.RefuseNameInvalid, "name", err.Error()+"."), nil
	}
	out, err := s.Store.RenameRuntimeTeam(ctx, id, ifMatch, name, audit)
	if e, err := fromStore(err); e != nil || err != nil {
		return out, e, err
	}
	s.changed(ctx)
	return out, nil, nil
}

func (s *Service) refinedBy(t team.Team) []string {
	refs := append([]string{t.ID, t.Key}, t.Aliases...)
	var out []string
	for _, key := range slices.Sorted(maps.Keys(s.Refinement)) {
		if slices.ContainsFunc(refs, func(r string) bool { return strings.EqualFold(r, s.Refinement[key]) }) {
			out = append(out, key)
		}
	}
	return out
}

// Remove removes a runtime Team, or starts its drain.
func (s *Service) Remove(ctx context.Context, t team.Team, drain bool, audit store.TeamAudit) (*store.RuntimeTeam, *Error, error) {
	if e := s.Accepting(); e != nil {
		return nil, e, nil
	}
	refined := s.refinedBy(t)
	if drain {
		out, err := s.Store.DrainRuntimeTeam(ctx, t.ID, refined, audit)
		if e, err := fromStore(err); e != nil || err != nil {
			return nil, e, err
		}
		s.changed(ctx)
		return &out, nil, nil
	}
	_, err := s.Store.RemoveRuntimeTeam(ctx, t.ID, refined, audit)
	if e, err := fromStore(err); e != nil || err != nil {
		return nil, e, err
	}
	s.changed(ctx)
	return nil, nil, nil
}

// Undrain cancels a drain.
func (s *Service) Undrain(ctx context.Context, id string, audit store.TeamAudit) (store.RuntimeTeam, *Error, error) {
	out, err := s.Store.UndrainRuntimeTeam(ctx, id, audit)
	if e, err := fromStore(err); e != nil || err != nil {
		return out, e, err
	}
	s.changed(ctx)
	return out, nil, nil
}

// ReleaseAlias releases an alias, refused while a static refinementTeam
// names it.
func (s *Service) ReleaseAlias(ctx context.Context, id, alias string, audit store.TeamAudit) (store.RuntimeTeam, *Error, error) {
	inUse := slices.ContainsFunc(slices.Collect(maps.Values(s.Refinement)), func(r string) bool { return strings.EqualFold(r, alias) })
	out, err := s.Store.ReleaseTeamAlias(ctx, id, alias, inUse, audit)
	if e, err := fromStore(err); e != nil || err != nil {
		return out, e, err
	}
	s.changed(ctx)
	return out, nil, nil
}

func (s *Service) changed(ctx context.Context) {
	if err := s.Refresh(ctx); err != nil && s.Log != nil {
		s.Log.Error("Team directory not refreshed after a change", "err", err)
	}
	if s.OnChange != nil {
		s.OnChange(ctx)
	}
}

// Refresh reloads the Team directory from the store.
func (s *Service) Refresh(ctx context.Context) error {
	records, err := s.Store.LiveTeams(ctx)
	if err != nil {
		return err
	}
	s.Directory.Set(store.Directory(records))
	return nil
}

// Reconcile decides every runtime Team's state under the current static
// Teams and installation, and finishes drains once OnStopped can block and
// revoke what an expired drain stops; before that it only decides states.
// It never fails boot: a Team it cannot decide keeps its state.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.OnStopped != nil {
		drained, err := s.Store.FinishDrains(ctx, s.now())
		if err != nil {
			return err
		}
		if len(drained.StoppedRunTokens) > 0 || len(drained.ForgeTokenIDs) > 0 {
			s.OnStopped(ctx, drained.StoppedRunTokens, drained.ForgeTokenIDs)
		}
	}
	teams, err := s.Store.RuntimeTeams(ctx)
	if err != nil {
		return err
	}
	in := s.Installation()
	changed := false
	for _, t := range teams {
		moved, err := s.Store.ApplyTeamDecision(ctx, t.ID, in.Decide(t.Tenant, t.Key, t.Manifest), in.StaticKeys)
		if err != nil {
			return err
		}
		changed = changed || moved
	}
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	if changed && s.OnChange != nil {
		s.OnChange(ctx)
	}
	return nil
}

// Team returns the runtime Team and its policy view.
func (s *Service) Team(ctx context.Context, id string) (store.RuntimeTeam, error) {
	return s.Store.RuntimeTeam(ctx, id)
}

// Lookup returns the live runtime Team a key, alias or id names.
func (s *Service) Lookup(ctx context.Context, key string) (store.RuntimeTeam, bool) {
	if s == nil || s.Directory == nil {
		return store.RuntimeTeam{}, false
	}
	t, _, ok := s.Directory.Resolve(key)
	if !ok || t.Source != team.SourceRuntime {
		return store.RuntimeTeam{}, false
	}
	rt, err := s.Store.RuntimeTeam(ctx, t.ID)
	return rt, err == nil
}

// ReservedUntil reports until when a released key of this Tenant stays
// reserved; nil when it is not.
func (s *Service) ReservedUntil(ctx context.Context, key string) (*time.Time, error) {
	if s == nil || s.Config == nil {
		return nil, nil
	}
	return s.Store.ReservedKey(ctx, s.tenant(), key, s.limits())
}

// PoolRun selects the next Run a pool of this Tenant and Harness claims,
// with the claim marker that keeps it on runtime Teams and under the
// Team's spend cap.
func (s *Service) PoolRun(ctx context.Context, tenant, harness string) (store.PoolRun, store.PoolClaim, error) {
	next, err := s.Store.NextPoolRun(ctx, tenant, harness)
	if err != nil {
		return next, store.PoolClaim{}, err
	}
	rt, err := s.Store.RuntimeTeam(ctx, next.TeamID)
	if err != nil {
		return next, store.PoolClaim{}, err
	}
	claim := store.PoolClaim{TeamID: rt.ID}
	if rt.Manifest.Spend.Cap != nil {
		claim.SpendCap = *rt.Manifest.Spend.Cap
	}
	return next, claim, nil
}

// LLMPolicy is the inference policy of a runtime Team's Role: its one
// model and its cap.
func (s *Service) LLMPolicy(key, role string) (models []string, budgetUSD float64, ok bool) {
	rt, found := s.Lookup(context.Background(), key)
	if !found {
		return nil, 0, false
	}
	for _, r := range rt.Manifest.Roles() {
		if r.Name == role {
			return []string{r.Model}, r.Cap, true
		}
	}
	return nil, 0, false
}

// Plan is the Shift plan of the active runtime Team key names, for the
// Shift engine. A shadowed or invalid Team has none, so it opens no Shift.
func (s *Service) Plan(key string) (plan.TeamPlan, bool) {
	rt, ok := s.Lookup(context.Background(), key)
	if !ok || rt.State != team.StateActive {
		return plan.TeamPlan{}, false
	}
	tp, err := rt.Manifest.TeamPlan()
	return tp, err == nil
}

// Harness is the Harness a runtime Team's Role runs on.
func (s *Service) Harness(key, role string) (string, bool) {
	rt, ok := s.Lookup(context.Background(), key)
	if !ok {
		return "", false
	}
	for _, r := range rt.Manifest.Roles() {
		if r.Name == role {
			return r.Harness, true
		}
	}
	return "", false
}

// sponsoredBy makes the proven person who creates or replaces a runtime
// Team its sponsor (ADR-0156). A sponsor naming anyone else is refused: the
// transaction token of every Run would act for them.
func sponsoredBy(m *team.Manifest, audit store.TeamAudit) *Error {
	actor := audit.Attribution.Actor
	if m.Sponsor != "" && m.Sponsor != actor {
		return refusal(team.RefuseFieldNotAllowed, "sponsor", "A runtime Team's sponsor is the proven person who creates or changes it; it cannot name anyone else.")
	}
	m.Sponsor = actor
	return nil
}
