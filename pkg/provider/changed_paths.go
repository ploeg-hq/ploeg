package provider

import "context"

// MaxChangedPaths is how many entries a ChangedPaths holds. A pull request
// that changed more is reported with Truncated.
const MaxChangedPaths = 300

// PathStatus is how a pull request changed one path, normalized from the
// forge's own vocabulary.
type PathStatus string

const (
	PathAdded    PathStatus = "added"
	PathModified PathStatus = "modified"
	PathDeleted  PathStatus = "deleted"
	PathRenamed  PathStatus = "renamed"
	PathCopied   PathStatus = "copied"
)

// ChangedPath is one path a pull request changes. PreviousPath is the
// source of a rename or copy, and empty otherwise.
type ChangedPath struct {
	Path         string
	Status       PathStatus
	PreviousPath string
}

// ChangedPaths is the list of paths a pull request changes at its current
// head, in the forge's order (VIK-1698). Paths holds at most MaxChangedPaths
// entries; Truncated says the pull request changed more.
type ChangedPaths struct {
	Paths     []ChangedPath
	Truncated bool
}

// ChangedPathsReader is implemented by a ForgeProvider that can list the
// paths a pull request changes. A forge without it leaves the paths unknown.
type ChangedPathsReader interface {
	ChangedPaths(ctx context.Context, repo string, pr int) (ChangedPaths, error)
}

// AppendChangedPath adds p to c unless its path is empty. It returns false,
// and marks c truncated, when c already holds MaxChangedPaths entries.
func AppendChangedPath(c *ChangedPaths, p ChangedPath) bool {
	if p.Path == "" {
		return true
	}
	if len(c.Paths) == MaxChangedPaths {
		c.Truncated = true
		return false
	}
	if p.PreviousPath == p.Path || (p.Status != PathRenamed && p.Status != PathCopied) {
		p.PreviousPath = ""
	}
	c.Paths = append(c.Paths, p)
	return true
}
