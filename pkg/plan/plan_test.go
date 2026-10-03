package plan

import (
	"strings"
	"testing"
)

// The chart renders money as strings (values.yaml types budgets as strings so
// YAML cannot mangle them), but hand-written JSON uses numbers. Both must
// parse.
func TestParse_MoneyAsStringOrNumber(t *testing.T) {
	p, err := Parse(`{
		"bronze": {"pool": "6", "rounds": [
			{"roles": [{"name": "analyst", "writes": false, "cap": "0.50"}]},
			{"roles": [{"name": "builder", "writes": true, "cap": 3}]}
		]}
	}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tp := p["bronze"]
	if float64(tp.Pool) != 6 {
		t.Errorf("pool = %v, want 6", float64(tp.Pool))
	}
	if got := p.RoleCap("bronze", "analyst"); got != 0.5 {
		t.Errorf("analyst cap = %v, want 0.5", got)
	}
	if got := p.RoleCap("bronze", "builder"); got != 3 {
		t.Errorf("builder cap = %v, want 3", got)
	}
}

// The chart serialises the whole plan including workload knobs (model, image,
// harness) that only the pod templates consume — the orchestrator must ignore
// them rather than reject them.
func TestParse_IgnoresChartOnlyKeys(t *testing.T) {
	_, err := Parse(`{"bronze": {"pool": "1", "rounds": [
		{"roles": [{"name": "analyst", "writes": false, "cap": "0.5",
			"model": "litellm_proxy/deepseek-chat", "harness": {"dind": false}, "maxReplicaCount": 1}]}
	]}}`)
	if err != nil {
		t.Fatalf("chart-only keys must be ignored, got: %v", err)
	}
}

func TestParse_EmptyIsPlanless(t *testing.T) {
	for _, in := range []string{"", "  ", "{}"} {
		p, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if len(p) != 0 {
			t.Errorf("Parse(%q) = %d plans, want 0", in, len(p))
		}
	}
}

func TestParse_RejectsBadPlans(t *testing.T) {
	cases := []struct {
		name, in, wantErr string
	}{
		{"mixed round",
			`{"t": {"pool": "1", "rounds": [{"roles": [
				{"name": "a", "writes": true}, {"name": "b", "writes": false}]}]}}`,
			"mixes a writer with readers"},
		{"two writers",
			`{"t": {"pool": "1", "rounds": [{"roles": [
				{"name": "a", "writes": true}, {"name": "b", "writes": true}]}]}}`,
			"at most one"},
		{"no rounds", `{"t": {"pool": "1", "rounds": []}}`, "at least one round"},
		{"empty round", `{"t": {"pool": "1", "rounds": [{"roles": []}]}}`, "no roles"},
		{"bad role name",
			`{"t": {"pool": "1", "rounds": [{"roles": [{"name": "Analyst!"}]}]}}`,
			"DNS label"},
		{"duplicate role in round",
			`{"t": {"pool": "1", "rounds": [{"roles": [{"name": "a"}, {"name": "a"}]}]}}`,
			"appears twice"},
		{"writes flag flips between rounds",
			`{"t": {"pool": "1", "rounds": [
				{"roles": [{"name": "a", "writes": false}]},
				{"roles": [{"name": "a", "writes": true}]}]}}`,
			"one writes flag"},
		{"negative pool", `{"t": {"pool": "-1", "rounds": [{"roles": [{"name": "a"}]}]}}`, "negative"},
		{"negative cap",
			`{"t": {"pool": "1", "rounds": [{"roles": [{"name": "a", "cap": "-0.5"}]}]}}`,
			"negative"},
		// A metered fan-out without caps does not fan out: the first claimant
		// reserves the whole pool and its siblings get ErrBudgetExhausted.
		{"uncapped role in a fan-out",
			`{"t": {"pool": "6", "rounds": [{"roles": [
				{"name": "a", "cap": "0.5"}, {"name": "b"}]}]}}`,
			"starves its siblings"},
		{"planner that writes",
			`{"t": {"pool": "1", "rounds": [{"roles": [{"name": "a", "writes": true, "planner": true}]}]}}`,
			"never writes"},
		{"garbage", `not json`, "invalid JSON"},
		{"bad money", `{"t": {"pool": "six", "rounds": [{"roles": [{"name": "a"}]}]}}`, "money value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.in)
			if err == nil {
				t.Fatalf("Parse accepted a bad plan")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// A single-role round needs no cap (the pool alone bounds it), and an
// unmetered plan (pool 0) needs none anywhere.
func TestParse_CapsOnlyRequiredForMeteredFanOut(t *testing.T) {
	if _, err := Parse(`{"t": {"pool": "6", "rounds": [{"roles": [{"name": "solo", "writes": true}]}]}}`); err != nil {
		t.Errorf("single uncapped role rejected: %v", err)
	}
	if _, err := Parse(`{"t": {"pool": "0", "rounds": [{"roles": [
		{"name": "a"}, {"name": "b"}]}]}}`); err != nil {
		t.Errorf("uncapped fan-out on an unmetered pool rejected: %v", err)
	}
}

// A role name may recur across rounds with the same writes flag: that is one
// workload doing another stint (the review→fix loop depends on it).
func TestParse_SameRoleAcrossRoundsIsOneWorkload(t *testing.T) {
	_, err := Parse(`{"t": {"pool": "1", "rounds": [
		{"roles": [{"name": "builder", "writes": true}]},
		{"roles": [{"name": "reviewer", "writes": false}]},
		{"roles": [{"name": "builder", "writes": true}]}
	]}}`)
	if err != nil {
		t.Fatalf("recurring role rejected: %v", err)
	}
}

func TestRoleCapUSD(t *testing.T) {
	p, err := Parse(`{"t": {"pool": "1", "rounds": [
		{"roles": [{"name": "a", "cap": "0.5"}, {"name": "b", "cap": "0.5"}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	roles := p["t"].Rounds[0].Roles
	if len(roles) != 2 || roles[0].Name != "a" || roles[0].CapUSD() != 0.5 || roles[0].Writes {
		t.Errorf("roles = %+v", roles)
	}
}

// --- the review loop's configuration (ADR-0017) -----------------------------

func TestParse_MaxFixRoundsNeedsAWriterToReopen(t *testing.T) {
	_, err := Parse(`{"t": {"pool": "6", "maxFixRounds": 2, "rounds": [
		{"roles": [{"name": "analyst", "cap": "0.5"}, {"name": "tests", "cap": "0.5"}]}
	]}}`)
	if err == nil {
		t.Fatal("a reader-only plan accepted fix rounds it can never run")
	}
	if !strings.Contains(err.Error(), "no writing round") {
		t.Errorf("error does not explain the problem: %v", err)
	}
	// Zero fix rounds on a reader-only plan is fine — the loop is off.
	if _, err := Parse(`{"t": {"pool": "6", "rounds": [
		{"roles": [{"name": "analyst", "cap": "0.5"}, {"name": "tests", "cap": "0.5"}]}
	]}}`); err != nil {
		t.Errorf("reader-only plan without fix rounds rejected: %v", err)
	}
}

func TestWriterRound_FindsTheLastWriter(t *testing.T) {
	p, err := Parse(`{"t": {"pool": "6", "maxFixRounds": 1, "rounds": [
		{"roles": [{"name": "analyst", "cap": "0.5"}, {"name": "tests", "cap": "0.5"}]},
		{"roles": [{"name": "builder", "writes": true, "cap": "3"}]},
		{"roles": [{"name": "reviewer", "cap": "0.75"}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	idx, ok := p["t"].WriterRound()
	if !ok || idx != 1 {
		t.Errorf("WriterRound = (%d, %v), want (1, true)", idx, ok)
	}
	if p["t"].MaxFixRounds != 1 {
		t.Errorf("maxFixRounds = %d, want 1", p["t"].MaxFixRounds)
	}
}

func TestPlans_IsPlanner(t *testing.T) {
	p, err := Parse(`{"t": {"rounds": [{"roles": [{"name": "planner", "planner": true}, {"name": "analyst"}]}, {"roles": [{"name": "builder", "writes": true}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	for role, want := range map[string]bool{"planner": true, "analyst": false, "builder": false, "": false} {
		if got := p.IsPlanner("t", role); got != want {
			t.Errorf("IsPlanner(t, %q) = %v, want %v", role, got, want)
		}
	}
	if p.IsPlanner("other", "planner") {
		t.Error("a planner leaked into another team")
	}
	if !p["t"].HasRole("builder") || p["t"].HasRole("reviewer") {
		t.Error("HasRole disagrees with the plan")
	}
}
