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

	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeBoard struct {
	mu           sync.Mutex
	project      int
	bucket       string
	labels       []string
	comments     string
	statusReads  int
	commentReads int
}

func (b *fakeBoard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case r.URL.Path == "/tasks/1900" && r.URL.Query().Get("expand") == "buckets":
		b.statusReads++
		labels := make([]string, 0, len(b.labels))
		for _, l := range b.labels {
			labels = append(labels, fmt.Sprintf(`{"title":%q}`, l))
		}
		fmt.Fprintf(w, `{"id":1900,"project_id":%d,"buckets":[{"title":%q}],"labels":[%s]}`, b.project, b.bucket, strings.Join(labels, ","))
	case r.URL.Path == "/tasks/1900/comments":
		b.commentReads++
		if b.comments == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, b.comments)
	default:
		http.NotFound(w, r)
	}
}

func (b *fakeBoard) set(bucket string, labels ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bucket, b.labels = bucket, labels
}

func gateServer(t *testing.T, board *fakeBoard) (*Server, string) {
	t.Helper()
	reset(t)
	api := httptest.NewServer(board)
	t.Cleanup(api.Close)
	m, err := gate.NewMap(gate.Statuses{Development: []string{"Doing"}, Test: []string{"In test"}, Acceptance: []string{"UAT"}, Done: []string{"Done"}})
	if err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	return &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers:       map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{Secret: testTrackerSecret, BaseURL: api.URL, Token: "fixture", DefaultTeam: "silver"}},
		Gates:          gate.Boards{"vikunja": {"10": m}},
		ForgeBots:      []string{"ploeg-bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}, token
}

func taskUpdated(t *testing.T, h http.Handler, project int, doer string) {
	t.Helper()
	body := fmt.Sprintf(`{"event_name":"task.updated","data":{"task":{"id":1900,"project_id":%d},"doer":{"username":%q}}}`, project, doer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedTrackerHook("vikunja", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook returned %d: %s", rec.Code, rec.Body)
	}
}

func gateRows(t *testing.T) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT gate || '/' || status || '/' || COALESCE(actor, '-') || '/' || COALESCE(reason, '-')
		FROM gate_transitions ORDER BY id`)
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
		out = append(out, s)
	}
	return out
}

func TestTrackerWebhook_RecordsGateMovesAndBounceReasons(t *testing.T) {
	board := &fakeBoard{project: 10}
	s, token := gateServer(t, board)
	h := s.Handler()
	ctx := context.Background()

	board.set("Doing")
	taskUpdated(t, h, 10, "dev")
	if board.statusReads != 0 || len(gateRows(t)) != 0 {
		t.Fatalf("a ticket without a Work Item read the board %d times and recorded %v", board.statusReads, gateRows(t))
	}

	item, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1900", Team: "silver", Title: "Gate me",
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg"}})
	if err != nil {
		t.Fatal(err)
	}
	taskUpdated(t, h, 10, "dev")
	taskUpdated(t, h, 10, "dev")
	board.set("In test")
	taskUpdated(t, h, 10, "dev")
	board.set("Doing", "bounce:environment")
	board.comments = `[{"id":1,"comment":"<p>bounce:defect the login form 500s</p>","created":"2999-01-01T00:00:00Z","author":{"username":"quinn"}}]`
	taskUpdated(t, h, 10, "quinn")
	board.comments = ""
	board.set("In test", "bounce:environment")
	taskUpdated(t, h, 10, "dev")
	board.set("Doing", "bounce:environment")
	taskUpdated(t, h, 10, "quinn")
	board.set("In test", "bounce:environment", "bounce:defect")
	taskUpdated(t, h, 10, "dev")
	board.set("Doing", "bounce:environment", "bounce:defect")
	taskUpdated(t, h, 10, "quinn")
	board.set("Backlog")
	taskUpdated(t, h, 10, "dev")
	board.set("In test")
	taskUpdated(t, h, 11, "dev")

	want := []string{
		"development/Doing/dev/-",
		"test/In test/dev/-",
		"development/Doing/quinn/defect",
		"test/In test/dev/-",
		"development/Doing/quinn/environment",
		"test/In test/dev/-",
		"development/Doing/quinn/-",
	}
	if got := gateRows(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows =\n%v\nwant\n%v\n(comment beats label; one label names the reason; two labels are unknown; an unmapped status or board records nothing)", got, want)
	}
	if board.commentReads != 3 {
		t.Fatalf("comments read %d times; only a bounce reads them", board.commentReads)
	}

	if _, err := testStore.RecordPullRequestFacts(ctx, store.PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90,
		WorkItemID: item, State: "open", HeadSHA: "h1", Review: &store.PullRequestReview{Reviewer: "anna", State: "approved", HeadSHA: "h1"}}); err != nil {
		t.Fatal(err)
	}
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/facts", item))
	var body struct {
		Facts struct {
			GateTransitions []struct {
				Gate   string  `json:"gate"`
				Reason *string `json:"reason"`
			} `json:"gateTransitions"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	moves := body.Facts.GateTransitions
	if len(moves) != 7 || moves[2].Reason == nil || *moves[2].Reason != "defect" || moves[6].Reason != nil {
		t.Fatalf("gate transitions = %s", raw)
	}
	if !strings.Contains(string(raw), `{"login":"quinn","roles":["mover"]}`) {
		t.Fatalf("roster lacks the person who moved the ticket out of test: %s", raw)
	}
}

func TestTrackerWebhook_AnAssignmentRecordsTheGateItStartsIn(t *testing.T) {
	board := &fakeBoard{project: 10}
	s, _ := gateServer(t, board)
	board.set("Doing")
	body := `{"event_name":"task.assignee.created","data":{"task":{"id":1900,"project_id":10,"title":"Gate me"},"assignee":{"username":"silver"},"doer":{"username":"paula"}}}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedTrackerHook("vikunja", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", rec.Code)
	}
	if got := gateRows(t); fmt.Sprint(got) != "[development/Doing/paula/-]" {
		t.Fatalf("rows = %v", got)
	}
}

func TestTrackerWebhook_GatesNeedAConfiguredBoard(t *testing.T) {
	board := &fakeBoard{project: 10}
	s, _ := gateServer(t, board)
	s.Gates = nil
	if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "1900", Team: "silver"}); err != nil {
		t.Fatal(err)
	}
	board.set("In test")
	taskUpdated(t, s.Handler(), 10, "dev")
	if board.statusReads != 0 || len(gateRows(t)) != 0 {
		t.Fatalf("without gates configured the board was read %d times", board.statusReads)
	}
}
