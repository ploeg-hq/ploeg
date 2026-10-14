package httpapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

var harnessName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// TeamHarnesses is each team's configured Harness per Role
// (PLOEG_TEAM_HARNESSES, ADR-0097). A plan-less team's one workload is the
// Role "".
type TeamHarnesses map[string]map[string]store.OperatorRoleHarness

// ParseTeamHarnesses reads PLOEG_TEAM_HARNESSES: a JSON object team -> Role
// -> {name, profile}. Empty is no configuration.
func ParseTeamHarnesses(raw string) (TeamHarnesses, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("exceeds 64 KiB")
	}
	var parsed map[string]map[string]struct {
		Name    string `json:"name"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("want a JSON object of team to Role to {name, profile}: %w", err)
	}
	out := TeamHarnesses{}
	for team, roles := range parsed {
		out[team] = map[string]store.OperatorRoleHarness{}
		for role, h := range roles {
			if !harnessName.MatchString(h.Name) {
				return nil, fmt.Errorf("team %q Role %q: harness name %q is not a harness name", team, role, h.Name)
			}
			entry := store.OperatorRoleHarness{Name: h.Name}
			if h.Profile != "" {
				if !harnessName.MatchString(h.Profile) {
					return nil, fmt.Errorf("team %q Role %q: profile %q is not a profile name", team, role, h.Profile)
				}
				profile := h.Profile
				entry.Profile = &profile
			}
			out[team][role] = entry
		}
	}
	return out, nil
}

func (s *Server) describeRoles(teams []store.OperatorTeam) {
	for i := range teams {
		for j := range teams[i].Roles {
			role := &teams[i].Roles[j]
			if h, ok := s.OperatorConfig.TeamHarnesses[teams[i].ID][role.ID]; ok {
				harness := h
				role.Harness = &harness
			}
			if s.LLMControl != nil {
				if p, ok := s.LLMControl.Policy(teams[i].ID, role.ID); ok && len(p.Models) == 1 {
					model := p.Models[0]
					role.Model = &model
				}
			}
		}
	}
}
