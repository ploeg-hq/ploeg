package work

import "strings"

// FollowUpProvider is the provider recorded on a Follow-Up that Ploeg created
// itself. No tracker is registered under it, so a Follow-Up never writes back
// to a tracker item that does not exist.
const FollowUpProvider = "ploeg"

// DefaultMaxRepairs is the repair cap used when a Team enables repairs
// without naming one.
const DefaultMaxRepairs = 2

// DefaultMaxReworks is the cap on Work Item re-queues by requests for
// changes used when a Team enables rework without naming one.
const DefaultMaxReworks = 3

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
	// ReworkReviewers are the forge logins whose request for changes sends
	// work back, besides every login the forge reports with write access to
	// the repository. Any other reviewer's request is refused and audited.
	ReworkReviewers []string `json:"reworkReviewers,omitempty" yaml:"reworkReviewers"`
	// MaxReworks caps how many times requests for changes queue one Work
	// Item again, each with its attempts reset and a fresh Shift pool. Zero
	// means DefaultMaxReworks.
	MaxReworks int `json:"maxReworks,omitempty" yaml:"maxReworks"`
	// RebaseOnConflict creates a rebase Follow-Up when the forge confirms that
	// a pull request this Team owns conflicts with its base branch while it
	// awaits review (ADR-0146). It shares MaxRepairs with RepairFailedChecks.
	RebaseOnConflict bool `json:"rebaseOnConflict,omitempty" yaml:"rebaseOnConflict"`
}

// RepairCap is the effective cap on repair Follow-Ups per pull request.
func (f ForgeFollowUps) RepairCap() int {
	if f.MaxRepairs <= 0 {
		return DefaultMaxRepairs
	}
	return f.MaxRepairs
}

// ReworkCap is the effective cap on re-queues by requests for changes per
// Work Item.
func (f ForgeFollowUps) ReworkCap() int {
	if f.MaxReworks <= 0 {
		return DefaultMaxReworks
	}
	return f.MaxReworks
}

// ListsReworkReviewer reports whether login is one of ReworkReviewers,
// ignoring case.
func (f ForgeFollowUps) ListsReworkReviewer(login string) bool {
	login = strings.TrimSpace(login)
	if login == "" {
		return false
	}
	for _, listed := range f.ReworkReviewers {
		if strings.EqualFold(strings.TrimSpace(listed), login) {
			return true
		}
	}
	return false
}

// Enabled reports whether any forge event acts for this Team.
func (f ForgeFollowUps) Enabled() bool {
	return f.RepairFailedChecks || f.ReworkOnChangesRequested || f.RebaseOnConflict
}
