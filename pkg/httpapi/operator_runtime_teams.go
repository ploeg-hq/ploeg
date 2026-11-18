package httpapi

import (
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/teamruntime"
)

func (s *Server) registerOperatorRuntimeTeams(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/teams", s.handleCreateTeam)
	mux.HandleFunc("PUT /api/v1/operator/teams/{team}", s.handleReplaceTeam)
	mux.HandleFunc("DELETE /api/v1/operator/teams/{team}", s.handleRemoveTeam)
	mux.HandleFunc("POST /api/v1/operator/teams/{team}/undrain", s.handleUndrainTeam)
	mux.HandleFunc("DELETE /api/v1/operator/teams/{team}/aliases/{alias}", s.handleReleaseTeamAlias)
}

func runtimeTeamJSON(t store.RuntimeTeam) map[string]any {
	view := map[string]any{
		"id": t.Key, "teamId": t.ID, "key": t.Key, "name": t.Name,
		"nameSource": team.NameSource{Source: team.NameFromOperatorAPI},
		"source":     team.SourceRuntime, "state": t.State, "revision": strconv.FormatInt(t.Revision, 10),
		"manifest": t.Manifest, "paused": nil, "queueDepth": 0,
		"roles": runtimeRoles(t.Manifest), "assignees": []string{}, "pinnedScopes": []string{}, "spendCap": nil,
	}
	if len(t.Aliases) > 0 {
		view["aliases"] = t.Aliases
	}
	if t.ShadowedBy != "" {
		view["shadowedBy"] = t.ShadowedBy
	}
	if t.InvalidReason != "" {
		view["invalidReason"] = t.InvalidReason
	}
	if d := t.DrainDeadline(); d != nil {
		view["drainDeadline"] = d.UTC().Format(time.RFC3339)
	}
	return view
}

func runtimeRoles(m team.Manifest) []map[string]any {
	out := []map[string]any{}
	for _, r := range m.Roles() {
		out = append(out, map[string]any{"id": r.Name, "queueDepth": 0,
			"harness": map[string]any{"name": r.Harness, "profile": nil}, "model": r.Model, "defaultModel": r.Model, "models": []string{r.Model}})
	}
	return out
}

func teamRefusal(w http.ResponseWriter, e *teamruntime.Error) {
	body := map[string]any{"code": e.Code, "message": e.Message}
	if e.Field != "" {
		body["field"] = e.Field
	}
	if e.ReservedUntil != nil {
		body["reservedUntil"] = e.ReservedUntil.UTC().Format(time.RFC3339)
	}
	if e.Headroom != nil {
		body["headroomUsd"] = *e.Headroom
	}
	if e.InUse != nil {
		body["inUse"] = e.InUse
	}
	out := map[string]any{"schemaVersion": "1.0", "error": body}
	if e.Current != nil && e.Current.Revision > 0 {
		out["team"] = runtimeTeamJSON(*e.Current)
	}
	operatorJSON(w, e.Status, out)
}

// teamCaller applies the gates of ADR-0198 section 8: the consumer's
// manageTeams, the installation's opt-in, and for any change but a name a
// proven person in every attribution mode, with the claims teamCreation
// names.
func (s *Server) teamCaller(w http.ResponseWriter, r *http.Request, nameOnly bool) (store.TeamAudit, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanManageTeams {
		operatorError(w, 403, "permission_required", "Creating, changing or removing Teams needs the consumer's manageTeams permission.")
		return store.TeamAudit{}, false
	}
	if e := s.RuntimeTeams.Accepting(); e != nil {
		teamRefusal(w, e)
		return store.TeamAudit{}, false
	}
	a, bad := operatorAttribution(r, p.Name)
	if bad != nil {
		operatorError(w, 400, bad.code, bad.message)
		return store.TeamAudit{}, false
	}
	if nameOnly {
		if a.Actor == "" {
			operatorError(w, 400, "actor_required", "An authenticated actor identity is required.")
			return store.TeamAudit{}, false
		}
		return store.TeamAudit{Attribution: a}, true
	}
	person, ok := ProvenPersonFromContext(r.Context())
	if !ok {
		operatorError(w, 403, reasonPersonTokenRequired, "A Team spends money, so it is created, changed or removed only by a proven person, in every attribution mode: send the person's exchanged token. Nothing changed.")
		return store.TeamAudit{}, false
	}
	cfg := s.RuntimeTeams.Config
	if !personHoldsClaims(w, person, cfg.RequireClaims, cfg.StepUp, "teamCreation.requireClaims") {
		return store.TeamAudit{}, false
	}
	if a.Actor != person.ID {
		operatorError(w, 403, reasonPersonTokenRequired, "The change must be attributed to the proven person.")
		return store.TeamAudit{}, false
	}
	return store.TeamAudit{Attribution: a}, true
}

func (s *Server) auditTeamRefusal(r *http.Request, audit store.TeamAudit, id, key string, e *teamruntime.Error) {
	_ = s.Store.AuditTeamRefusal(r.Context(), audit.Attribution, id, key, e.Code, map[string]any{"field": e.Field})
}

func readTeamBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, teamBodyLimit+1))
	if err != nil || len(raw) > teamBodyLimit {
		operatorError(w, 413, "invalid_request", "A Team body is at most 64 KiB.")
		return nil, false
	}
	return raw, true
}

func dryRunOf(w http.ResponseWriter, r *http.Request, allowed ...string) (bool, bool) {
	q := r.URL.Query()
	for k := range q {
		if k != "dryRun" && !slices.Contains(allowed, k) {
			operatorError(w, 400, "invalid_request", "Unknown query parameter "+k+".")
			return false, false
		}
	}
	switch q.Get("dryRun") {
	case "", "false":
		return false, true
	case "true":
		return true, true
	}
	operatorError(w, 400, "invalid_request", "dryRun is true or false.")
	return false, false
}

func ifMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		operatorError(w, 428, "if_match_required", "Send If-Match with the Team's revision.")
		return 0, false
	}
	rev, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		operatorError(w, 412, "stale_revision", "If-Match is not a revision of this Team.")
		return 0, false
	}
	return rev, true
}

func dryRunJSON(d *teamruntime.DryRun) map[string]any {
	out := map[string]any{"schemaVersion": "1.0", "dryRun": true, "manifest": d.Manifest, "state": d.Decision.State, "headroomUsd": d.Headroom}
	if d.Decision.InvalidReason != "" {
		out["invalidReason"] = d.Decision.InvalidReason
	}
	return out
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunOf(w, r)
	if !ok {
		return
	}
	audit, ok := s.teamCaller(w, r, false)
	if !ok {
		return
	}
	raw, ok := readTeamBody(w, r)
	if !ok {
		return
	}
	out, created, preview, e, err := s.RuntimeTeams.Create(r.Context(), raw, dryRun, audit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if e != nil {
		if !dryRun {
			s.auditTeamRefusal(r, audit, "", "", e)
		}
		teamRefusal(w, e)
		return
	}
	if preview != nil {
		operatorJSON(w, 200, dryRunJSON(preview))
		return
	}
	status := 200
	if created {
		status = 201
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(out.Revision, 10)+`"`)
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "team": runtimeTeamJSON(out), "created": created})
}

func (s *Server) runtimeTeamRef(w http.ResponseWriter, r *http.Request) (team.Team, bool) {
	t, _, ok := s.resolveTeamRef(w, r)
	if !ok {
		return t, false
	}
	if t.Source == team.SourceStatic {
		operatorError(w, 409, "team_is_static", "Team "+t.Key+" is configured in Ploeg's configuration file; change it there.")
		return t, false
	}
	return t, true
}

func (s *Server) handleReplaceTeam(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunOf(w, r)
	if !ok {
		return
	}
	audit, ok := s.teamCaller(w, r, false)
	if !ok {
		return
	}
	t, ok := s.runtimeTeamRef(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	raw, ok := readTeamBody(w, r)
	if !ok {
		return
	}
	out, preview, e, err := s.RuntimeTeams.Replace(r.Context(), t.ID, rev, raw, dryRun, audit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if e != nil {
		if !dryRun {
			s.auditTeamRefusal(r, audit, t.ID, t.Key, e)
		}
		teamRefusal(w, e)
		return
	}
	if preview != nil {
		operatorJSON(w, 200, dryRunJSON(preview))
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(out.Revision, 10)+`"`)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "team": runtimeTeamJSON(out)})
}

func (s *Server) patchRuntimeName(w http.ResponseWriter, r *http.Request, t team.Team, name string) {
	audit, ok := s.teamCaller(w, r, true)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	out, e, err := s.RuntimeTeams.Rename(r.Context(), t.ID, rev, name, audit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if e != nil {
		teamRefusal(w, e)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(out.Revision, 10)+`"`)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "team": runtimeTeamJSON(out)})
}

func (s *Server) handleRemoveTeam(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "drain" {
			operatorError(w, 400, "invalid_request", "Removing a Team accepts only drain.")
			return
		}
	}
	drain := q.Get("drain") == "true"
	audit, ok := s.teamCaller(w, r, false)
	if !ok {
		return
	}
	ref := r.PathValue("team")
	t, _, found := s.Teams.Resolve(ref)
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if t.Source == team.SourceStatic {
		operatorError(w, 409, "team_is_static", "Team "+t.Key+" is configured in Ploeg's configuration file; remove it there.")
		return
	}
	draining, e, err := s.RuntimeTeams.Remove(r.Context(), t, drain, audit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if e != nil {
		s.auditTeamRefusal(r, audit, t.ID, t.Key, e)
		teamRefusal(w, e)
		return
	}
	if draining != nil && draining.DrainingSince != nil {
		operatorJSON(w, 202, map[string]any{"schemaVersion": "1.0", "team": runtimeTeamJSON(*draining)})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUndrainTeam(w http.ResponseWriter, r *http.Request) {
	audit, ok := s.teamCaller(w, r, false)
	if !ok {
		return
	}
	t, ok := s.runtimeTeamRef(w, r)
	if !ok {
		return
	}
	out, e, err := s.RuntimeTeams.Undrain(r.Context(), t.ID, audit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if e != nil {
		teamRefusal(w, e)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "team": runtimeTeamJSON(out)})
}

func (s *Server) handleReleaseTeamAlias(w http.ResponseWriter, r *http.Request) {
	audit, ok := s.teamCaller(w, r, false)
	if !ok {
		return
	}
	t, ok := s.runtimeTeamRef(w, r)
	if !ok {
		return
	}
	out, e, err := s.RuntimeTeams.ReleaseAlias(r.Context(), t.ID, r.PathValue("alias"), audit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if e != nil {
		teamRefusal(w, e)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "team": runtimeTeamJSON(out)})
}

// withRuntimeTeams replaces each runtime Team's row of the Teams read with
// its runtime view and adds the live runtime Teams the consumer may read.
func (s *Server) withRuntimeTeams(r *http.Request, p OperatorPrincipal, teams []store.OperatorTeam) []any {
	out := make([]any, 0, len(teams))
	depth := map[string]int64{}
	for _, t := range teams {
		if t.Source == team.SourceRuntime {
			depth[t.TeamID] = t.QueueDepth
			continue
		}
		out = append(out, t)
	}
	if s.RuntimeTeams == nil || s.Teams == nil {
		return out
	}
	for _, t := range s.Teams.All() {
		if t.Source != team.SourceRuntime || !p.allowsResolvedTeam(t) {
			continue
		}
		rt, err := s.RuntimeTeams.Team(r.Context(), t.ID)
		if err != nil {
			continue
		}
		view := runtimeTeamJSON(rt)
		view["queueDepth"] = depth[t.ID]
		out = append(out, view)
	}
	return out
}
