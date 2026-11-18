package team

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
)

var sponsorID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}$`)

// Why a runtime Team is invalid (ADR-0198 section 5).
const (
	InvalidHarnessNotOffered   = "harness_not_offered"
	InvalidHarnessNotQualified = "harness_not_qualified"
	InvalidModelNotAllowed     = "model_not_allowed"
	InvalidTargetUnavailable   = "target_unavailable"
	InvalidSpendExceedsTenant  = "spend_exceeds_tenant"
	InvalidCreationDisabled    = "creation_disabled"
)

// Installation is what decides a runtime Team's state: the static Teams by
// key, the opt-in, the pools, the models and the Tenant allowances.
type Installation struct {
	StaticKeys   map[string]string
	Accepting    bool
	Pools        Pools
	Models       []string
	Allowances   map[string]spendcap.Cap
	TargetExists func(key string) bool
}

// Decision is a runtime Team's state and why.
type Decision struct {
	State         string
	ShadowedBy    string
	InvalidReason string
}

// Decide gives a runtime Team its state. A static Team with its key
// shadows it, because static wins; otherwise the first cause Ploeg can no
// longer run it for makes it invalid; otherwise it is active.
func (in Installation) Decide(tenant, key string, m Manifest) Decision {
	if id, ok := in.StaticKeys[strings.ToLower(key)]; ok {
		return Decision{State: StateShadowed, ShadowedBy: id}
	}
	invalid := func(reason string) Decision { return Decision{State: StateInvalid, InvalidReason: reason} }
	if !in.Accepting {
		return invalid(InvalidCreationDisabled)
	}
	for _, role := range m.Roles() {
		pool, ok := in.Pools.For(tenant, role.Harness)
		if !ok {
			return invalid(InvalidHarnessNotOffered)
		}
		if !pool.Qualified() {
			return invalid(InvalidHarnessNotQualified)
		}
	}
	for _, role := range m.Roles() {
		if !slices.Contains(in.Models, role.Model) {
			return invalid(InvalidModelNotAllowed)
		}
	}
	for _, key := range m.Intake.Targets {
		if in.TargetExists == nil || !in.TargetExists(key) {
			return invalid(InvalidTargetUnavailable)
		}
	}
	allowance, ok := in.Allowances[tenant]
	if !ok || m.Spend.Cap == nil || m.Spend.Cap.Period != allowance.Period || m.Spend.Cap.USD > allowance.USD {
		return invalid(InvalidSpendExceedsTenant)
	}
	return Decision{State: StateActive}
}
