package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type clickupTask struct {
	statusType string
	tags       []string
	broken     bool
}

type clickupBoard struct {
	mu       sync.Mutex
	tasks    map[string]clickupTask
	comments map[string]int
	api      *httptest.Server
}

func newClickupBoard(t *testing.T) *clickupBoard {
	t.Helper()
	b := &clickupBoard{tasks: map[string]clickupTask{}, comments: map[string]int{}}
	b.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/task/")
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method == http.MethodPost && strings.HasSuffix(id, "/comment") {
			b.comments[strings.TrimSuffix(id, "/comment")]++
			return
		}
		task, ok := b.tasks[id]
		if !ok || task.broken {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		tags := []map[string]string{}
		for _, tag := range task.tags {
			tags = append(tags, map[string]string{"name": tag})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "clickup fixture", "date_updated": "1",
			"status": map[string]string{"status": task.statusType, "type": task.statusType},
			"list":   map[string]string{"id": "10"}, "tags": tags})
	}))
	t.Cleanup(b.api.Close)
	return b
}

func (b *clickupBoard) set(id string, task clickupTask) {
	b.mu.Lock()
	b.tasks[id] = task
	b.mu.Unlock()
}

func (b *clickupBoard) provider() *clickup.Provider {
	return &clickup.Provider{Secret: testTrackerSecret, DefaultTeam: "bronze", BaseURL: b.api.URL, Token: "fixture",
		Log: slog.New(slog.DiscardHandler)}
}

func clickupHook(t *testing.T, h http.Handler, event, taskID string) {
	t.Helper()
	body := fmt.Sprintf(`{"event":%q,"task_id":%q,"history_items":[{"field":"assignee_add","after":{"username":"builder"}}]}`, event, taskID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("clickup", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("%s for %s: HTTP %d", event, taskID, w.Code)
	}
}

func clickupRoute(t *testing.T, taskID string) (int, work.Target, string) {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE provider='clickup' AND external_id=$1`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	var target work.Target
	var hint string
	if n == 0 {
		return 0, target, ""
	}
	if err := testPool.QueryRow(ctx, `SELECT target_forge, target_owner, target_repo, target_base_branch, route_hint
		FROM work_items WHERE provider='clickup' AND external_id=$1`, taskID).
		Scan(&target.Forge, &target.Owner, &target.Repo, &target.BaseBranch, &hint); err != nil {
		t.Fatal(err)
	}
	return n, target, hint
}

func TestClickupTagsRouteWorkLikeVikunjaLabels(t *testing.T) {
	reset(t)
	board := newClickupBoard(t)
	board.set("cu-untagged", clickupTask{statusType: "open", tags: []string{"do-next"}})
	board.set("cu-homelab", clickupTask{statusType: "open", tags: []string{"repo/homelab-cluster"}})
	board.set("cu-unregistered", clickupTask{statusType: "open", tags: []string{"repo/ploeg"}})
	board.set("cu-conflict", clickupTask{statusType: "open", tags: []string{"repo/glide", "repo/homelab-cluster"}})
	s := routingServer(t, newBoard(t), readyForge(), nil)
	s.Trackers = map[string]provider.TrackerProvider{"clickup": board.provider()}
	h := s.Handler()

	for _, id := range []string{"cu-untagged", "cu-homelab", "cu-unregistered", "cu-conflict"} {
		clickupHook(t, h, "taskAssigneeUpdated", id)
	}

	if n, target, hint := clickupRoute(t, "cu-untagged"); n != 1 || target != glideTarget || hint != "" {
		t.Errorf("untagged task: %d items, target %+v, hint %q; want the list default", n, target, hint)
	}
	if n, target, hint := clickupRoute(t, "cu-homelab"); n != 1 || target != homelabTarget || hint != "repo/homelab-cluster" {
		t.Errorf("repo/homelab-cluster tag: %d items, target %+v, hint %q; want the tagged target", n, target, hint)
	}
	for _, id := range []string{"cu-unregistered", "cu-conflict"} {
		if n, _, _ := clickupRoute(t, id); n != 0 {
			t.Errorf("%s was queued; an unregistered or conflicting repo tag must be refused", id)
		}
		if reasons := refusalReasons(t, id); len(reasons) != 1 || reasons[0] == "" {
			t.Errorf("%s refusal audit rows = %q, want one with a reason", id, reasons)
		}
		board.mu.Lock()
		comments := board.comments[id]
		board.mu.Unlock()
		if comments != 1 {
			t.Errorf("%s got %d refusal comments, want 1", id, comments)
		}
	}
}

func clickupShift(t *testing.T, externalID string) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "clickup", ExternalID: externalID, Team: "bronze", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/cu-"+externalID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "builder"}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClosingAClickupTaskWithdrawsOnlyUnstartedWorkItConfirmedClosed(t *testing.T) {
	reset(t)
	ctx := context.Background()
	board := newClickupBoard(t)
	unstarted := clickupShift(t, "cu-closed")
	started := clickupShift(t, "cu-started")
	stillOpen := clickupShift(t, "cu-open")
	unreadable := clickupShift(t, "cu-unreadable")
	if _, err := testPool.Exec(ctx, `UPDATE agent_runs SET started_at = now() WHERE work_item_id = $1`, started); err != nil {
		t.Fatal(err)
	}
	board.set("cu-closed", clickupTask{statusType: "closed"})
	board.set("cu-started", clickupTask{statusType: "done"})
	board.set("cu-open", clickupTask{statusType: "custom"})
	board.set("cu-unreadable", clickupTask{statusType: "closed", broken: true})
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers: map[string]provider.TrackerProvider{"clickup": board.provider()},
		Engine:   &shiftengine.Engine{Store: testStore, Log: slog.New(slog.DiscardHandler), Uniform: true}}
	h := s.Handler()

	for _, id := range []string{"cu-closed", "cu-started", "cu-open", "cu-unreadable", "cu-never-seen"} {
		clickupHook(t, h, "taskStatusUpdated", id)
	}

	if got := snapshotItem(t, unstarted); got.state != string(work.StateWithdrawn) || got.liveShifts != 0 || got.closeReason != store.CloseReasonWithdrawnClosed {
		t.Fatalf("closing an unstarted ClickUp task did not withdraw it: %+v", got)
	}
	var audited int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='work_item.withdrawn' AND actor='webhook:clickup'`, unstarted).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("withdrawal audit rows=%d err=%v", audited, err)
	}
	for name, id := range map[string]int64{"started": started, "still open": stillOpen, "unreadable": unreadable} {
		if got := snapshotItem(t, id); got.state != "queued" || got.liveShifts != 1 {
			t.Errorf("%s task was withdrawn: %+v", name, got)
		}
	}
}
