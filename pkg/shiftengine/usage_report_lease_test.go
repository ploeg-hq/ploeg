package shiftengine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestPublishUsage_ConcurrentPublishersCreateOneReport(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "990")
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/41"}, nil, nil)); err != nil {
		t.Fatal(err)
	}

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

	var publishers sync.WaitGroup
	for range 2 {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			e.publishUsageReport(ctx, *si)
		}()
	}
	publishers.Wait()

	if got := len(forge.commentsMatching(usageReportMarker)); got != 1 {
		t.Fatalf("usage report comments = %d, want 1", got)
	}
}
