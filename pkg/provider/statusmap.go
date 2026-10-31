package provider

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// MappableStates are the Ploeg states a TrackerProvider is told through
// SetStatus. A tracker whose statuses are its operator's own words maps
// these, and only these, to them (ADR-0183).
var MappableStates = []work.State{work.StateAwaitingReview, work.StateNeedsHuman, work.StateDone}

// ParseStatusMap reads "state=tracker status" pairs separated by commas,
// such as "awaiting_review=in review,needs_human=blocked,done=complete". A
// state outside MappableStates, an empty status or a state named twice is an
// error, so a typo fails at start-up instead of silently never moving an
// item. Status names are kept as given.
func ParseStatusMap(s string) (map[work.State]string, error) {
	out := map[work.State]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, name, ok := strings.Cut(pair, "=")
		key, name = strings.TrimSpace(key), strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("status map: %q is not state=status", pair)
		}
		state, known := work.ParseState(key)
		if !known || !slices.Contains(MappableStates, state) {
			return nil, fmt.Errorf("status map: %q is not one of %v", key, MappableStates)
		}
		if _, dup := out[state]; dup {
			return nil, fmt.Errorf("status map: %s is named twice", state)
		}
		out[state] = name
	}
	return out, nil
}
