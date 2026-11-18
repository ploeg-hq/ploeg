package config

import (
	"fmt"
	"regexp"

	"github.com/ploeg-hq/ploeg/pkg/connection"
)

// DefaultMaxRuntimeTargets is how many live runtime Work Targets a Tenant
// holds when targetRegistration names no max (ADR-0193 section 5).
const DefaultMaxRuntimeTargets = 100

// MaxRuntimeTargetsCeiling bounds targetRegistration.max.
const MaxRuntimeTargetsCeiling = 10000

// TargetRegistration is the installation's opt-in to Work Targets a proven
// operator registers at runtime (ADR-0193 section 5). Omitted, or Enabled
// false, refuses every registration with registration_not_accepted.
type TargetRegistration struct {
	Enabled bool `yaml:"enabled"`
	// Connections names the store-backed forge connections whose grant may
	// make a runtime target, or ["*"] for every one.
	Connections []string `yaml:"connections"`
	// Max is the number of live runtime targets per Tenant; omitted is
	// DefaultMaxRuntimeTargets.
	Max *int `yaml:"max"`
	// RequireClaims are the person token claims operator authority needs,
	// matched as ADR-0150 matches claims.
	RequireClaims map[string][]string `yaml:"requireClaims"`
	// StepUp is the authentication the person's token must show.
	StepUp *connection.StepUp `yaml:"stepUp"`
}

// Limit is the number of live runtime targets a Tenant may hold.
func (t *TargetRegistration) Limit() int {
	if t == nil || t.Max == nil {
		return DefaultMaxRuntimeTargets
	}
	return *t.Max
}

// Accepting reports whether runtime registration is switched on.
func (t *TargetRegistration) Accepting() bool { return t != nil && t.Enabled }

// AcceptsConnection reports whether a target may be registered through the
// store-backed forge connection id.
func (t *TargetRegistration) AcceptsConnection(id string) bool {
	if !t.Accepting() {
		return false
	}
	for _, entry := range t.Connections {
		if entry == "*" || entry == id {
			return true
		}
	}
	return false
}

var registrationConnection = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (f *File) validateTargetRegistration() error {
	t := f.TargetRegistration
	if t == nil {
		return nil
	}
	seen := map[string]bool{}
	for i, entry := range t.Connections {
		if (entry != "*" && !registrationConnection.MatchString(entry)) || seen[entry] {
			return fmt.Errorf("targetRegistration.connections[%d]: %q is not a connection id or \"*\", or is named twice", i, entry)
		}
		seen[entry] = true
	}
	if t.Enabled && len(t.Connections) == 0 {
		return fmt.Errorf("targetRegistration.connections: name at least one store-backed forge connection or \"*\"")
	}
	if t.Max != nil && (*t.Max < 1 || *t.Max > MaxRuntimeTargetsCeiling) {
		return fmt.Errorf("targetRegistration.max: must be between 1 and %d, got %d", MaxRuntimeTargetsCeiling, *t.Max)
	}
	for name, values := range t.RequireClaims {
		if name == "" || len(values) == 0 {
			return fmt.Errorf("targetRegistration.requireClaims: claim %q needs at least one value", name)
		}
	}
	if s := t.StepUp; s != nil && s.MaxAge < 0 {
		return fmt.Errorf("targetRegistration.stepUp.maxAge: must not be negative")
	}
	return nil
}
