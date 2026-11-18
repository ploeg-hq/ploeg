package config

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultMaxTrackerBindings is how many live runtime bindings a Team accepts
// when its trackerBindings names no max (ADR-0186).
const DefaultMaxTrackerBindings = 20

// MaxTrackerBindingsCeiling bounds trackerBindings.max.
const MaxTrackerBindingsCeiling = 1000

// TrackerBindings is a Team's opt-in to runtime Scope bindings (ADR-0186):
// the tracker connections a binding for the Team may use and how many live
// bindings it may hold.
type TrackerBindings struct {
	// Connections names each connection a binding may use: a configured
	// tracker connection by name, "*" for any store-backed tracker
	// connection in state active, or "*:<kind>" for any such connection of
	// that kind (ADR-0195). A wildcard never matches a static connection.
	Connections []string `yaml:"connections"`
	// Max is the number of live bindings the Team may hold; omitted is
	// DefaultMaxTrackerBindings.
	Max *int `yaml:"max"`
}

// Limit is the number of live bindings the Team may hold.
func (b TrackerBindings) Limit() int {
	if b.Max == nil {
		return DefaultMaxTrackerBindings
	}
	return *b.Max
}

// ConnectionRef is what decides whether a Team's trackerBindings admits a
// connection: its id, its adapter kind, whether it is store-backed and
// whether it is active.
type ConnectionRef struct {
	ID      string
	Kind    string
	Runtime bool
	Active  bool
}

// Accepts reports whether a binding for the Team may use c.
func (b TrackerBindings) Accepts(c ConnectionRef) bool {
	for _, entry := range b.Connections {
		switch {
		case entry == "*":
			if c.Runtime && c.Active {
				return true
			}
		case strings.HasPrefix(entry, "*:"):
			if c.Runtime && c.Active && strings.TrimPrefix(entry, "*:") == c.Kind {
				return true
			}
		case entry == c.ID:
			return true
		}
	}
	return false
}

var (
	bindingConnectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	bindingConnectionKind = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// TeamTrackerBindings returns the Team's trackerBindings, and false when the
// Team does not exist or accepts no bindings.
func (f *File) TeamTrackerBindings(team string) (TrackerBindings, bool) {
	t, ok := f.Teams[team]
	if !ok || t.TrackerBindings == nil {
		return TrackerBindings{}, false
	}
	return *t.TrackerBindings, true
}

func (f *File) validateTrackerBindings() error {
	for _, name := range sortedTeamNames(f.Teams) {
		b := f.Teams[name].TrackerBindings
		if b == nil {
			continue
		}
		where := fmt.Sprintf("teams.%s.trackerBindings", name)
		if len(b.Connections) == 0 {
			return fmt.Errorf("%s.connections: name at least one connection, \"*\" or \"*:<kind>\"", where)
		}
		seen := map[string]bool{}
		for i, entry := range b.Connections {
			valid := entry == "*" || bindingConnectionName.MatchString(entry) ||
				(strings.HasPrefix(entry, "*:") && bindingConnectionKind.MatchString(strings.TrimPrefix(entry, "*:")))
			if !valid || seen[entry] {
				return fmt.Errorf("%s.connections[%d]: %q is not a connection name, \"*\" or \"*:<kind>\", or is named twice", where, i, entry)
			}
			seen[entry] = true
		}
		if b.Max != nil && (*b.Max < 1 || *b.Max > MaxTrackerBindingsCeiling) {
			return fmt.Errorf("%s.max: must be between 1 and %d, got %d", where, MaxTrackerBindingsCeiling, *b.Max)
		}
	}
	return nil
}
