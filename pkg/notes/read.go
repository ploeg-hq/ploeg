package notes

import (
	"sort"
	"time"
)

// Scope is what one Run may read, taken from Ploeg's records of its Work
// Item and never from the request: its root Work Item, its repository and
// its Client ("" until Ploeg records Clients).
type Scope struct {
	RootWorkItem string
	Repository   string
	Client       string
}

// Reads reports whether a Run in s may read n. A note of another Client is
// never readable, whatever its level, and a note with a Client is never
// readable by a Run without one.
func (s Scope) Reads(n Note) bool {
	if n.Client != s.Client {
		return false
	}
	switch n.Level {
	case LevelWorkItem:
		return s.RootWorkItem != "" && n.RootWorkItem == s.RootWorkItem
	case LevelRepository:
		return s.Repository != "" && n.Repository == s.Repository
	}
	return false
}

// Withheld counts the notes ForRun left out, by reason, so a briefing can
// say what it did not show without showing it.
type Withheld struct {
	OutOfScope int
	Expired    int
	Superseded int
	Retracted  int
}

// View is the notes one Run is briefed with.
type View struct {
	// Notes are current and readable: narrowest level first, newest first
	// within a level, each key once.
	Notes    []Note
	Withheld Withheld
}

// ForRun selects from all the notes a Run in s may read at now. A note
// another readable note supersedes, or a person retracted, is history and
// left out; so is an expired one. contested names the keys an open
// contradiction flag holds, and marks them so the agent sees that a person
// has not decided yet. It is the only filter between the graph and a
// briefing, so the graph's rows never reach a Run unchecked.
func ForRun(s Scope, all []Note, contested map[string]bool, now time.Time) View {
	var v View
	seen := map[string]bool{}
	var readable []Note
	for _, n := range all {
		if n.Key == "" || seen[n.Key] {
			continue
		}
		seen[n.Key] = true
		if !s.Reads(n) {
			v.Withheld.OutOfScope++
			continue
		}
		readable = append(readable, n)
	}
	superseded := map[string]bool{}
	for _, n := range readable {
		if n.Supersedes != "" {
			superseded[n.Supersedes] = true
		}
	}
	for _, n := range readable {
		switch {
		case n.Type == TypeRetraction:
			v.Withheld.Retracted++
		case superseded[n.Key]:
			v.Withheld.Superseded++
		case n.Expired(now):
			v.Withheld.Expired++
		default:
			n.Contested = contested[n.Key]
			v.Notes = append(v.Notes, n)
		}
	}
	sort.SliceStable(v.Notes, func(i, j int) bool {
		a, b := v.Notes[i], v.Notes[j]
		if levelRank[a.Level] != levelRank[b.Level] {
			return levelRank[a.Level] < levelRank[b.Level]
		}
		return a.WrittenAt.After(b.WrittenAt)
	})
	return v
}
