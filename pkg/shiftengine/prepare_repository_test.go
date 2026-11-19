package shiftengine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var prepareTarget = work.Target{Forge: "webgrip", Owner: "o", Repo: "r", BaseBranch: "development"}

func admitPrepare(t *testing.T, e *Engine, requestID string) int64 {
	t.Helper()
	ctx := context.Background()
	a, err := testStore.AdmitOperatorWorkItem(ctx, "workbench", "alice", store.AdmitOperatorWorkItem{
		RequestID: requestID, Team: "bronze", Target: "r", Kind: work.KindPrepareRepository,
		Title: "Add AGENTS.md to o/r", Description: "Ploeg's prepare_repository brief",
	}, prepareTarget, 2)
	if err != nil {
		t.Fatal(err)
	}
	item, err := testStore.WorkItem(ctx, a.WorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !item.PreparesRepository() {
		t.Fatalf("the admitted item has kind %q", item.Kind)
	}
	if err := e.EnsureShift(ctx, a.WorkItemID, item); err != nil {
		t.Fatal(err)
	}
	return a.WorkItemID
}

func TestPrepareRepository_ARequestForChangesOpensNoFixRoundAndTheShiftAwaitsReview(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e := loopEngine(t, 10, 2)
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.DefaultForge = "webgrip"
	id := admitPrepare(t, e, "prepare-loop")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)

	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 0 {
		t.Fatalf("a request for changes re-opened the prepare writer (pending=%d)", n)
	}
	if got := itemState(t, id); got != string(work.StateAwaitingReview) {
		t.Errorf("item state = %q, want awaiting_review", got)
	}
	if got := closeReason(t, id); got != reasonPlanExhausted {
		t.Errorf("close reason = %q, want %q", got, reasonPlanExhausted)
	}
	posted := false
	for _, c := range forge.comments {
		posted = posted || strings.Contains(c.Body, "reviewer says so")
	}
	if !posted {
		t.Errorf("the reviewer's findings were not posted on the pull request: %+v", forge.comments)
	}
}

func TestPrepareRepository_TheSameVerdictOnAChangeStillReopensTheWriter(t *testing.T) {
	ctx := context.Background()
	e := loopEngine(t, 10, 2)
	id := startLoopShift(t, e, "prepare-control")

	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, harness.VerdictRequestChanges)

	if n, _ := testStore.PendingRuns(ctx, "bronze", "builder"); n != 1 {
		t.Fatalf("request_changes on a change Work Item did not re-open the writer (pending=%d)", n)
	}
}

func TestPrepareRepository_CancelClosesThePullRequestAndKeepsTheBranch(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &closingForge{fakeForge: &fakeForge{}, open: map[string][]int{}}
	e := loopEngine(t, 10, 2)
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.DefaultForge = "webgrip"
	id := admitPrepare(t, e, "prepare-cancel")
	runRound(t, e, id, "builder", work.OutcomePROpened, "")

	got := e.ClosePreparePullRequest(ctx, id, "alice")

	if !got.Closed || got.PullRequest != 1 || got.Error != "" || got.Repository != "o/r" {
		t.Fatalf("closure = %+v, want pull request 1 of o/r closed", got)
	}
	if len(forge.closed) != 1 || forge.closed[0] != 1 {
		t.Errorf("closed = %v, want [1]", forge.closed)
	}
	if n := len(forge.comments); n == 0 || !strings.Contains(forge.comments[n-1].Body, "alice cancelled the prepare") ||
		!strings.Contains(forge.comments[n-1].Body, "The branch is kept") {
		t.Errorf("comments = %+v, want one naming who cancelled and that the branch stays", forge.comments)
	}
}

func TestPrepareRepository_AForgeThatCannotCloseLeavesThePullRequestOpenAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &closingForge{fakeForge: &fakeForge{}, open: map[string][]int{}, closeErr: errors.New("HTTP 403")}
	e := loopEngine(t, 10, 2)
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.DefaultForge = "webgrip"
	id := admitPrepare(t, e, "prepare-cancel-refused")
	runRound(t, e, id, "builder", work.OutcomePROpened, "")

	got := e.ClosePreparePullRequest(ctx, id, "alice")

	if got.Closed || got.PullRequest != 1 || !strings.Contains(got.Error, "HTTP 403") {
		t.Fatalf("closure = %+v, want pull request 1 left open with the forge's answer", got)
	}
	if !e.PullRequestStillOpen(ctx, prepareTarget, 1) {
		t.Error("the pull request the cancel left open is not reported open")
	}

	plain := loopEngine(t, 10, 2)
	plain.Forges = map[string]provider.ForgeProvider{"webgrip": &fakeForge{}}
	plain.DefaultForge = "webgrip"
	if got := plain.ClosePreparePullRequest(ctx, id, "alice"); got.Closed || !strings.Contains(got.Error, "cannot close pull requests") {
		t.Errorf("closure without a PullRequestCloser = %+v, want it named", got)
	}
}

type recheckingGate struct {
	mu      sync.Mutex
	asked   []work.Target
	before  target.Readiness
	after   target.Readiness
	unknown bool
}

func (g *recheckingGate) Recheck(_ context.Context, t work.Target) (before, after target.Readiness, seen, registered bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, t)
	return g.before, g.after, true, true
}

func (g *recheckingGate) KeyOf(work.Target) (string, bool) { return "r", true }

func TestPrepareRepository_AMergeClosesItDoneAndReinspectsTheTarget(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e := loopEngine(t, 10, 2)
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.DefaultForge = "webgrip"
	id := admitPrepare(t, e, "prepare-merge")
	runRound(t, e, id, "builder", work.OutcomePROpened, "")
	runRound(t, e, id, "reviewer", work.OutcomeNoChangeNeeded, "")
	if got := itemState(t, id); got != string(work.StateAwaitingReview) {
		t.Fatalf("item state = %q, want awaiting_review before the merge", got)
	}
	gate := &recheckingGate{
		before: target.Readiness{Reasons: []string{target.ReasonInstructionFileMissing}},
		after:  target.Readiness{Ready: true},
	}
	w := newReviewWatch(forge, &fakeTracker{})
	w.Readiness = gate

	if err := w.HandleForgeEvent(ctx, "webgrip", provider.ForgeEvent{Kind: provider.ForgePRMerged, Repo: "o/r", PR: 1}); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, id); got != string(work.StateDone) {
		t.Errorf("item state = %q, want done", got)
	}
	if len(gate.asked) != 1 || gate.asked[0] != prepareTarget {
		t.Errorf("rechecked %v, want the prepared target once", gate.asked)
	}
	if got := auditActions(t, id, "target."); len(got) != 1 || got[0] != store.ActionReadinessChanged {
		t.Errorf("audit = %v, want one target.readiness_changed", got)
	}
}
