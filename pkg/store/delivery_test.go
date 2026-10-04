package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// VIK-1732 (ADR-0059): the admitted delivery record is what the Shift's
// readers get back, a replay of the same report is the original success, and
// the record a replay finds is the one first stored.
func TestAdmittedDeliveryIsReadBackAndReplayed(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "1732", Team: "silver", Title: "t",
		Target: &work.Target{Forge: "home", Owner: "o", Repo: "r"},
	})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-1732", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.ReserveLLMAccount(ctx, LLMAccount{RunToken: run.RunToken, Alias: "fixture", Authorized: 1,
		Models: []string{"model"}, TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	d := &harness.Delivery{Forge: "home", Repository: "o/r", Branch: "agent/vik-1732", Observed: harness.DeliveryOpened,
		Number: 5, URL: "https://forgejo/o/r/pulls/5", Head: strings.Repeat("c", 40)}
	rep := Report(work.OutcomePROpened, "opened", "", []string{d.URL}, nil, nil).WithDelivery(d)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, rep); err != nil {
		t.Fatalf("replay of the same report: %v", err)
	}

	reports, err := testStore.RoundReports(ctx, shiftID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Delivery == nil || !reflect.DeepEqual(*reports[0].Delivery, *d) {
		t.Fatalf("RoundReports delivery = %+v, want %+v", reports, d)
	}

	forceItemState(t, id, "awaiting_review", 1)
	items, err := testStore.AwaitingReview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Delivery == nil || items[0].Delivery.Number != 5 {
		t.Fatalf("AwaitingReview = %+v, want the delivery record of pull request 5", items)
	}
}

func TestRunsWithoutObservedDeliveryAreCounted(t *testing.T) {
	ctx := context.Background()
	itemID, shiftID := openShift(t, 5)
	for _, source := range []string{"legacy", "legacy", "mismatch", "worker"} {
		mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, state, finished_at, delivery_source)
			VALUES ($1, $2, 'silver', gen_random_uuid()::text, 'finished', now(), $3)`, itemID, shiftID, source)
	}
	mustExec(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, state, finished_at, delivery_source)
		VALUES ($1, $2, 'silver', gen_random_uuid()::text, 'finished', now() - interval '2 days', 'legacy')`, itemID, shiftID)
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.RunsWithoutObservedDeliveryLastDay; got["legacy"] != 2 || got["mismatch"] != 1 || len(got) != 2 {
		t.Fatalf("runs without observed delivery = %v, want legacy 2 and mismatch 1", got)
	}
}

func TestAdmitDeliveryForOlderWorkersAndReaders(t *testing.T) {
	writer := runBinding{writes: true, target: work.Target{Owner: "o", Repo: "r"}, branch: "agent/vik-1"}
	own, foreign := "https://forgejo/o/r/pulls/2", "https://forgejo/other/repo/pulls/123"

	rep, a := admitDelivery(Report(work.OutcomePROpened, "s", "", []string{foreign, own}, nil, nil), writer)
	if rep.Outcome != work.OutcomePROpened || !reflect.DeepEqual(rep.Links, []string{own}) || a.source != DeliverySourceLegacy {
		t.Errorf("older writer with its own link: outcome=%s links=%v source=%s", rep.Outcome, rep.Links, a.source)
	}

	reader := writer
	reader.writes = false
	rep, _ = admitDelivery(Report(work.OutcomeNoChangeNeeded, "s", "", []string{foreign}, nil, nil).
		WithVerdict("request_changes").WithFindings("f").
		WithDelivery(&harness.Delivery{Repository: "o/r", Branch: "agent/vik-1", Observed: harness.DeliveryUnknown}), reader)
	if rep.Outcome != work.OutcomeNoChangeNeeded || rep.Verdict != "request_changes" || rep.Findings != "f" || len(rep.Links) != 0 {
		t.Errorf("reader: outcome=%s verdict=%s findings=%q links=%v", rep.Outcome, rep.Verdict, rep.Findings, rep.Links)
	}

	untargeted := runBinding{writes: true, branch: "agent/vik-1"}
	rep, _ = admitDelivery(Report(work.OutcomePROpened, "s", "", []string{foreign}, nil, nil), untargeted)
	if rep.Outcome != work.OutcomePROpened {
		t.Errorf("a Work Item with no Target has nothing to bind to, got %s", rep.Outcome)
	}

	late := Report(work.OutcomePROpened, "s", "", []string{own}, nil, nil).WithProblemAndSolution("p", "q").
		WithDelivery(&harness.Delivery{Repository: "o/r", Branch: "agent/vik-1", Observed: harness.DeliveryOpened,
			Number: 2, URL: own, Head: strings.Repeat("d", 40)})
	timeout := string(work.FailureTimeout)
	late.FailureReason = &timeout
	rep, _ = admitDelivery(late, writer)
	if rep.Outcome != work.OutcomePROpened || rep.FailureReason == nil || rep.Problem != "p" || rep.Solution != "q" {
		t.Errorf("a real pull request with a late failure lost evidence: %+v", rep)
	}
}
