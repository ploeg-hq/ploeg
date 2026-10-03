package work

// FollowUpProvider is the provider recorded on a Follow-Up that Ploeg created
// itself. No tracker is registered under it, so a Follow-Up never writes back
// to a tracker item that does not exist.
const FollowUpProvider = "ploeg"

// DefaultMaxRepairs is the repair cap used when a Team enables repairs
// without naming one.
const DefaultMaxRepairs = 2

// ForgeFollowUps is one Team's opt-in to acting on forge events. The zero
// value acts on nothing, so a deployment that does not configure it behaves
// as before.
type ForgeFollowUps struct {
	// RepairFailedChecks creates a repair Follow-Up when a check fails on a
	// pull request branch this Team owns.
	RepairFailedChecks bool `json:"repairFailedChecks,omitempty" yaml:"repairFailedChecks"`
	// MaxRepairs caps the repair Follow-Ups created for one pull request.
	// Zero means DefaultMaxRepairs.
	MaxRepairs int `json:"maxRepairs,omitempty" yaml:"maxRepairs"`
	// ReworkOnChangesRequested sends a person's request for changes on this
	// Team's pull request back to the Team as a fix Round or a new Shift.
	ReworkOnChangesRequested bool `json:"reworkOnChangesRequested,omitempty" yaml:"reworkOnChangesRequested"`
}

// RepairCap is the effective cap on repair Follow-Ups per pull request.
func (f ForgeFollowUps) RepairCap() int {
	if f.MaxRepairs <= 0 {
		return DefaultMaxRepairs
	}
	return f.MaxRepairs
}

// Enabled reports whether any forge event acts for this Team.
func (f ForgeFollowUps) Enabled() bool {
	return f.RepairFailedChecks || f.ReworkOnChangesRequested
}
