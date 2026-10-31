package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func clickupAssigneeHook(t *testing.T, h http.Handler, taskID, field, who string) {
	t.Helper()
	side := "after"
	if field == "assignee_rem" {
		side = "before"
	}
	body := fmt.Sprintf(`{"event":"taskAssigneeUpdated","task_id":%q,"history_items":[{"id":"%s-%s-%s","field":%q,"date":"1759310400000",%q:{"username":%q}}]}`,
		taskID, taskID, field, who, field, side, who)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("clickup", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("%s %s on %s: HTTP %d", field, who, taskID, w.Code)
	}
}

func clickupItem(t *testing.T, taskID string) (state, team string, found bool) {
	t.Helper()
	err := testPool.QueryRow(context.Background(), `SELECT state, team FROM work_items WHERE provider='clickup' AND external_id=$1`, taskID).Scan(&state, &team)
	if err != nil {
		return "", "", false
	}
	return state, team, true
}

func TestOnlyAnAgentMembersAssignmentHandsAClickUpTaskToPloeg(t *testing.T) {
	reset(t)
	board := newClickupBoard(t)
	board.set("cu-self", clickupTask{statusType: "open"})
	board.set("cu-agent", clickupTask{statusType: "open"})
	cu := board.provider()
	cu.Agents = []string{"Agent-Member"}
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers:   map[string]provider.TrackerProvider{"clickup": cu},
		ScopeTeams: map[string]string{"10": "silver"}}
	h := s.Handler()

	clickupAssigneeHook(t, h, "cu-self", "assignee_add", "a-person")
	if _, _, found := clickupItem(t, "cu-self"); found {
		t.Fatal("a person assigning themselves on a pinned List queued work")
	}

	clickupAssigneeHook(t, h, "cu-agent", "assignee_add", "agent-member")
	state, team, found := clickupItem(t, "cu-agent")
	if !found || state != "queued" || team != "silver" {
		t.Fatalf("agent assignment = %q/%q (found %v); want queued for the List's Team", state, team, found)
	}

	clickupAssigneeHook(t, h, "cu-agent", "assignee_rem", "a-person")
	if state, _, _ := clickupItem(t, "cu-agent"); state != "queued" {
		t.Fatalf("removing a person withdrew the agent's work: %q", state)
	}
	clickupAssigneeHook(t, h, "cu-agent", "assignee_rem", "agent-member")
	if state, _, _ := clickupItem(t, "cu-agent"); state == "queued" {
		t.Fatal("removing the agent member left the work queued")
	}
}
