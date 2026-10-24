package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func rebaseServer(t *testing.T, policies map[string]work.ForgeFollowUps) *Server {
	t.Helper()
	reset(t)
	log := slog.New(slog.DiscardHandler)
	return &Server{
		Store: testStore, Log: log, LeaseTTL: time.Minute,
		Engine:    &shiftengine.Engine{Store: testStore, Plans: plan.Plans{}, Uniform: true, Log: log},
		FollowUps: policies,
	}
}

func TestAConfirmedConflictQueuesARebaseFollowUpOnTheSameBranch(t *testing.T) {
	s := rebaseServer(t, map[string]work.ForgeFollowUps{"bronze": {RebaseOnConflict: true, MaxRepairs: 2}})
	source := awaitingReviewItem(t, "2397")

	s.RebaseConflicted(context.Background(), "forgejo", "webgrip/ploeg", "agent/vik-2397", 9, "development")

	got := followUps(t, source)
	if len(got) != 1 || got[0].Branch != "agent/vik-2397" || got[0].State != string(work.StateQueued) || got[0].Team != "bronze" {
		t.Fatalf("follow-ups = %+v, want one queued rebase on agent/vik-2397 for bronze", got)
	}
	item, err := testStore.WorkItem(context.Background(), got[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pull request #9", "conflicts with its base branch development", "rebase it onto origin/development", "--force-with-lease", "VIK-2397"} {
		if !strings.Contains(item.Description, want) {
			t.Errorf("rebase brief lacks %q:\n%s", want, item.Description)
		}
	}
	if !strings.HasPrefix(item.Title, "Rebase onto development: ") {
		t.Errorf("title = %q", item.Title)
	}
	if si, err := testStore.LiveShiftForItem(context.Background(), got[0].ID); err != nil || si == nil || si.Branch != "agent/vik-2397" {
		t.Errorf("follow-up shift = %+v (%v), want one on the pull request's branch", si, err)
	}

	s.RebaseConflicted(context.Background(), "forgejo", "webgrip/ploeg", "agent/vik-2397", 9, "development")
	if got := followUps(t, source); len(got) != 1 {
		t.Errorf("follow-ups = %d, want one open rebase per pull request", len(got))
	}
}

func TestAConflictOnATeamThatDoesNotRebaseQueuesNothing(t *testing.T) {
	s := rebaseServer(t, map[string]work.ForgeFollowUps{"bronze": {RepairFailedChecks: true}})
	source := awaitingReviewItem(t, "2398")
	s.RebaseConflicted(context.Background(), "forgejo", "webgrip/ploeg", "agent/vik-2398", 9, "development")
	if got := followUps(t, source); len(got) != 0 {
		t.Errorf("follow-ups = %+v, want none without forgeFollowUps.rebaseOnConflict", got)
	}
}
