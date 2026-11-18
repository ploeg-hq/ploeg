package config

import (
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
)

// DefaultMaxRuntimeTeams is how many live runtime Teams a Tenant holds when
// teamCreation names no max (ADR-0198 section 12).
const DefaultMaxRuntimeTeams = 10

// MaxRuntimeTeamsCeiling bounds teamCreation.max.
const MaxRuntimeTeamsCeiling = 50

// TeamCreation is the installation's opt-in to Teams a proven person
// creates through the operator API (ADR-0198 section 8). Omitted, or
// Enabled false, refuses every change with creation_not_accepted.
type TeamCreation struct {
	Enabled bool `yaml:"enabled"`
	// Models are the gateway models a runtime Team's Role may name.
	Models []string `yaml:"models"`
	// May is what a runtime Team's permissions may grant.
	May TeamCreationGrants `yaml:"may"`
	// MaxRoleCap bounds a runtime Role's per-Run cap.
	MaxRoleCap *USD `yaml:"maxRoleCap"`
	// Max is the number of live runtime Teams per Tenant.
	Max *int `yaml:"max"`
	// Tenants holds each Tenant's spend allowance; a Tenant without one
	// creates no Team.
	Tenants map[string]TeamCreationTenant `yaml:"tenants"`
	// RequireClaims are the person token claims operator authority needs.
	RequireClaims map[string][]string `yaml:"requireClaims"`
	// StepUp is the authentication the person's token must show.
	StepUp *connection.StepUp `yaml:"stepUp"`
}

// TeamCreationGrants is the most a runtime Team's may list may hold.
type TeamCreationGrants struct {
	Write   bool     `yaml:"write"`
	Comment bool     `yaml:"comment"`
	Egress  []string `yaml:"egress"`
}

// TeamCreationTenant is one Tenant's allowance: the caps of its live runtime
// Teams add up to at most Spend.
type TeamCreationTenant struct {
	Spend spendcap.Cap `yaml:"spend"`
}

// Accepting reports whether runtime Teams are switched on.
func (t *TeamCreation) Accepting() bool { return t != nil && t.Enabled }

// Limit is the number of live runtime Teams a Tenant may hold.
func (t *TeamCreation) Limit() int {
	if t == nil || t.Max == nil {
		return DefaultMaxRuntimeTeams
	}
	return *t.Max
}

// AllowsModel reports whether a runtime Role may name model.
func (t *TeamCreation) AllowsModel(model string) bool {
	return t != nil && slices.Contains(t.Models, model)
}

// RoleCapLimit is the highest per-Run cap a runtime Role may set; 0 is none.
func (t *TeamCreation) RoleCapLimit() float64 {
	if t == nil || t.MaxRoleCap == nil {
		return 0
	}
	return float64(*t.MaxRoleCap)
}

func (f *File) validateTeamCreation() error {
	t := f.TeamCreation
	if t == nil {
		return nil
	}
	seen := map[string]bool{}
	for i, m := range t.Models {
		if !gatewayModel.MatchString(m) || seen[m] {
			return fmt.Errorf("teamCreation.models[%d]: %q is not a gateway model, or is named twice", i, m)
		}
		seen[m] = true
	}
	if t.Enabled && len(t.Models) == 0 {
		return fmt.Errorf("teamCreation.models: name at least one gateway model a runtime Role may use")
	}
	if t.MaxRoleCap != nil && (!(float64(*t.MaxRoleCap) > 0) || math.IsInf(float64(*t.MaxRoleCap), 0)) {
		return fmt.Errorf("teamCreation.maxRoleCap: must be a finite amount above 0")
	}
	if t.Max != nil && (*t.Max < 1 || *t.Max > MaxRuntimeTeamsCeiling) {
		return fmt.Errorf("teamCreation.max: must be between 1 and %d, got %d", MaxRuntimeTeamsCeiling, *t.Max)
	}
	for _, tenant := range slices.Sorted(maps.Keys(t.Tenants)) {
		spend := t.Tenants[tenant].Spend
		if err := spend.Validate(); err != nil || !spend.Capped() {
			return fmt.Errorf("teamCreation.tenants.%s.spend: needs usd above 0 and a period (day, week or month)", tenant)
		}
	}
	for i, name := range t.May.Egress {
		if !registrationConnection.MatchString(name) {
			return fmt.Errorf("teamCreation.may.egress[%d]: %q is not an allowlist name", i, name)
		}
	}
	for name, values := range t.RequireClaims {
		if name == "" || len(values) == 0 {
			return fmt.Errorf("teamCreation.requireClaims: claim %q needs at least one value", name)
		}
	}
	if s := t.StepUp; s != nil && s.MaxAge < 0 {
		return fmt.Errorf("teamCreation.stepUp.maxAge: must not be negative")
	}
	return nil
}
