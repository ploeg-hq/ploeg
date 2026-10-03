package flow

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

func TestDefaultKind(t *testing.T) {
	cases := []struct {
		status string
		gate   gate.Gate
		want   Kind
	}{
		{"Doing", gate.Development, Active},
		{"In test", gate.Test, Active},
		{"In review", gate.Test, Active},
		{"Ready for test", gate.Test, Waiting},
		{"Waiting for client", gate.Development, Waiting},
		{"To review", gate.Test, Waiting},
		{"Merge queue", gate.Development, Waiting},
		{"Blocked", gate.Development, Blocked},
		{"On hold", "", Blocked},
		{"Blocked by legal", gate.Acceptance, Blocked},
		{"UAT", gate.Acceptance, Waiting},
		{"Done", gate.Done, Done},
		{"Shipped", gate.Done, Done},
		{"Closed", "", Done},
		{"Backlog", "", Waiting},
		{"Refinement", "", Waiting},
		{"Ready", "", Waiting},
		{"Open", "", Waiting},
		{"In progress", "", Active},
		{"Code review", "", Active},
		{"QA", "", Active},
		{"Something else", "", Waiting},
		{"  DOING  ", gate.Development, Active},
	}
	for _, tc := range cases {
		if got := DefaultKind(tc.status, tc.gate); got != tc.want {
			t.Errorf("DefaultKind(%q, %q) = %s, want %s", tc.status, tc.gate, got, tc.want)
		}
	}
}

func TestKindMapOverridesTheDefaults(t *testing.T) {
	m, err := NewKindMap(Kinds{Active: []string{"UAT", "Refinement"}, Waiting: []string{"In test"}, Blocked: []string{"Parked"}, Done: []string{"Won't do"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		status string
		gate   gate.Gate
		want   Kind
	}{
		{"uat", gate.Acceptance, Active},
		{"Refinement", "", Active},
		{"IN TEST", gate.Test, Waiting},
		{"Parked", "", Blocked},
		{"Won't do", "", Done},
		{"Doing", gate.Development, Active},
		{"Blocked", gate.Development, Blocked},
	}
	for _, tc := range cases {
		if got := m.Kind(tc.status, tc.gate); got != tc.want {
			t.Errorf("Kind(%q, %q) = %s, want %s", tc.status, tc.gate, got, tc.want)
		}
	}
	if !m.Configured(" uat ") || m.Configured("Doing") {
		t.Fatal("Configured must report exactly the listed statuses")
	}
	var zero KindMap
	if zero.Kind("Doing", gate.Development) != Active || zero.Configured("Doing") {
		t.Fatal("the zero KindMap applies the defaults")
	}
}

func TestNewKindMapRefusesBadLists(t *testing.T) {
	for _, k := range []Kinds{
		{Active: []string{""}},
		{Active: []string{" Doing"}},
		{Active: []string{"Doing", "doing"}},
		{Active: []string{"Doing"}, Waiting: []string{"DOING"}},
	} {
		if _, err := NewKindMap(k); err == nil {
			t.Errorf("%+v was accepted", k)
		}
	}
	if _, err := NewKindMap(Kinds{}); err != nil {
		t.Fatalf("an empty list applies the defaults: %v", err)
	}
}
