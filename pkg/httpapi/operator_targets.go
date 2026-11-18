package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

// Bounds of a runtime target request (ADR-0193).
const (
	maxTargetBody        = 16 * 1024
	targetProbesPerMin   = 30
	targetProbeWindow    = time.Minute
	targetRequestTimeout = 60 * time.Second
)

// WorkTargets serves the runtime Work Target routes (ADR-0193). Nil, or a
// nil Service, answers every change registration_not_accepted.
type WorkTargets struct {
	Service *worktarget.Service

	mu     sync.Mutex
	probes map[string][]time.Time
}

func (t *WorkTargets) allowProbe(consumer string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.probes == nil {
		t.probes = map[string][]time.Time{}
	}
	recent := t.probes[consumer][:0]
	for _, at := range t.probes[consumer] {
		if now.Sub(at) < targetProbeWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= targetProbesPerMin {
		t.probes[consumer] = recent
		return false
	}
	t.probes[consumer] = append(recent, now)
	return true
}

func (s *Server) registerOperatorTargets(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/targets", s.handleListTargets)
	mux.HandleFunc("POST /api/v1/operator/targets", s.handleRegisterTarget)
	mux.HandleFunc("PUT /api/v1/operator/targets/{key}", s.handleReplaceTarget)
	mux.HandleFunc("DELETE /api/v1/operator/targets/{key}", s.handleRemoveTarget)
}

func isTargetProbeCall(r *http.Request) bool {
	return (r.Method == http.MethodPost || r.Method == http.MethodPut) && strings.HasPrefix(r.URL.Path, "/api/v1/operator/targets")
}

func (s *Server) targetService() *worktarget.Service {
	if s.WorkTargets == nil {
		return nil
	}
	return s.WorkTargets.Service
}

func targetError(w http.ResponseWriter, e *worktarget.Error) {
	body := map[string]any{"code": e.Code, "message": e.Message}
	if len(e.Checks) > 0 {
		body["checks"] = e.Checks
	}
	if e.Static != nil {
		body["static"] = e.Static
	}
	if len(e.Bindings) > 0 {
		body["bindings"] = e.Bindings
	}
	out := map[string]any{"schemaVersion": "1.0", "error": body}
	if e.Current != nil {
		out["target"] = worktarget.RuntimeView(*e.Current)
	}
	operatorJSON(w, e.Status, out)
}

// targetCaller applies the gates of ADR-0193 section 5 to a change: the
// consumer's registerTargets, the installation's opt-in, a proven person in
// every attribution mode, asserted included, and the claims and step-up
// targetRegistration names.
func (s *Server) targetCaller(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, store.WorkTargetAudit, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanRegisterTargets {
		operatorError(w, 403, "permission_required", "Registering Work Targets needs the consumer's registerTargets permission.")
		return p, store.WorkTargetAudit{}, false
	}
	svc := s.targetService()
	if svc == nil {
		operatorError(w, 422, "registration_not_accepted", "This installation does not accept runtime Work Targets (targetRegistration.enabled).")
		return p, store.WorkTargetAudit{}, false
	}
	if e := svc.Accepting(); e != nil {
		targetError(w, e)
		return p, store.WorkTargetAudit{}, false
	}
	person, ok := ProvenPersonFromContext(r.Context())
	if !ok {
		operatorError(w, 403, reasonPersonTokenRequired, "A Work Target is registered, changed or removed only by a proven person, in every attribution mode: send the person's exchanged token. Nothing changed.")
		return p, store.WorkTargetAudit{}, false
	}
	cfg := svc.Config
	if !personHoldsClaims(w, person, cfg.RequireClaims, cfg.StepUp, "targetRegistration.requireClaims") {
		return p, store.WorkTargetAudit{}, false
	}
	a, bad := operatorAttribution(r, p.Name)
	if bad != nil {
		operatorError(w, 400, bad.code, bad.message)
		return p, store.WorkTargetAudit{}, false
	}
	if a.Actor != person.ID {
		operatorError(w, 403, reasonPersonTokenRequired, "The change must be attributed to the proven person.")
		return p, store.WorkTargetAudit{}, false
	}
	return p, store.WorkTargetAudit{Attribution: a}, true
}

// personHoldsClaims answers person_claim_required or an RFC 9470 step-up
// challenge when person lacks a claim value or the authentication required.
func personHoldsClaims(w http.ResponseWriter, person ProvenPerson, required map[string][]string, step *connection.StepUp, section string) bool {
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !claimHoldsOneOf(name, person.claims[name], required[name]) {
			operatorError(w, 403, "person_claim_required", fmt.Sprintf("The person token carries none of the values %s names in its %q claim; nothing changed.", section, name))
			return false
		}
	}
	if missing := unmetStepUp(step, person.claims, time.Now()); missing != "" {
		challenge := `Bearer realm="ploeg-operator", error="insufficient_user_authentication", error_description="` + missing + `"`
		if len(step.AcrValues) > 0 {
			challenge += `, acr_values="` + strings.Join(step.AcrValues, " ") + `"`
		}
		if step.MaxAge > 0 {
			challenge += `, max_age=` + strconv.Itoa(step.MaxAge)
		}
		w.Header().Set("WWW-Authenticate", challenge)
		operatorError(w, 401, "insufficient_user_authentication", "The person must authenticate again: "+missing+".")
		return false
	}
	return true
}

// targetsVisible is which keys a consumer without registerTargets may read:
// the targets its Teams' routes and bindings reach (ADR-0193 section 5).
func (s *Server) targetsVisible(p OperatorPrincipal) func(string) bool {
	if p.CanRegisterTargets {
		return nil
	}
	keys := map[string]bool{}
	if s.TrackerBindings != nil && s.TrackerBindings.Registry != nil {
		for _, route := range s.TrackerBindings.Registry.StaticRoutes() {
			if route.Rule.Team != "" && !p.AllowsTeam(route.Rule.Team) {
				continue
			}
			if route.Rule.Team == "" && !p.AllowsTeam("*") {
				continue
			}
			keys[strings.ToLower(route.Rule.Default)] = true
			for _, a := range route.Rule.Allow {
				keys[strings.ToLower(a)] = true
			}
		}
		for _, b := range s.TrackerBindings.Registry.Bindings() {
			if p.AllowsTeam(b.Team) {
				keys[strings.ToLower(b.Default)] = true
				for _, a := range b.Allow {
					keys[strings.ToLower(a)] = true
				}
			}
		}
	}
	return func(key string) bool { return keys[strings.ToLower(key)] }
}

func (s *Server) handleListTargets(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	p, _ := OperatorPrincipalFromContext(r.Context())
	views := []worktarget.View{}
	if svc := s.targetService(); svc != nil {
		views = svc.List(s.targetsVisible(p))
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "targets": views})
}

type registerTargetRequest struct {
	Connection string `json:"connection"`
	Repository string `json:"repository"`
	Branch     string `json:"branch,omitempty"`
	Key        string `json:"key,omitempty"`
	Label      string `json:"label,omitempty"`
}

type replaceTargetRequest struct {
	Branch *string `json:"branch,omitempty"`
	Label  *string `json:"label,omitempty"`
}

func decodeTarget(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		operatorError(w, 415, "json_required", "Use application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTargetBody))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			operatorError(w, 413, "too_large", "A Work Target request is at most 16 KiB.")
			return false
		}
		operatorError(w, 400, "invalid_request", "The target request is invalid. A target names a connection and a repository as owner/name, with an optional branch, key and label; any field it does not know is refused.")
		return false
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		operatorError(w, 400, "invalid_request", "Supply one JSON object.")
		return false
	}
	return true
}

// validTargetText refuses a raw URL, a clone address or anything shaped
// like a token in any field, rather than ignoring it (ADR-0193 section 3).
func validTargetText(w http.ResponseWriter, req registerTargetRequest) bool {
	for _, v := range []string{req.Connection, req.Repository, req.Branch, req.Key, req.Label} {
		if worktarget.LooksLikeSecret(v) || strings.Contains(v, "@") && v != req.Label {
			operatorError(w, 400, "invalid_request", "A target request carries no URL, clone address or credential.")
			return false
		}
	}
	switch {
	case !operatorName.MatchString(req.Connection):
		operatorError(w, 400, "invalid_request", "connection is a forge connection id.")
	case !worktarget.ValidRepository(req.Repository):
		operatorError(w, 400, "invalid_request", "repository is the forge path owner/name (group/subgroup/project on GitLab), never a URL.")
	case req.Branch != "" && !worktarget.ValidBranch(req.Branch):
		operatorError(w, 400, "invalid_request", "branch is not a valid branch name.")
	default:
		return true
	}
	return false
}

func (s *Server) handleRegisterTarget(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunQuery(w, r)
	if !ok {
		return
	}
	p, audit, ok := s.targetCaller(w, r)
	if !ok {
		return
	}
	var req registerTargetRequest
	if !decodeTarget(w, r, &req) || !validTargetText(w, req) {
		return
	}
	if !s.WorkTargets.allowProbe(p.Name, time.Now()) {
		operatorError(w, 429, "probe_rate_limited", "This consumer probed too many repositories in the last minute.")
		return
	}
	in := worktarget.Request{Connection: req.Connection, Repository: req.Repository, Branch: req.Branch, Key: req.Key, Label: req.Label}
	svc := s.targetService()
	if dryRun {
		s.answerTargetDryRun(w, r.Context(), svc, in)
		return
	}
	t, created, e := svc.Register(r.Context(), audit, in)
	if e != nil {
		targetError(w, e)
		return
	}
	status := 200
	if created {
		status = 201
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(t.Revision, 10)))
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "target": worktarget.RuntimeView(t), "created": created})
}

func (s *Server) answerTargetDryRun(w http.ResponseWriter, ctx context.Context, svc *worktarget.Service, in worktarget.Request) {
	plan := svc.Probe(ctx, in)
	body := map[string]any{"schemaVersion": "1.0", "dryRun": true, "wouldRegister": plan.Refusal == nil && plan.Existing == nil,
		"exists": plan.Existing != nil, "checks": plan.Checks, "target": worktarget.RuntimeView(plan.Target),
		"prepareAvailable": plan.Target.Readiness.PrepareAvailable}
	if plan.Existing != nil {
		body["target"] = worktarget.RuntimeView(*plan.Existing)
	}
	if plan.Refusal != nil {
		body["refusal"] = map[string]any{"status": plan.Refusal.Status, "code": plan.Refusal.Code, "message": plan.Refusal.Message}
	}
	operatorJSON(w, 200, body)
}

func (s *Server) handleReplaceTarget(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	_, audit, ok := s.targetCaller(w, r)
	if !ok {
		return
	}
	key, ok := pathName(w, r, "key")
	if !ok {
		return
	}
	match := r.Header.Get("If-Match")
	if match == "" {
		operatorError(w, 428, "if_match_required", "Send If-Match: \"<revision>\".")
		return
	}
	m := bindingRevision.FindStringSubmatch(match)
	if m == nil {
		operatorError(w, 412, "revision_stale", "If-Match is not a revision of this target.")
		return
	}
	revision, _ := strconv.ParseInt(m[1], 10, 64)
	var req replaceTargetRequest
	if !decodeTarget(w, r, &req) {
		return
	}
	if req.Branch != nil && !worktarget.ValidBranch(*req.Branch) || req.Label != nil && worktarget.LooksLikeSecret(*req.Label) {
		operatorError(w, 400, "invalid_request", "branch is a branch name and label is free text without a URL or credential.")
		return
	}
	t, e := s.targetService().Replace(r.Context(), audit, key, revision, req.Branch, req.Label)
	if e != nil {
		targetError(w, e)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(t.Revision, 10)))
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "target": worktarget.RuntimeView(t)})
}

func (s *Server) handleRemoveTarget(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	_, audit, ok := s.targetCaller(w, r)
	if !ok {
		return
	}
	key, ok := pathName(w, r, "key")
	if !ok {
		return
	}
	removal, e := s.targetService().Remove(r.Context(), audit, key)
	if e != nil {
		targetError(w, e)
		return
	}
	if removal.Removed && len(removal.WorkItems) > 0 {
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "target": worktarget.RuntimeView(removal.Target), "openWorkItems": removal.WorkItems})
		return
	}
	w.WriteHeader(204)
}

func (s *Server) renderWorkTargetMetrics() []byte {
	svc := s.targetService()
	if svc == nil {
		return nil
	}
	var b metricWriter
	b.family("ploeg_work_targets", "gauge", "Live runtime Work Targets (ADR-0193) by state, as of this replica's last rebuild.")
	counts := svc.Counts()
	for _, state := range sortedKeys(counts) {
		b.sample("ploeg_work_targets", float64(counts[state]), "state", state)
	}
	return b.Bytes()
}
