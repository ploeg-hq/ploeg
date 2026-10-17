package plan

import "testing"

func TestReviewOnly_KeepsTheReadersOfTheLastReviewRoundAndNoWriter(t *testing.T) {
	plans, err := Parse(`{"bronze": {"pool": 9.20, "maxFixRounds": 2, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 2}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 0.40},
		           {"name": "shadow-reviewer", "writes": false, "shadow": true, "cap": 0.40}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	review, ok := plans["bronze"].ReviewOnly()
	if !ok {
		t.Fatal("a plan with a reviewer must reduce to a review-only plan")
	}
	if len(review.Rounds) != 1 || len(review.Rounds[0].Roles) != 2 {
		t.Fatalf("rounds = %+v, want one round with reviewer and shadow", review.Rounds)
	}
	for _, r := range review.Rounds[0].Roles {
		if r.Writes {
			t.Errorf("role %q writes; nothing may write to someone else's pull request", r.Name)
		}
	}
	if review.Rounds[0].Roles[0].Name != "reviewer" || !review.Rounds[0].Roles[1].Shadow {
		t.Errorf("roles = %+v", review.Rounds[0].Roles)
	}
	if got := review.PoolUSD(); got < 0.79 || got > 0.81 {
		t.Errorf("pool = %.2f, want the sum of the caps 0.80", got)
	}
	if review.MaxFixRounds != 0 {
		t.Errorf("maxFixRounds = %d, want 0: no verdict re-opens a writer", review.MaxFixRounds)
	}
	if _, hasWriter := review.WriterRound(); hasWriter {
		t.Error("a review-only plan has no writer round")
	}
}

func TestReviewOnly_RefusesAPlanWithoutAnActingReader(t *testing.T) {
	for label, spec := range map[string]string{
		"writer only": `{"t": {"rounds": [{"roles": [{"name": "builder", "writes": true}]}]}}`,
		"planner only": `{"t": {"rounds": [{"roles": [{"name": "planner", "writes": false, "planner": true}]},
			{"roles": [{"name": "builder", "writes": true}]}]}}`,
	} {
		plans, err := Parse(spec)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if _, ok := plans["t"].ReviewOnly(); ok {
			t.Errorf("%s: reduced to a review-only plan although no Role explains", label)
		}
	}
}

func TestReviewOnly_AnUncappedReaderLeavesThePoolUnmeteredAndTheTeamPoolBounds(t *testing.T) {
	plans, err := Parse(`{"t": {"pool": 0.30, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 2}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 0.40}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	review, _ := plans["t"].ReviewOnly()
	if got := review.PoolUSD(); got < 0.29 || got > 0.31 {
		t.Errorf("pool = %.2f, want the team pool 0.30 when it is below the caps", got)
	}
	uncapped, _ := Parse(`{"t": {"rounds": [
		{"roles": [{"name": "builder", "writes": true}]},
		{"roles": [{"name": "reviewer", "writes": false}]}
	]}}`)
	review, _ = uncapped["t"].ReviewOnly()
	if review.PoolUSD() != 0 {
		t.Errorf("pool = %.2f, want 0 (unmetered) for an uncapped reader", review.PoolUSD())
	}
}
