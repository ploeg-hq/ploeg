package shiftengine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const partialPullRequest = "https://forge.example/webgrip/ploeg/pulls/77"

func stuckWriterComment(t *testing.T, externalID, stuckReason string, links ...string) string {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
	tracker := &fakeTracker{}
	e := newEngine(bronzePlan(10))
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	itemID, _ := targetedBronzeShift(t, e, externalID)

	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 0)
	if err != nil || run == nil {
		t.Fatalf("claim builder: %v", err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomeStuck, "delivered the export, not the import", stuckReason, links, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, itemID); got != string(work.StateNeedsHuman) {
		t.Fatalf("item state = %q, want needs_human", got)
	}
	if len(tracker.comments) != 1 {
		t.Fatalf("tracker comments = %d, want 1: %q", len(tracker.comments), tracker.comments)
	}
	return tracker.comments[0]
}

func TestStuckWriterWithAPullRequest_DoesNotAskToMerge(t *testing.T) {
	const unmet = "The import criterion needs an API the tracker does not offer."
	got := stuckWriterComment(t, "3601", unmet, partialPullRequest)

	for _, want := range []string{"stuck", unmet, partialPullRequest, "partial work"} {
		if !strings.Contains(got, want) {
			t.Errorf("comment missing %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"finished this item", "opened a pull request.", "Please review and merge"} {
		if strings.Contains(got, absent) {
			t.Errorf("a stuck item's comment says %q:\n%s", absent, got)
		}
	}
}

func TestStuckWriterWithoutAPullRequest_SaysItIsStuck(t *testing.T) {
	const unmet = "The repository has no import module to extend."
	got := stuckWriterComment(t, "3602", unmet)

	for _, want := range []string{"stuck", unmet, "No pull request was opened."} {
		if !strings.Contains(got, want) {
			t.Errorf("comment missing %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"finished this item", "merge"} {
		if strings.Contains(got, absent) {
			t.Errorf("a stuck item's comment says %q:\n%s", absent, got)
		}
	}
}

func TestTrackerMessage_ClipsALongStuckReasonToOneLine(t *testing.T) {
	stuck := &store.RunReport{Role: "builder", Round: 2, Outcome: string(work.OutcomeStuck),
		StuckReason: "first line\n" + strings.Repeat("x", 2000)}
	got := trackerMessage(work.StateNeedsHuman, "run stuck", "", 1, 2, nil, false, stuck)

	line := ""
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "**Could not be done:**") {
			line = l
		}
	}
	if !strings.Contains(line, "first line xxx") {
		t.Fatalf("the stuck reason is not on one line:\n%s", got)
	}
	if n := len([]rune(strings.TrimPrefix(line, "**Could not be done:** "))); n != maxStuckReasonRunes+1 {
		t.Errorf("stuck reason is %d runes, want %d and an ellipsis", n, maxStuckReasonRunes+1)
	}
}
