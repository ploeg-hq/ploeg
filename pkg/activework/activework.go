// Package activework tells a Run which other active Shifts work in its
// repository and which paths they share with it (ADR-0123). A briefing
// calls For and renders the Report; the package reads only what pkg/store
// records and changes nothing.
package activework

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// maxListedPaths is how many paths of one Shift a Report lists.
const maxListedPaths = 10

// Reader is the store a Report is read from.
type Reader interface {
	WorkItemPaths(ctx context.Context, workItemID int64) (store.WorkPaths, error)
	ActiveWork(ctx context.Context, workItemID int64) ([]store.ActiveShift, error)
}

// Shift is one other active Shift in the same repository. Shared holds the
// paths both Work Items touch or plan to touch; Overlaps is true when it is
// not empty. Paths lists up to ten of the other Shift's own paths, changed
// first, and PathsKnown is false when it reported none and its pull request
// has no captured diff yet.
type Shift struct {
	WorkItemID  string
	Ref         string
	Title       string
	Team        string
	ShiftID     string
	Branch      string
	PullRequest int
	OpenedAt    time.Time
	Shared      []string
	Overlaps    bool
	Paths       []string
	MorePaths   int
	PathsKnown  bool
}

// Report is the other active work in a Work Item's repository: the Shifts
// that share a path first, then the rest, each group oldest first.
type Report struct {
	Repository string
	Shifts     []Shift
}

// Overlapping lists the Shifts that share a path with the Work Item.
func (r Report) Overlapping() []Shift {
	var out []Shift
	for _, s := range r.Shifts {
		if s.Overlaps {
			out = append(out, s)
		}
	}
	return out
}

// For reads the other active work in workItemID's repository.
func For(ctx context.Context, st Reader, workItemID int64) (Report, error) {
	own, err := st.WorkItemPaths(ctx, workItemID)
	if err != nil {
		return Report{}, err
	}
	if own.Repo == "" {
		return Report{}, nil
	}
	others, err := st.ActiveWork(ctx, workItemID)
	if err != nil {
		return Report{}, err
	}
	return Compare(own, others), nil
}

// Compare builds the Report of own against the other active Shifts.
func Compare(own store.WorkPaths, others []store.ActiveShift) Report {
	r := Report{Repository: own.Owner + "/" + own.Repo}
	mine := slices.Concat(own.Changed, own.Planned)
	for _, o := range others {
		theirs := slices.Concat(o.Paths.Changed, o.Paths.Planned)
		s := Shift{WorkItemID: o.WorkItemID, Ref: work.Reference(work.WorkItem{Provider: o.Provider, ExternalID: o.ExternalID}),
			Title: o.Title, Team: o.Team, ShiftID: o.ShiftID, Branch: o.Branch, PullRequest: o.Paths.PullRequest, OpenedAt: o.OpenedAt,
			Shared: shared(mine, theirs), PathsKnown: o.Paths.ChangedKnown || len(o.Paths.Planned) > 0}
		s.Overlaps = len(s.Shared) > 0
		listed := distinct(theirs)
		if len(listed) > maxListedPaths {
			s.MorePaths = len(listed) - maxListedPaths
			listed = listed[:maxListedPaths]
		}
		s.Paths = listed
		r.Shifts = append(r.Shifts, s)
	}
	slices.SortStableFunc(r.Shifts, func(a, b Shift) int {
		if a.Overlaps != b.Overlaps {
			if a.Overlaps {
				return -1
			}
			return 1
		}
		return 0
	})
	return r
}

// Covers reports whether path a and path b name the same file, or one is a
// directory (ending in /) that holds the other.
func Covers(a, b string) bool {
	if a == b {
		return true
	}
	if strings.HasSuffix(a, "/") && strings.HasPrefix(b, a) {
		return true
	}
	return strings.HasSuffix(b, "/") && strings.HasPrefix(a, b)
}

func shared(mine, theirs []string) []string {
	var out []string
	for _, m := range distinct(mine) {
		for _, t := range theirs {
			if Covers(m, t) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func distinct(paths []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Markdown renders the Report for a briefing, within maxBytes (0 means no
// limit). Titles are tracker text and are quoted as data. An empty Report
// renders nothing.
func (r Report) Markdown(maxBytes int) string {
	if len(r.Shifts) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Other active work in %s (%d Shift(s); %d share a path with this Work Item):\n", r.Repository, len(r.Shifts), len(r.Overlapping()))
	for _, s := range r.Shifts {
		line := "- " + s.Ref + " " + strconv.Quote(s.Title) + ", Team " + s.Team + ", branch " + s.Branch
		if s.PullRequest > 0 {
			line += ", pull request #" + strconv.Itoa(s.PullRequest)
		}
		switch {
		case s.Overlaps:
			line += ". Shares: " + strings.Join(s.Shared, ", ")
		case !s.PathsKnown:
			line += ". Paths not known yet"
		default:
			line += ". Touches: " + strings.Join(s.Paths, ", ")
			if s.MorePaths > 0 {
				line += fmt.Sprintf(" and %d more", s.MorePaths)
			}
		}
		line += "\n"
		if maxBytes > 0 && b.Len()+len(line) > maxBytes {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}
