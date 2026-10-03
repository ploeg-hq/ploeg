package httpapi

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func testServer(t *testing.T, spec string) *Server {
	t.Helper()
	r, err := target.NewMapResolver(spec, "webgrip")
	if err != nil {
		t.Fatalf("NewMapResolver: %v", err)
	}
	return &Server{Targets: r, Log: slog.New(slog.DiscardHandler)}
}

func TestResolveTarget_AssignsResolvedTarget(t *testing.T) {
	s := testServer(t, "11/silver=webgrip/ploeg@development")
	item := work.WorkItem{Team: "silver", ExternalScope: "11"}
	s.resolveTarget(context.Background(), &item, true, provider.TrackerEvent{ExternalID: "596"})

	if item.Target == nil {
		t.Fatal("target not assigned")
	}
	if item.Target.Owner != "webgrip" || item.Target.Repo != "ploeg" || item.Target.BaseBranch != "development" {
		t.Errorf("target = %+v", item.Target)
	}
	if item.RouteRule != "11/silver" {
		t.Errorf("route rule = %q, want 11/silver", item.RouteRule)
	}
}

// An unmapped scope must still queue the item: the worker falls back to its
// env repo, which is the pre-decoupling behavior. Failing ingest here would
// drop work on a config gap.
func TestResolveTarget_UnmappedLeavesItemQueueable(t *testing.T) {
	s := testServer(t, "11/silver=webgrip/ploeg")
	item := work.WorkItem{Team: "bronze", ExternalScope: "99"}
	s.resolveTarget(context.Background(), &item, true, provider.TrackerEvent{ExternalID: "1"})

	if item.Target != nil || item.RouteRule != "" {
		t.Errorf("unmapped scope must leave the target unresolved, got %+v", item.Target)
	}
	// The scope is still recorded — that is what makes the onboarding worklist
	// a query rather than a log scrape.
	if item.ExternalScope != "99" {
		t.Errorf("scope must survive an unmapped resolution, got %q", item.ExternalScope)
	}
}

func TestResolveTarget_NoScopeAndNoResolver(t *testing.T) {
	s := testServer(t, "11=webgrip/ploeg")
	item := work.WorkItem{Team: "silver"} // provider sent no scope
	s.resolveTarget(context.Background(), &item, true, provider.TrackerEvent{ExternalID: "1"})
	if item.Target != nil {
		t.Errorf("no scope must mean no target, got %+v", item.Target)
	}

	// Nil resolver = no mapping configured at all: every item stays
	// unresolved and every worker uses its env repo.
	bare := &Server{Log: slog.New(slog.DiscardHandler)}
	item2 := work.WorkItem{Team: "silver", ExternalScope: "11"}
	bare.resolveTarget(context.Background(), &item2, true, provider.TrackerEvent{ExternalID: "1"})
	if item2.Target != nil {
		t.Errorf("nil resolver must resolve nothing, got %+v", item2.Target)
	}
}
