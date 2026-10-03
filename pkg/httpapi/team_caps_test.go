package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestClaim_OverTheTeamCapIsEmptyHanded(t *testing.T) {
	reset(t)
	shiftFixture(t, "900", 10, []store.Role{{Name: "analyst", Cap: 1}})
	shiftFixture(t, "901", 10, []store.Role{{Name: "analyst", Cap: 1}})
	h := (&Server{
		Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		TeamCaps:       config.RunCaps{"bronze": 1},
	}).Handler()

	if code, _ := postClaim(t, h, `{"team":"bronze","role":"analyst"}`); code != http.StatusOK {
		t.Fatalf("first claim returned %d, want 200", code)
	}
	if code, _ := postClaim(t, h, `{"team":"bronze","role":"analyst"}`); code != http.StatusNoContent {
		t.Fatalf("claim over the cap returned %d, want 204 so the worker exits 0", code)
	}
	if n, _ := testStore.PendingRuns(context.Background(), "bronze", "analyst"); n != 1 {
		t.Errorf("pending = %d, want 1: the refused claim must leave its Run queued", n)
	}
}

func TestClaim_RoleLessClaimHonoursTheTeamCap(t *testing.T) {
	reset(t)
	ctx := context.Background()
	for _, id := range []string{"910", "911"} {
		if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
			Provider: "vikunja", ExternalID: id, Team: "bronze", Title: "t",
		}); err != nil {
			t.Fatal(err)
		}
	}
	h := (&Server{
		Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		TeamCaps:       config.RunCaps{"bronze": 1},
	}).Handler()

	if code, _ := postClaim(t, h, `{"team":"bronze"}`); code != http.StatusOK {
		t.Fatalf("first claim returned %d, want 200", code)
	}
	if code, _ := postClaim(t, h, `{"team":"bronze"}`); code != http.StatusNoContent {
		t.Fatalf("claim over the cap returned %d, want 204", code)
	}
}
