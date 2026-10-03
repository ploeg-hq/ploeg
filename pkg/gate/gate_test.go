package gate

import (
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestWalkDerivesHistoryBouncesAndRightFirstTime(t *testing.T) {
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	j, ok := Walk([]Transition{
		{Gate: Development, At: at(0)},
		{Gate: Test, At: at(1), Actor: "dev"},
		{Gate: Test, At: at(2)},
		{Gate: Development, At: at(3), Actor: "qa", Reason: ReasonDefect},
		{Gate: Test, At: at(4)},
		{Gate: Development, At: at(5), Actor: "qa"},
		{Gate: Test, At: at(6)},
		{Gate: Acceptance, At: at(7), Actor: "qa"},
		{Gate: Development, At: at(8), Actor: "po", Reason: ReasonEnvironment},
		{Gate: Acceptance, At: at(9)},
		{Gate: "imagined", At: at(10)},
	})
	if !ok {
		t.Fatal("no journey")
	}
	if j.Current != Acceptance || len(j.History) != 9 || j.History[8].Left != nil || !j.History[0].Left.Equal(at(1)) {
		t.Fatalf("history = %+v, current %s", j.History, j.Current)
	}
	want := []Bounce{
		{From: Test, To: Development, At: at(3), Reason: ReasonDefect, Actor: "qa"},
		{From: Test, To: Development, At: at(5), Reason: ReasonUnknown, Actor: "qa"},
		{From: Acceptance, To: Development, At: at(8), Reason: ReasonEnvironment, Actor: "po"},
	}
	if !reflect.DeepEqual(j.Bounces, want) {
		t.Fatalf("bounces = %+v", j.Bounces)
	}
	if !reflect.DeepEqual(j.RightFirstTime, map[Gate]int{Test: 2, Acceptance: 0}) {
		t.Fatalf("rightFirstTime = %v; an environment bounce must not count", j.RightFirstTime)
	}
	if j.Evolved {
		t.Fatal("evolved without a requirement bounce")
	}
}

func TestWalkMarksARequirementBounceAfterAcceptanceEvolved(t *testing.T) {
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	before, _ := Walk([]Transition{{Gate: Test, At: base}, {Gate: Development, At: base.Add(time.Hour), Reason: ReasonRequirement}})
	if before.Evolved || before.RightFirstTime[Test] != 0 {
		t.Fatalf("a requirement bounce out of test: evolved %v, rightFirstTime %v", before.Evolved, before.RightFirstTime)
	}
	after, _ := Walk([]Transition{{Gate: Acceptance, At: base}, {Gate: Test, At: base.Add(time.Hour), Reason: ReasonRequirement}})
	if !after.Evolved || after.RightFirstTime[Acceptance] != 0 {
		t.Fatalf("a requirement bounce out of acceptance: evolved %v, rightFirstTime %v", after.Evolved, after.RightFirstTime)
	}
	if _, ok := Walk(nil); ok {
		t.Fatal("a journey without transitions")
	}
}
