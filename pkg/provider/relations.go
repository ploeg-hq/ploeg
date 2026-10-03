package provider

import "context"

// TrackerParent is the parent of a tracker item: an epic or parent task,
// named by its tracker id. Title is empty when the tracker did not say.
type TrackerParent struct {
	ExternalID string
	Title      string
}

// RelationReader is implemented by a TrackerProvider that can read an item's
// parents, which Ploeg needs to show epics as sets on Run cards (ADR-0053). A
// provider without it records no sets.
type RelationReader interface {
	Parents(ctx context.Context, externalID string) ([]TrackerParent, error)
}
