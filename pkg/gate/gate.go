// Package gate maps tracker statuses to the delivery gates a Work Item
// passes, and reads the reason a Work Item bounced back (ADR-0051).
package gate

import (
	"fmt"
	"html"
	"regexp"
	"strings"
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

// IsBounce reports whether a move from one gate to another goes back.
func IsBounce(from, to Gate) bool {
	return from.Known() && to.Known() && to.Rank() < from.Rank()
}
