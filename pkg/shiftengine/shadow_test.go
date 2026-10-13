package shiftengine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0094: a shadow reads beside the reviewer and is recorded, never obeyed.

func shadowPlan(t *testing.T) plan.Plans {
	t.Helper()
	plans, err := plan.Parse(`{"bronze": {"pool": 10, "maxFixRounds": 2, "rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 2}]},
		{"roles": [{"name": "reviewer", "cap": 0.4}, {"name": "shadow-reviewer", "shadow": true, "cap": 0.4}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	return plans
}

func startShadowShift(t *testing.T, e *Engine, externalID string) int64 {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		ExternalScope: "11", RouteRule: "11/bronze",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	return id
}

// reportRole claims one pending Run of role and reports it without evaluating.
func reportRole(t *testing.T, role string, rep func(store.ClaimedRun) error) {
	t.Helper()
	run, err := testStore.ClaimRole(context.Background(), "bronze", role, time.Minute, 1)
	if err != nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	if err := rep(*run); err != nil {
		t.Fatalf("report %s: %v", role, err)
	}
}

func verdictReport(outcome work.Outcome, verdict, findings string) func(store.ClaimedRun) error {
	return func(run store.ClaimedRun) error {
		rep := store.Report(outcome, "read it", "", nil, nil, nil).WithVerdict(verdict).WithFindings(findings)
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken, rep)
		return err
	}
}

func buildAndOpenPR(t *testing.T, e *Engine, id int64) {
	t.Helper()
	reportRole(t, "builder", func(run store.ClaimedRun) error {
		_, err := testStore.ReportOutcome(context.Background(), run.RunToken,
			store.Report(work.OutcomePROpened, "opened", "", []string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/7"}, nil, nil))
		return err
	})
	evaluate(t, e, id)
}

// The acceptance case: a shadow asking for changes next to an approving
// reviewer neither opens a fix round nor stops the approval.
func TestShadow_RequestChangesBesideAnApprovalDoesNotAct(t *testing.T) {
	ctx := context.Background()
	forge := &fakeForge{}
	e := newEngine(shadowPlan(t))
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	id := startShadowShift(t, e, "2033")

	buildAndOpenPR(t, e, id)
	reportRole(t, "reviewer", verdictReport(work.OutcomeNoChangeNeeded, harness.VerdictApprove, "- reviewer: looks right"))
	reportRole(t, "shadow-reviewer", verdictReport(work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges, "- shadow: rename the flag"))
	evaluate(t, e, id)

	if got := closeReason(t, id); got != reasonApproved {
		t.Fatalf("close reason = %q, want %q", got, reasonApproved)
	}
	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Fatalf("a shadow's request_changes re-opened the writer (pending=%d)", n)
	}
	if got := itemState(t, id); got != string(work.StateAwaitingReview) {
		t.Fatalf("item state = %q, want awaiting_review", got)
	}
	published := false
	for _, c := range forge.comments {
		if strings.Contains(c.Body, "rename the flag") {
			t.Fatalf("a shadow's findings reached the pull request:\n%s", c.Body)
		}
		published = published || strings.Contains(c.Body, "reviewer: looks right")
	}
	if !published {
		t.Fatalf("the reviewer's findings were not published: %+v", forge.comments)
	}

	var shadow bool
	var verdict, findings string
	if err := testPool.QueryRow(ctx, `SELECT shadow, verdict, findings FROM agent_runs WHERE role = 'shadow-reviewer'`).
		Scan(&shadow, &verdict, &findings); err != nil {
		t.Fatal(err)
	}
	if !shadow || verdict != harness.VerdictRequestChanges || !strings.Contains(findings, "rename the flag") {
		t.Fatalf("shadow run recorded shadow=%v verdict=%q findings=%q", shadow, verdict, findings)
	}
}

// The other direction: the reviewer's request still opens the fix round when
// the shadow approves, the writer is briefed only by the reviewer, and the
// review round that follows runs the shadow again.
func TestShadow_ApprovalDoesNotStopTheReviewersRequest(t *testing.T) {
	ctx := context.Background()
	e := newEngine(shadowPlan(t))
	id := startShadowShift(t, e, "2034")

	buildAndOpenPR(t, e, id)
	reportRole(t, "reviewer", verdictReport(work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges, "- reviewer: missing test"))
	reportRole(t, "shadow-reviewer", verdictReport(work.OutcomeNoChangeNeeded, harness.VerdictApprove, "- shadow: fine"))
	evaluate(t, e, id)

	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("the reviewer's request_changes did not re-open the writer (pending=%d)", n)
	}
	reportRole(t, "builder", func(run store.ClaimedRun) error {
		_, err := testStore.ReportOutcome(ctx, run.RunToken,
			store.Report(work.OutcomePRUpdated, "fixed", "", []string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/7"}, nil, nil))
		return err
	})
	evaluate(t, e, id)
	for _, role := range []string{"reviewer", "shadow-reviewer"} {
		if n, _ := testStore.PendingRuns(ctx, "bronze", role); n != 1 {
			t.Fatalf("the re-opened review round has %d pending %s Runs, want 1", n, role)
		}
	}
}

// A shadow that reports stuck or fails is recorded: it parks nothing, is not
// retried and does not make the review read as missing.
func TestShadow_StuckOrFailedShadowChangesNothing(t *testing.T) {
	for name, rep := range map[string]func(store.ClaimedRun) error{
		"stuck": func(run store.ClaimedRun) error {
			_, err := testStore.ReportOutcome(context.Background(), run.RunToken,
				store.Report(work.OutcomeStuck, "cannot tell", "the diff is unreadable", nil, nil, nil))
			return err
		},
		"failed": func(run store.ClaimedRun) error {
			reason := string(work.FailureAgentError)
			_, err := testStore.ReportOutcome(context.Background(), run.RunToken,
				store.Report(work.OutcomeFailed, "crashed", "", nil, nil, &reason))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := newEngine(shadowPlan(t))
			id := startShadowShift(t, e, "2035-"+name)

			buildAndOpenPR(t, e, id)
			reportRole(t, "reviewer", verdictReport(work.OutcomeNoChangeNeeded, harness.VerdictApprove, ""))
			reportRole(t, "shadow-reviewer", rep)
			evaluate(t, e, id)

			if got := closeReason(t, id); got != reasonApproved {
				t.Fatalf("close reason = %q, want %q", got, reasonApproved)
			}
			if n, _ := testStore.PendingRuns(ctx, "bronze", "shadow-reviewer"); n != 0 {
				t.Fatalf("the shadow was retried (pending=%d)", n)
			}
		})
	}
}

// A shadow's cost counts in the Shift like any Run's (ADR-0012).
func TestShadow_CostCountsInTheShift(t *testing.T) {
	ctx := context.Background()
	e := newEngine(shadowPlan(t))
	id := startShadowShift(t, e, "2036")

	buildAndOpenPR(t, e, id)
	reportRole(t, "reviewer", verdictReport(work.OutcomeNoChangeNeeded, harness.VerdictApprove, ""))
	reportRole(t, "shadow-reviewer", func(run store.ClaimedRun) error {
		usage, _ := json.Marshal(map[string]any{"costUsd": 0.07})
		_, err := testStore.ReportOutcome(ctx, run.RunToken,
			store.Report(work.OutcomeNoChangeNeeded, "read it", "", nil, usage, nil).WithVerdict(harness.VerdictApprove))
		return err
	})
	var spent float64
	if err := testPool.QueryRow(ctx, `SELECT spent::float8 FROM shifts WHERE work_item_id = $1`, id).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent < 0.07 {
		t.Fatalf("shift spent = %v, want the shadow's 0.07 counted", spent)
	}
}

// The usage report shows a shadow's cost and keeps its verdict off the
// pull request.
func TestShadow_UsageReportRowHidesTheVerdict(t *testing.T) {
	row := runRow(store.RunUsage{Role: "shadow-reviewer", Round: 2, Shadow: true, Outcome: "no_change_needed",
		Verdict: harness.VerdictRequestChanges, HasUsage: true, CostUSD: 0.07})
	if !strings.Contains(row, "shadow-reviewer (shadow)") || strings.Contains(row, "request changes") || !strings.Contains(row, "0,07") {
		t.Fatalf("row = %q", row)
	}
}
