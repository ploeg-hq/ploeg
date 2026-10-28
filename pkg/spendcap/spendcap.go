// Package spendcap is a Team's cap on its total spend per calendar period
// (ADR-0162): the figure ploegd checks before it starts a Run.
package spendcap

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Period is the calendar window a cap counts over, in UTC.
type Period string

// The periods a cap may count over. A week starts on Monday.
const (
	Day   Period = "day"
	Week  Period = "week"
	Month Period = "month"
)

// Valid reports whether p is one of the supported periods.
func (p Period) Valid() bool {
	switch p {
	case Day, Week, Month:
		return true
	}
	return false
}

// Start is the beginning of the period that contains at, in UTC.
func (p Period) Start(at time.Time) time.Time {
	at = at.UTC()
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	switch p {
	case Week:
		sinceMonday := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -sinceMonday)
	case Month:
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return day
}

// End is the beginning of the period after the one that contains at.
func (p Period) End(at time.Time) time.Time {
	start := p.Start(at)
	switch p {
	case Week:
		return start.AddDate(0, 0, 7)
	case Month:
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 1)
}

// Cap bounds what one Team's Runs may spend in a period: the spend settled in
// the period plus every hold its Runs keep. A zero Cap bounds nothing.
type Cap struct {
	USD    float64 `json:"usd" yaml:"usd"`
	Period Period  `json:"period" yaml:"period"`
}

// Capped reports whether the cap bounds anything.
func (c Cap) Capped() bool { return c.USD > 0 }

// Validate refuses a negative, non-finite or unperiodic cap. A zero USD is
// valid and means no cap.
func (c Cap) Validate() error {
	if math.IsNaN(c.USD) || math.IsInf(c.USD, 0) || c.USD < 0 {
		return fmt.Errorf("usd must be a finite amount of 0 (no cap) or more, got %v", c.USD)
	}
	if c.USD > 0 && !c.Period.Valid() {
		return fmt.Errorf("period must be day, week or month, got %q", c.Period)
	}
	return nil
}

// Caps maps a team to its cap. A team that is absent is not capped.
type Caps map[string]Cap

// Parse reads a JSON object of team name to cap, the shape the chart renders
// into PLOEG_TEAM_SPEND_CAPS. An empty string is no caps.
func Parse(raw string) (Caps, error) {
	caps := Caps{}
	if strings.TrimSpace(raw) == "" {
		return caps, nil
	}
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return nil, err
	}
	teams := make([]string, 0, len(caps))
	for team := range caps {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	for _, team := range teams {
		if err := caps[team].Validate(); err != nil {
			return nil, fmt.Errorf("team %s: %w", team, err)
		}
	}
	return caps, nil
}
