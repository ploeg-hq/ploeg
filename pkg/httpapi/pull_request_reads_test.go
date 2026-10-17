package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type recordingReader struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingReader) Consider(_ context.Context, forge, repo string, pr int, author string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, forge+" "+repo+" #"+strconv.Itoa(pr)+" by "+author)
}

func TestForgeWebhook_OpenedAndPushedPullRequestsReachTheReader(t *testing.T) {
	reset(t)
	if _, err := testPool.Exec(context.Background(), "DELETE FROM forge_deliveries"); err != nil {
		t.Fatal(err)
	}
	explainer := &recordingReader{}
	log := slog.New(slog.DiscardHandler)
	h := (&Server{Store: testStore, Log: log,
		Forges:           map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{Secret: "shh", Log: log}},
		PullRequestReads: explainer,
	}).Handler()
	opened := pullRequestEvent("opened", "h1")
	opened["sender"] = map[string]any{"login": "renovate"}
	if code := forgePostEvent(t, h, "shh", "pull_request", "dep-1", opened); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	pushed := pullRequestEvent("synchronized", "h2")
	pushed["sender"] = map[string]any{"login": "renovate"}
	if code := forgePostEvent(t, h, "shh", "pull_request_sync", "dep-2", pushed); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	if code := forgePostEvent(t, h, "shh", "pull_request_review_approved", "dep-3", map[string]any{
		"review": map[string]any{"type": "pull_request_review_approved"}, "repository": map[string]any{"full_name": "webgrip/ploeg"},
		"pull_request": map[string]any{"number": 18, "head": map[string]any{"ref": "renovate/x"}}, "sender": map[string]any{"login": "ryan"},
	}); code != http.StatusAccepted {
		t.Fatalf("review webhook returned %d", code)
	}
	explainer.mu.Lock()
	defer explainer.mu.Unlock()
	want := []string{"forgejo webgrip/ploeg #18 by renovate", "forgejo webgrip/ploeg #18 by renovate"}
	if len(explainer.calls) != 2 || explainer.calls[0] != want[0] || explainer.calls[1] != want[1] {
		t.Errorf("explainer calls = %q, want %q", explainer.calls, want)
	}
}

// A failed check or a request for changes on a pull request Ploeg only
// explains must never make a Team push to that pull request (ADR-0109).
func TestFailedCheckOnAReadPullRequestCreatesNoRepair(t *testing.T) {
	ctx := context.Background()
	h := followUpServer(t, enabled(0))
	id, _, result, err := testStore.EnsurePullRequestReading(ctx, store.PullRequestReading{
		Forge: "forgejo", Target: work.Target{Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}, Team: "bronze",
		Number: 18, Title: "Read", Description: "d", URL: "u", HeadBranch: "renovate/x", Author: "renovate", PullRequestTitle: "t",
	})
	if err != nil || result != store.PullRequestReadingCreated {
		t.Fatalf("reading: %q %v", result, err)
	}
	e := &shiftengine.Engine{Store: testStore, Log: slog.New(slog.DiscardHandler), Plans: mustPlans(t, `{"bronze": {"rounds": [
		{"roles": [{"name": "builder", "writes": true, "cap": 2}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": 0.4}]}]}}`)}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "explained", "", nil, nil, nil).WithVerdict("approve")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := workItemState(t, id); got != "awaiting_review" {
		t.Fatalf("setup: reading is %q, want awaiting_review", got)
	}

	if code := forgeEvent(t, h, "chk-1", checkFailed("renovate/x")); code != http.StatusAccepted {
		t.Fatalf("status webhook returned %d", code)
	}
	if code := forgeEvent(t, h, "rev-1", changesRequested("renovate/x", "ryan", "please pin it")); code != http.StatusAccepted {
		t.Fatalf("review webhook returned %d", code)
	}
	if rows := followUps(t, id); len(rows) != 0 {
		t.Errorf("follow-ups on the read pull request = %+v, want none", rows)
	}
	if live, _ := testStore.LiveShiftForItem(ctx, id); live != nil {
		t.Error("a request for changes opened a shift on someone else's pull request")
	}
	if got := workItemState(t, id); got != "awaiting_review" {
		t.Errorf("reading is %q after the events, want awaiting_review", got)
	}
}

func mustPlans(t *testing.T, spec string) plan.Plans {
	t.Helper()
	plans, err := plan.Parse(spec)
	if err != nil {
		t.Fatal(err)
	}
	return plans
}
