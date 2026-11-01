package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// delegateBoard fakes the ClickUp API for tasks on List 10 whose Team
// dropdown field is set or not, and keeps every comment as shown.
type delegateBoard struct {
	mu       sync.Mutex
	teams    map[string]string
	comments map[string][]string
	api      *httptest.Server
}

func newDelegateBoard(t *testing.T) *delegateBoard {
	t.Helper()
	b := &delegateBoard{teams: map[string]string{}, comments: map[string][]string{}}
	b.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/task/")
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method == http.MethodPost && strings.HasSuffix(id, "/comment") {
			var body struct {
				CommentText string `json:"comment_text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.comments[strings.TrimSuffix(id, "/comment")] = append(b.comments[strings.TrimSuffix(id, "/comment")], body.CommentText)
			return
		}
		team, ok := b.teams[id]
		if !ok {
			http.Error(w, `{"err":"Task not found"}`, http.StatusNotFound)
			return
		}
		fields := []map[string]any{}
		if team != "" {
			options := []map[string]any{}
			value := any(nil)
			for i, option := range []string{"Bronze", "Silver", "Platinum"} {
				options = append(options, map[string]any{"id": "opt-" + strings.ToLower(option), "name": option, "orderindex": i})
				if option == team {
					value = i
				}
			}
			fields = append(fields, map[string]any{"id": "f1", "name": "Team", "type": "drop_down", "value": value,
				"type_config": map[string]any{"options": options}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "delegate fixture", "date_updated": "1",
			"status": map[string]string{"status": "open", "type": "open"}, "list": map[string]string{"id": "10"},
			"custom_fields": fields})
	}))
	t.Cleanup(b.api.Close)
	return b
}

func (b *delegateBoard) set(id, team string) {
	b.mu.Lock()
	b.teams[id] = team
	b.mu.Unlock()
}

func (b *delegateBoard) commentsOn(id string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.comments[id]...)
}

func delegateServer(board *delegateBoard) *Server {
	cu := &clickup.Provider{Secret: testTrackerSecret, DefaultTeam: "bronze", BaseURL: board.api.URL, Token: "fixture",
		Agents: []string{"agent-member"}, Log: slog.New(slog.DiscardHandler)}
	return &Server{Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers:       map[string]provider.TrackerProvider{"clickup": cu},
		ScopeTeams:     map[string]string{"10": "bronze"},
		OperatorConfig: OperatorConfig{Teams: map[string][]string{"bronze": {}, "silver": {}}}}
}

func TestAClickUpTeamFieldWinsOverTheListPinAndItsAbsenceFallsBackToIt(t *testing.T) {
	reset(t)
	board := newDelegateBoard(t)
	board.set("cu-field", "Silver")
	board.set("cu-plain", "")
	h := delegateServer(board).Handler()

	clickupAssigneeHook(t, h, "cu-field", "assignee_add", "agent-member")
	if state, team, found := clickupItem(t, "cu-field"); !found || state != "queued" || team != "silver" {
		t.Fatalf("field-set task = %q/%q (found %v); want queued for the field's Team over the List pin", state, team, found)
	}
	clickupAssigneeHook(t, h, "cu-plain", "assignee_add", "agent-member")
	if state, team, found := clickupItem(t, "cu-plain"); !found || state != "queued" || team != "bronze" {
		t.Fatalf("task without the field = %q/%q (found %v); want the List's pinned Team", state, team, found)
	}
}

func TestAClickUpTeamFieldNamingAnUnknownTeamIsRefusedAtIntake(t *testing.T) {
	reset(t)
	ctx := context.Background()
	board := newDelegateBoard(t)
	board.set("cu-platinum", "Platinum")
	h := delegateServer(board).Handler()

	clickupAssigneeHook(t, h, "cu-platinum", "assignee_add", "agent-member")

	if _, _, found := clickupItem(t, "cu-platinum"); found {
		t.Fatal("a task naming a Team the installation lacks was queued")
	}
	var code, reason, team string
	if err := testPool.QueryRow(ctx, `SELECT detail->>'code', detail->>'reason', detail->>'team' FROM audit_log
		WHERE action='work_item.route_refused' AND detail->>'external_id'='cu-platinum'`).Scan(&code, &reason, &team); err != nil {
		t.Fatalf("refusal audit row: %v", err)
	}
	if code != target.RefusalTeamUnknown {
		t.Errorf("code = %q, want %q", code, target.RefusalTeamUnknown)
	}
	if !strings.Contains(reason, `"platinum"`) || !strings.Contains(reason, "bronze, silver") {
		t.Errorf("reason = %q, want the field's value and the known Teams", reason)
	}
	if team != "bronze" {
		t.Errorf("refusal recorded under %q, want the List's Team so its consumer sees it", team)
	}
	comments := board.commentsOn("cu-platinum")
	if len(comments) != 1 || !strings.Contains(comments[0], "platinum") || !strings.Contains(comments[0], "bronze, silver") || strings.Contains(comments[0], "<p>") {
		t.Fatalf("comments = %q, want one that names the Teams without markup", comments)
	}
	refusals, err := testStore.RecentRouteRefusals(ctx, []string{"bronze"}, time.Now().Add(-time.Hour), 10)
	if err != nil || len(refusals) != 1 || refusals[0].Code != target.RefusalTeamUnknown {
		t.Fatalf("operator refusals = %+v, %v; want the one team_unknown row", refusals, err)
	}
}

// Before ADR-0187 slice A, trackerUnassigned recomputed the Team from the
// List pin alone, so a Work Item the field had routed to another Team could
// not be withdrawn by unassigning the agent member.
func TestUnassigningWithdrawsWorkWhoseTeamTheFieldChose(t *testing.T) {
	reset(t)
	board := newDelegateBoard(t)
	board.set("cu-field-stop", "Silver")
	h := delegateServer(board).Handler()

	clickupAssigneeHook(t, h, "cu-field-stop", "assignee_add", "agent-member")
	if state, team, _ := clickupItem(t, "cu-field-stop"); state != "queued" || team != "silver" {
		t.Fatalf("assignment = %q/%q, want queued for silver", state, team)
	}
	clickupAssigneeHook(t, h, "cu-field-stop", "assignee_rem", "agent-member")
	if state, _, _ := clickupItem(t, "cu-field-stop"); state != string(work.StateWithdrawn) {
		t.Fatalf("state after unassignment = %q, want withdrawn", state)
	}
}
