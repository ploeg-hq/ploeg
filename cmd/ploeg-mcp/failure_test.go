package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestGetWorkSaysWhetherTheTicketOrTheSystemStoppedIt(t *testing.T) {
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "2051", Team: "copper", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, id, "copper", "ploeg/2051", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []store.Role{{Name: "writer", Writes: true, Cap: 2}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "copper", "writer", time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	reason := string(work.FailureHarnessConfig)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeFailed, "acp agent rejected its configuration: Error: bad MCP server", "", nil, nil, &reason)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.CloseShiftAndSettle(ctx, shift, work.CloseHarnessConfig, work.StateNeedsHuman, "shift stopped: a system configuration problem, not the ticket"); err != nil {
		t.Fatal(err)
	}
	handler, token := ploegdFor(t, "copper")
	cs := connect(t, handler, token)

	got, _ := call[getOut](t, cs, "ploeg_get_work", map[string]any{"ref": fmt.Sprint(id)})
	if got.LatestShift == nil || got.LatestShift.CloseFailure == nil || got.LatestShift.CloseFailure.Cause != work.CauseSystem {
		t.Fatalf("latest shift = %+v, want closeFailure with cause system", got.LatestShift)
	}
	if len(got.Runs) != 1 || got.Runs[0].Failure == nil || got.Runs[0].Failure.Reason != reason {
		t.Fatalf("runs = %+v, want the Run's failure explained", got.Runs)
	}
	if !strings.Contains(got.Summary, "Cause: system.") {
		t.Errorf("summary %q does not say the system stopped it", got.Summary)
	}
	runs, _ := call[runsOut](t, cs, "ploeg_recent_runs", map[string]any{"team": "copper"})
	for _, r := range runs.Runs {
		if r.ID == got.Runs[0].ID && (r.Failure == nil || r.Failure.Cause != work.CauseSystem) {
			t.Fatalf("recent run = %+v, want its failure explained", r)
		}
	}
}
