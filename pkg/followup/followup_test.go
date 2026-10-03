package followup

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func proposals(n int, ready bool) []harness.CreatedWorkItem {
	out := make([]harness.CreatedWorkItem, n)
	for i := range out {
		out[i] = harness.CreatedWorkItem{Title: "item", Ready: ready, Kind: work.CreatedSplit}
	}
	return out
}

func src() Source {
	return Source{Team: "bronze", Outcome: work.OutcomeFollowUpCreated}
}

func accepted(ds []Decision) (n int) {
	for _, d := range ds {
		if d.Accepted {
			n++
		}
	}
	return n
}

func TestDefault_IsConservative(t *testing.T) {
	p := Default()
	if p.AutoDispatch || p.MaxCreatedPerRun != 5 || p.MaxDepth != 2 || p.RefinementTeam != "" || p.ItemBudgetUSD <= 0 || p.PoolUSD < p.ItemBudgetUSD {
		t.Fatalf("defaults changed: %+v", p)
	}
	for _, d := range Decide(p, src(), proposals(2, true), nil) {
		if d.State != work.StateProposed {
			t.Errorf("a default Team dispatched created work without approval: %+v", d)
		}
	}
}

func TestDecide_MaxCreatedPerRunRejectsTheRestWithAReason(t *testing.T) {
	p := Default()
	p.PoolUSD = 100
	ds := Decide(p, src(), proposals(7, true), nil)
	if len(ds) != 7 {
		t.Fatalf("an entry vanished: %d decisions for 7 proposals", len(ds))
	}
	if accepted(ds) != 5 {
		t.Fatalf("accepted %d, want 5", accepted(ds))
	}
	for _, d := range ds[5:] {
		if d.Accepted || !strings.Contains(d.Reason, "maxCreatedPerRun 5") {
			t.Errorf("entry %d: %+v", d.Index, d)
		}
	}
}

func TestDecide_MaxDepth(t *testing.T) {
	p := Default()
	s := src()
	s.Depth = 1
	if ds := Decide(p, s, proposals(1, true), nil); !ds[0].Accepted || ds[0].Depth != 2 {
		t.Fatalf("depth 2 is within maxDepth 2: %+v", ds[0])
	}
	s.Depth = 2
	ds := Decide(p, s, proposals(2, true), nil)
	for _, d := range ds {
		if d.Accepted || !strings.Contains(d.Reason, "depth 3 exceeds maxDepth 2") {
			t.Errorf("entry %d: %+v", d.Index, d)
		}
	}
}

func TestDecide_FloodProtection(t *testing.T) {
	p := Default()
	p.PoolUSD = 100
	s := src()
	s.OpenCreated = p.MaxOpen - 2
	ds := Decide(p, s, proposals(4, true), nil)
	if accepted(ds) != 2 {
		t.Fatalf("accepted %d with room for 2", accepted(ds))
	}
	for _, d := range ds[2:] {
		if d.Accepted || !strings.Contains(d.Reason, "maxOpen") {
			t.Errorf("entry %d: %+v", d.Index, d)
		}
	}
}

func TestDecide_BudgetPool(t *testing.T) {
	p := Default()
	s := src()
	s.PoolAllotted = p.PoolUSD - p.ItemBudgetUSD
	ds := Decide(p, s, proposals(2, true), nil)
	if !ds[0].Accepted || ds[0].BudgetUSD != p.ItemBudgetUSD {
		t.Fatalf("the last allotment was refused: %+v", ds[0])
	}
	if ds[1].Accepted || !strings.Contains(ds[1].Reason, "pool exhausted") {
		t.Fatalf("the pool was overdrawn: %+v", ds[1])
	}
}

func TestDecide_FailedSourceCreatesNothing(t *testing.T) {
	s := src()
	s.Outcome = work.OutcomeFailed
	for _, d := range Decide(Default(), s, proposals(2, true), nil) {
		if d.Accepted || !strings.Contains(d.Reason, "failed") {
			t.Errorf("entry %d: %+v", d.Index, d)
		}
	}
}

func TestDecide_RequestedTeamMustBeKnown(t *testing.T) {
	props := []harness.CreatedWorkItem{
		{Title: "a", Ready: true, Kind: work.CreatedDiscovered, Team: "silver"},
		{Title: "b", Ready: true, Kind: work.CreatedDiscovered, Team: "nowhere"},
	}
	ds := Decide(Default(), src(), props, func(team string) bool { return team == "silver" })
	if !ds[0].Accepted || ds[0].Team != "silver" {
		t.Errorf("known team refused: %+v", ds[0])
	}
	if ds[1].Accepted || !strings.Contains(ds[1].Reason, `unknown team "nowhere"`) {
		t.Errorf("unknown team accepted: %+v", ds[1])
	}
}

func TestDecide_NotReadyRouting(t *testing.T) {
	cases := []struct {
		name       string
		auto       bool
		refinement string
		ready      bool
		team       string
		state      work.State
	}{
		{"not ready, no refinement, approval", false, "", false, "bronze", work.StateProposed},
		{"not ready, no refinement, autoDispatch", true, "", false, "bronze", work.StateProposed},
		{"not ready, refinement, approval", false, "planning", false, "planning", work.StateProposed},
		{"not ready, refinement, autoDispatch", true, "planning", false, "planning", work.StateQueued},
		{"ready, refinement, autoDispatch", true, "planning", true, "bronze", work.StateQueued},
		{"ready, approval", false, "", true, "bronze", work.StateProposed},
	}
	for _, c := range cases {
		p := Default()
		p.AutoDispatch = c.auto
		p.RefinementTeam = c.refinement
		d := Decide(p, src(), proposals(1, c.ready), func(string) bool { return true })[0]
		if !d.Accepted || d.Team != c.team || d.State != c.state {
			t.Errorf("%s: got team %q state %q (%+v)", c.name, d.Team, d.State, d)
		}
	}
}
