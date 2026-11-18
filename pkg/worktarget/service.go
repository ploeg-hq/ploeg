package worktarget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Service registers, merges and reconciles runtime Work Targets.
type Service struct {
	Store       Store
	Config      *config.TargetRegistration
	Connections Connections
	Connectors  map[string]provider.Connector
	Instances   map[string]provider.ConnectionInstance
	// Static are the targets the configuration file registers, by key.
	Static       map[string]work.Target
	DefaultForge string
	// ForgeHosts maps a static forge registry id to its host, so a runtime
	// target on another id for the same forge still meets the static
	// targets it conflicts with.
	ForgeHosts map[string]string
	// Minting names the connection kinds whose connections mint a per-Run
	// credential (ADR-0192). A connection of any other kind cannot register
	// a runtime target: a Run on it would have nothing to push with.
	Minting map[string]bool
	// Gate is the readiness gate the runtime targets join.
	Gate     *target.Gate
	Bindings Bindings
	Tenant   string
	Log      *slog.Logger
	Now      func() time.Time

	mu       sync.RWMutex
	revision int64
	live     []store.WorkTarget
	loaded   bool
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

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Service) staticForge(t work.Target) string {
	if t.Forge == "" {
		return s.DefaultForge
	}
	return t.Forge
}

// SplitRepository splits a forge path into the owner and name a work.Target
// carries: GitLab subgroups stay in the owner.
func SplitRepository(repository string) (owner, name string) {
	i := strings.LastIndex(repository, "/")
	if i < 0 {
		return "", repository
	}
	return repository[:i], repository[i+1:]
}

// WorkTarget is the work.Target a runtime target routes to.
func WorkTarget(t store.WorkTarget) work.Target {
	owner, name := SplitRepository(t.Repository)
	return work.Target{Forge: t.Forge, Owner: owner, Repo: name, BaseBranch: t.Branch}
}

// StaticKeyConflict returns the static key equal to key, compared without
// case: a runtime key may never equal a static key, whatever its spelling.
func (s *Service) StaticKeyConflict(key string) (string, bool) {
	for _, k := range sortedKeys(s.Static) {
		if strings.EqualFold(k, key) {
			return k, true
		}
	}
	return "", false
}

// StaticRepositoryConflict returns the static entry that names repository on
// forge, on any branch, where forge is the same id or the same host.
func (s *Service) StaticRepositoryConflict(forge, repository string) *store.StaticEntry {
	for _, k := range sortedKeys(s.Static) {
		t := s.Static[k]
		if s.SameForge(s.staticForge(t), forge) && strings.EqualFold(t.Owner+"/"+t.Repo, repository) {
			return &store.StaticEntry{Entry: "targets." + k, Repository: t.Owner + "/" + t.Repo}
		}
	}
	return nil
}

// SameForge reports whether two forge ids name one forge: the same id, or
// ids whose instances share a host.
func (s *Service) SameForge(a, b string) bool {
	if a == b {
		return true
	}
	ha, hb := s.forgeHost(a), s.forgeHost(b)
	return ha != "" && ha == hb
}

func (s *Service) forgeHost(id string) string {
	if inst, ok := s.Instances[id]; ok {
		raw := inst.URL
		if raw == "" && inst.Kind == "github" {
			raw = "https://github.com"
		}
		if host := HostOf(raw); host != "" {
			return host
		}
	}
	return strings.ToLower(s.ForgeHosts[id])
}

// HostOf is the lower-case host of a forge URL, with GitHub's API host read
// as github.com.
func HostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "api.github.com" {
		return "github.com"
	}
	return host
}

func (s *Service) staticConflict(t store.WorkTarget) *store.StaticEntry {
	if k, ok := s.StaticKeyConflict(t.Key); ok {
		st := s.Static[k]
		return &store.StaticEntry{Entry: "targets." + k, Repository: st.Owner + "/" + st.Repo}
	}
	return s.StaticRepositoryConflict(t.Forge, t.Repository)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RuntimeTarget implements trackerbinding.RuntimeTargets.
func (s *Service) RuntimeTarget(key string) (trackerbinding.RuntimeTarget, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.live {
		if t.Key == key {
			return trackerbinding.RuntimeTarget{Target: WorkTarget(t), State: t.State, Reason: t.InvalidReason}, true
		}
	}
	return trackerbinding.RuntimeTarget{}, false
}

// ActiveRuntimeTargets implements trackerbinding.RuntimeTargets: the live
// runtime targets in state active, by key.
func (s *Service) ActiveRuntimeTargets() map[string]work.Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]work.Target{}
	for _, t := range s.live {
		if t.State == store.WorkTargetActive {
			out[t.Key] = WorkTarget(t)
		}
	}
	return out
}

// Live returns the live runtime targets as of the last rebuild.
func (s *Service) Live() []store.WorkTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.live)
}

// Counts returns how many live runtime targets are in each state, for
// ploeg_work_targets{state}.
func (s *Service) Counts() map[string]int {
	out := map[string]int{store.WorkTargetActive: 0, store.WorkTargetShadowed: 0, store.WorkTargetInvalid: 0}
	for _, t := range s.Live() {
		out[t.State]++
	}
	return out
}

// Registered names the key a repository on forge is registered under,
// statically first, for the connection repository picker.
func (s *Service) Registered(forge, repository string) (string, bool) {
	for _, k := range sortedKeys(s.Static) {
		t := s.Static[k]
		if s.SameForge(s.staticForge(t), forge) && strings.EqualFold(t.Owner+"/"+t.Repo, repository) {
			return k, true
		}
	}
	for _, t := range s.Live() {
		if t.Forge == forge && strings.EqualFold(t.Repository, repository) {
			return t.Key, true
		}
	}
	return "", false
}

// Rebuild reads the live runtime targets, records the state each is in
// under the static configuration, swaps them into the readiness gate and
// rebuilds the effective routing (ADR-0193 section 4). A store failure keeps
// the targets of the last rebuild, and the static targets are never touched.
func (s *Service) Rebuild(ctx context.Context) error {
	rev, err := s.Store.WorkTargetRevision(ctx)
	if err != nil {
		return fmt.Errorf("runtime targets: read revision: %w", err)
	}
	rows, err := s.Store.LiveWorkTargets(ctx, s.tenant())
	if err != nil {
		return fmt.Errorf("runtime targets: read: %w", err)
	}
	for i, t := range rows {
		next, action, ok := s.stateOf(ctx, t)
		if !ok {
			continue
		}
		changed, err := s.Store.SetWorkTargetState(ctx, t.ID, t, next, action, store.WorkTargetAudit{Actor: "ploegd"}, nil)
		if err != nil {
			s.log().Error("runtime target state not recorded", "target", t.Key, "err", err)
			continue
		}
		if changed {
			s.log().Warn("runtime target state changed", "target", t.Key, "from", t.State, "to", next.State, "reason", next.InvalidReason)
		}
		rows[i].State, rows[i].ShadowedBy, rows[i].InvalidReason = next.State, next.ShadowedBy, next.InvalidReason
	}
	s.install(rev, rows)
	if s.Bindings != nil {
		if err := s.Bindings.Rebuild(ctx); err != nil {
			s.log().Warn("tracker bindings not rebuilt after the runtime targets changed", "err", err)
		}
	}
	return nil
}

// stateOf decides the state a live target is in without asking the forge.
// A static entry shadows it and keeps any invalid reason it had, and when
// the entry goes the target returns to invalid with that reason, or to
// active only when it had none; a fresh probe restores it later. A target
// whose connection is no longer usable is invalid. ok is false when it
// stays as it is.
func (s *Service) stateOf(ctx context.Context, t store.WorkTarget) (store.WorkTargetStateChange, string, bool) {
	usable, known := s.connectionUsable(ctx, t.Connection)
	reason := t.InvalidReason
	if known && !usable && reason == "" {
		reason = store.WorkTargetConnectionUnusable
	}
	if entry := s.staticConflict(t); entry != nil {
		if t.State == store.WorkTargetShadowed && t.ShadowedBy != nil && t.ShadowedBy.Entry == entry.Entry && t.InvalidReason == reason {
			return store.WorkTargetStateChange{}, "", false
		}
		action := store.AuditWorkTargetShadowed
		if t.State == store.WorkTargetShadowed {
			action = store.AuditWorkTargetInvalid
		}
		return store.WorkTargetStateChange{State: store.WorkTargetShadowed, ShadowedBy: entry, InvalidReason: reason}, action, true
	}
	switch {
	case t.State == store.WorkTargetShadowed && reason != "":
		return store.WorkTargetStateChange{State: store.WorkTargetInvalid, InvalidReason: reason}, store.AuditWorkTargetUnshadowed, true
	case t.State == store.WorkTargetShadowed:
		return store.WorkTargetStateChange{State: store.WorkTargetActive}, store.AuditWorkTargetUnshadowed, true
	case t.State == store.WorkTargetActive && reason != "":
		return store.WorkTargetStateChange{State: store.WorkTargetInvalid, InvalidReason: reason}, store.AuditWorkTargetInvalid, true
	}
	return store.WorkTargetStateChange{}, "", false
}

func (s *Service) connectionUsable(ctx context.Context, id string) (usable, known bool) {
	if s.Connections == nil {
		return false, false
	}
	info, found, err := s.Connections.ForgeConnection(ctx, id)
	if err != nil {
		return false, false
	}
	return found && info.Runtime && info.State == ConnectionActive, true
}

func (s *Service) install(rev int64, rows []store.WorkTarget) {
	entries := map[string]target.RuntimeEntry{}
	for _, t := range rows {
		if t.State == store.WorkTargetActive {
			entries[t.Key] = target.RuntimeEntry{Target: WorkTarget(t), Inspector: &connectionInspector{s: s, connection: t.Connection}}
		}
	}
	s.mu.Lock()
	s.revision, s.live, s.loaded = rev, rows, true
	s.mu.Unlock()
	if s.Gate != nil {
		s.Gate.SetRuntime(entries)
	}
}

// Poll rebuilds whenever another replica changed a runtime target, and
// reconciles every target against its forge once an hour, until ctx ends.
func (s *Service) Poll(ctx context.Context) {
	poll := time.NewTicker(PollEvery)
	defer poll.Stop()
	reconcile := time.NewTicker(ReconcileEvery)
	defer reconcile.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			s.PollOnce(ctx)
		case <-reconcile.C:
			s.Reconcile(ctx)
		}
	}
}

// PollOnce rebuilds when the highest target revision changed.
func (s *Service) PollOnce(ctx context.Context) {
	rev, err := s.Store.WorkTargetRevision(ctx)
	if err != nil {
		s.log().Warn("runtime target revision unreadable; routing unchanged", "err", err)
		return
	}
	s.mu.RLock()
	same := s.loaded && rev == s.revision
	s.mu.RUnlock()
	if same {
		return
	}
	if err := s.Rebuild(ctx); err != nil {
		s.log().Warn("runtime target rebuild failed; routing unchanged", "err", err)
	}
}

type connectionInspector struct {
	s          *Service
	connection string
}

func (c *connectionInspector) InspectRepository(ctx context.Context, owner, repo, branch string) (provider.RepositoryState, error) {
	grants, inst, cred, e := c.s.grantsOf(ctx, c.connection)
	if e != nil {
		return provider.RepositoryState{}, errors.New(e.Message)
	}
	return grants.InspectGrantedRepository(ctx, inst, cred, owner+"/"+repo, branch)
}

// grantsOf returns the grant prober, instance and credential of a usable
// runtime forge connection, or the refusal registration answers with.
func (s *Service) grantsOf(ctx context.Context, id string) (provider.RepositoryGrantProber, provider.ConnectionInstance, provider.ConnectionCredential, *Error) {
	grants, inst, cred, _, e := s.connectionGrants(ctx, id)
	return grants, inst, cred, e
}

func (s *Service) connectionGrants(ctx context.Context, id string) (provider.RepositoryGrantProber, provider.ConnectionInstance, provider.ConnectionCredential, ConnectionInfo, *Error) {
	var info ConnectionInfo
	if s.Connections == nil {
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, refuse(404, "unknown_connection", "No forge connection "+id+" exists.")
	}
	info, found, err := s.Connections.ForgeConnection(ctx, id)
	switch {
	case err != nil:
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, refuse(422, "connection_unusable", "The connection could not be read.")
	case !found:
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, refuse(404, "unknown_connection", "No forge connection "+id+" exists.")
	case !info.Runtime:
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, refuse(422, "registration_not_accepted", "A static forge connection cannot grant runtime targets; connect the forge through a store-backed connection (ADR-0195).")
	case info.State != ConnectionActive:
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, refuse(422, "connection_unusable", "The connection is "+info.State+"; reconnect it first.")
	}
	grants := s.Connectors[info.Kind].Grants
	if grants == nil {
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, refuse(422, "capability_missing", "The adapter of kind "+info.Kind+" cannot probe a repository grant (RepositoryGrantProber).")
	}
	cred, err := s.Connections.Credential(ctx, id)
	if err != nil {
		return nil, provider.ConnectionInstance{}, provider.ConnectionCredential{}, info, refuse(422, "connection_unusable", "The connection's credential could not be read.")
	}
	inst, ok := s.Instances[info.Instance]
	if !ok {
		inst = provider.ConnectionInstance{ID: info.Instance, Kind: info.Kind}
	}
	return grants, inst, cred, info, nil
}

func (s *Service) connectionInstance(ctx context.Context, id string) (string, *Error) {
	if s.Connections == nil {
		return "", refuse(404, "unknown_connection", "No forge connection "+id+" exists.")
	}
	info, found, err := s.Connections.ForgeConnection(ctx, id)
	if err != nil {
		return "", refuse(422, "connection_unusable", "The connection could not be read.")
	}
	if !found {
		return "", refuse(404, "unknown_connection", "No forge connection "+id+" exists.")
	}
	return info.Instance, nil
}
