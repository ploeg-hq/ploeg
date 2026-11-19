// Package trackerbinding keeps the runtime Scope bindings of ADR-0186: it
// merges them under the static Routing Rules, decides each binding's state,
// probes a binding before it is stored, and swaps the routing table ploegd
// resolves from after every change.
package trackerbinding

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// PollEvery is how often a replica reads the highest binding revision and
// rebuilds when it changed (ADR-0186 section 4).
const PollEvery = 30 * time.Second

// WebhookCheckEvery is how often bindings without a confirmed webhook are
// checked again.
const WebhookCheckEvery = time.Hour

// Connection is a tracker connection a binding names (ADR-0186 section 1). A
// static connection is a configured tracker, named by its kind today; a
// store-backed connection (ADR-0195) comes from another Connections source
// with Runtime set.
type Connection struct {
	ID      string
	Kind    string
	Runtime bool
	// State is the connection's own state; a static connection is active.
	State   string
	Tracker provider.TrackerProvider
	// Agents are the tracker users whose assignment hands work over
	// (trackers.clickup.agents).
	Agents []string
	// HandOverAgent makes the connection's own account a hand-over agent for
	// the Scopes bound through it (ADR-0195 section 9).
	HandOverAgent bool
	// WebhookEndpoint is the URL a webhook for this connection delivers to;
	// empty accepts any on the tracker's webhook path.
	WebhookEndpoint string
	// RegisterWebhooks lets Ploeg register a webhook where a Scope has none.
	RegisterWebhooks bool
	// Secrets keeps the signing secret of a webhook Ploeg registers; nil
	// leaves such a binding awaiting_webhook.
	Secrets provider.WebhookSecretSink
}

// ConnectionActive is the state of a usable connection.
const ConnectionActive = "active"

// Ref is what a Team's trackerBindings matches.
func (c Connection) Ref() config.ConnectionRef {
	return config.ConnectionRef{ID: c.ID, Kind: c.Kind, Runtime: c.Runtime, Active: c.State == ConnectionActive}
}

// Connections finds a tracker connection by id. This is the seam ADR-0195's
// store-backed connections plug into, keyed by connection id.
type Connections interface {
	TrackerConnection(ctx context.Context, id string) (Connection, bool)
}

// StaticConnections are the configured tracker connections, by id.
type StaticConnections map[string]Connection

// TrackerConnection implements Connections.
func (s StaticConnections) TrackerConnection(_ context.Context, id string) (Connection, bool) {
	c, ok := s[id]
	return c, ok
}

// Chain looks a connection up in each source in turn.
type Chain []Connections

// TrackerConnection implements Connections.
func (c Chain) TrackerConnection(ctx context.Context, id string) (Connection, bool) {
	for _, src := range c {
		if src == nil {
			continue
		}
		if conn, ok := src.TrackerConnection(ctx, id); ok {
			return conn, true
		}
	}
	return Connection{}, false
}

// RuntimeTarget is a Work Target registered at runtime (ADR-0193), as the
// effective registry sees it.
type RuntimeTarget struct {
	Target work.Target
	State  string
	Reason string
}

// RuntimeTargetActive is the state of a runtime target in the effective
// registry; RuntimeTargetRemoved never returns to it.
const (
	RuntimeTargetActive  = "active"
	RuntimeTargetRemoved = "removed"
)

// RuntimeTargets is the seam ADR-0193's runtime targets plug into. Nil means
// the effective registry is the static targets alone.
type RuntimeTargets interface {
	RuntimeTarget(key string) (RuntimeTarget, bool)
	ActiveRuntimeTargets() map[string]work.Target
}

// Store is the persistence a Registry needs.
type Store interface {
	LiveTrackerBindings(ctx context.Context) ([]store.TrackerBinding, error)
	TrackerBindingRevision(ctx context.Context) (int64, error)
	SetTrackerBindingState(ctx context.Context, id int64, from string, to store.BindingStateChange, action string) (bool, error)
	SetTrackerBindingWebhook(ctx context.Context, id int64, webhookState string) error
	TrackerBindingWebhookUsers(ctx context.Context, connection, webhookID string, except int64) (int, error)
	RegisteredTrackerBindingWebhook(ctx context.Context, connection, scope string) (string, error)
}

// Registry is the effective routing of one ploegd: the static rules first,
// then the live bindings, swapped into Live after every change.
type Registry struct {
	Store  Store
	Config *config.File
	// Static is the routing table the configuration declares, with its
	// rules' origins and the static target registry.
	Static       target.Table
	DefaultForge string
	Connections  Connections
	Runtime      RuntimeTargets
	// Teams resolves a binding's Team id (ADR-0198). Nil decides by the
	// configuration file's Teams alone.
	Teams func(id string) (team.Team, bool)
	// TeamByKey resolves a Team's current key (ADR-0198). Nil knows only the
	// configuration file's Teams.
	TeamByKey func(key string) (team.Team, bool)
	// Readiness gates the static targets.
	Readiness *target.Gate
	Live      *target.Live
	Log       *slog.Logger

	rebuilding sync.Mutex
	mu         sync.RWMutex
	revision   int64
	bindings   []store.TrackerBinding
	pins       map[string]string
	statuses   map[string]map[string]map[work.State]string
	loaded     bool
}

// Rebuild reads the live bindings, records the state each one is in now,
// builds the effective routing table and swaps it in. When the bindings
// cannot be read the static rules alone stay in force and the error is
// returned (ADR-0186: a store outage never takes the static routes down).
func (r *Registry) Rebuild(ctx context.Context) error {
	r.rebuilding.Lock()
	defer r.rebuilding.Unlock()
	rev, err := r.Store.TrackerBindingRevision(ctx)
	if err != nil {
		r.swapStaticOnly()
		return fmt.Errorf("tracker bindings: read revision: %w", err)
	}
	live, err := r.Store.LiveTrackerBindings(ctx)
	if err != nil {
		r.swapStaticOnly()
		return fmt.Errorf("tracker bindings: read: %w", err)
	}
	for i, b := range live {
		next := r.StateOf(ctx, b)
		if sameState(b, next) {
			continue
		}
		action := transitionEvent(b.State, next.State)
		changed, err := r.Store.SetTrackerBindingState(ctx, b.ID, b.State, next, action)
		if err != nil {
			r.log().Error("tracker binding state not recorded", "binding", b.ID, "err", err)
			continue
		}
		if changed {
			r.logTransition(b, next)
		}
		live[i].State, live[i].ShadowedBy, live[i].InvalidReason, live[i].InvalidDetail = next.State, next.ShadowedBy, next.InvalidReason, next.InvalidDetail
	}
	r.install(rev, live)
	return nil
}

func sameState(b store.TrackerBinding, next store.BindingStateChange) bool {
	if b.State != next.State || b.InvalidReason != next.InvalidReason {
		return false
	}
	if (b.ShadowedBy == nil) != (next.ShadowedBy == nil) || (b.ShadowedBy != nil && b.ShadowedBy.Entry != next.ShadowedBy.Entry) {
		return false
	}
	if (b.InvalidDetail == nil) != (next.InvalidDetail == nil) {
		return false
	}
	return b.InvalidDetail == nil || *b.InvalidDetail == *next.InvalidDetail
}

func transitionEvent(from, to string) string {
	switch {
	case from == to:
		return ""
	case to == store.BindingShadowed:
		return store.AuditBindingShadowed
	case to == store.BindingInvalid:
		return store.AuditBindingInvalid
	case from == store.BindingShadowed:
		return store.AuditBindingUnshadowed
	case from == store.BindingInvalid:
		return store.AuditBindingRestored
	}
	return ""
}

func (r *Registry) logTransition(b store.TrackerBinding, next store.BindingStateChange) {
	switch next.State {
	case store.BindingShadowed:
		r.log().Warn("tracker binding shadowed by a static route; it routes nothing until the static route is removed",
			"binding", b.ID, "team", b.Team, "scope", b.ScopeID, "static_entry", next.ShadowedBy.Entry)
	case store.BindingInvalid:
		r.log().Warn("tracker binding invalid; it routes nothing", "binding", b.ID, "team", b.Team, "scope", b.ScopeID, "reason", next.InvalidReason)
	default:
		r.log().Info("tracker binding back in force", "binding", b.ID, "team", b.Team, "scope", b.ScopeID, "state", next.State)
	}
}

// StateOf decides the state a live binding is in under the current static
// configuration and effective registry (ADR-0186 section 4).
func (r *Registry) StateOf(ctx context.Context, b store.TrackerBinding) store.BindingStateChange {
	if state, decided := r.teamState(b); decided {
		return state
	}
	if b.State == store.BindingInvalid && b.InvalidDetail != nil && b.InvalidDetail.TargetState == RuntimeTargetRemoved {
		return store.BindingStateChange{State: store.BindingInvalid, InvalidReason: b.InvalidReason, InvalidDetail: b.InvalidDetail}
	}
	for _, key := range destinationKeys(b.Default, b.Allow) {
		if r.Known(key) {
			continue
		}
		detail := &store.InvalidDetail{Target: key}
		if r.Runtime != nil {
			if rt, ok := r.Runtime.RuntimeTarget(key); ok {
				detail.TargetState, detail.TargetReason = rt.State, rt.Reason
			}
		}
		return store.BindingStateChange{State: store.BindingInvalid, InvalidReason: store.BindingTargetUnavailable, InvalidDetail: detail}
	}
	kind := b.Connection
	if c, ok := r.connection(ctx, b.Connection); ok {
		kind = c.Kind
	}
	if entry := r.StaticConflict(kind, b.ScopeID, b.Team); entry != nil {
		return store.BindingStateChange{State: store.BindingShadowed, ShadowedBy: entry}
	}
	if b.Webhook.State == provider.WebhookPresent {
		return store.BindingStateChange{State: store.BindingActive}
	}
	return store.BindingStateChange{State: store.BindingAwaitingWebhook}
}

func destinationKeys(def string, allow []string) []string {
	keys := []string{}
	if def != "" {
		keys = append(keys, def)
	}
	return append(keys, allow...)
}

// Known reports whether key is in the effective registry: the static
// targets plus the runtime targets in state active.
func (r *Registry) Known(key string) bool {
	if _, ok := r.Static.Targets[key]; ok {
		return true
	}
	if r.Runtime == nil {
		return false
	}
	_, ok := r.Runtime.ActiveRuntimeTargets()[key]
	return ok
}

// StaticConflict returns the static route a binding of scope for team
// through a connection of kind conflicts with: the same Scope for the same
// Team, the same Scope with no Team, or the same Scope on another tracker.
func (r *Registry) StaticConflict(kind, scope, team string) *store.StaticEntry {
	for _, rule := range r.Static.Rules {
		if rule.Scope != scope {
			continue
		}
		if rule.Team == "" || rule.Team == team || (rule.Tracker != "" && rule.Tracker != kind) {
			return staticEntry(rule)
		}
	}
	return nil
}

func staticEntry(rule target.Rule) *store.StaticEntry {
	e := &store.StaticEntry{Entry: rule.Origin, Team: rule.Team, Default: rule.Default, Allow: rule.Allow}
	if e.Entry == "" {
		e.Entry = "PLOEG_TARGET_MAP " + rule.ID()
	}
	if rule.Target.Resolved() {
		e.Repository = rule.Target.Owner + "/" + rule.Target.Repo
	}
	return e
}

func (r *Registry) connection(ctx context.Context, id string) (Connection, bool) {
	if r.Connections == nil {
		return Connection{}, false
	}
	return r.Connections.TrackerConnection(ctx, id)
}

func (r *Registry) swapStaticOnly() {
	r.mu.RLock()
	loaded := r.loaded
	r.mu.RUnlock()
	if loaded {
		return
	}
	r.install(0, nil)
}

func (r *Registry) install(rev int64, live []store.TrackerBinding) {
	table := target.Table{Rules: slices.Clone(r.Static.Rules), Targets: r.effectiveTargets()}
	pins := map[string]string{}
	statuses := map[string]map[string]map[work.State]string{}
	routedTeams := map[string][]string{}
	staticScopes := map[string]bool{}
	for _, rule := range r.Static.Rules {
		staticScopes[rule.Scope] = true
	}
	for _, b := range live {
		if !Routed(b.State) {
			continue
		}
		table.Rules = append(table.Rules, target.Rule{Scope: b.ScopeID, Team: b.Team, Default: b.Default, Allow: slices.Clone(b.Allow),
			Origin: fmt.Sprintf("tracker_bindings[%d]", b.ID)})
		routedTeams[b.ScopeID] = append(routedTeams[b.ScopeID], b.Team)
		if m := statusMap(b.Statuses); len(m) > 0 {
			if statuses[b.Connection] == nil {
				statuses[b.Connection] = map[string]map[work.State]string{}
			}
			statuses[b.Connection][b.ScopeID] = m
		}
	}
	for scope, teams := range routedTeams {
		if len(teams) == 1 && !staticScopes[scope] {
			pins[scope] = teams[0]
		}
	}
	resolver, err := target.New(table, r.DefaultForge)
	if err != nil {
		r.log().Error("effective routing table refused; the static rules alone stay in force", "err", err)
		resolver, err = target.New(target.Table{Rules: r.Static.Rules, Targets: r.Static.Targets}, r.DefaultForge)
		if err != nil {
			r.log().Error("static routing table refused", "err", err)
			return
		}
		pins, statuses = map[string]string{}, map[string]map[string]map[work.State]string{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded && rev < r.revision {
		return
	}
	r.revision, r.bindings, r.pins, r.statuses, r.loaded = rev, live, pins, statuses, true
	if r.Live != nil {
		r.Live.Store(resolver)
	}
}

func (r *Registry) effectiveTargets() map[string]work.Target {
	if r.Runtime == nil {
		return r.Static.Targets
	}
	out := map[string]work.Target{}
	for k, v := range r.Runtime.ActiveRuntimeTargets() {
		out[k] = v
	}
	for k, v := range r.Static.Targets {
		out[k] = v
	}
	return out
}

// Routed reports whether a binding in state routes work: active, and
// awaiting_webhook so that a delivery that does arrive is not lost.
func Routed(state string) bool {
	return state == store.BindingActive || state == store.BindingAwaitingWebhook
}

func statusMap(statuses map[string]string) map[work.State]string {
	out := map[work.State]string{}
	for k, v := range statuses {
		if state, ok := work.ParseState(k); ok && v != "" {
			out[state] = v
		}
	}
	return out
}

// Poll rebuilds whenever another replica changed a binding, and rechecks the
// webhooks of bindings still awaiting one, until ctx ends.
func (r *Registry) Poll(ctx context.Context) {
	poll := time.NewTicker(PollEvery)
	defer poll.Stop()
	hooks := time.NewTicker(WebhookCheckEvery)
	defer hooks.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			r.pollOnce(ctx)
		case <-hooks.C:
			r.RecheckWebhooks(ctx)
		}
	}
}

func (r *Registry) pollOnce(ctx context.Context) {
	rev, err := r.Store.TrackerBindingRevision(ctx)
	if err != nil {
		r.log().Warn("tracker binding revision unreadable; routing unchanged", "err", err)
		return
	}
	r.mu.RLock()
	same := rev == r.revision && r.loaded
	r.mu.RUnlock()
	if same {
		return
	}
	if err := r.Rebuild(ctx); err != nil {
		r.log().Warn("tracker binding rebuild failed; routing unchanged", "err", err)
	}
}

// RecheckWebhooks asks each connection that can check webhooks about the
// Scopes of its bindings whose webhook is not confirmed, and moves them
// between awaiting_webhook and active with the answer.
func (r *Registry) RecheckWebhooks(ctx context.Context) {
	byConnection := map[string][]store.TrackerBinding{}
	for _, b := range r.Bindings() {
		if Routed(b.State) {
			byConnection[b.Connection] = append(byConnection[b.Connection], b)
		}
	}
	changed := false
	for id, bindings := range byConnection {
		c, ok := r.connection(ctx, id)
		if !ok {
			continue
		}
		checker, ok := c.Tracker.(provider.WebhookChecker)
		if !ok {
			continue
		}
		scopes := make([]string, 0, len(bindings))
		for _, b := range bindings {
			scopes = append(scopes, b.ScopeID)
		}
		found := map[string]string{}
		for _, f := range checker.CheckWebhooks(ctx, scopes, c.WebhookEndpoint) {
			found[f.Scope] = f.Webhook
		}
		for _, b := range bindings {
			state, ok := found[b.ScopeID]
			if !ok || state == b.Webhook.State {
				continue
			}
			if err := r.Store.SetTrackerBindingWebhook(ctx, b.ID, state); err != nil {
				r.log().Error("tracker binding webhook state not recorded", "binding", b.ID, "err", err)
				continue
			}
			changed = true
		}
	}
	if changed {
		if err := r.Rebuild(ctx); err != nil {
			r.log().Warn("tracker binding rebuild failed", "err", err)
		}
	}
}

// Bindings returns the live bindings as of the last rebuild.
func (r *Registry) Bindings() []store.TrackerBinding {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.bindings)
}

// Counts returns how many live bindings are in each state, for
// ploeg_tracker_bindings{state}.
func (r *Registry) Counts() map[string]int {
	out := map[string]int{store.BindingActive: 0, store.BindingAwaitingWebhook: 0, store.BindingShadowed: 0, store.BindingInvalid: 0}
	for _, b := range r.Bindings() {
		out[b.State]++
	}
	return out
}

// ScopeTeam is the Team the one routed binding of a Scope pins its work to,
// like a static project's team; a Scope a static rule covers, or that
// several Teams bind, pins nothing.
func (r *Registry) ScopeTeam(scope string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	team, ok := r.pins[scope]
	return team, ok
}

// Paused reports whether a routed binding of a Scope for team is paused, as
// of the last rebuild, so an assignment it would route is refused instead of
// admitted or left to fall through to another route. A paused binding stays
// in the routing table, its pin, its statuses and its connection, so work it
// admitted before the pause runs and writes back as before.
func (r *Registry) Paused(scope, team string) bool {
	if scope == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.bindings {
		if b.ScopeID == scope && b.Team == team && Routed(b.State) && b.Paused() {
			return true
		}
	}
	return false
}

// RoutedConnection is the connection the routed bindings of a Scope name, as
// of the last rebuild. A Scope is bound through one connection at most
// (scope_bound_elsewhere). A Scope any static rule names has none, also
// when a binding for another Team routes it, so a static route never gains
// a connection's hand-over agent or credential (ADR-0195 section 9).
func (r *Registry) RoutedConnection(scope string) (string, bool) {
	for _, rule := range r.Static.Rules {
		if rule.Scope == scope {
			return "", false
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.bindings {
		if b.ScopeID == scope && Routed(b.State) {
			return b.Connection, true
		}
	}
	return "", false
}

// RegisteredWebhook reports whether a live binding relies on a webhook Ploeg
// registered with this id, as of the last rebuild, so a delivery naming any
// other id is not verified with a stored secret.
func (r *Registry) RegisteredWebhook(id string) bool {
	if id == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.bindings {
		if b.Webhook.ID == id && b.Webhook.Source == store.BindingWebhookRegistered {
			return true
		}
	}
	return false
}

// ScopeStatuses returns the status names the binding of a Scope through a
// connection overrides the connection's map with.
func (r *Registry) ScopeStatuses(connection string) func(scope string) map[work.State]string {
	return func(scope string) map[work.State]string {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.statuses[connection][scope]
	}
}

// StaticRoute is one static Routing Rule as the list routes show it.
type StaticRoute struct {
	Rule  target.Rule
	Entry *store.StaticEntry
}

// StaticRoutes returns the static rules, Team routes first then by origin.
func (r *Registry) StaticRoutes() []StaticRoute {
	out := make([]StaticRoute, 0, len(r.Static.Rules))
	for _, rule := range r.Static.Rules {
		out = append(out, StaticRoute{Rule: rule, Entry: staticEntry(rule)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Entry.Entry < out[j].Entry.Entry })
	return out
}

func (r *Registry) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

func normalIdentity(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// teamState decides a binding by its Team: a runtime Team that is shadowed
// or invalid makes it invalid with team_unavailable (ADR-0198 section 13); a
// Team in neither the file nor the runtime directory makes it invalid with
// team_removed. decided false lets the other checks run.
func (r *Registry) teamState(b store.TrackerBinding) (store.BindingStateChange, bool) {
	if r.Teams != nil && b.TeamID != "" {
		if t, ok := r.Teams(b.TeamID); ok && t.Source == team.SourceRuntime {
			if t.State != team.StateActive {
				return store.BindingStateChange{State: store.BindingInvalid, InvalidReason: store.BindingTeamUnavailable}, true
			}
			return store.BindingStateChange{}, false
		}
	}
	if _, ok := r.Config.Teams[b.Team]; !ok {
		return store.BindingStateChange{State: store.BindingInvalid, InvalidReason: store.BindingTeamRemoved}, true
	}
	return store.BindingStateChange{}, false
}

// TeamPolicy is the binding policy of the Team key names: its
// teams.<key>.trackerBindings, or for an active runtime Team the
// installation's teamCreation.trackerBindings. false accepts no binding.
func (r *Registry) TeamPolicy(key string) (config.TrackerBindings, bool) {
	if policy, ok := r.Config.TeamTrackerBindings(key); ok {
		return policy, true
	}
	if _, static := r.Config.Teams[key]; static || r.TeamByKey == nil || r.Config.TeamCreation == nil || r.Config.TeamCreation.TrackerBindings == nil {
		return config.TrackerBindings{}, false
	}
	t, ok := r.TeamByKey(key)
	if !ok || t.Source != team.SourceRuntime || t.State != team.StateActive || t.Draining {
		return config.TrackerBindings{}, false
	}
	return *r.Config.TeamCreation.TrackerBindings, true
}

// KnowsTeam reports whether key names a Team in the file or a live runtime
// Team.
func (r *Registry) KnowsTeam(key string) bool {
	if _, ok := r.Config.Teams[key]; ok {
		return true
	}
	if r.TeamByKey == nil {
		return false
	}
	t, ok := r.TeamByKey(key)
	return ok && t.Source == team.SourceRuntime
}
