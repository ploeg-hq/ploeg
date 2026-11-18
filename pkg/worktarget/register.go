package worktarget

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Plan is what a probe found: the target registration would store, every
// check's result and, when registration would be refused, the refusal.
type Plan struct {
	Target   store.WorkTarget
	Checks   []Check
	Refusal  *Error
	Existing *store.WorkTarget
}

func (p *Plan) pass(name, detail string) {
	p.Checks = append(p.Checks, Check{Name: name, Result: "pass", Detail: detail})
}

func (p *Plan) fail(name, detail string, e *Error) {
	p.Checks = append(p.Checks, Check{Name: name, Result: "fail", Detail: detail})
	if p.Refusal == nil {
		e.Checks = nil
		p.Refusal = e
	}
}

func (p *Plan) unknown(name, detail string, e *Error) {
	p.Checks = append(p.Checks, Check{Name: name, Result: "unknown", Detail: detail})
	if p.Refusal == nil {
		p.Refusal = e
	}
}

func (p *Plan) refusal() *Error {
	if p.Refusal == nil {
		return nil
	}
	e := *p.Refusal
	e.Checks = p.Checks
	return &e
}

// Accepting is the refusal every changing route answers when the
// installation does not take runtime targets.
func (s *Service) Accepting() *Error {
	if !s.Config.Accepting() {
		return refuse(422, "registration_not_accepted", "This installation does not accept runtime Work Targets (targetRegistration.enabled).")
	}
	return nil
}

// DeriveKey is the key Ploeg gives a target that names none:
// <forge>:<owner>:<name>, every / of the path written as :, in lower case.
func DeriveKey(forge, repository string) string {
	return strings.ToLower(forge + ":" + strings.ReplaceAll(repository, "/", ":"))
}

// Probe runs every check of a registration and reports each result; it
// writes nothing (ADR-0193 section 4).
func (s *Service) Probe(ctx context.Context, req Request) Plan {
	plan := Plan{Target: store.WorkTarget{Tenant: s.tenant(), Connection: req.Connection, Repository: req.Repository,
		Branch: req.Branch, Label: req.Label, Key: req.Key, State: store.WorkTargetActive}}
	if e := s.Accepting(); e != nil {
		plan.fail("registration_accepted", e.Message, e)
		return plan
	}
	if !s.Config.AcceptsConnection(req.Connection) {
		plan.fail("registration_accepted", "targetRegistration.connections does not name "+req.Connection,
			refuse(422, "registration_not_accepted", "targetRegistration.connections does not let connection "+req.Connection+" grant runtime targets."))
		return plan
	}
	if len(req.Label) > MaxLabel {
		plan.fail("label", "longer than 200 characters", refuse(422, "probe_failed", "A label is at most 200 characters."))
	} else {
		plan.pass("label", "")
	}
	forge, e := s.connectionInstance(ctx, req.Connection)
	if e != nil {
		plan.fail("connection_usable", e.Message, e)
		return plan
	}
	plan.Target.Forge = forge
	if plan.Target.Key == "" {
		plan.Target.Key = DeriveKey(forge, req.Repository)
	}
	switch key := plan.Target.Key; {
	case !operatorName.MatchString(key) || !keyPattern.MatchString(key):
		plan.fail("key_pattern", "a runtime key is lowercase and matches ^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$",
			refuse(422, "probe_failed", "The key "+key+" is not a valid runtime key: lowercase, matching the operator API's name pattern."))
	default:
		plan.pass("key_pattern", "")
	}
	if static, ok := s.StaticKeyConflict(plan.Target.Key); ok {
		e := refuse(409, "key_taken", "The key equals the static target "+static+"; a runtime key may never equal a static key.")
		st := s.Static[static]
		e.Static = &store.StaticEntry{Entry: "targets." + static, Repository: st.Owner + "/" + st.Repo}
		plan.fail("key_free", "static target "+static, e)
	} else if entry := s.StaticRepositoryConflict(forge, req.Repository); entry != nil {
		e := refuse(409, "conflicts_with_static", "The static entry "+entry.Entry+" already registers this repository; static targets win.")
		e.Static = entry
		plan.fail("no_static_conflict", entry.Entry, e)
	} else {
		plan.pass("no_static_conflict", "")
	}
	s.runtimeConflicts(ctx, &plan)
	if plan.Refusal != nil && plan.Existing == nil {
		return plan
	}
	grants, inst, cred, e := s.grantsOf(ctx, req.Connection)
	if e != nil {
		name := "connection_usable"
		if e.Code == "capability_missing" {
			name = "capability"
		}
		plan.fail(name, e.Message, e)
		return plan
	}
	plan.pass("connection_usable", "")
	plan.pass("capability", "")
	grant, err := grants.ProbeRepositoryGrant(ctx, inst, cred, req.Repository)
	switch {
	case errors.Is(err, provider.ErrRepositoryNotGranted):
		plan.fail("granted", "the connection's grant does not reach "+req.Repository,
			refuse(422, "not_granted", "The connection's grant does not reach "+req.Repository+"; grant it at the forge first."))
		return plan
	case errors.Is(err, provider.ErrRepositoryNotFound):
		plan.fail("repository_found", req.Repository+" is not on the forge",
			refuse(422, "repository_not_found", "The forge shows no repository "+req.Repository+"."))
		return plan
	case errors.Is(err, provider.ErrCredentialRefused):
		plan.fail("connection_usable", "the forge refused the connection's credential",
			refuse(422, "connection_unusable", "The forge refused the connection's credential; reconnect it."))
		return plan
	case errors.Is(err, provider.ErrInstallationSuspended):
		plan.fail("connection_usable", "the installation is suspended", refuse(422, "connection_unusable", "The installation is suspended."))
		return plan
	case err != nil:
		plan.unknown("granted", "the forge could not be asked", refuse(422, "probe_failed", "The forge could not be asked whether the grant reaches the repository."))
		return plan
	}
	if !strings.EqualFold(grant.FullName, req.Repository) {
		plan.fail("repository_found", "the forge knows it as "+grant.FullName,
			refuse(422, "repository_not_found", "The forge knows this repository as "+grant.FullName+"; register that name."))
		return plan
	}
	plan.pass("granted", strings.Join(grant.Permissions, ","))
	if !grant.CanWrite {
		plan.fail("write_access", "the grant cannot push branches and open pull requests",
			refuse(422, "grant_insufficient", "The connection's grant on "+req.Repository+" is read-only; a Run must push and open pull requests."))
		return plan
	}
	plan.pass("write_access", "")
	plan.Target.Repository, plan.Target.RepositoryID = grant.FullName, grant.ID
	if plan.Target.Branch == "" {
		plan.Target.Branch = grant.DefaultBranch
	}
	readiness, ok := s.inspect(ctx, grants, inst, cred, plan.Target.Repository, plan.Target.Branch)
	plan.Target.Readiness = readiness
	switch {
	case !ok:
		plan.unknown("readiness", readiness.Reason, refuse(422, "probe_failed", "The forge could not be asked whether the repository is ready."))
	case readiness.Ready:
		plan.pass("readiness", "")
	case readiness.PrepareAvailable:
		plan.Checks = append(plan.Checks, Check{Name: "readiness", Result: "fail", Detail: "no instruction file; a prepare-repository Work Item may add it (ADR-0194)"})
	default:
		e := refuse(422, "target_not_ready", "The repository is not ready: "+strings.Join(readiness.Reasons, ", ")+".")
		plan.fail("readiness", strings.Join(readiness.Reasons, ","), e)
	}
	return plan
}

func (s *Service) runtimeConflicts(ctx context.Context, plan *Plan) {
	t := plan.Target
	if t.Forge == "" {
		return
	}
	holder, err := s.Store.LiveWorkTarget(ctx, t.Tenant, t.Key)
	if err == nil && !(holder.Forge == t.Forge && strings.EqualFold(holder.Repository, t.Repository)) {
		e := refuse(409, "key_taken", "The runtime target of "+holder.Repository+" holds this key.")
		plan.fail("key_free", holder.Repository, e)
		return
	}
	if err != nil && !errors.Is(err, store.ErrOperatorNotFound) {
		plan.unknown("key_free", "the store could not be read", refuse(503, "store_unavailable", "The runtime targets could not be read."))
		return
	}
	current, found, err := s.Store.WorkTargetForRepository(ctx, t.Tenant, t.Forge, t.Repository)
	switch {
	case err != nil:
		plan.unknown("target_exists", "the store could not be read", refuse(503, "store_unavailable", "The runtime targets could not be read."))
	case found && current.RemovedAt == nil:
		if current.SameBody(store.NewWorkTarget{Key: t.Key, Connection: t.Connection, Branch: firstNonEmpty(t.Branch, current.Branch), Label: t.Label}) {
			plan.Existing = &current
			plan.pass("target_exists", "the same target is registered")
			return
		}
		e := refuse(409, "target_exists", "A runtime target for this repository exists with another body.")
		e.Current = &current
		plan.fail("target_exists", current.Key, e)
	default:
		plan.pass("key_free", "")
		if len(s.Live()) >= s.Config.Limit() {
			plan.fail("limit", "", refuse(429, "target_limit", "The installation holds its maximum of live runtime targets (targetRegistration.max)."))
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Service) inspect(ctx context.Context, grants provider.RepositoryGrantProber, inst provider.ConnectionInstance, cred provider.ConnectionCredential, repository, branch string) (store.TargetReadiness, bool) {
	at := s.now().UTC()
	state, err := grants.InspectGrantedRepository(ctx, inst, cred, repository, branch)
	if err != nil {
		return store.TargetReadiness{Unknown: true, Reason: "could not read " + repository + " from the forge", CheckedAt: &at}, false
	}
	return Verdict(state, repository, at), true
}

// Verdict turns what a forge reported about a repository into the
// structured readiness of ADR-0194.
func Verdict(state provider.RepositoryState, repository string, at time.Time) store.TargetReadiness {
	var reasons, sentences []string
	if state.Archived {
		reasons, sentences = append(reasons, ReasonArchived), append(sentences, "is archived")
	}
	if state.Mirror {
		reasons, sentences = append(reasons, ReasonMirror), append(sentences, "is a mirror")
	}
	if !state.AgentsFile {
		reasons, sentences = append(reasons, ReasonInstructionFileMissing), append(sentences, "has no AGENTS.md on branch \""+state.Branch+"\"")
	}
	if len(reasons) == 0 {
		return store.TargetReadiness{Ready: true, CheckedAt: &at}
	}
	return store.TargetReadiness{Reasons: reasons, Reason: repository + " " + strings.Join(sentences, ", "), CheckedAt: &at,
		PrepareAvailable: len(reasons) == 1 && reasons[0] == ReasonInstructionFileMissing}
}

func (s *Service) refused(ctx context.Context, audit store.WorkTargetAudit, t store.WorkTarget, e *Error) {
	failed := []Check{}
	for _, c := range e.Checks {
		if c.Result != "pass" {
			failed = append(failed, c)
		}
	}
	if err := s.Store.AuditWorkTarget(ctx, store.AuditWorkTargetRefused, store.WorkTarget{Key: t.Key, Connection: t.Connection, Forge: t.Forge, Repository: t.Repository},
		audit, map[string]any{"code": e.Code, "failedChecks": failed}); err != nil {
		s.log().Error("runtime target refusal not audited", "err", err)
	}
}

// Register probes and stores a runtime target: created is false when the
// same target was registered already.
func (s *Service) Register(ctx context.Context, audit store.WorkTargetAudit, req Request) (store.WorkTarget, bool, *Error) {
	plan := s.Probe(ctx, req)
	if plan.Existing != nil && plan.Refusal == nil {
		return *plan.Existing, false, nil
	}
	if e := plan.refusal(); e != nil {
		s.refused(ctx, audit, plan.Target, e)
		return store.WorkTarget{}, false, e
	}
	t := plan.Target
	out, created, err := s.Store.CreateWorkTarget(ctx, store.NewWorkTarget{Tenant: t.Tenant, Key: t.Key, Connection: t.Connection, Forge: t.Forge,
		Repository: t.Repository, RepositoryID: t.RepositoryID, Branch: t.Branch, Label: t.Label, Readiness: t.Readiness, Max: s.Config.Limit()}, audit)
	if e := storeRefusal(err); e != nil {
		s.refused(ctx, audit, t, e)
		return store.WorkTarget{}, false, e
	}
	if err := s.Rebuild(ctx); err != nil {
		s.log().Warn("runtime targets not rebuilt after a registration; the poll retries", "err", err)
	}
	return out, created, nil
}

func storeRefusal(err error) *Error {
	var exists *store.WorkTargetExistsError
	var taken *store.WorkTargetKeyTakenError
	var stale *store.WorkTargetStaleError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exists):
		e := refuse(409, "target_exists", "A runtime target for this repository exists with another body.")
		e.Current = &exists.Current
		return e
	case errors.As(err, &taken):
		return refuse(409, "key_taken", "The runtime target of "+taken.Holder.Repository+" holds this key.")
	case errors.As(err, &stale):
		e := refuse(412, "revision_stale", "The target changed since that revision.")
		e.Current = &stale.Current
		return e
	case errors.Is(err, store.ErrWorkTargetLimit):
		return refuse(429, "target_limit", "The installation holds its maximum of live runtime targets (targetRegistration.max).")
	case errors.Is(err, store.ErrOperatorNotFound):
		return refuse(404, "unknown_target", "No live runtime target has this key.")
	}
	return refuse(503, "store_unavailable", "The runtime targets could not be written.")
}

// Replace changes a live target's branch or label and probes it again;
// connection, repository and key cannot change (ADR-0193 section 3).
func (s *Service) Replace(ctx context.Context, audit store.WorkTargetAudit, key string, revision int64, branch, label *string) (store.WorkTarget, *Error) {
	if e := s.Accepting(); e != nil {
		return store.WorkTarget{}, e
	}
	current, err := s.Store.LiveWorkTarget(ctx, s.tenant(), key)
	if err != nil {
		return store.WorkTarget{}, storeRefusal(err)
	}
	if current.Revision != revision {
		e := refuse(412, "revision_stale", "The target changed since that revision.")
		e.Current = &current
		return store.WorkTarget{}, e
	}
	next := store.WorkTargetReplacement{Tenant: s.tenant(), Key: current.Key, Revision: revision, Branch: current.Branch, Label: current.Label, Readiness: current.Readiness}
	if branch != nil {
		next.Branch = *branch
	}
	if label != nil {
		next.Label = *label
	}
	if len(next.Label) > MaxLabel {
		return store.WorkTarget{}, refuse(422, "probe_failed", "A label is at most 200 characters.")
	}
	grants, inst, cred, e := s.grantsOf(ctx, current.Connection)
	if e != nil {
		s.refused(ctx, audit, current, e)
		return store.WorkTarget{}, e
	}
	readiness, ok := s.inspect(ctx, grants, inst, cred, current.Repository, next.Branch)
	switch {
	case !ok:
		e = refuse(422, "probe_failed", "The forge could not be asked whether the repository is ready.")
	case !readiness.Ready && !readiness.PrepareAvailable:
		e = refuse(422, "target_not_ready", "The repository is not ready on that branch: "+strings.Join(readiness.Reasons, ", ")+".")
	}
	if e != nil {
		e.Checks = []Check{{Name: "readiness", Result: "fail", Detail: readiness.Reason}}
		s.refused(ctx, audit, current, e)
		return store.WorkTarget{}, e
	}
	next.Readiness = readiness
	out, err := s.Store.ReplaceWorkTarget(ctx, next, audit)
	if e := storeRefusal(err); e != nil {
		return store.WorkTarget{}, e
	}
	if err := s.Rebuild(ctx); err != nil {
		s.log().Warn("runtime targets not rebuilt after a replacement; the poll retries", "err", err)
	}
	return out, nil
}

// Removal is what a removal left behind: the open Work Items pinned to the
// target, whose next claim is refused.
type Removal struct {
	Target    store.WorkTarget
	Removed   bool
	WorkItems []string
}

// Remove removes a live target unless a live binding names its key
// (ADR-0193 section 6). Removing it again changes nothing.
func (s *Service) Remove(ctx context.Context, audit store.WorkTargetAudit, key string) (Removal, *Error) {
	if e := s.Accepting(); e != nil {
		return Removal{}, e
	}
	if uses := s.bindingsNaming(key); len(uses) > 0 {
		e := refuse(409, "target_in_use", "Live tracker bindings name this key; remove or change them first.")
		e.Bindings = uses
		return Removal{}, e
	}
	var pinned []string
	if current, err := s.Store.LiveWorkTarget(ctx, s.tenant(), key); err == nil {
		owner, name := SplitRepository(current.Repository)
		if ids, err := s.Store.OpenWorkItemsOnRepository(ctx, current.Forge, owner, name, 100); err == nil {
			pinned = ids
		}
	}
	out, removed, err := s.Store.RemoveWorkTarget(ctx, s.tenant(), key, audit, map[string]any{"openWorkItems": pinned})
	if errors.Is(err, store.ErrOperatorNotFound) {
		return Removal{}, refuse(404, "unknown_target", "No runtime target ever had this key.")
	}
	if err != nil {
		return Removal{}, refuse(503, "store_unavailable", "The runtime targets could not be written.")
	}
	if removed {
		if err := s.Rebuild(ctx); err != nil {
			s.log().Warn("runtime targets not rebuilt after a removal; the poll retries", "err", err)
		}
	}
	if pinned == nil {
		pinned = []string{}
	}
	return Removal{Target: out, Removed: removed, WorkItems: pinned}, nil
}

func (s *Service) bindingsNaming(key string) []BindingUse {
	if s.Bindings == nil {
		return nil
	}
	var out []BindingUse
	for _, b := range s.Bindings.Bindings() {
		named := strings.EqualFold(b.Default, key)
		for _, a := range b.Allow {
			named = named || strings.EqualFold(a, key)
		}
		if named {
			out = append(out, BindingUse{ID: itoa(b.ID), Team: b.Team})
		}
	}
	return out
}
