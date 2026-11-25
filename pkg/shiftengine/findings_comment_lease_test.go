package shiftengine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func reviewedRound(t *testing.T, e *Engine, externalID string) (store.ShiftInfo, []store.RunReport, *fakeForge) {
	t.Helper()
	ctx := context.Background()
	id, forge := usageReportEnv(t, e, externalID)
	e.UsageReport = false
	writer, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, writer.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/41"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	reader, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, reader.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
			WithFindings("- the retry loop is unbounded")); err != nil {
		t.Fatal(err)
	}
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	reports, err := testStore.RoundReports(ctx, si.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *si, reports, forge
}

func TestPublishRound_ConcurrentEvaluatorsPostOneComment(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	si, reports, forge := reviewedRound(t, e, "991")

	var mu sync.Mutex
	listed := 0
	bothListed := make(chan struct{})
	forge.afterList = func() {
		mu.Lock()
		listed++
		if listed == 2 {
			close(bothListed)
		}
		mu.Unlock()
		select {
		case <-bothListed:
		case <-time.After(time.Second):
		}
	}

	var evaluators sync.WaitGroup
	for range 2 {
		evaluators.Add(1)
		go func() {
			defer evaluators.Done()
			e.publishRound(ctx, si, reports, si.Round)
		}()
	}
	evaluators.Wait()

	if got := len(forge.comments); got != 1 {
		t.Fatalf("findings comments = %d, want 1: %+v", got, forge.comments)
	}
}

func TestPublishRound_AnEvaluatorThatComesLaterPostsNothing(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	si, reports, forge := reviewedRound(t, e, "992")

	e.publishRound(ctx, si, reports, si.Round)
	e.publishRound(ctx, si, reports, si.Round)

	if got := len(forge.comments); got != 1 {
		t.Fatalf("findings comments = %d, want 1: %+v", got, forge.comments)
	}
}

func TestPublishRound_FindingsQuotingAnotherRunsMarkerAreStillPosted(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	si, reports, forge := reviewedRound(t, e, "993")
	review := reports[len(reports)-1]
	if err := forge.Comment(ctx, "webgrip/ploeg", 41, "a person quotes "+findingsMarker(review.RunID)); err != nil {
		t.Fatal(err)
	}

	e.publishRound(ctx, si, reports, si.Round)

	if got := len(forge.commentsMatching(findingsMarker(review.RunID))); got != 1 {
		t.Fatalf("findings comments = %d, want 1: %+v", got, forge.comments)
	}
}

func TestPublishRound_TwoRunsOfOneRoleInOneRoundEachPost(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	si, reports, forge := reviewedRound(t, e, "994")
	again := reports[len(reports)-1]
	again.RunID++
	reports = append(reports, again)

	e.publishRound(ctx, si, reports, si.Round)

	if got := len(forge.comments); got != 2 {
		t.Fatalf("findings comments = %d, want 2: %+v", got, forge.comments)
	}
}

func TestPublishRound_AFailedListStillPostsTheReview(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	si, reports, forge := reviewedRound(t, e, "995")
	forge.listErr = errors.New("list unavailable")

	e.publishRound(ctx, si, reports, si.Round)

	if got := len(forge.comments); got != 1 {
		t.Fatalf("findings comments = %d, want 1: %+v", got, forge.comments)
	}
}
