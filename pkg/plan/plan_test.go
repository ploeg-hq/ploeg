package plan

import (
	"encoding/json"
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
		{"role named ask",
			`{"t": {"pool": "1", "rounds": [{"roles": [{"name": "ask"}]}]}}`,
			"reserved for Asks"},
		{"role named chat",
			`{"t": {"pool": "1", "rounds": [{"roles": [{"name": "chat"}]}]}}`,
			"reserved for chat messages"},
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

// ADR-0091: the plan hash names one plan shape. The same plan parsed twice
// hashes the same, any change to what ploegd reads changes it, and a
// plan-less team has none.
func TestHash_NamesOnePlanShape(t *testing.T) {
	const doc = `{"bronze": {"pool": "6", "rounds": [{"roles": [{"name": "builder", "writes": true, "cap": "3"}]}]}}`
	a, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(strings.ReplaceAll(doc, `"cap": "3"`, `"cap": 3`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash("bronze") == "" || a.Hash("bronze") != b.Hash("bronze") || !strings.HasPrefix(a.Hash("bronze"), "sha256:") || len(a.Hash("bronze")) != 71 {
		t.Fatalf("hash = %q and %q, want one sha256 for the same plan", a.Hash("bronze"), b.Hash("bronze"))
	}
	c, err := Parse(strings.ReplaceAll(doc, `"cap": "3"`, `"cap": "4"`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash("bronze") == a.Hash("bronze") {
		t.Fatal("a changed cap kept the plan hash")
	}
	if a.Hash("silver") != "" {
		t.Fatalf("plan-less team hash = %q, want none", a.Hash("silver"))
	}
}

// ADR-0096: a shadow is a capped reader beside a reader that acts.
func TestParse_ShadowRole(t *testing.T) {
	const ok = `{"bronze": {"pool": "8", "maxFixRounds": 2, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": "2"}]},
		{"roles": [{"name": "reviewer", "cap": "0.40"}, {"name": "shadow-reviewer", "shadow": true, "cap": "0.40"}]}
	]}}`
	p, err := Parse(ok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !p.IsShadow("bronze", "shadow-reviewer") || p.IsShadow("bronze", "reviewer") || p.IsShadow("silver", "shadow-reviewer") {
		t.Fatal("IsShadow disagrees with the plan")
	}

	cases := map[string]struct{ in, wantErr string }{
		"writer": {`{"t": {"rounds": [{"roles": [{"name": "b", "writes": true, "shadow": true, "cap": "1"}]}]}}`,
			"only a reading Role can be a shadow"},
		"planner": {`{"t": {"rounds": [{"roles": [{"name": "r", "cap": "1"}, {"name": "p", "planner": true, "shadow": true, "cap": "1"}]}]}}`,
			"a shadow and a planner"},
		"no cap": {`{"t": {"rounds": [{"roles": [{"name": "r"}, {"name": "s", "shadow": true}]}]}}`,
			"needs its own cap"},
		"alone": {`{"t": {"rounds": [{"roles": [{"name": "s", "shadow": true, "cap": "1"}]}]}}`,
			"needs a reading Role beside it"},
		"flips": {`{"t": {"rounds": [
			{"roles": [{"name": "r", "cap": "1"}, {"name": "s", "shadow": true, "cap": "1"}]},
			{"roles": [{"name": "s", "cap": "1"}]}]}}`, "one shadow flag"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(c.in)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
		})
	}
}

// ADR-0091 hashes stay stable for every plan without a shadow, and a shadow
// changes the hash, so Runs before and after adding one are told apart.
func TestHash_ShadowOnlyChangesPlansThatHaveOne(t *testing.T) {
	const doc = `{"t": {"rounds": [{"roles": [{"name": "r", "cap": "1"}, {"name": "s", "cap": "1"}]}]}}`
	plain, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(plain["t"]); strings.Contains(string(b), "shadow") {
		t.Fatalf("a plan without a shadow encodes %s, which would change its ADR-0091 hash", b)
	}
	shadowed, err := Parse(strings.Replace(doc, `{"name": "s",`, `{"name": "s", "shadow": true,`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if shadowed.Hash("t") == plain.Hash("t") {
		t.Fatal("adding a shadow kept the plan hash")
	}
}

func TestMaxRoundsCountsEachFixRoundAsAWriterAndAReview(t *testing.T) {
	p := Plans{"t": {MaxFixRounds: 3, Rounds: []Round{{Roles: []Role{{Name: "w", Writes: true}}}, {Roles: []Role{{Name: "r"}}}}}}
	if got := p.MaxRounds("t"); got != 8 {
		t.Errorf("MaxRounds = %d, want 2 planned + 2×3 fix = 8", got)
	}
	if got := p.MaxRounds("unknown"); got != 0 {
		t.Errorf("MaxRounds of an unknown team = %d, want 0", got)
	}
}
