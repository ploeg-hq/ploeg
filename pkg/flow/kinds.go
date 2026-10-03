// Package flow derives a Run card's flow figures from recorded tracker
// statuses, Runs, merges and deploys: time in every status, lead and cycle
// time, flow efficiency and working time under a team calendar (ADR-0057).
// Every figure describes the card and the team's process, never a person.
package flow

import (
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

// Kind is what a status means for flow: someone works on the ticket
// (active), it waits for someone (waiting), something stops it (blocked), or
// it is finished (done).
type Kind string

const (
	Active  Kind = "active"
	Waiting Kind = "waiting"
	Blocked Kind = "blocked"
	Done    Kind = "done"
)

// Kinds is one board's configured status kinds: for each kind, the tracker
// statuses or bucket titles that have it. A status listed here overrides
// the default rules of DefaultKind.
type Kinds struct {
	Active  []string `yaml:"active" json:"active,omitempty"`
	Waiting []string `yaml:"waiting" json:"waiting,omitempty"`
	Blocked []string `yaml:"blocked" json:"blocked,omitempty"`
	Done    []string `yaml:"done" json:"done,omitempty"`
}

// KindMap resolves a status to its kind: the configured kind when the board
// lists the status, the default rules otherwise. The zero value applies the
// defaults only.
type KindMap struct {
	byStatus map[string]Kind
}

// StatusKey is how statuses compare: trimmed and without case.
func StatusKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NewKindMap validates k: no empty or padded name and no status under two
// kinds. An empty Kinds is valid and applies the defaults only.
func NewKindMap(k Kinds) (KindMap, error) {
	m := KindMap{byStatus: map[string]Kind{}}
	for _, entry := range []struct {
		kind  Kind
		names []string
	}{{Active, k.Active}, {Waiting, k.Waiting}, {Blocked, k.Blocked}, {Done, k.Done}} {
		for _, name := range entry.names {
			if name == "" || strings.TrimSpace(name) != name || len(name) > 256 {
				return KindMap{}, fmt.Errorf("%s: status %q must be 1 to 256 characters with no surrounding space", entry.kind, name)
			}
			key := StatusKey(name)
			if prev, dup := m.byStatus[key]; dup {
				if prev == entry.kind {
					return KindMap{}, fmt.Errorf("%s: status %q is listed twice", entry.kind, name)
				}
				return KindMap{}, fmt.Errorf("%s: status %q is already %s", entry.kind, name, prev)
			}
			m.byStatus[key] = entry.kind
		}
	}
	return m, nil
}

// Configured reports whether the board lists status under a kind.
func (m KindMap) Configured(status string) bool {
	_, ok := m.byStatus[StatusKey(status)]
	return ok
}

// Kind returns the kind of status in gate g, which is empty when the status
// maps to no gate.
func (m KindMap) Kind(status string, g gate.Gate) Kind {
	if k, ok := m.byStatus[StatusKey(status)]; ok {
		return k
	}
	return DefaultKind(status, g)
}

// BlockedPatterns, WaitingPatterns and ActivePatterns are the words
// DefaultKind looks for anywhere in a status name, and DoneNames and
// WaitingNames the names it matches whole, all compared without case.
var (
	BlockedPatterns = []string{"blocked", "on hold", "on-hold", "impeded"}
	DoneNames       = []string{"done", "closed", "complete", "completed", "released", "resolved"}
	WaitingPatterns = []string{"ready", "waiting", "awaiting", "pending", "to review", "to test", "to do", "todo", "backlog", "queue", "icebox"}
	WaitingNames    = []string{"new", "open"}
	ActivePatterns  = []string{"in progress", "doing", "progress", "develop", "review", "testing", "in test", "qa", "build", "working"}
)

// DefaultKind is the kind of a status the board does not configure:
//
//  1. a name with a blocked pattern ("blocked", "on hold") is blocked;
//  2. the done gate, or a name that is a done name ("done", "closed"), is done;
//  3. a name with a waiting pattern ("ready", "waiting", "to review",
//     "queue", "backlog") or a waiting name ("new", "open") is waiting;
//  4. the development and test gates are active;
//  5. a status in no gate whose name has an active pattern ("in progress",
//     "review", "testing") is active;
//  6. everything else, the acceptance gate included, is waiting.
func DefaultKind(status string, g gate.Gate) Kind {
	name := StatusKey(status)
	switch {
	case containsAny(name, BlockedPatterns):
		return Blocked
	case g == gate.Done || equalsAny(name, DoneNames):
		return Done
	case containsAny(name, WaitingPatterns) || equalsAny(name, WaitingNames):
		return Waiting
	case g == gate.Development || g == gate.Test:
		return Active
	case g == "" && containsAny(name, ActivePatterns):
		return Active
	default:
		return Waiting
	}
}

func containsAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}

func equalsAny(name string, names []string) bool {
	for _, n := range names {
		if name == n {
			return true
		}
	}
	return false
}

// Boards holds the kind map of every board that records its statuses, by
// tracker provider name and then by the provider's container id. A board is
// here when it maps gates or configures status kinds.
type Boards map[string]map[string]KindMap

// Lookup returns the kind map of one board.
func (b Boards) Lookup(provider, scope string) (KindMap, bool) {
	m, ok := b[provider][scope]
	return m, ok
}

// Has reports whether any board of provider records its statuses.
func (b Boards) Has(provider string) bool { return len(b[provider]) > 0 }
