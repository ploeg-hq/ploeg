package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/knowledgegraph"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph/omnigraphtest"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func workItemNotesServer(t *testing.T, graph *omnigraphtest.Notes) *Server {
	t.Helper()
	return &Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		Notes: &knowledgegraph.Notes{
			Client: &omnigraph.Client{BaseURL: graph.URL, Token: graph.Token},
			Graph:  graph.Graph,
		},
	}
}

func runKnowledgeGET(t *testing.T, h http.Handler, token, query string) (*httptest.ResponseRecorder, harness.KnowledgeResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+token+"/knowledge"+query, nil))
	var resp harness.KnowledgeResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode knowledge: %v", err)
		}
	}
	return rec, resp
}

func reportLearnings(t *testing.T, h http.Handler, token string, learnings ...harness.Learning) {
	t.Helper()
	rec := postOutcome(t, h, token, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "done", Learnings: learnings})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d %s", rec.Code, rec.Body)
	}
}

func setRoot(t *testing.T, child, root int64) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET root_work_item_id = $2 WHERE id = $1`, child, root); err != nil {
		t.Fatal(err)
	}
}

func dueNow(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE work_item_note_outbox SET next_attempt_at = now(), claimed_until = NULL`); err != nil {
		t.Fatal(err)
	}
}

var pitfall = harness.Learning{Type: "Pitfall", Title: "CSP forbids inline style", Body: "Use SVG attributes.", Tags: []string{"csp"}}

// A Run's learning becomes a note that the next Run of the same root reads,
// and that no Run under another root ever sees (ADR-0088, proposed).
func TestWorkItemNote_ReachesLaterRunsOfTheSameRootOnly(t *testing.T) {
	reset(t)
	graph := omnigraphtest.NewNotes(t)
	s := workItemNotesServer(t, graph)
	h := s.Handler()

	root := ingestFor(t, "9300", "bronze")
	first := claimLegacy(t, h, "bronze")
	reportLearnings(t, h, first.RunToken, pitfall, harness.Learning{Type: "Fact", Title: "no body"})
	s.SweepNoteWrites(context.Background())
	if keys := graph.Keys(); len(keys) != 1 {
		t.Fatalf("graph holds %v, want the one complete learning", keys)
	}

	child := ingestFor(t, "9301", "bronze")
	setRoot(t, child, root)
	second := claimLegacy(t, h, "bronze")
	rec, resp := runKnowledgeGET(t, h, second.RunToken, "")
	if rec.Code != 200 || resp.Notes.Status != harness.KnowledgeOK || len(resp.Notes.Items) != 1 {
		t.Fatalf("child Run read %d %+v", rec.Code, resp.Notes)
	}
	note := resp.Notes.Items[0]
	if note.Title != pitfall.Title || note.Body != pitfall.Body || note.SuggestedScope != "work-item" ||
		!strings.HasPrefix(note.WrittenBy, "run:ploeg-") || resp.Notes.Commit == "" {
		t.Errorf("note %+v commit %q", note, resp.Notes.Commit)
	}

	ingestFor(t, "9302", "silver")
	other := claimLegacy(t, h, "silver")
	if _, resp := runKnowledgeGET(t, h, other.RunToken, ""); resp.Notes.Status != harness.KnowledgeOK || len(resp.Notes.Items) != 0 {
		t.Errorf("a Run under another root read %+v", resp.Notes.Items)
	}
}

// The scope is the Run's own record; nothing in the request widens it.
func TestKnowledgeRead_ScopeComesFromTheWorkItemNotTheRequest(t *testing.T) {
	reset(t)
	graph := omnigraphtest.NewNotes(t)
	s := workItemNotesServer(t, graph)
	h := s.Handler()

	writer := ingestFor(t, "9310", "bronze")
	reportLearnings(t, h, claimLegacy(t, h, "bronze").RunToken, pitfall)
	s.SweepNoteWrites(context.Background())

	ingestFor(t, "9311", "bronze")
	reader := claimLegacy(t, h, "bronze")
	for _, q := range []string{"", "?root=" + itoa(writer), "?root_work_item=" + itoa(writer) + "&scope=tenant"} {
		if _, resp := runKnowledgeGET(t, h, reader.RunToken, q); len(resp.Notes.Items) != 0 {
			t.Errorf("query %q reached another root's notes: %+v", q, resp.Notes.Items)
		}
	}
	if rec, _ := runKnowledgeGET(t, h, strings.Repeat("0", 48), ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown Run read knowledge: %d", rec.Code)
	}
}

// A graph that does not answer leaves the Run's knowledge unavailable, never
// an error that stops it, and keeps the learnings for a later write.
func TestKnowledgeRead_UnavailableGraphDoesNotStopTheRun(t *testing.T) {
	reset(t)
	graph := omnigraphtest.NewNotes(t)
	graph.Down = true
	s := workItemNotesServer(t, graph)
	h := s.Handler()

	ingestFor(t, "9320", "bronze")
	run := claimLegacy(t, h, "bronze")
	rec, resp := runKnowledgeGET(t, h, run.RunToken, "")
	if rec.Code != http.StatusOK || resp.Notes.Status != harness.KnowledgeUnavailable || resp.Notes.Items == nil {
		t.Fatalf("knowledge with the graph down: %d %+v", rec.Code, resp.Notes)
	}
	reportLearnings(t, h, run.RunToken, pitfall)
	s.SweepNoteWrites(context.Background())
	graph.Down = false
	dueNow(t)
	s.SweepNoteWrites(context.Background())
	if keys := graph.Keys(); len(keys) != 1 {
		t.Errorf("after the graph came back it holds %v", keys)
	}
}

// Without a notes graph the route says off and the outcome queues nothing.
func TestKnowledgeRead_OffWithoutANotesGraph(t *testing.T) {
	reset(t)
	h := apiServer(t, nil)
	ingestFor(t, "9330", "bronze")
	run := claimLegacy(t, h, "bronze")
	if _, resp := runKnowledgeGET(t, h, run.RunToken, ""); resp.Notes.Status != harness.KnowledgeOff {
		t.Errorf("status %q", resp.Notes.Status)
	}
	reportLearnings(t, h, run.RunToken, pitfall)
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM work_item_note_outbox`).Scan(&n); err != nil || n != 0 {
		t.Errorf("queued %d notes without a graph (%v)", n, err)
	}
}

// A write whose answer was lost is read back, not repeated, and a ploegd
// that stopped before writing writes the note once when it is back.
func TestNoteOutbox_WritesOnceAcrossARestart(t *testing.T) {
	reset(t)
	graph := omnigraphtest.NewNotes(t)
	s := workItemNotesServer(t, graph)
	h := s.Handler()

	ingestFor(t, "9340", "bronze")
	run := claimLegacy(t, h, "bronze")
	reportLearnings(t, h, run.RunToken, pitfall, harness.Learning{Type: "Fact", Title: "second", Body: "b", Scope: "repository"})
	rec := postOutcome(t, h, run.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "done", Learnings: []harness.Learning{pitfall}})
	if rec.Code >= 500 {
		t.Fatalf("replayed outcome: %d", rec.Code)
	}

	claimed, err := testStore.ClaimNoteWrites(context.Background(), 10, time.Minute)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claimed %d (%v)", len(claimed), err)
	}
	s.SweepNoteWrites(context.Background())
	if graph.Calls["add_note"] != 0 {
		t.Fatalf("a held note was written by a second sweep")
	}

	graph.LoseWriteAnswers = 1
	restarted := workItemNotesServer(t, graph)
	dueNow(t)
	restarted.SweepNoteWrites(context.Background())
	dueNow(t)
	restarted.SweepNoteWrites(context.Background())
	restarted.SweepNoteWrites(context.Background())

	if keys := graph.Keys(); len(keys) != 2 {
		t.Fatalf("graph holds %v", keys)
	}
	if graph.Calls["add_note"] != 2 {
		t.Errorf("add_note called %d times, want each note written once", graph.Calls["add_note"])
	}
	var unwritten int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM work_item_note_outbox WHERE written_at IS NULL`).Scan(&unwritten); err != nil || unwritten != 0 {
		t.Errorf("%d notes left unwritten (%v)", unwritten, err)
	}
	for _, key := range graph.Keys() {
		if graph.Note(key)["suggested_scope"] == "repository" {
			return
		}
	}
	t.Errorf("the suggested repository scope was not kept")
}

// No graph credential ever leaves ploegd: not in the claim, not in the
// knowledge the Run reads (ADR-0087, proposed).
func TestWorkerAndHarnessNeverHoldAGraphToken(t *testing.T) {
	reset(t)
	graph := omnigraphtest.NewNotes(t)
	s := workItemNotesServer(t, graph)
	h := s.Handler()
	ingestFor(t, "9350", "bronze")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(`{"team":"bronze"}`)))
	var claim claimResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &claim); err != nil {
		t.Fatal(err)
	}
	knowledge, _ := runKnowledgeGET(t, h, claim.RunToken, "")
	for name, body := range map[string]string{"claim": rec.Body.String(), "knowledge": knowledge.Body.String()} {
		if strings.Contains(body, graph.Token) || strings.Contains(body, graph.URL) {
			t.Errorf("the %s response carries the graph's credential or address", name)
		}
	}
}

func TestKnowledgeBodiesMatchTheRunAPIContract(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/run-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/knowledgeResponse")
	if err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	for name, body := range map[string]harness.KnowledgeResponse{
		"off":         {Notes: harness.KnowledgeNotes{Status: harness.KnowledgeOff, Items: []harness.WorkItemNote{}}},
		"unavailable": {Notes: harness.KnowledgeNotes{Status: harness.KnowledgeUnavailable, Items: []harness.WorkItemNote{}}},
		"concepts": {Notes: harness.KnowledgeNotes{Status: harness.KnowledgeOff, Items: []harness.WorkItemNote{}},
			Concepts: &harness.KnowledgeConcepts{Status: harness.KnowledgeOK, Commit: "01M5", Items: []harness.KnowledgeConcept{
				{Scope: "repository", Path: "pitfalls/csp.md", Document: "---\ntype: Pitfall\n---\nbody\n"}}}},
		"notes": {Notes: harness.KnowledgeNotes{Status: harness.KnowledgeOK, Commit: "01M4", Items: []harness.WorkItemNote{{
			Key: "ploeg-abc/0", WorkItem: "43", Repository: "forgejo/acme/shop", Type: "Pitfall", Title: "t",
			Tags: []string{"csp"}, Body: "b", SuggestedScope: "repository", WrittenBy: "run:ploeg-abc", WrittenAt: written,
		}}}},
	} {
		b, _ := json.Marshal(body)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s violates the contract: %v", name, err)
		}
	}
}

func doc(title string) string {
	return "---\ntype: Fact\ntitle: " + title + "\n---\n" + title + " body.\n"
}

func setTarget(t *testing.T, id int64, forge, owner, repo string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET target_forge = $2, target_owner = $3, target_repo = $4 WHERE id = $1`, id, forge, owner, repo); err != nil {
		t.Fatal(err)
	}
}

// A Run reads the Tenant's concepts and its own repository's, never another
// repository's, nor a Client's while Ploeg records no Client for its Work
// Item (ADR-0087, proposed).
func TestKnowledgeRead_ServesOnlyTheRunsScopes(t *testing.T) {
	reset(t)
	graph := omnigraphtest.NewKnowledge(t)
	graph.Put("tenant/conventions/commits.md", "tenant", "", "", doc("Commits"))
	graph.Put("repository/forgejo/acme/shop/pitfalls/csp.md", "repository", "", "forgejo/acme/shop", doc("CSP"))
	graph.Put("repository/forgejo/acme/other/pitfalls/secret.md", "repository", "", "forgejo/acme/other", doc("Other"))
	graph.Put("client/globex/facts/sla.md", "client", "globex", "", doc("SLA"))
	s := workItemNotesServer(t, omnigraphtest.NewNotes(t))
	s.Knowledge = &knowledgegraph.Knowledge{Client: &omnigraph.Client{BaseURL: graph.URL, Token: graph.Token}, Graph: graph.Graph}
	h := s.Handler()

	id := ingestFor(t, "9400", "bronze")
	setTarget(t, id, "forgejo", "acme", "shop")
	run := claimLegacy(t, h, "bronze")
	rec, resp := runKnowledgeGET(t, h, run.RunToken, "?repository=forgejo/acme/other&client=globex")
	if rec.Code != 200 || resp.Concepts == nil || resp.Concepts.Status != harness.KnowledgeOK {
		t.Fatalf("knowledge: %d %+v", rec.Code, resp.Concepts)
	}
	got := map[string]string{}
	for _, c := range resp.Concepts.Items {
		got[c.Scope+":"+c.Path] = c.Document
	}
	if len(got) != 2 || got["tenant:conventions/commits.md"] == "" || got["repository:pitfalls/csp.md"] != doc("CSP") {
		t.Errorf("served %v", got)
	}
	if resp.Concepts.Commit == "" {
		t.Error("no commit recorded")
	}

	graph.Down = true
	if rec, resp := runKnowledgeGET(t, h, run.RunToken, ""); rec.Code != 200 || resp.Concepts.Status != harness.KnowledgeUnavailable || resp.Notes.Status != harness.KnowledgeOK {
		t.Errorf("with the knowledge graph down: %d %+v %+v", rec.Code, resp.Concepts, resp.Notes)
	}
}

// Concepts are read at one commit: a write between the reads makes ploegd
// read them all again.
func TestKnowledgeRead_ConceptsComeFromOneCommit(t *testing.T) {
	graph := omnigraphtest.NewKnowledge(t)
	graph.Put("tenant/a.md", "tenant", "", "", doc("A"))
	graph.BumpBetweenReads = 1
	k := &knowledgegraph.Knowledge{Client: &omnigraph.Client{BaseURL: graph.URL, Token: graph.Token}, Graph: graph.Graph}
	items, commit, err := k.Concepts(context.Background(), "", "forgejo/acme/shop")
	if err != nil || len(items) != 1 || commit != "commit-2" {
		t.Errorf("items %+v commit %q err %v", items, commit, err)
	}
}
