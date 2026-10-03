// Package gate maps tracker statuses to the delivery gates a Work Item
// passes and derives bounces from the recorded moves (ADR-0051).
package gate

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Gate is one stage of delivery. Gates are ordered: development, test,
// acceptance, done.
type Gate string

const (
	Development Gate = "development"
	Test        Gate = "test"
	Acceptance  Gate = "acceptance"
	Done        Gate = "done"
)

// Order lists every gate from first to last.
var Order = []Gate{Development, Test, Acceptance, Done}

// Rank is the gate's position in Order, or -1 for an unknown gate.
func (g Gate) Rank() int {
	for i, o := range Order {
		if o == g {
			return i
		}
	}
	return -1
}

// Known reports whether g is one of Order.
func (g Gate) Known() bool { return g.Rank() >= 0 }

// Reason is why a Work Item bounced back to an earlier gate.
type Reason string

const (
	ReasonDefect        Reason = "defect"
	ReasonRequirement   Reason = "requirement"
	ReasonMisunderstood Reason = "misunderstood"
	ReasonEnvironment   Reason = "environment"
	ReasonUnknown       Reason = "unknown"
)

// ReasonPrefix starts a label title or a comment that names a bounce reason,
// as in "bounce:defect".
const ReasonPrefix = "bounce:"

var reasons = []Reason{ReasonDefect, ReasonRequirement, ReasonMisunderstood, ReasonEnvironment}

var markup = regexp.MustCompile(`<[^>]*>`)

// ParseReason reads a bounce reason from a label title or a comment. The
// text, with HTML tags removed and leading space trimmed, must start with
// ReasonPrefix followed by a known reason and then the end, a space or
// punctuation. Case is ignored.
func ParseReason(text string) (Reason, bool) {
	plain := strings.ToLower(strings.TrimSpace(html.UnescapeString(markup.ReplaceAllString(text, " "))))
	rest, ok := strings.CutPrefix(plain, ReasonPrefix)
	if !ok {
		return "", false
	}
	rest = strings.TrimLeft(rest, " ")
	for _, r := range reasons {
		tail, ok := strings.CutPrefix(rest, string(r))
		if !ok {
			continue
		}
		if tail == "" || !wordChar(tail[0]) {
			return r, true
		}
	}
	return "", false
}

func wordChar(b byte) bool {
	return b == '_' || b == '-' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// Statuses is the configured mapping of one board: for each gate, the
// tracker statuses or bucket titles that put a Work Item in it.
type Statuses struct {
	Development []string `yaml:"development" json:"development,omitempty"`
	Test        []string `yaml:"test" json:"test,omitempty"`
	Acceptance  []string `yaml:"acceptance" json:"acceptance,omitempty"`
	Done        []string `yaml:"done" json:"done,omitempty"`
}

func (s Statuses) byGate() []struct {
	gate  Gate
	names []string
} {
	return []struct {
		gate  Gate
		names []string
	}{{Development, s.Development}, {Test, s.Test}, {Acceptance, s.Acceptance}, {Done, s.Done}}
}

// Map resolves a tracker status to a gate. Status names are compared
// trimmed and case-insensitively.
type Map struct {
	byStatus map[string]Gate
}

func statusKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NewMap validates s: at least one status, no empty or padded name, and no
// status in two gates.
func NewMap(s Statuses) (Map, error) {
	m := Map{byStatus: map[string]Gate{}}
	for _, g := range s.byGate() {
		for _, name := range g.names {
			if name == "" || strings.TrimSpace(name) != name {
				return Map{}, fmt.Errorf("%s: status %q must be non-empty with no surrounding space", g.gate, name)
			}
			key := statusKey(name)
			if prev, dup := m.byStatus[key]; dup {
				if prev == g.gate {
					return Map{}, fmt.Errorf("%s: status %q is listed twice", g.gate, name)
				}
				return Map{}, fmt.Errorf("%s: status %q is already mapped to %s", g.gate, name, prev)
			}
			m.byStatus[key] = g.gate
		}
	}
	if len(m.byStatus) == 0 {
		return Map{}, fmt.Errorf("maps no status to any gate")
	}
	return m, nil
}

// Resolve returns the gate of a Work Item whose tracker reports statuses,
// and the status that decided it. ok is false when no status maps to a gate
// or when statuses map to two different gates.
func (m Map) Resolve(statuses []string) (g Gate, status string, ok bool) {
	for _, s := range statuses {
		mapped, found := m.byStatus[statusKey(s)]
		if !found {
			continue
		}
		if g != "" && mapped != g {
			return "", "", false
		}
		if g == "" {
			g, status = mapped, s
		}
	}
	return g, status, g != ""
}

// Boards holds the gate map of every configured board, by tracker provider
// name and then by the provider's container id.
type Boards map[string]map[string]Map

// Lookup returns the map of one board.
func (b Boards) Lookup(provider, scope string) (Map, bool) {
	m, ok := b[provider][scope]
	return m, ok
}

// Has reports whether any board of provider has a gate map.
func (b Boards) Has(provider string) bool { return len(b[provider]) > 0 }

// Transition is one recorded move of a Work Item into a gate. Reason is the
// bounce reason found when the move was recorded, empty when none was.
type Transition struct {
	Gate   Gate
	Status string
	At     time.Time
	Actor  string
	Reason Reason
}

// IsBounce reports whether a move from one gate to another goes back.
func IsBounce(from, to Gate) bool {
	return from.Known() && to.Known() && to.Rank() < from.Rank()
}

// Visit is one stay in a gate. Left is nil for the current gate.
type Visit struct {
	Gate    Gate
	Entered time.Time
	Left    *time.Time
}

// Bounce is a move back to an earlier gate.
type Bounce struct {
	From   Gate
	To     Gate
	At     time.Time
	Reason Reason
	Actor  string
}

// Counts reports whether a bounce counts against delivery: only defect and
// unknown bounces do.
func (b Bounce) Counts() bool { return b.Reason == ReasonDefect || b.Reason == ReasonUnknown }

// Journey is what the transitions of one Work Item say, oldest first.
type Journey struct {
	Current Gate
	History []Visit
	Bounces []Bounce
	// RightFirstTime has one entry for every gate after the first that the
	// Work Item entered: the number of counting bounces that left it.
	RightFirstTime map[Gate]int
	// Evolved is true when a requirement bounce left acceptance or done.
	Evolved bool
}

// Walk derives the journey of transitions, which must be ordered oldest
// first. A transition into the gate the Work Item is already in changes
// nothing. It returns false when there is no transition.
func Walk(transitions []Transition) (Journey, bool) {
	var j Journey
	j.RightFirstTime = map[Gate]int{}
	for _, t := range transitions {
		if !t.Gate.Known() || t.Gate == j.Current {
			continue
		}
		if n := len(j.History); n > 0 {
			at := t.At
			j.History[n-1].Left = &at
			if IsBounce(j.Current, t.Gate) {
				reason := t.Reason
				if reason == "" {
					reason = ReasonUnknown
				}
				b := Bounce{From: j.Current, To: t.Gate, At: t.At, Reason: reason, Actor: t.Actor}
				j.Bounces = append(j.Bounces, b)
				if b.Counts() {
					j.RightFirstTime[b.From]++
				}
				if b.Reason == ReasonRequirement && b.From.Rank() >= Acceptance.Rank() {
					j.Evolved = true
				}
			}
		}
		if t.Gate != Development {
			if _, seen := j.RightFirstTime[t.Gate]; !seen {
				j.RightFirstTime[t.Gate] = 0
			}
		}
		j.History = append(j.History, Visit{Gate: t.Gate, Entered: t.At})
		j.Current = t.Gate
	}
	return j, len(j.History) > 0
}

// SortedGates returns the keys of counts in gate order.
func SortedGates(counts map[Gate]int) []Gate {
	out := make([]Gate, 0, len(counts))
	for g := range counts {
		out = append(out, g)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Rank() < out[k].Rank() })
	return out
}
