package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func targetedShift(t *testing.T, externalID string, roles []store.Role) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		Target: &work.Target{Owner: "Acme", Repo: "Shop", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-"+externalID, 0)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, roles); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
}

func fastCheckServer() http.Handler {
	return (&Server{
		Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		RoleCaps: plan.Plans{}, WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		FastChecks: map[string][]string{FastCheckRoute("", "acme/shop"): {"go vet ./...", "go test -short ./..."}},
	}).Handler()
}

func TestClaim_AWritingRunGetsItsRepositorysFastCheck(t *testing.T) {
	reset(t)
	targetedShift(t, "2397", []store.Role{{Name: "builder", Writes: true}})
	code, claim := postClaim(t, fastCheckServer(), `{"team":"bronze","role":"builder"}`)
	if code != http.StatusOK || !slices.Equal(claim.FastCheck, []string{"go vet ./...", "go test -short ./..."}) {
		t.Fatalf("claim: %d fastCheck=%v, want the repository's two commands", code, claim.FastCheck)
	}
}

func TestClaim_AReadingRunAndAnUndeclaredRepositoryGetNoFastCheck(t *testing.T) {
	reset(t)
	targetedShift(t, "2398", []store.Role{{Name: "reviewer"}})
	if code, claim := postClaim(t, fastCheckServer(), `{"team":"bronze","role":"reviewer"}`); code != http.StatusOK || claim.FastCheck != nil {
		t.Fatalf("reader claim: %d fastCheck=%v, want none: a reader never pushes", code, claim.FastCheck)
	}

	reset(t)
	shiftFixture(t, "2399", 0, []store.Role{{Name: "builder", Writes: true}})
	if code, claim := postClaim(t, fastCheckServer(), `{"team":"bronze","role":"builder"}`); code != http.StatusOK || claim.FastCheck != nil {
		t.Fatalf("claim without a declared fast check: %d fastCheck=%v, want none", code, claim.FastCheck)
	}
}
