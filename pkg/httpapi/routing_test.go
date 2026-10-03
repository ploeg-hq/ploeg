package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type boardFixture struct {
	mu       sync.Mutex
	labels   map[string][]string
	comments map[string][]string
	api      *httptest.Server
}

func newBoard(t *testing.T) *boardFixture {
	t.Helper()
	b := &boardFixture{labels: map[string][]string{}, comments: map[string][]string{}}
	b.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/tasks/")
		if r.Method == http.MethodPut && strings.HasSuffix(id, "/comments") {
			var body struct {
				Comment string `json:"comment"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.mu.Lock()
			b.comments[strings.TrimSuffix(id, "/comments")] = append(b.comments[strings.TrimSuffix(id, "/comments")], body.Comment)
			b.mu.Unlock()
			return
		}
		b.mu.Lock()
		labels := b.labels[id]
		b.mu.Unlock()
		type label struct {
			Title string `json:"title"`
		}
		task := map[string]any{"id": json.Number(id), "title": "routing fixture", "project_id": 10, "updated": "r1", "done": false}
		var ls []label
		for _, l := range labels {
			ls = append(ls, label{Title: l})
		}
		task["labels"] = ls
		_ = json.NewEncoder(w).Encode(task)
	}))
	t.Cleanup(b.api.Close)
	return b
}

func (b *boardFixture) commentsOn(id string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.comments[id]...)
}

type fakeForge struct {
	mu     sync.Mutex
	states map[string]provider.RepositoryState
	down   bool
}

func (f *fakeForge) InspectRepository(_ context.Context, owner, repository, branch string) (provider.RepositoryState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return provider.RepositoryState{}, errors.New("forge unreachable")
	}
	state, ok := f.states[owner+"/"+repository]
	if !ok {
		return provider.RepositoryState{}, errors.New("HTTP 404")
	}
	state.Branch = branch
	return state, nil
}

func (f *fakeForge) set(repo string, state provider.RepositoryState) {
	f.mu.Lock()
	f.states[repo] = state
	f.mu.Unlock()
}

var (
	glideTarget   = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "glide", BaseBranch: "development"}
	homelabTarget = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "homelab-cluster", BaseBranch: "main"}
)

func routingServer(t *testing.T, board *boardFixture, forge *fakeForge, engine *shiftengine.Engine) *Server {
	t.Helper()
	targets, err := target.New(target.Table{
		Targets: map[string]work.Target{"glide": glideTarget, "homelab-cluster": homelabTarget},
		Rules:   []target.Rule{{Scope: "10", Default: "glide", Allow: []string{"homelab-cluster"}}},
	}, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	gate := target.NewGate(targets.Registered(), map[string]provider.RepositoryInspector{"forgejo": forge})
	gate.Load(context.Background())
	s := &Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Targets:        targets,
		Readiness:      gate,
		Trackers: map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{Secret: testTrackerSecret,
			DefaultTeam: "bronze", BaseURL: board.api.URL, Token: "fixture", Log: slog.New(slog.DiscardHandler),
		}},
	}
	if engine != nil {
		s.Engine = engine
	}
	return s
}

func readyForge() *fakeForge {
	return &fakeForge{states: map[string]provider.RepositoryState{
		"webgrip/glide":           {AgentsFile: true},
		"webgrip/homelab-cluster": {AgentsFile: true},
	}}
}

func assign(t *testing.T, h http.Handler, taskID string) {
	t.Helper()
	body := fmt.Sprintf(`{"event_name":"task.assignee.created","data":{"task":{"id":%s,"title":"routing fixture","project_id":10},"assignee":{"username":"builder"}}}`, taskID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("vikunja", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("assignment of %s: HTTP %d", taskID, w.Code)
	}
}

type pinnedRoute struct {
	target    work.Target
	rule      string
	hint      string
	workItems int
}

func pinned(t *testing.T, taskID string) pinnedRoute {
	t.Helper()
	var p pinnedRoute
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE provider='vikunja' AND external_id=$1`, taskID).Scan(&p.workItems); err != nil {
		t.Fatal(err)
	}
	if p.workItems == 0 {
		return p
	}
	if err := testPool.QueryRow(ctx, `SELECT target_forge, target_owner, target_repo, target_base_branch, route_rule, route_hint
		FROM work_items WHERE provider='vikunja' AND external_id=$1`, taskID).
		Scan(&p.target.Forge, &p.target.Owner, &p.target.Repo, &p.target.BaseBranch, &p.rule, &p.hint); err != nil {
		t.Fatal(err)
	}
	return p
}

func refusalReasons(t *testing.T, taskID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT detail->>'reason' FROM audit_log
		WHERE action='work_item.route_refused' AND work_item_id IS NULL AND detail->>'external_id'=$1`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, reason)
	}
	return out
}

func TestIngestWithoutARepoLabelKeepsTheBoardDefault(t *testing.T) {
	reset(t)
	board := newBoard(t)
	board.labels["5001"] = []string{"do-next", "theme/routing"}
	h := routingServer(t, board, readyForge(), nil).Handler()

	assign(t, h, "5001")

	got := pinned(t, "5001")
	if got.workItems != 1 || got.target != glideTarget || got.rule != "10" || got.hint != "" {
		t.Errorf("pinned %+v, want project 10's default webgrip/glide@development with no hint", got)
	}
}

func TestIngestPinsTheTargetARepoLabelSelects(t *testing.T) {
	reset(t)
	board := newBoard(t)
	board.labels["5002"] = []string{"repo/homelab-cluster"}
	h := routingServer(t, board, readyForge(), nil).Handler()

	assign(t, h, "5002")

	got := pinned(t, "5002")
	if got.workItems != 1 || got.target != homelabTarget || got.rule != "10" || got.hint != "repo/homelab-cluster" {
		t.Errorf("pinned %+v, want webgrip/homelab-cluster@main selected by the label", got)
	}
	var detail string
	if err := testPool.QueryRow(context.Background(), `SELECT detail->>'route_hint' FROM audit_log WHERE action='work_item.queued'`).Scan(&detail); err != nil || detail != "repo/homelab-cluster" {
		t.Errorf("queued audit route_hint = %q (%v), want the label", detail, err)
	}
}

func TestIngestRefusalQueuesNothingAndTellsTheTicket(t *testing.T) {
	cases := map[string][]string{
		"5003": {"repo/ploeg"},
		"5004": {"repo/glide", "repo/homelab-cluster"},
	}
	for taskID, labels := range cases {
		t.Run(taskID, func(t *testing.T) {
			reset(t)
			board := newBoard(t)
			board.labels[taskID] = labels
			s := routingServer(t, board, readyForge(), nil)
			h := s.Handler()

			assign(t, h, taskID)

			if got := pinned(t, taskID); got.workItems != 0 {
				t.Fatalf("a refused item was stored: %+v", got)
			}
			reasons := refusalReasons(t, taskID)
			if len(reasons) != 1 || reasons[0] == "" {
				t.Fatalf("refusal audit rows = %q, want one with a reason", reasons)
			}
			comments := board.commentsOn(taskID)
			if len(comments) != 1 || !strings.Contains(comments[0], "did not queue") {
				t.Fatalf("ticket comments = %q, want one refusal comment", comments)
			}
			if status, _ := postClaim(t, h, `{"team":"bronze"}`); status != http.StatusNoContent {
				t.Errorf("claim after a refusal = %d; nothing may run, least of all in the worker's env repository", status)
			}
		})
	}
}

func TestIngestRefusesATargetThatIsNotReady(t *testing.T) {
	reset(t)
	board := newBoard(t)
	board.labels["5005"] = []string{"repo/homelab-cluster"}
	forge := readyForge()
	forge.set("webgrip/homelab-cluster", provider.RepositoryState{Archived: true, AgentsFile: true})
	h := routingServer(t, board, forge, nil).Handler()

	assign(t, h, "5005")

	if got := pinned(t, "5005"); got.workItems != 0 {
		t.Fatalf("an item for an archived target was stored: %+v", got)
	}
	if reasons := refusalReasons(t, "5005"); len(reasons) != 1 || !strings.Contains(reasons[0], "archived") {
		t.Errorf("refusal reasons = %q, want the archive named", reasons)
	}
}

func TestIngestRefusesLabelRoutingWhenTheTrackerCannotBeRead(t *testing.T) {
	reset(t)
	board := newBoard(t)
	s := routingServer(t, board, readyForge(), nil)
	s.Trackers["vikunja"] = &vikunja.Provider{Secret: testTrackerSecret, DefaultTeam: "bronze", Log: slog.New(slog.DiscardHandler)}
	h := s.Handler()

	assign(t, h, "5006")

	if got := pinned(t, "5006"); got.workItems != 0 {
		t.Fatalf("an item whose labels were unreadable was routed: %+v", got)
	}
	if reasons := refusalReasons(t, "5006"); len(reasons) != 1 || !strings.Contains(reasons[0], "labels could not be read") {
		t.Errorf("refusal reasons = %q", reasons)
	}
}

func finishedRun(t *testing.T, taskID string) (outcome, stuckReason, failureReason, state string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(), `SELECT r.outcome, r.stuck_reason, COALESCE(r.failure_reason, ''), w.state
		FROM agent_runs r JOIN work_items w ON w.id = r.work_item_id
		WHERE w.external_id=$1 AND r.state='finished' ORDER BY r.id DESC LIMIT 1`, taskID).
		Scan(&outcome, &stuckReason, &failureReason, &state); err != nil {
		t.Fatal(err)
	}
	return outcome, stuckReason, failureReason, state
}

func TestClaimStopsARunWhoseTargetBecameUnready(t *testing.T) {
	for name, engine := range map[string]*shiftengine.Engine{
		"pre-shift claim":  nil,
		"shift role claim": {Store: testStore, Log: slog.New(slog.DiscardHandler), Uniform: true},
	} {
		t.Run(name, func(t *testing.T) {
			reset(t)
			board := newBoard(t)
			board.labels["5007"] = []string{"repo/homelab-cluster"}
			forge := readyForge()
			h := routingServer(t, board, forge, engine).Handler()
			assign(t, h, "5007")
			if got := pinned(t, "5007"); got.target != homelabTarget {
				t.Fatalf("pinned %+v", got)
			}

			forge.set("webgrip/homelab-cluster", provider.RepositoryState{})
			if status, _ := postClaim(t, h, `{"team":"bronze"}`); status != http.StatusNoContent {
				t.Fatalf("claim = %d, want no run started", status)
			}

			outcome, stuckReason, _, state := finishedRun(t, "5007")
			if outcome != string(work.OutcomeStuck) || !strings.Contains(stuckReason, "has no AGENTS.md") {
				t.Errorf("run outcome %q reason %q, want stuck with the readiness reason", outcome, stuckReason)
			}
			if state != string(work.StateNeedsHuman) {
				t.Errorf("item state = %q, want needs_human", state)
			}
		})
	}
}

func TestClaimRetriesWhenReadinessCannotBeChecked(t *testing.T) {
	reset(t)
	board := newBoard(t)
	forge := readyForge()
	h := routingServer(t, board, forge, nil).Handler()
	assign(t, h, "5008")

	forge.mu.Lock()
	forge.down = true
	forge.mu.Unlock()
	if status, _ := postClaim(t, h, `{"team":"bronze"}`); status != http.StatusNoContent {
		t.Fatalf("claim = %d, want no run started", status)
	}
	outcome, _, failureReason, state := finishedRun(t, "5008")
	if outcome != string(work.OutcomeFailed) || failureReason != string(work.FailureInfraNode) || state != string(work.StateQueued) {
		t.Errorf("outcome %q failure %q state %q, want a retryable infra failure", outcome, failureReason, state)
	}

	forge.mu.Lock()
	forge.down = false
	forge.mu.Unlock()
	status, claimed := postClaim(t, h, `{"team":"bronze"}`)
	if status != http.StatusOK || claimed.WorkItem.Target == nil || *claimed.WorkItem.Target != glideTarget {
		t.Fatalf("claim after the forge recovered = %d %+v", status, claimed.WorkItem.Target)
	}
}
