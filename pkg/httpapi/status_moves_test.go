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
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type flowBoard struct {
	mu          sync.Mutex
	project     int
	buckets     []string
	statusReads int
}

func (b *flowBoard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case r.URL.Path == "/tasks/1900" && r.URL.Query().Get("expand") == "buckets":
		b.statusReads++
		buckets := make([]string, 0, len(b.buckets))
		for _, title := range b.buckets {
			buckets = append(buckets, fmt.Sprintf(`{"title":%q}`, title))
		}
		fmt.Fprintf(w, `{"id":1900,"project_id":%d,"created":"2026-09-17T09:00:00Z","buckets":[%s],"labels":[]}`, b.project, strings.Join(buckets, ","))
	case r.URL.Path == "/tasks/1900/comments":
		fmt.Fprint(w, `[]`)
	default:
		http.NotFound(w, r)
	}
}

func (b *flowBoard) set(buckets ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buckets = buckets
}

func flowServer(t *testing.T, board *flowBoard, gated bool) (*Server, string) {
	t.Helper()
	reset(t)
	api := httptest.NewServer(board)
	t.Cleanup(api.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers:       map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{Secret: testTrackerSecret, BaseURL: api.URL, Token: "fixture", DefaultTeam: "silver"}},
		ForgeBots:      []string{"ploeg-bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}
	if gated {
		m, err := gate.NewMap(gate.Statuses{Development: []string{"Doing"}, Test: []string{"In test"}, Done: []string{"Done"}})
		if err != nil {
			t.Fatal(err)
		}
		s.Gates = gate.Boards{"vikunja": {"10": m}}
	}
	return s, token
}

func taskUpdatedAt(t *testing.T, h http.Handler, project int, updated string) {
	t.Helper()
	task := fmt.Sprintf(`{"id":1900,"project_id":%d}`, project)
	if updated != "" {
		task = fmt.Sprintf(`{"id":1900,"project_id":%d,"updated":%q}`, project, updated)
	}
	body := fmt.Sprintf(`{"event_name":"task.updated","data":{"task":%s,"doer":{"username":"dev"}}}`, task)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedTrackerHook("vikunja", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook returned %d: %s", rec.Code, rec.Body)
	}
}

func statusTransitionRows(t *testing.T) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT status || '/' || COALESCE(gate, '-') || '/' || observed::text || '/' ||
		to_char(at AT TIME ZONE 'UTC', 'DD HH24:MI') FROM status_transitions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if i := strings.Index(s, "/true/"); i >= 0 {
			s = s[:i] + "/observed"
		}
		out = append(out, s)
	}
	return out
}

type statusFacts struct {
	Facts struct {
		WorkItem struct {
			TrackerCreatedAt *time.Time `json:"trackerCreatedAt"`
			EstimateSeconds  *int64     `json:"estimateSeconds"`
		} `json:"workItem"`
		StatusTransitions []struct {
			Status   string  `json:"status"`
			Gate     *string `json:"gate"`
			Observed bool    `json:"observed"`
		} `json:"statusTransitions"`
	} `json:"facts"`
}

func readStatusFacts(t *testing.T, s *Server, token string, id int64) statusFacts {
	t.Helper()
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/facts", id))
	var body statusFacts
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestTrackerWebhook_RecordsEveryStatusMoveOfAGatedBoard(t *testing.T) {
	board := &flowBoard{project: 10}
	s, token := flowServer(t, board, true)
	h := s.Handler()
	ctx := context.Background()

	board.set("Backlog")
	taskUpdatedAt(t, h, 10, "2026-09-21T08:00:00Z")
	if board.statusReads != 0 || len(statusTransitionRows(t)) != 0 {
		t.Fatalf("a ticket without a Work Item read the board %d times", board.statusReads)
	}
	item, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1900", Team: "silver", Title: "Flow me", ExternalScope: "10",
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg"}})
	if err != nil {
		t.Fatal(err)
	}
	taskUpdatedAt(t, h, 10, "2026-09-21T08:00:00Z")
	taskUpdatedAt(t, h, 10, "2026-09-21T08:30:00Z")
	board.set("Doing")
	taskUpdatedAt(t, h, 10, "2026-09-21T09:00:00Z")
	board.set("Parked")
	taskUpdatedAt(t, h, 10, "2026-09-21T11:00:00Z")
	board.set("Doing")
	taskUpdatedAt(t, h, 10, "")
	board.set("In test", "Sprint 41")
	taskUpdatedAt(t, h, 10, "2026-09-21T10:00:00Z")
	board.set("Sprint 41", "Sprint 42")
	taskUpdatedAt(t, h, 10, "2026-09-22T09:00:00Z")
	board.set("Doing")
	taskUpdatedAt(t, h, 11, "2026-09-22T09:00:00Z")

	want := []string{"Backlog/-/false/21 08:00", "Doing/development/false/21 09:00", "Parked/-/false/21 11:00", "Doing/development/observed",
		"In test/test/observed"}
	if got := statusTransitionRows(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows =\n%v\nwant\n%v\n(unmapped statuses count, a repeat is kept once, a missing or earlier tracker time is observed, two unmapped buckets record nothing, another board records nothing)", got, want)
	}
	if got := gateRows(t); fmt.Sprint(got) != "[development/Doing/dev/- test/In test/dev/-]" {
		t.Fatalf("gate rows = %v; an unmapped status records no gate move", got)
	}
	var created time.Time
	if err := testPool.QueryRow(ctx, `SELECT tracker_created_at FROM work_items WHERE id = $1`, item).Scan(&created); err != nil ||
		!created.Equal(time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("tracker creation time = %v, %v", created, err)
	}

	f := readStatusFacts(t, s, token, item).Facts
	if len(f.StatusTransitions) != 5 || f.StatusTransitions[1].Gate == nil || *f.StatusTransitions[1].Gate != "development" ||
		f.StatusTransitions[2].Status != "Parked" || f.StatusTransitions[2].Gate != nil || !f.StatusTransitions[4].Observed {
		t.Fatalf("status transitions = %+v", f.StatusTransitions)
	}
}

func TestTrackerWebhook_ABoardWithoutGatesRecordsNoStatus(t *testing.T) {
	board := &flowBoard{project: 10}
	s, _ := flowServer(t, board, false)
	if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "1900", Team: "silver"}); err != nil {
		t.Fatal(err)
	}
	board.set("In progress")
	taskUpdatedAt(t, s.Handler(), 10, "2026-09-21T09:00:00Z")
	if board.statusReads != 0 || len(statusTransitionRows(t)) != 0 || len(gateRows(t)) != 0 {
		t.Fatalf("a board without gates was read %d times and recorded %v", board.statusReads, statusTransitionRows(t))
	}
}

func TestClickUpWebhook_RecordsStatusesCreationAndEstimate(t *testing.T) {
	reset(t)
	var mu sync.Mutex
	status := "to do"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/task/abc" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"id":"abc","name":"Estimate me","date_created":"1758272400000","time_estimate":7200000,
			"status":{"status":%q,"type":"custom"},"list":{"id":"901"},"tags":[]}`, status)
	}))
	defer api.Close()
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	gates, err := gate.NewMap(gate.Statuses{Development: []string{"in progress"}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers:       map[string]provider.TrackerProvider{"clickup": &clickup.Provider{Secret: testTrackerSecret, BaseURL: api.URL, Token: "fixture", DefaultTeam: "silver"}},
		Gates:          gate.Boards{"clickup": {"901": gates}},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}
	post := func(event string, history string) {
		t.Helper()
		body := fmt.Sprintf(`{"event":%q,"task_id":"abc","history_items":[%s]}`, event, history)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, signedTrackerHook("clickup", body))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("webhook returned %d: %s", rec.Code, rec.Body)
		}
	}
	post("taskAssigneeUpdated", `{"field":"assignee_add","after":{"username":"silver"},"user":{"username":"paula"},"date":"1758531600000"}`)
	mu.Lock()
	status = "in progress"
	mu.Unlock()
	post("taskStatusUpdated", `{"field":"status","user":{"username":"dev"},"date":"1758535200000"}`)

	if got := statusTransitionRows(t); fmt.Sprint(got) != "[to do/-/false/22 09:00 in progress/development/false/22 10:00]" {
		t.Fatalf("rows = %v", got)
	}
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM work_items WHERE provider = 'clickup' AND external_id = 'abc'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f := readStatusFacts(t, s, token, id).Facts
	if f.WorkItem.EstimateSeconds == nil || *f.WorkItem.EstimateSeconds != 7200 || f.WorkItem.TrackerCreatedAt == nil ||
		!f.WorkItem.TrackerCreatedAt.Equal(time.UnixMilli(1758272400000).UTC()) || len(f.StatusTransitions) != 2 {
		t.Fatalf("facts = %+v", f)
	}
}
