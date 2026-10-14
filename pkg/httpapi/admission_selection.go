package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func normalHarnessKey(key string) string {
	if key == "acp" {
		return store.HarnessKey("acp", "")
	}
	return key
}

func (s *Server) selections() RunSelections {
	return RunSelections{Harnesses: s.OperatorConfig.TeamHarnesses, LLM: s.LLMControl}
}

func (s *Server) admissionSelection(w http.ResponseWriter, in store.AdmitOperatorWorkItem) bool {
	roles, _ := s.Engine.PlanRoles(in.Team)
	if in.Harness != "" && !s.admitsHarness(w, in.Team, roles, in.Harness) {
		return false
	}
	return in.Model == "" || s.admitsModel(w, in.Team, roles, in.Model)
}

func (s *Server) admitsHarness(w http.ResponseWriter, team string, roles []string, key string) bool {
	configured := s.OperatorConfig.TeamHarnesses[team]
	listed := false
	allowed := make([]string, 0, len(roles))
	for _, role := range roles {
		h, ok := configured[role]
		if !ok {
			allowed = append(allowed, roleLabel(role)+": none")
			continue
		}
		if h.Admits(key) {
			return true
		}
		listed = listed || h.Allows(key)
		keys := make([]string, 0, len(h.Allowlist()))
		for _, a := range h.Allowlist() {
			keys = append(keys, harnessKeyOf(a.OperatorRoleHarness))
		}
		allowed = append(allowed, roleLabel(role)+": "+strings.Join(keys, ", "))
	}
	if listed {
		operatorError(w, 422, "harness_unqualified", "Harness "+key+" is not qualified. Team "+team+" admits it only as a Role's default Harness, or after it sets allowUnqualifiedHarnesses.")
		return false
	}
	operatorError(w, 422, "harness_not_allowed", "Harness "+key+" is not in the allowlist of any Role of team "+team+". Allowed: "+strings.Join(allowed, "; ")+".")
	return false
}

func (s *Server) admitsModel(w http.ResponseWriter, team string, roles []string, model string) bool {
	allowed := make([]string, 0, len(roles))
	managed := false
	for _, role := range roles {
		var p LLMPolicy
		ok := false
		if s.LLMControl != nil {
			p, ok = s.LLMControl.Policy(team, role)
		}
		if !ok {
			allowed = append(allowed, roleLabel(role)+": none")
			continue
		}
		managed = true
		if slices.Contains(p.Models, model) {
			return true
		}
		allowed = append(allowed, roleLabel(role)+": "+strings.Join(p.Models, ", "))
	}
	if !managed {
		operatorError(w, 422, "model_requires_managed_inference", "Team "+team+" has no managed inference policy, so its Runs call their configured model and an admission cannot choose one.")
		return false
	}
	operatorError(w, 422, "model_not_allowed", "Model "+model+" is not listed by any Role of team "+team+". Allowed: "+strings.Join(allowed, "; ")+".")
	return false
}

func roleLabel(role string) string {
	if role == "" {
		return "(no Role)"
	}
	return role
}

func (s *Server) resolvedRoles(in store.AdmitOperatorWorkItem) []map[string]any {
	out := []map[string]any{}
	if s.Engine == nil {
		return out
	}
	roles, _ := s.Engine.PlanRoles(in.Team)
	selections := s.selections()
	for _, role := range roles {
		harness, model := selections.RoleSelection(in.Team, role).Resolve(in.Harness, in.Model)
		out = append(out, map[string]any{"role": role, "harness": optionalString(harness), "model": optionalString(model)})
	}
	return out
}
