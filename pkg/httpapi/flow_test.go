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

	"github.com/ploeg-hq/ploeg/pkg/flow"
	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/store"
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

func flowServer(t *testing.T, board *flowBoard, gated bool, kinds flow.Kinds) (*Server, string) {
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
	km, err := flow.NewKindMap(kinds)
	if err != nil {
		t.Fatal(err)
	}
	s.StatusBoards = flow.Boards{"vikunja": {"10": km}}
	cal, err := flow.NewCalendar(flow.Hours{Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	s.WorkingCalendars = map[string]flow.Calendar{"silver": cal}
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

func TestTrackerWebhook_RecordsEveryStatusMoveForTheCardsFlow(t *testing.T) {
	board := &flowBoard{project: 10}
	s, token := flowServer(t, board, true, flow.Kinds{Blocked: []string{"Parked"}})
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

	if _, err := testStore.RecordPullRequestFacts(ctx, store.PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 91,
		WorkItemID: item, State: "merged", MergedAt: ptrTime(time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)), MergedBy: "ryan",
		MergeCommitSHA: strings.Repeat("9", 40)}); err != nil {
		t.Fatal(err)
	}
	var pr int64
	if err := testPool.QueryRow(ctx, `SELECT id FROM pull_requests WHERE number = 91`).Scan(&pr); err != nil {
		t.Fatal(err)
	}
	dep, err := testStore.RecordDeployment(ctx, store.Deployment{Forge: "forgejo", Owner: "webgrip", Name: "ploeg", Environment: "production",
		SHA: strings.Repeat("a", 40), DeployedAt: time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC), Source: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.MarkDeployed(ctx, pr, dep.ID); err != nil {
		t.Fatal(err)
	}
	bug, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1901", Team: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO card_cracks (team, card_work_item_id, bug_work_item_id, state, severity, share, discovery,
		proposed_by, proposed_at, confirmed_by, confirmed_at, mend_number, mended_at, mended_by, mend_by_steward)
		VALUES ('silver', $1, $2, 'confirmed', 'S3', 'primary', 'discovered', 'anna', $3, 'bram', $3, 92, $4, 'ryan', true)`,
		item, bug, time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC), time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", item))
	var body struct {
		Card store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	f := body.Card.Flow
	if f == nil || len(f.Statuses) != 4 || f.Statuses[2].Kind != flow.Blocked || f.Statuses[3].Status != "In test" || !f.Statuses[3].Current ||
		f.BlockedSeconds == nil || *f.BlockedSeconds != f.Statuses[2].Seconds {
		t.Fatalf("flow = %s (Parked is blocked by the board's statusKinds)", raw)
	}
	if f.LeadTime == nil || f.LeadTime.Start != "tracker_created" || f.LeadTime.End != "release" ||
		!f.LeadTime.To.Equal(time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("lead time = %+v", f.LeadTime)
	}
	if f.TimeToProduction == nil || f.TimeToProduction.Seconds != 4*3600 || f.TimeToProduction.WorkingSeconds != 4*3600 ||
		len(f.Restores) != 1 || f.Restores[0].Seconds != 3*3600 || f.Calendar.Timezone != "UTC" {
		t.Fatalf("delivery and restore = %s", raw)
	}
	if body.Card.Grade == nil || body.Card.Grade.Formula != store.GradeFormula {
		t.Fatalf("grade = %s", raw)
	}

	list := operatorSchemaGET(t, s, token, "cards?member=ryan")
	if !strings.Contains(string(list), `"flow":{`) {
		t.Fatalf("the card list carries the same flow: %s", list)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestTrackerWebhook_StatusKindsAloneRecordStatusesWithoutGates(t *testing.T) {
	board := &flowBoard{project: 10}
	s, _ := flowServer(t, board, false, flow.Kinds{})
	if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "1900", Team: "silver"}); err != nil {
		t.Fatal(err)
	}
	board.set("In progress")
	taskUpdatedAt(t, s.Handler(), 10, "2026-09-21T09:00:00Z")
	if got := statusTransitionRows(t); fmt.Sprint(got) != "[In progress/-/false/21 09:00]" || len(gateRows(t)) != 0 {
		t.Fatalf("rows = %v, gate rows = %v", got, gateRows(t))
	}
	s.StatusBoards = nil
	board.set("Done")
	reads := board.statusReads
	taskUpdatedAt(t, s.Handler(), 10, "2026-09-21T10:00:00Z")
	if board.statusReads != reads {
		t.Fatal("a board with neither gates nor statusKinds must not be read")
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
	km, err := flow.NewKindMap(flow.Kinds{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers:       map[string]provider.TrackerProvider{"clickup": &clickup.Provider{Secret: testTrackerSecret, BaseURL: api.URL, Token: "fixture", DefaultTeam: "silver"}},
		StatusBoards:   flow.Boards{"clickup": {"901": km}},
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

	if got := statusTransitionRows(t); fmt.Sprint(got) != "[to do/-/false/22 09:00 in progress/-/false/22 10:00]" {
		t.Fatalf("rows = %v", got)
	}
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM work_items WHERE provider = 'clickup' AND external_id = 'abc'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", id))
	var body struct {
		Card store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	f := body.Card.Flow
	if f == nil || f.EstimateSeconds == nil || *f.EstimateSeconds != 7200 || f.LeadTime == nil || f.LeadTime.Start != "tracker_created" ||
		!f.LeadTime.From.Equal(time.UnixMilli(1758272400000).UTC()) || !f.LeadTime.Running {
		t.Fatalf("flow = %s", raw)
	}
	if fmt.Sprint(f.NotCollected) != "[holidays]" || f.Calendar.Timezone != "Europe/Amsterdam" {
		t.Fatalf("notCollected %v, calendar %+v", f.NotCollected, f.Calendar)
	}
}
