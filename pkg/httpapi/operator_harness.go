package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

var harnessName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Harness qualification as the chart renders it (ADR-0098).
const (
	HarnessQualified   = "qualified"
	HarnessUnqualified = "unqualified"
)

// TeamHarnesses is each team's Harness configuration per Role
// (PLOEG_TEAM_HARNESSES, ADR-0097, ADR-0098). A plan-less team's one workload
// is the Role "".
type TeamHarnesses map[string]map[string]RoleHarnesses

// RoleHarnesses is a Role's default Harness, its allowlist and whether its
// team admits unqualified Harnesses. An empty Allowed is the default alone.
type RoleHarnesses struct {
	store.OperatorRoleHarness
	Allowed          []AllowedHarness
	AllowUnqualified bool
	// Carrier is CarrierClient when the person's own client carries the
	// Role's claude-environment Runs (ADR-0112), and EnvironmentID the
	// Team's self-hosted environment those Runs dispatch to.
	Carrier       string
	EnvironmentID string
}

// ClientCarried reports whether the person's own client carries this Role's
// Runs of its default Harness (ADR-0112).
func (r RoleHarnesses) ClientCarried() bool { return r.Carrier == store.CarrierClient }

// AllowedHarness is one allowlist entry. Qualification is HarnessQualified,
// HarnessUnqualified or empty when the chart did not say.
type AllowedHarness struct {
	store.OperatorRoleHarness
	Qualification string
}

func harnessKeyOf(h store.OperatorRoleHarness) string {
	profile := ""
	if h.Profile != nil {
		profile = *h.Profile
	}
	return store.HarnessKey(h.Name, profile)
}

// DefaultKey is the Harness key of the Role's default Harness.
func (r RoleHarnesses) DefaultKey() string { return harnessKeyOf(r.OperatorRoleHarness) }

// Allowlist is the Role's allowlist, the default alone when none was given.
func (r RoleHarnesses) Allowlist() []AllowedHarness {
	if len(r.Allowed) > 0 {
		return r.Allowed
	}
	return []AllowedHarness{{OperatorRoleHarness: r.OperatorRoleHarness}}
}

// Allows reports whether key is in the Role's allowlist.
func (r RoleHarnesses) Allows(key string) bool {
	return slices.ContainsFunc(r.Allowlist(), func(a AllowedHarness) bool { return harnessKeyOf(a.OperatorRoleHarness) == key })
}

// Admits reports whether an admission may give this Role key: it is allowed,
// and it is qualified, the Role's default or the team opted into
// unqualified Harnesses.
func (r RoleHarnesses) Admits(key string) bool {
	if key == r.DefaultKey() {
		return true
	}
	for _, a := range r.Allowlist() {
		if harnessKeyOf(a.OperatorRoleHarness) == key {
			return a.Qualification == HarnessQualified || r.AllowUnqualified
		}
	}
	return false
}

type rawHarness struct {
	Name          string `json:"name"`
	Profile       string `json:"profile"`
	Qualification string `json:"qualification"`
}

func (h rawHarness) parse(team, role string) (store.OperatorRoleHarness, error) {
	if !harnessName.MatchString(h.Name) {
		return store.OperatorRoleHarness{}, fmt.Errorf("team %q Role %q: harness name %q is not a harness name", team, role, h.Name)
	}
	entry := store.OperatorRoleHarness{Name: h.Name}
	if h.Profile != "" {
		if !harnessName.MatchString(h.Profile) {
			return store.OperatorRoleHarness{}, fmt.Errorf("team %q Role %q: profile %q is not a profile name", team, role, h.Profile)
		}
		profile := h.Profile
		entry.Profile = &profile
	}
	return entry, nil
}

// ParseTeamHarnesses reads PLOEG_TEAM_HARNESSES: a JSON object team -> Role
// -> {name, profile, allowed, allowUnqualified, carrier, environmentId},
// where allowed lists {name, profile, qualification} and must contain the
// default, and carrier client with a ccpool_ environmentId marks a
// claude-environment Role whose Runs the person's own client carries
// (ADR-0112). Empty is no configuration.
func ParseTeamHarnesses(raw string) (TeamHarnesses, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("exceeds 64 KiB")
	}
	var parsed map[string]map[string]struct {
		rawHarness
		Allowed          []rawHarness `json:"allowed"`
		AllowUnqualified bool         `json:"allowUnqualified"`
		Carrier          string       `json:"carrier"`
		EnvironmentID    string       `json:"environmentId"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("want a JSON object of team to Role to {name, profile}: %w", err)
	}
	out := TeamHarnesses{}
	for team, roles := range parsed {
		out[team] = map[string]RoleHarnesses{}
		for role, h := range roles {
			def, err := h.parse(team, role)
			if err != nil {
				return nil, err
			}
			entry := RoleHarnesses{OperatorRoleHarness: def, AllowUnqualified: h.AllowUnqualified, Carrier: h.Carrier, EnvironmentID: h.EnvironmentID}
			switch {
			case h.Carrier != "" && h.Carrier != store.CarrierClient:
				return nil, fmt.Errorf("team %q Role %q: carrier %q is not client", team, role, h.Carrier)
			case h.Carrier != "" && h.Name != "claude-environment":
				return nil, fmt.Errorf("team %q Role %q: only a claude-environment Harness is carried by a client, not %q", team, role, h.Name)
			case h.Carrier != "" && !strings.HasPrefix(h.EnvironmentID, "ccpool_"):
				return nil, fmt.Errorf("team %q Role %q: a client-carried Harness needs a ccpool_ environmentId", team, role)
			}
			seen := map[string]bool{}
			for _, a := range h.Allowed {
				allowed, err := a.parse(team, role)
				if err != nil {
					return nil, err
				}
				if a.Qualification != "" && a.Qualification != HarnessQualified && a.Qualification != HarnessUnqualified {
					return nil, fmt.Errorf("team %q Role %q: qualification %q is neither qualified nor unqualified", team, role, a.Qualification)
				}
				key := harnessKeyOf(allowed)
				if seen[key] {
					return nil, fmt.Errorf("team %q Role %q: harness %q is allowed twice", team, role, key)
				}
				seen[key] = true
				entry.Allowed = append(entry.Allowed, AllowedHarness{OperatorRoleHarness: allowed, Qualification: a.Qualification})
			}
			if len(entry.Allowed) > 0 && !seen[entry.DefaultKey()] {
				return nil, fmt.Errorf("team %q Role %q: the default harness %q is not in its allowlist", team, role, entry.DefaultKey())
			}
			out[team][role] = entry
		}
	}
	return out, nil
}

func (s *Server) describeRoles(ctx context.Context, teams []store.OperatorTeam) error {
	ids := make([]string, 0, len(teams))
	for _, team := range teams {
		ids = append(ids, team.ID)
	}
	observed, err := s.Store.ObservedHarnessVersions(ctx, ids)
	if err != nil {
		return err
	}
	pending, err := s.Store.PendingRunsByHarness(ctx)
	if err != nil {
		return err
	}
	for i := range teams {
		for j := range teams[i].Roles {
			role := &teams[i].Roles[j]
			h, configured := s.OperatorConfig.TeamHarnesses[teams[i].ID][role.ID]
			if configured {
				harness := h.OperatorRoleHarness
				role.Harness = &harness
				role.Harnesses = describeAllowlist(h, observed[teams[i].ID][role.ID])
			}
			role.PendingByHarness = []store.OperatorPendingHarness{}
			for _, p := range pending {
				if p.Team == teams[i].ID && p.Role == role.ID {
					role.PendingByHarness = append(role.PendingByHarness, store.OperatorPendingHarness{
						Harness: p.Harness, Pending: p.Pending, Allowed: configured && h.Allows(p.Harness)})
				}
			}
			if s.LLMControl != nil {
				if p, ok := s.LLMControl.Policy(teams[i].ID, role.ID); ok {
					role.Models = append([]string{}, p.Models...)
					if model := p.Default(); model != "" {
						role.Model = &model
						role.DefaultModel = &model
					}
				}
			}
		}
	}
	return nil
}

func describeAllowlist(h RoleHarnesses, observed map[string]string) []store.OperatorAllowedHarness {
	out := []store.OperatorAllowedHarness{}
	for _, a := range h.Allowlist() {
		key := harnessKeyOf(a.OperatorRoleHarness)
		entry := store.OperatorAllowedHarness{Key: key, Name: a.Name, Profile: a.Profile, Default: key == h.DefaultKey()}
		if a.Qualification != "" {
			qualification := a.Qualification
			entry.Qualification = &qualification
		}
		if version, ok := observed[key]; ok {
			entry.ObservedVersion = &version
		}
		out = append(out, entry)
	}
	return out
}
