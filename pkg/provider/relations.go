package provider

import "context"

// TrackerParent is the parent of a tracker item: an epic or parent task,
// named by its tracker id. Title is empty when the tracker did not say.
type TrackerParent struct {
	ExternalID string
	Title      string
}

// RelationReader is implemented by a TrackerProvider that can read an item's
// parents, which Ploeg records as the Work Item's epics (ADR-0053). A
// provider without it records no epics.
type RelationReader interface {
	Parents(ctx context.Context, externalID string) ([]TrackerParent, error)
}

// DependencyKind is how a tracker says one item waits for another.
type DependencyKind string

const (
	// DependencyBlockedBy is a "blocked by" relation: the item cannot start
	// until the other is done.
	DependencyBlockedBy DependencyKind = "blocked_by"
	// DependencyFollows is a "follows" relation: the item comes after the
	// other one.
	DependencyFollows DependencyKind = "follows"
)

// MaxTrackerDependencies is how many dependencies a provider reports for one
// item.
const MaxTrackerDependencies = 50

// TrackerDependency is an item another tracker item waits for, named by its
// tracker id, with whether the tracker marks it done (ADR-0123).
type TrackerDependency struct {
	ExternalID string
	Title      string
	Kind       DependencyKind
	Done       bool
}

// DependencyReader is implemented by a TrackerProvider that can read the
// items an item waits for. Ploeg holds dispatch of a Work Item while one of
// them is not done (ADR-0123). A provider without it holds nothing.
type DependencyReader interface {
	Dependencies(ctx context.Context, externalID string) ([]TrackerDependency, error)
}
