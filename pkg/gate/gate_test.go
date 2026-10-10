package gate

import (
	"strings"
	"testing"
)

func TestParseReason(t *testing.T) {
	for _, tc := range []struct {
		text string
		want Reason
		ok   bool
	}{
		{"bounce:defect", ReasonDefect, true},
		{"Bounce:Requirement", ReasonRequirement, true},
		{"  bounce: misunderstood the login flow", ReasonMisunderstood, true},
		{"<p>bounce:environment — staging was down</p>", ReasonEnvironment, true},
		{"bounce:defect.", ReasonDefect, true},
		{"bounce:defective", "", false},
		{"bounce:unknown", "", false},
		{"bounce:", "", false},
		{"please bounce:defect", "", false},
		{"defect", "", false},
		{"", "", false},
	} {
		got, ok := ParseReason(tc.text)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseReason(%q) = %q, %v; want %q, %v", tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNewMapRejectsInvalidMappings(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Statuses
		want string
	}{
		{"empty", Statuses{}, "maps no status"},
		{"blank status", Statuses{Test: []string{""}}, "non-empty"},
		{"padded status", Statuses{Test: []string{" In test"}}, "surrounding space"},
		{"two gates", Statuses{Test: []string{"Review"}, Acceptance: []string{"review"}}, "already mapped to test"},
		{"twice in one gate", Statuses{Done: []string{"Done", "done"}}, "listed twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewMap(tc.s); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestMapResolve(t *testing.T) {
	m, err := NewMap(Statuses{Development: []string{"Doing"}, Test: []string{"In test"}, Acceptance: []string{"Acceptance", "UAT"}, Done: []string{"Done"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		statuses []string
		gate     Gate
		status   string
		ok       bool
	}{
		{[]string{"in test"}, Test, "in test", true},
		{[]string{"Backlog", "UAT"}, Acceptance, "UAT", true},
		{[]string{"UAT", "Acceptance"}, Acceptance, "UAT", true},
		{[]string{"Doing", "Done"}, "", "", false},
		{[]string{"Backlog"}, "", "", false},
		{nil, "", "", false},
	} {
		g, status, ok := m.Resolve(tc.statuses)
		if g != tc.gate || status != tc.status || ok != tc.ok {
			t.Errorf("Resolve(%q) = %q %q %v; want %q %q %v", tc.statuses, g, status, ok, tc.gate, tc.status, tc.ok)
		}
	}
}

func TestIsBounceOnlyForAMoveBackBetweenKnownGates(t *testing.T) {
	for _, c := range []struct {
		from, to Gate
		want     bool
	}{
		{Test, Development, true},
		{Done, Acceptance, true},
		{Development, Test, false},
		{Test, Test, false},
		{"", Development, false},
		{Test, "review", false},
	} {
		if got := IsBounce(c.from, c.to); got != c.want {
			t.Errorf("IsBounce(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
