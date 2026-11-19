package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Bounds of a tracker binding request (ADR-0186).
const (
	maxBindingBody      = 64 * 1024
	maxBindingLabel     = 200
	maxBindingScopeID   = 256
	maxBindingScopeName = 512
	maxBindingAllow     = 50
	maxBindingStatus    = 200
	bindingProbesPerMin = 30
	bindingProbeWindow  = time.Minute
	bindingTimeout      = 30 * time.Second
)

// bindingStatusStates are the states a binding's statuses may name.
var bindingStatusStates = []string{string(work.StateAwaitingReview), string(work.StateNeedsHuman), string(work.StateDone)}

var bindingRevision = regexp.MustCompile(`^"([1-9][0-9]{0,18})"$`)

// TrackerBindings serves the tracker binding routes (ADR-0186). Nil answers
// every one of them 404.
type TrackerBindings struct {
	Registry *trackerbinding.Registry

	mu     sync.Mutex
	probes map[string][]time.Time
}

// allowProbe is the per-consumer limit on probing calls, so binding attempts
// cannot hammer a tracker.
func (t *TrackerBindings) allowProbe(consumer string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.probes == nil {
		t.probes = map[string][]time.Time{}
	}
	recent := t.probes[consumer][:0]
	for _, at := range t.probes[consumer] {
		if now.Sub(at) < bindingProbeWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= bindingProbesPerMin {
		t.probes[consumer] = recent
		return false
	}
	t.probes[consumer] = append(recent, now)
	return true
}

func (s *Server) registerOperatorTrackerBindings(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/tracker-bindings", s.handleListTrackerBindings)
	mux.HandleFunc("GET /api/v1/operator/teams/{team}/tracker-bindings", s.handleListTrackerBindings)
	mux.HandleFunc("POST /api/v1/operator/teams/{team}/tracker-bindings", s.handleCreateTrackerBinding)
	mux.HandleFunc("PUT /api/v1/operator/teams/{team}/tracker-bindings/{id}", s.handleReplaceTrackerBinding)
	mux.HandleFunc("DELETE /api/v1/operator/teams/{team}/tracker-bindings/{id}", s.handleRemoveTrackerBinding)
	mux.HandleFunc("POST /api/v1/operator/teams/{team}/tracker-bindings/{id}/pause", s.handlePauseTrackerBinding(true))
	mux.HandleFunc("POST /api/v1/operator/teams/{team}/tracker-bindings/{id}/resume", s.handlePauseTrackerBinding(false))
}

type bindingScope struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type bindingFieldsRequest struct {
	Default  string            `json:"default,omitempty"`
	Allow    []string          `json:"allow,omitempty"`
	Statuses map[string]string `json:"statuses,omitempty"`
	Label    string            `json:"label,omitempty"`
}

type createBindingRequest struct {
	Connection string        `json:"connection"`
	Scope      *bindingScope `json:"scope"`
	bindingFieldsRequest
}

func operatorErrorWith(w http.ResponseWriter, status int, code, message string, extra map[string]any) {
	body := map[string]any{"schemaVersion": "1.0", "error": map[string]string{"code": code, "message": message}}
	for k, v := range extra {
		body[k] = v
	}
	operatorJSON(w, status, body)
}

// bindingCaller checks the gates every binding route shares: the Team's
// name, the consumer's Team scope and, for a change, bindScopes and an
// actor. It answers the refusal itself.
func (s *Server) bindingCaller(w http.ResponseWriter, r *http.Request, team string, change bool) (OperatorPrincipal, store.Attribution, bool) {
	return s.bindingCallerFor(w, r, team, change, true)
}

// bindingCallerFor is bindingCaller; knownTeam false lets a binding of a Team
// the file no longer has be removed.
func (s *Server) bindingCallerFor(w http.ResponseWriter, r *http.Request, team string, change, knownTeam bool) (OperatorPrincipal, store.Attribution, bool) {
	p, _ := OperatorPrincipalFromContext(r.Context())
	if change && !p.CanBindScopes {
		operatorError(w, 403, "permission_required", "This consumer may not bind tracker Scopes; it needs bindScopes.")
		return p, store.Attribution{}, false
	}
	if team != "" && !operatorName.MatchString(team) {
		operatorError(w, 400, "invalid_request", "Invalid team identifier.")
		return p, store.Attribution{}, false
	}
	if team != "" && !s.allowsTeamKey(p, team) {
		operatorError(w, 403, "team_not_allowed", "This team is outside the consumer scope.")
		return p, store.Attribution{}, false
	}
	if s.TrackerBindings == nil || s.TrackerBindings.Registry == nil {
		operatorError(w, 404, "not_found", "This deployment serves no tracker bindings.")
		return p, store.Attribution{}, false
	}
	if team != "" && knownTeam {
		if !s.TrackerBindings.Registry.KnowsTeam(team) {
			operatorError(w, 404, "unknown_team", "No Team "+team+" is configured.")
			return p, store.Attribution{}, false
		}
	}
	if !change {
		return p, store.Attribution{}, true
	}
	a, bad := operatorAttribution(r, p.Name)
	if bad != nil {
		operatorError(w, 400, bad.code, bad.message)
		return p, a, false
	}
	if a.Actor == "" {
		operatorError(w, 400, "actor_required", "An authenticated actor identity is required.")
		return p, a, false
	}
	return p, a, true
}

func (s *Server) handleListTrackerBindings(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "Tracker bindings does not accept query parameters.")
		return
	}
	p, _, ok := s.bindingCaller(w, r, team, false)
	if !ok {
		return
	}
	reg := s.TrackerBindings.Registry
	visible := func(t string) bool { return (team == "" || t == team) && p.AllowsTeam(t) }
	out := []map[string]any{}
	for _, route := range reg.StaticRoutes() {
		if route.Rule.Team != "" && !visible(route.Rule.Team) {
			continue
		}
		out = append(out, staticBindingJSON(route))
	}
	for _, b := range reg.Bindings() {
		if visible(b.Team) {
			out = append(out, bindingJSON(b))
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "bindings": out})
}

func decodeBinding(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		operatorError(w, 415, "json_required", "Use application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBindingBody))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			operatorError(w, 413, "too_large", "A tracker binding request is at most 64 KiB.")
			return false
		}
		operatorError(w, 400, "invalid_request", "The binding request is invalid. A binding names a connection, a scope and registered target keys in default and allow; a raw repo, branch or forge is refused, as is any field it does not know.")
		return false
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		operatorError(w, 400, "invalid_request", "Supply one JSON object.")
		return false
	}
	return true
}

// fields validates and normalises the destination and options of a binding.
func (f bindingFieldsRequest) fields() (store.TrackerBindingFields, string) {
	out := store.TrackerBindingFields{Default: f.Default, Label: f.Label, Statuses: map[string]string{}}
	if f.Default != "" && !operatorName.MatchString(f.Default) {
		return out, "default must be a registered target key."
	}
	if len(f.Allow) > maxBindingAllow {
		return out, "allow names at most 50 target keys."
	}
	for _, key := range f.Allow {
		if !operatorName.MatchString(key) {
			return out, "allow must hold registered target keys."
		}
	}
	out.Allow = slices.Compact(slices.Sorted(slices.Values(f.Allow)))
	if out.Default == "" && len(out.Allow) == 0 {
		return out, "A binding needs a default target or allowed targets."
	}
	for state, name := range f.Statuses {
		name = strings.TrimSpace(name)
		if !slices.Contains(bindingStatusStates, state) || name == "" || len(name) > maxBindingStatus || strings.Contains(name, ",") {
			return out, "statuses maps awaiting_review, needs_human and done to tracker status names of 1 to 200 characters."
		}
		out.Statuses[state] = name
	}
	if len(f.Label) > maxBindingLabel {
		return out, "label is at most 200 characters."
	}
	return out, ""
}

func (s *Server) bindingConnection(ctx context.Context, w http.ResponseWriter, team, id string) (trackerbinding.Connection, bool) {
	reg := s.TrackerBindings.Registry
	if s.runtimeTeamDraining(team) {
		operatorError(w, 409, "team_draining", "Runtime Team "+team+" is draining and takes no new binding.")
		return trackerbinding.Connection{}, false
	}
	policy, accepts := reg.TeamPolicy(team)
	if !accepts {
		operatorError(w, 422, "bindings_not_accepted", "Team "+team+" accepts no runtime tracker bindings: its manifest names no trackerBindings.")
		return trackerbinding.Connection{}, false
	}
	var c trackerbinding.Connection
	ok := false
	if reg.Connections != nil {
		c, ok = reg.Connections.TrackerConnection(ctx, id)
	}
	if !ok {
		operatorError(w, 422, "unknown_connection", "No tracker connection "+id+" is configured.")
		return c, false
	}
	if !policy.Accepts(c.Ref()) {
		operatorError(w, 422, "bindings_not_accepted", "Team "+team+" accepts no bindings through connection "+id+": its trackerBindings.connections does not name it.")
		return c, false
	}
	return c, true
}

func dryRunQuery(w http.ResponseWriter, r *http.Request) (bool, bool) {
	if r.URL.RawQuery == "" {
		return false, true
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["dryRun"]) != 1 || (q.Get("dryRun") != "true" && q.Get("dryRun") != "false") {
		operatorError(w, 400, "invalid_request", "The only query parameter is dryRun=true or dryRun=false.")
		return false, false
	}
	return q.Get("dryRun") == "true", true
}

func (s *Server) handleCreateTrackerBinding(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	dryRun, ok := dryRunQuery(w, r)
	if !ok {
		return
	}
	p, attribution, ok := s.bindingCaller(w, r, team, true)
	if !ok {
		return
	}
	var in createBindingRequest
	if !decodeBinding(w, r, &in) {
		return
	}
	fields, bad := in.fields()
	if bad == "" && !operatorName.MatchString(in.Connection) {
		bad = "connection must name a tracker connection."
	}
	if bad == "" && (in.Scope == nil || (in.Scope.ID == "") == (in.Scope.Name == "") ||
		len(in.Scope.ID) > maxBindingScopeID || len(in.Scope.Name) > maxBindingScopeName ||
		strings.TrimSpace(in.Scope.ID) != in.Scope.ID) {
		bad = "scope is {\"id\": …} or {\"name\": …}, exactly one of them."
	}
	if bad != "" {
		operatorError(w, 400, "invalid_request", bad)
		return
	}
	conn, ok := s.bindingConnection(r.Context(), w, team, in.Connection)
	if !ok {
		return
	}
	if !s.TrackerBindings.allowProbe(p.Name, time.Now()) {
		operatorError(w, 429, "probe_rate_limited", "This consumer probes tracker Scopes too often; try again in a minute.")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), bindingTimeout)
	defer cancel()
	reg := s.TrackerBindings.Registry
	audit := store.BindingAudit{Attribution: attribution}
	key := store.TrackerBinding{Team: team, Connection: conn.ID, ScopeID: in.Scope.ID}
	if existing := liveByKey(reg, key); existing != nil && !dryRun {
		s.answerExisting(ctx, w, *existing, fields, audit)
		return
	}
	res := reg.Probe(ctx, trackerbinding.Request{Team: team, Connection: conn, ScopeID: in.Scope.ID, ScopeName: in.Scope.Name,
		Fields: fields, Assignees: s.OperatorConfig.TeamAssignees[team]})
	key.ScopeID = res.ScopeID
	proposed := store.TrackerBinding{Team: team, Connection: conn.ID, ScopeID: res.ScopeID, ScopeName: res.ScopeName,
		TrackerBindingFields: fields, State: store.BindingAwaitingWebhook, Webhook: res.Webhook}
	if res.Webhook.State == provider.WebhookPresent {
		proposed.State = store.BindingActive
	}
	if dryRun {
		s.answerDryRun(w, reg, team, proposed, res)
		return
	}
	if res.Refusal != "" {
		s.refuseBinding(ctx, w, key, res, audit)
		return
	}
	if existing := liveByKey(reg, key); existing != nil {
		s.answerExisting(ctx, w, *existing, fields, audit)
		return
	}
	policy, _ := reg.TeamPolicy(team)
	webhook, state, fresh := reg.Wire(ctx, conn, res.ScopeID, res.Webhook)
	b, created, err := s.Store.CreateTrackerBinding(ctx, store.NewTrackerBinding{Team: team, Connection: conn.ID, ScopeID: res.ScopeID,
		ScopeName: res.ScopeName, TrackerBindingFields: fields, State: state, Webhook: webhook, Max: policy.Limit()}, audit)
	if fresh && (err != nil || !created) {
		reg.Unwire(ctx, store.TrackerBinding{ID: -1, Connection: conn.ID, ScopeID: res.ScopeID, Webhook: webhook})
	}
	if s.answerStoreError(ctx, w, key, err, audit) {
		return
	}
	s.rebuildBindings(ctx)
	if !created {
		s.answerExisting(ctx, w, b, fields, audit)
		return
	}
	s.Log.Info("tracker binding created", "binding", b.ID, "team", team, "connection", conn.ID, "scope", b.ScopeID, "state", b.State, "consumer", p.Name)
	w.Header().Set("ETag", `"`+strconv.FormatInt(b.Revision, 10)+`"`)
	operatorJSON(w, 201, map[string]any{"schemaVersion": "1.0", "binding": bindingJSON(b), "created": true, "checks": checksJSON(res.Checks)})
}

func liveByKey(reg *trackerbinding.Registry, key store.TrackerBinding) *store.TrackerBinding {
	if key.ScopeID == "" {
		return nil
	}
	for _, b := range reg.Bindings() {
		if b.Team == key.Team && b.Connection == key.Connection && b.ScopeID == key.ScopeID {
			return &b
		}
	}
	return nil
}

func (s *Server) answerExisting(ctx context.Context, w http.ResponseWriter, b store.TrackerBinding, fields store.TrackerBindingFields, audit store.BindingAudit) {
	if !b.TrackerBindingFields.Same(fields) {
		s.auditRefusal(ctx, b, "binding_exists", []any{}, audit)
		operatorErrorWith(w, 409, "binding_exists", "A live binding with this connection, Scope and Team exists with another body; replace it with its revision.",
			map[string]any{"binding": bindingJSON(b)})
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(b.Revision, 10)+`"`)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "binding": bindingJSON(b), "created": false, "checks": []any{}})
}

func (s *Server) answerDryRun(w http.ResponseWriter, reg *trackerbinding.Registry, team string, proposed store.TrackerBinding, res trackerbinding.Result) {
	refusal := any(nil)
	switch {
	case res.Refusal != "":
		refusal = map[string]any{"code": res.Refusal, "message": res.Message}
	case liveByKey(reg, proposed) != nil && !liveByKey(reg, proposed).TrackerBindingFields.Same(proposed.TrackerBindingFields):
		refusal = map[string]any{"code": "binding_exists", "message": "A live binding with this connection, Scope and Team exists with another body."}
	case liveByKey(reg, proposed) == nil && s.liveCount(team) >= s.teamLimit(team):
		refusal = map[string]any{"code": "binding_limit", "message": "The Team holds its maximum of live tracker bindings."}
	}
	body := map[string]any{"schemaVersion": "1.0", "dryRun": true, "refusal": refusal,
		"binding": bindingJSON(proposed), "checks": checksJSON(res.Checks)}
	if res.Conflict != nil {
		body["conflict"] = conflictJSON(res.Conflict)
	}
	operatorJSON(w, 200, body)
}

func (s *Server) liveCount(team string) int {
	n := 0
	for _, b := range s.TrackerBindings.Registry.Bindings() {
		if b.Team == team {
			n++
		}
	}
	return n
}

func (s *Server) teamLimit(team string) int {
	policy, _ := s.TrackerBindings.Registry.TeamPolicy(team)
	return policy.Limit()
}

var refusalStatus = map[string]int{
	trackerbinding.RefuseUnknownTarget:       422,
	trackerbinding.RefuseConflictsWithStatic: 409,
	trackerbinding.RefuseScopeBoundElsewhere: 409,
	trackerbinding.RefuseCapabilityMissing:   422,
	trackerbinding.RefuseProbeFailed:         422,
	trackerbinding.RefuseTargetNotReady:      422,
}

func (s *Server) refuseBinding(ctx context.Context, w http.ResponseWriter, key store.TrackerBinding, res trackerbinding.Result, audit store.BindingAudit) {
	failed := checksJSON(res.Failed())
	s.auditRefusal(ctx, key, res.Refusal, failed, audit)
	extra := map[string]any{"checks": checksJSON(res.Checks)}
	if res.Conflict != nil {
		extra["conflict"] = conflictJSON(res.Conflict)
	}
	if res.Elsewhere != nil {
		extra["conflict"] = map[string]any{"source": "runtime", "entry": nil, "connection": res.Elsewhere.Connection}
	}
	operatorErrorWith(w, refusalStatus[res.Refusal], res.Refusal, res.Message, extra)
}

func (s *Server) auditRefusal(ctx context.Context, key store.TrackerBinding, code string, failed any, audit store.BindingAudit) {
	if err := s.Store.AuditTrackerBinding(ctx, store.AuditBindingRefused, key, audit, map[string]any{"code": code, "failedChecks": failed}); err != nil {
		s.Log.Error("tracker binding refusal audit failed", "team", key.Team, "err", err)
	}
}

func (s *Server) answerStoreError(ctx context.Context, w http.ResponseWriter, key store.TrackerBinding, err error, audit store.BindingAudit) bool {
	if err == nil {
		return false
	}
	var exists *store.BindingExistsError
	var elsewhere *store.ScopeBoundElsewhereError
	var stale *store.StaleRevisionError
	switch {
	case errors.As(err, &exists):
		s.auditRefusal(ctx, key, "binding_exists", []any{}, audit)
		operatorErrorWith(w, 409, "binding_exists", "A live binding with this connection, Scope and Team exists with another body; replace it with its revision.",
			map[string]any{"binding": bindingJSON(exists.Current)})
	case errors.As(err, &elsewhere):
		s.auditRefusal(ctx, key, "scope_bound_elsewhere", []any{}, audit)
		operatorErrorWith(w, 409, "scope_bound_elsewhere", "The Scope is bound through connection "+elsewhere.Other.Connection+".",
			map[string]any{"conflict": map[string]any{"source": "runtime", "entry": nil, "connection": elsewhere.Other.Connection}})
	case errors.Is(err, store.ErrBindingLimit):
		s.auditRefusal(ctx, key, "binding_limit", []any{}, audit)
		operatorError(w, 429, "binding_limit", "The Team holds its maximum of live tracker bindings (trackerBindings.max).")
	case errors.As(err, &stale):
		w.Header().Set("ETag", `"`+strconv.FormatInt(stale.Current.Revision, 10)+`"`)
		operatorErrorWith(w, 412, "stale_revision", "The binding changed since that revision; read it again.",
			map[string]any{"binding": bindingJSON(stale.Current)})
	case errors.Is(err, store.ErrOperatorNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
	default:
		s.Log.Error("tracker binding write failed", "team", key.Team, "err", err)
		operatorError(w, 503, "unavailable", "Ploeg could not store the binding.")
	}
	return true
}

func (s *Server) rebuildBindings(ctx context.Context) {
	if err := s.TrackerBindings.Registry.Rebuild(ctx); err != nil {
		s.Log.Error("routing rebuild after a binding change failed; the next poll retries", "err", err)
	}
}

func bindingID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != r.PathValue("id") {
		operatorError(w, 400, "invalid_request", "Invalid binding identifier.")
		return 0, false
	}
	return id, true
}

func (s *Server) handleReplaceTrackerBinding(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "This resource does not accept query parameters.")
		return
	}
	p, attribution, ok := s.bindingCaller(w, r, team, true)
	if !ok {
		return
	}
	id, ok := bindingID(w, r)
	if !ok {
		return
	}
	match := r.Header.Values("If-Match")
	if len(match) == 0 {
		operatorError(w, 428, "if_match_required", `Send If-Match: "<revision>" with the binding's current revision.`)
		return
	}
	m := bindingRevision.FindStringSubmatch(strings.TrimSpace(match[0]))
	if len(match) != 1 || m == nil {
		operatorError(w, 400, "invalid_request", `If-Match is one quoted binding revision, such as "17".`)
		return
	}
	revision, _ := strconv.ParseInt(m[1], 10, 64)
	var in bindingFieldsRequest
	if !decodeBinding(w, r, &in) {
		return
	}
	fields, bad := in.fields()
	if bad != "" {
		operatorError(w, 400, "invalid_request", bad)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), bindingTimeout)
	defer cancel()
	audit := store.BindingAudit{Attribution: attribution}
	current, err := s.Store.TrackerBindingByID(ctx, team, id)
	if err == nil && current.RemovedAt != nil {
		err = store.ErrOperatorNotFound
	}
	if err == nil && current.Revision != revision {
		err = &store.StaleRevisionError{Current: current}
	}
	if s.answerStoreError(ctx, w, current, err, audit) {
		return
	}
	conn, ok := s.bindingConnection(ctx, w, team, current.Connection)
	if !ok {
		return
	}
	if !s.TrackerBindings.allowProbe(p.Name, time.Now()) {
		operatorError(w, 429, "probe_rate_limited", "This consumer probes tracker Scopes too often; try again in a minute.")
		return
	}
	reg := s.TrackerBindings.Registry
	res := reg.Probe(ctx, trackerbinding.Request{Team: team, Connection: conn, ScopeID: current.ScopeID, Fields: fields,
		Assignees: s.OperatorConfig.TeamAssignees[team], Except: current.ID})
	if res.Refusal != "" {
		s.refuseBinding(ctx, w, current, res, audit)
		return
	}
	webhook := res.Webhook
	if current.Webhook.Source == store.BindingWebhookRegistered {
		webhook.ID, webhook.Source = current.Webhook.ID, current.Webhook.Source
		if webhook.State == "unchecked" {
			webhook.State = current.Webhook.State
		}
	}
	state := store.BindingAwaitingWebhook
	if webhook.State == provider.WebhookPresent {
		state = store.BindingActive
	}
	b, err := s.Store.ReplaceTrackerBinding(ctx, store.BindingReplacement{Team: team, ID: id, Revision: revision,
		TrackerBindingFields: fields, State: state, Webhook: webhook}, audit)
	if s.answerStoreError(ctx, w, current, err, audit) {
		return
	}
	s.rebuildBindings(ctx)
	s.Log.Info("tracker binding replaced", "binding", b.ID, "team", team, "revision", b.Revision, "consumer", p.Name)
	w.Header().Set("ETag", `"`+strconv.FormatInt(b.Revision, 10)+`"`)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "binding": bindingJSON(b), "created": false, "checks": checksJSON(res.Checks)})
}

func (s *Server) handleRemoveTrackerBinding(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "This resource does not accept query parameters.")
		return
	}
	p, attribution, ok := s.bindingCallerFor(w, r, team, true, false)
	if !ok {
		return
	}
	id, ok := bindingID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), bindingTimeout)
	defer cancel()
	reg := s.TrackerBindings.Registry
	audit := store.BindingAudit{Attribution: attribution}
	b, removed, err := s.Store.RemoveTrackerBinding(ctx, team, id, audit, func(b store.TrackerBinding) map[string]any {
		return map[string]any{"webhook": reg.UnwirePlan(ctx, b)}
	})
	if s.answerStoreError(ctx, w, store.TrackerBinding{Team: team, ID: id}, err, audit) {
		return
	}
	if removed {
		if reg.Unwire(ctx, b) {
			if err := s.Store.AuditTrackerBinding(ctx, store.AuditBindingWebhookRemoved, b, audit, map[string]any{"webhookId": b.Webhook.ID}); err != nil {
				s.Log.Error("tracker binding webhook removal audit failed", "binding", b.ID, "err", err)
			}
		}
		s.rebuildBindings(ctx)
		s.Log.Info("tracker binding removed", "binding", b.ID, "team", team, "consumer", p.Name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePauseTrackerBinding pauses or resumes a runtime binding (ADR-0204)
// under the gates its removal has. It answers 200 with the binding and
// whether this call changed it, so a retry is safe.
func (s *Server) handlePauseTrackerBinding(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		team := r.PathValue("team")
		if r.URL.RawQuery != "" {
			operatorError(w, 400, "invalid_request", "This resource does not accept query parameters.")
			return
		}
		p, attribution, ok := s.bindingCallerFor(w, r, team, true, false)
		if !ok {
			return
		}
		id, ok := bindingID(w, r)
		if !ok {
			return
		}
		if r.ContentLength != 0 {
			operatorError(w, 400, "invalid_request", "Pausing or resuming a binding takes no body.")
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), bindingTimeout)
		defer cancel()
		audit := store.BindingAudit{Attribution: attribution}
		b, changed, err := s.Store.SetTrackerBindingPaused(ctx, team, id, paused, audit)
		if s.answerStoreError(ctx, w, store.TrackerBinding{Team: team, ID: id}, err, audit) {
			return
		}
		if changed {
			s.rebuildBindings(ctx)
			s.Log.Info("tracker binding pause changed", "binding", b.ID, "team", team, "paused", paused, "consumer", p.Name)
		}
		w.Header().Set("ETag", `"`+strconv.FormatInt(b.Revision, 10)+`"`)
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "binding": bindingJSON(b), "changed": changed})
	}
}

func checksJSON(checks []trackerbinding.Check) []map[string]any {
	out := make([]map[string]any, 0, len(checks))
	for _, c := range checks {
		code := any(nil)
		if c.Code != "" && !c.OK {
			code = c.Code
		}
		out = append(out, map[string]any{"check": c.Name, "ok": c.OK, "code": code, "message": c.Message})
	}
	return out
}

func conflictJSON(e *store.StaticEntry) map[string]any {
	return map[string]any{"source": "static", "entry": e.Entry, "team": nullString(e.Team), "repository": nullString(e.Repository),
		"default": nullString(e.Default), "allow": nonNil(e.Allow)}
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func bindingJSON(b store.TrackerBinding) map[string]any {
	id, revision, created, updated := any(nil), any(nil), any(nil), any(nil)
	if b.ID > 0 {
		id, revision = strconv.FormatInt(b.ID, 10), strconv.FormatInt(b.Revision, 10)
		created, updated = b.CreatedAt.UTC().Format(time.RFC3339Nano), b.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	statuses := any(nil)
	if len(b.Statuses) > 0 {
		statuses = b.Statuses
	}
	shadowed := any(nil)
	if b.ShadowedBy != nil {
		shadowed = conflictJSON(b.ShadowedBy)
	}
	invalid := any(nil)
	if b.InvalidDetail != nil {
		invalid = map[string]any{"target": nullString(b.InvalidDetail.Target), "targetState": nullString(b.InvalidDetail.TargetState),
			"targetReason": nullString(b.InvalidDetail.TargetReason)}
	}
	webhookState := b.Webhook.State
	if webhookState == "" {
		webhookState = "unchecked"
	}
	return map[string]any{
		"id": id, "source": "runtime", "entry": nil, "team": b.Team, "connection": b.Connection,
		"scope":   map[string]any{"id": nullString(b.ScopeID), "name": nullString(b.ScopeName)},
		"default": nullString(b.Default), "allow": nonNil(b.Allow), "repository": nil,
		"statuses": statuses, "label": nullString(b.Label), "state": b.State,
		"shadowedBy": shadowed, "invalidReason": nullString(b.InvalidReason), "invalidDetail": invalid,
		"webhook": map[string]any{"state": webhookState, "id": nullString(b.Webhook.ID),
			"createdByBinding": b.Webhook.Source == store.BindingWebhookRegistered},
		"revision": revision, "createdBy": nullString(b.CreatedBy), "updatedBy": nullString(b.UpdatedBy),
		"createdAt": created, "updatedAt": updated,
		"paused": b.Paused(), "pausedAt": pausedAt(b), "pausedBy": nullString(b.PausedBy),
	}
}

func pausedAt(b store.TrackerBinding) any {
	if !b.Paused() {
		return nil
	}
	return b.PausedAt.UTC().Format(time.RFC3339Nano)
}

func staticBindingJSON(route trackerbinding.StaticRoute) map[string]any {
	rule := route.Rule
	return map[string]any{
		"id": nil, "source": "static", "entry": route.Entry.Entry, "team": nullString(rule.Team), "connection": nullString(rule.Tracker),
		"scope":   map[string]any{"id": rule.Scope, "name": nil},
		"default": nullString(rule.Default), "allow": nonNil(rule.Allow), "repository": nullString(route.Entry.Repository),
		"statuses": nil, "label": nil, "state": store.BindingActive,
		"shadowedBy": nil, "invalidReason": nil, "invalidDetail": nil, "webhook": nil,
		"revision": nil, "createdBy": nil, "updatedBy": nil, "createdAt": nil, "updatedAt": nil,
		"paused": false, "pausedAt": nil, "pausedBy": nil,
	}
}

func (s *Server) renderBindingMetrics() []byte {
	if s.TrackerBindings == nil || s.TrackerBindings.Registry == nil {
		return nil
	}
	var b metricWriter
	b.family("ploeg_tracker_bindings", "gauge", "Live runtime tracker bindings (ADR-0186) by state, as of this replica's last rebuild.")
	counts := s.TrackerBindings.Registry.Counts()
	for _, state := range sortedKeys(counts) {
		b.sample("ploeg_tracker_bindings", float64(counts[state]), "state", state)
	}
	return b.Bytes()
}

// allowsTeamKey reports whether the consumer may use the Team key names: by
// its Team scope, or for a runtime Team through manageTeams (ADR-0198).
func (s *Server) allowsTeamKey(p OperatorPrincipal, key string) bool {
	if p.AllowsTeam(key) {
		return true
	}
	t, how, ok := s.Teams.Resolve(key)
	return ok && how == team.ByKey && p.allowsResolvedTeam(t)
}

// runtimeTeamDraining reports whether key names a runtime Team whose drain
// started.
func (s *Server) runtimeTeamDraining(key string) bool {
	t, how, ok := s.Teams.Resolve(key)
	return ok && how == team.ByKey && t.Source == team.SourceRuntime && t.Draining
}
