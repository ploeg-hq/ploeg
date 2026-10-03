package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

func (f *cardFixture) move(g gate.Gate, status, actor string, minutes int, reason gate.Reason) bool {
	f.t.Helper()
	recorded, err := testStore.RecordGateMove(context.Background(), GateMove{Provider: "vikunja", ExternalID: f.externalID(),
		Gate: g, Status: status, Actor: actor, At: *f.at(minutes), Reason: reason})
	if err != nil {
		f.t.Fatal(err)
	}
	return recorded
}

func (f *cardFixture) externalID() string {
	f.t.Helper()
	var id string
	if err := testStore.pool.QueryRow(context.Background(), `SELECT external_id FROM work_items WHERE id = $1`, f.item).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestRecordGateMove_KeepsOnlyMovesIntoAnotherGate(t *testing.T) {
	f := newCardFixture(t, "1700")
	ctx := context.Background()
	pos, err := testStore.GatePosition(ctx, "vikunja", "1700")
	if err != nil || pos.WorkItemID != f.item || pos.Gate != "" {
		t.Fatalf("position before any move = %+v, %v", pos, err)
	}
	if !f.move(gate.Development, "Doing", "dev", 0, "") || f.move(gate.Development, "Doing", "dev", 1, "") {
		t.Fatal("a repeated status must be recorded once")
	}
	if !f.move(gate.Test, "In test", "dev", 2, gate.ReasonDefect) {
		t.Fatal("move to test not recorded")
	}
	if !f.move(gate.Development, "Doing", "qa", 3, gate.ReasonDefect) || !f.move(gate.Test, "In test", "dev", 4, "") ||
		!f.move(gate.Development, "Doing", "qa", 5, gate.ReasonUnknown) {
		t.Fatal("bounces not recorded")
	}
	rows, err := testStore.pool.Query(ctx, `SELECT gate, COALESCE(reason, '-'), COALESCE(actor, '-') FROM gate_transitions WHERE work_item_id = $1 ORDER BY id`, f.item)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var g, reason, actor string
		if err := rows.Scan(&g, &reason, &actor); err != nil {
			t.Fatal(err)
		}
		got = append(got, g+"/"+reason+"/"+actor)
	}
	want := []string{"development/-/dev", "test/-/dev", "development/defect/qa", "test/-/dev", "development/-/qa"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v; a reason is kept only on a bounce, and unknown is stored as NULL", got)
	}
	pos, err = testStore.GatePosition(ctx, "vikunja", "1700")
	if err != nil || pos.Gate != gate.Development || !pos.Entered.Equal(*f.at(5)) {
		t.Fatalf("position = %+v, %v", pos, err)
	}
	if _, err := testStore.RecordGateMove(ctx, GateMove{Provider: "vikunja", ExternalID: "nope", Gate: gate.Test, Status: "x"}); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("an unknown ticket: %v", err)
	}
	if _, err := testStore.GatePosition(ctx, "vikunja", "nope"); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("an unknown ticket's position: %v", err)
	}
	if _, err := testStore.RecordGateMove(ctx, GateMove{Provider: "vikunja", ExternalID: "1700", Gate: "review", Status: "x"}); err == nil {
		t.Fatal("an unknown gate was recorded")
	}
}

func TestOperatorCard_GatesBouncesAndRosterFromTrackerMoves(t *testing.T) {
	f := newCardFixture(t, "1701")
	f.openShift("agent/vik-1701")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 10, outcome: "pr_opened", usage: `{"costUsd": 0.5}`, authorizedUSD: 2,
		links: []string{"https://forge.example/webgrip/ploeg/pulls/70"}})
	f.pr(PullRequestFacts{Number: 70, Branch: "agent/vik-1701", State: "open", HeadSHA: "h1"})
	f.pr(PullRequestFacts{Number: 70, Review: &PullRequestReview{Reviewer: "anna", State: "approved", HeadSHA: "h1"}})
	f.move(gate.Development, "Doing", "ploeg-bot", 0, "")
	f.move(gate.Test, "In test", "dev", 20, "")
	f.move(gate.Development, "Doing", "quinn", 30, gate.ReasonDefect)
	f.move(gate.Test, "In test", "dev", 40, "")
	f.move(gate.Acceptance, "UAT", "quinn", 50, "")
	f.move(gate.Test, "In test", "paula", 60, gate.ReasonRequirement)
	f.move(gate.Acceptance, "UAT", "quinn", 70, "")
	f.move(gate.Done, "Done", "paula", 80, "")

	card := f.card("ploeg-bot")
	g := card.Gates
	if g == nil || g.Current != "done" || len(g.History) != 8 || g.History[7].LeftAt != nil || !g.History[0].LeftAt.Equal(*f.at(20)) {
		t.Fatalf("gates = %+v", g)
	}
	want := []CardBounce{
		{From: "test", To: "development", At: *f.at(30), Reason: "defect", Actor: "quinn"},
		{From: "acceptance", To: "test", At: *f.at(60), Reason: "requirement", Actor: "paula"},
	}
	if !reflect.DeepEqual(g.Bounces, want) {
		t.Fatalf("bounces = %+v", g.Bounces)
	}
	if !reflect.DeepEqual(g.RightFirstTime, map[string]int{"test": 1, "acceptance": 0, "done": 0}) {
		t.Fatalf("rightFirstTime = %v", g.RightFirstTime)
	}
	if !card.Evolved {
		t.Fatal("a requirement bounce out of acceptance must mark the card evolved")
	}
	if fmt.Sprintf("%+v", card.Roster) != "[{Name:anna Roles:[reviewer]} {Name:paula Roles:[acceptor]} {Name:quinn Roles:[qa]}]" {
		t.Fatalf("roster = %+v", card.Roster)
	}
	gr := card.Grade
	if gr == nil || gr.Inputs.Delivery.DefectBounces == nil || *gr.Inputs.Delivery.DefectBounces != 1 ||
		gr.Inputs.Delivery.BudgetShare == nil || *gr.Inputs.Delivery.BudgetShare != 0.25 || gr.Inputs.Review.ReviewRounds != 1 ||
		gr.Subgrades.Delivery != 8.5 || gr.Inputs.Durability.LiveSince != nil || !gr.Provisional || gr.Overall != 8.5 {
		t.Fatalf("grade = %+v; a human approval grades the card, only the defect bounce counts", gr)
	}
}

func TestOperatorCard_WithoutGatesOrAVerdictGradeAndGatesStayAbsent(t *testing.T) {
	f := newCardFixture(t, "1702")
	f.openShift("agent/vik-1702")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 10, outcome: "pr_opened",
		links: []string{"https://forge.example/webgrip/ploeg/pulls/71"}})
	f.pr(PullRequestFacts{Number: 71, Branch: "agent/vik-1702", State: "open", HeadSHA: "h1"})
	f.pr(PullRequestFacts{Number: 71, Review: &PullRequestReview{Reviewer: "ploeg-bot", State: "approved", HeadSHA: "h1"}})
	f.pr(PullRequestFacts{Number: 71, Review: &PullRequestReview{Reviewer: "anna", State: "commented", HeadSHA: "h1"}})

	card := f.card("ploeg-bot")
	if card.Grade != nil || card.Gates != nil || card.Evolved {
		t.Fatalf("grade %+v gates %+v evolved %v; a bot approval or a comment is no verdict, and no move means no gates", card.Grade, card.Gates, card.Evolved)
	}
}

func TestOperatorCard_AMergedPlayIsGradedFromItsRelease(t *testing.T) {
	f := newCardFixture(t, "1703")
	f.openShift("agent/vik-1703")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 10, outcome: "failed", usage: `{"costUsd": 3}`, authorizedUSD: 2})
	f.run(fixtureRun{role: "builder", writes: true, startMin: 20, durationMin: 10, outcome: "pr_opened",
		links: []string{"https://forge.example/webgrip/ploeg/pulls/72"}})
	merged := time.Now().Add(-50 * 24 * time.Hour).UTC().Truncate(time.Second)
	f.pr(PullRequestFacts{Number: 72, Branch: "agent/vik-1703", State: "merged", HeadSHA: "h1", MergeCommitSHA: "m1", MergedAt: &merged, MergedBy: "ryan"})

	card := f.card()
	gr := card.Grade
	if card.Release == nil || card.Release.Source != "merge" || gr == nil {
		t.Fatalf("release %+v grade %+v", card.Release, gr)
	}
	if gr.Inputs.Durability.DaysLive != 50 || gr.Inputs.Durability.LiveSince == nil || !gr.Inputs.Durability.LiveSince.Equal(merged) ||
		gr.Inputs.Delivery.DefectBounces != nil || gr.Inputs.Delivery.FailedRuns != 1 || gr.Inputs.Review.ReviewRounds != 0 {
		t.Fatalf("inputs = %+v", gr.Inputs)
	}
	if !reflect.DeepEqual(gr.Qualifiers, []string{"OB", "RT"}) || gr.Subgrades.Delivery != 7.5 || gr.Subgrades.Durability != 8 {
		t.Fatalf("grade = %+v", gr)
	}
}
