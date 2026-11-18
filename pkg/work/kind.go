package work

import "slices"

// Kind says what a Work Item may deliver (ADR-0194). It is set at Admission,
// stored on the Work Item and never changes.
type Kind string

const (
	// KindChange is every Work Item that asks for a change: tracker work,
	// Follow-Ups, readings and ordinary Admissions.
	KindChange Kind = "change"
	// KindPrepareRepository is a Work Item whose only deliverable is a pull
	// request adding PrepareRepositoryDeliverable at the root of a target that
	// misses nothing else to be ready. Only a proven person's operator
	// Admission creates one.
	KindPrepareRepository Kind = "prepare_repository"
)

// PrepareRepositoryDeliverable is the one path a prepare_repository Work Item
// may add, and the only path it may change.
const PrepareRepositoryDeliverable = "AGENTS.md"

// Kinds lists every Kind.
var Kinds = []Kind{KindChange, KindPrepareRepository}

// ParseKind reads a Kind; empty is KindChange.
func ParseKind(s string) (Kind, bool) {
	if s == "" {
		return KindChange, true
	}
	k := Kind(s)
	return k, slices.Contains(Kinds, k)
}

// PreparesRepository reports whether the item is a prepare_repository Work Item.
func (it WorkItem) PreparesRepository() bool { return it.Kind == KindPrepareRepository }

// ChangedPath is one path a branch changes against its base, with git's
// one-letter status (A added, M modified, D deleted, T type changed).
type ChangedPath struct {
	Status string
	Path   string
	// Mode is git's mode of the path after the change: 100644 for a regular
	// file, 120000 for a symbolic link, 160000 for a gitlink.
	Mode string
}

// RegularFileMode is the only mode the prepare deliverable may have.
const RegularFileMode = "100644"

// OutsidePrepareDeliverable returns the changes a prepare_repository Run may
// not deliver: anything but exactly one added root AGENTS.md that is a
// regular, non-executable file (not a symbolic link or a gitlink). An empty
// change set is outside too, named as the missing deliverable.
func OutsidePrepareDeliverable(changes []ChangedPath) []string {
	var outside []string
	added := false
	for _, c := range changes {
		if c.Path == PrepareRepositoryDeliverable && c.Status == "A" && c.Mode == RegularFileMode && !added {
			added = true
			continue
		}
		entry := c.Status + " " + c.Path
		if c.Mode != "" && c.Mode != RegularFileMode && c.Status != "D" {
			entry += " (mode " + c.Mode + ")"
		}
		outside = append(outside, entry)
	}
	if !added {
		outside = append(outside, "missing: no added "+PrepareRepositoryDeliverable)
	}
	return outside
}
