package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/teamruntime"
)

const teamBodyLimit = 64 * 1024

func (s *Server) registerOperatorTeams(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/teams/{team}", s.handleOperatorTeam)
	mux.HandleFunc("PATCH /api/v1/operator/teams/{team}", s.handlePatchTeam)
	s.registerOperatorRuntimeTeams(mux)
}

func (s *Server) identifyTeams(teams []store.OperatorTeam) {
	if s.Teams == nil {
		return
	}
	for i := range teams {
		t, _, ok := s.Teams.Resolve(teams[i].ID)
		if !ok {
			continue
		}
		identifyTeam(&teams[i], t)
	}
}

func identifyTeam(view *store.OperatorTeam, t team.Team) {
	view.TeamID = t.ID
	view.Key = t.Key
	view.Aliases = slices.Clone(t.Aliases)
	view.Source = t.Source
	view.State = t.State
	view.Name = t.Name
	if t.Source != team.SourceStatic {
		source := t.NameSource
		view.NameSource = &source
		return
	}
	defaulted := view.Name == ""
	if defaulted {
		view.Name = team.DefaultName(t.Key, roleHarnesses(view.Roles))
	}
	source := team.StaticNameSource(t.Key, defaulted)
	view.NameSource = &source
}

func roleHarnesses(roles []store.OperatorRole) []team.RoleHarness {
	out := make([]team.RoleHarness, 0, len(roles))
	for _, r := range roles {
		h := team.RoleHarness{Role: r.ID}
		if r.Harness != nil {
			h.Harness = r.Harness.Name
		}
		out = append(out, h)
	}
	return out
}

func (p OperatorPrincipal) allowsResolvedTeam(t team.Team) bool {
	if p.Teams == nil || (t.Source == team.SourceRuntime && p.CanManageTeams) {
		return true
	}
	for _, ref := range append([]string{t.ID, t.Key}, t.Aliases...) {
		if p.AllowsTeam(ref) {
			return true
		}
	}
	return false
}

func (s *Server) resolveTeamRef(w http.ResponseWriter, r *http.Request) (team.Team, team.Resolution, bool) {
	ref := r.PathValue("team")
	if !operatorName.MatchString(ref) {
		operatorError(w, 400, "invalid_request", "Invalid team identifier.")
		return team.Team{}, 0, false
	}
	t, how, ok := s.Teams.Resolve(ref)
	p, _ := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.allowsResolvedTeam(t) {
		if !ok && s.RuntimeTeams != nil {
			if until, err := s.RuntimeTeams.ReservedUntil(r.Context(), ref); err == nil && until != nil {
				teamRefusal(w, &teamruntime.Error{Status: 404, Code: "unknown_team", ReservedUntil: until,
					Message: "Key " + ref + " was released and no longer names a Team; it stays reserved so it cannot reach a new one."})
				return team.Team{}, 0, false
			}
		}
		operatorError(w, 404, "unknown_team", "No Team "+ref+" is in this consumer's scope.")
		return team.Team{}, 0, false
	}
	return t, how, true
}

func (s *Server) handleOperatorTeam(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "A Team does not accept query parameters.")
		return
	}
	t, how, ok := s.resolveTeamRef(w, r)
	if !ok {
		return
	}
	if how == team.ByAlias {
		w.Header().Set("Content-Location", "/api/v1/operator/teams/"+t.Key)
	}
	if t.Source == team.SourceRuntime && s.RuntimeTeams != nil {
		rt, err := s.RuntimeTeams.Team(r.Context(), t.ID)
		if err != nil {
			operatorError(w, 404, "unknown_team", "No Team "+t.Key+" is live.")
			return
		}
		w.Header().Set("ETag", `"`+strconv.FormatInt(rt.Revision, 10)+`"`)
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "team": runtimeTeamJSON(rt)})
		return
	}
	view, err := s.operatorTeamView(r, t)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "team": view})
}

func (s *Server) operatorTeamView(r *http.Request, t team.Team) (store.OperatorTeam, error) {
	teams, err := s.Store.OperatorTeams(r.Context(), []string{t.Key}, s.OperatorConfig.Teams)
	if err != nil {
		return store.OperatorTeam{}, err
	}
	idx := slices.IndexFunc(teams, func(v store.OperatorTeam) bool { return v.ID == t.Key })
	if idx < 0 {
		teams = append(teams, store.OperatorTeam{ID: t.Key, Roles: []store.OperatorRole{}, Assignees: []string{}, PinnedScopes: []string{}})
		idx = len(teams) - 1
	}
	view := teams[idx : idx+1]
	s.decorateTeams(view)
	if err := s.describeRoles(r.Context(), view); err != nil {
		return store.OperatorTeam{}, err
	}
	s.identifyTeams(view)
	return view[0], nil
}

func (s *Server) decorateTeams(teams []store.OperatorTeam) {
	for i := range teams {
		if assignees := s.OperatorConfig.TeamAssignees[teams[i].ID]; len(assignees) > 0 {
			teams[i].Assignees = append([]string{}, assignees...)
			sort.Strings(teams[i].Assignees)
		}
		if scopes := s.OperatorConfig.TeamScopes[teams[i].ID]; len(scopes) > 0 {
			teams[i].PinnedScopes = append([]string{}, scopes...)
			sort.Strings(teams[i].PinnedScopes)
		}
		teams[i].PrepareRepository = &store.OperatorPrepareRepository{MaxBudgetUSD: s.prepareCap(teams[i].ID)}
	}
}

type teamNamePatch struct {
	Name *string `json:"name"`
}

func (s *Server) handlePatchTeam(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "Changing a Team's name does not accept query parameters.")
		return
	}
	t, _, ok := s.resolveTeamRef(w, r)
	if !ok {
		return
	}
	var patch teamNamePatch
	dec := json.NewDecoder(io.LimitReader(r.Body, teamBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil || patch.Name == nil {
		operatorError(w, 400, "invalid_request", "A Team PATCH carries only {\"name\": \"…\"}.")
		return
	}
	attribution, _ := store.AttributionFrom(r.Context())
	if t.Source == team.SourceStatic {
		configKey := team.ConfigurationNameKey(t.Key)
		_ = s.Store.AuditTeamRefusal(r.Context(), attribution, t.ID, t.Key, "name_set_by_configuration",
			map[string]any{"configKey": configKey})
		operatorJSON(w, 409, map[string]any{"schemaVersion": "1.0", "error": map[string]string{
			"code":      "name_set_by_configuration",
			"message":   "Team " + t.Key + " is configured in Ploeg's configuration file, which alone sets its name. Change " + configKey + " in that file and redeploy; the operator API does not store a name for it.",
			"configKey": configKey,
		}})
		return
	}
	s.patchRuntimeName(w, r, t, *patch.Name)
}
