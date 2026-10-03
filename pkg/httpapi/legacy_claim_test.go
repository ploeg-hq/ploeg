package httpapi

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type runCensus struct {
	runs, unpooled, leases int
}

func censusForItem(t *testing.T, externalID string) runCensus {
	t.Helper()
	var c runCensus
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM agent_runs r WHERE r.work_item_id = w.id AND r.state <> 'pending'),
			(SELECT count(*) FROM agent_runs r WHERE r.work_item_id = w.id AND r.shift_id IS NULL),
			(SELECT count(*) FROM leases l WHERE l.work_item_id = w.id)
		FROM work_items w WHERE w.external_id = $1`, externalID).Scan(&c.runs, &c.unpooled, &c.leases); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClaim_RolelessWorkerLosingTheShiftRunLeasesNothing(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftFixture(t, "810", 10, []store.Role{{Writes: true}})
	h := apiServer(t, nil)

	sibling, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sibling.Exec(ctx,
		`SELECT id FROM agent_runs WHERE team = 'bronze' AND role = '' AND state = 'pending' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	code, resp := postClaim(t, h, `{"team":"bronze"}`)
	if err := sibling.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusNoContent {
		t.Fatalf("losing worker got %d with %+v, want 204: the Shift's Run was held by its sibling", code, resp)
	}
	if c := censusForItem(t, "810"); c != (runCensus{}) {
		t.Fatalf("losing worker left %+v, want no claimed run, no unpooled run and no lease", c)
	}

	code, resp = postClaim(t, h, `{"team":"bronze"}`)
	if code != http.StatusOK || resp.Shift == 0 {
		t.Fatalf("sibling claim got %d with %+v, want the Shift's Run", code, resp)
	}
	if c := censusForItem(t, "810"); c != (runCensus{runs: 1, leases: 1}) {
		t.Errorf("after the sibling's claim: %+v, want one pooled run holding the one lease", c)
	}
}

func TestClaim_ConcurrentRolelessWorkersClaimOneShiftRun(t *testing.T) {
	reset(t)
	shiftFixture(t, "811", 10, []store.Role{{Writes: true}})
	h := apiServer(t, nil)

	const workers = 8
	start := make(chan struct{})
	codes := make(chan int, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, _ := postClaim(t, h, `{"team":"bronze"}`)
			codes <- code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)

	won := 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			won++
		case http.StatusNoContent:
		default:
			t.Errorf("a worker got %d, want 200 or 204", code)
		}
	}
	if won != 1 {
		t.Errorf("%d workers were handed work, want exactly one", won)
	}
	if c := censusForItem(t, "811"); c != (runCensus{runs: 1, leases: 1}) {
		t.Errorf("after %d racing workers: %+v, want one pooled run holding the one lease", workers, c)
	}
}

func TestClaim_ExhaustedShiftIsNotFollowedByALegacyClaim(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftFixture(t, "812", 0.01, []store.Role{{Writes: true}})
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "813", Team: "bronze", Title: "no shift",
	}); err != nil {
		t.Fatal(err)
	}
	h := apiServer(t, nil)

	if code, resp := postClaim(t, h, `{"team":"bronze"}`); code != http.StatusNoContent {
		t.Fatalf("claim behind an exhausted Shift got %d with %+v, want 204", code, resp)
	}
	for _, id := range []string{"812", "813"} {
		if c := censusForItem(t, id); c != (runCensus{}) {
			t.Errorf("work item %s: %+v, want nothing claimed", id, c)
		}
	}
}
