package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func knowledgeAPI(t *testing.T, status int, body harness.KnowledgeResponse) (*APIClient, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runs/run-1/knowledge" {
			http.NotFound(w, r)
			return
		}
		calls++
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return &APIClient{Base: srv.URL, HC: srv.Client()}, &calls
}

func noteResponse(notes ...harness.WorkItemNote) harness.KnowledgeResponse {
	return harness.KnowledgeResponse{Notes: harness.KnowledgeNotes{Status: harness.KnowledgeOK, Commit: "commit-7", Items: notes}}
}

// Notes of the same work reach the pack whatever the Work Item's words, under
// their own heading after reviewed knowledge, with the commit they were read at.
func TestWorkItemNotes_BriefTheRunUnderTheirOwnHeading(t *testing.T) {
	note := harness.WorkItemNote{Key: "ploeg-abc/0", Type: "Pitfall", Title: "CSP forbids inline style", Body: "Use SVG attributes.",
		WrittenBy: "run:ploeg-abc", WrittenAt: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)}
	api, _ := knowledgeAPI(t, http.StatusOK, noteResponse(note))
	w := &Worker{Cfg: Config{}, Log: discardLog(), API: api}

	notes := w.workItemNotes(context.Background(), "run-1")
	if !notes.on || notes.source == nil || notes.provenance.Commit != "commit-7" {
		t.Fatalf("notes %+v", notes)
	}
	dir := t.TempDir()
	brief := w.knowledgePack(t.TempDir(), work.WorkItem{Title: "Translate the login page"}, dir, notes)
	if brief == nil || len(brief.Concepts) != 1 || !strings.HasPrefix(brief.Concepts[0], "notes/") {
		t.Fatalf("brief %+v", brief)
	}
	if !strings.Contains(brief.Index, "## Notes from earlier work on this Work Item") || !strings.Contains(brief.Index, "without review") {
		t.Errorf("index does not set the notes apart:\n%s", brief.Index)
	}
	if got := brief.Sources[len(brief.Sources)-1]; got.Name != notesSource || got.Commit != "commit-7" {
		t.Errorf("provenance %+v", got)
	}
	doc, err := os.ReadFile(filepath.Join(dir, brief.Concepts[0]))
	if err != nil || !strings.Contains(string(doc), "status: unreviewed") || !strings.Contains(string(doc), "Use SVG attributes.") {
		t.Errorf("note document %q (%v)", doc, err)
	}
}

// An unreachable graph never stops a Run: the Task Spec records it as
// unavailable and the Run is still invited to write notes.
func TestKnowledgeRead_UnavailableGraphDoesNotStopTheWorker(t *testing.T) {
	api, _ := knowledgeAPI(t, http.StatusOK, harness.KnowledgeResponse{Notes: harness.KnowledgeNotes{Status: harness.KnowledgeUnavailable, Items: []harness.WorkItemNote{}}})
	w := &Worker{Cfg: Config{}, Log: discardLog(), API: api}
	notes := w.workItemNotes(context.Background(), "run-1")
	brief := w.knowledgePack(t.TempDir(), work.WorkItem{Title: "x"}, t.TempDir(), notes)
	if !notes.on || brief == nil || len(brief.Concepts) != 0 || brief.Sources[0].Status != harness.KnowledgeUnavailable {
		t.Fatalf("notes %+v brief %+v", notes, brief)
	}
	var prompt strings.Builder
	writeKnowledge(&prompt, harness.TaskSpec{Knowledge: brief})
	if prompt.Len() != 0 {
		t.Errorf("an empty pack reached the prompt: %q", prompt.String())
	}
}

// A ploegd without the route, or one that writes no notes, leaves the Run as
// it was before notes existed.
func TestWorkItemNotes_OffOrOlderPloegdChangesNothing(t *testing.T) {
	for name, status := range map[string]int{"older ploegd": http.StatusNotFound, "off": http.StatusOK} {
		api, calls := knowledgeAPI(t, status, harness.KnowledgeResponse{Notes: harness.KnowledgeNotes{Status: harness.KnowledgeOff, Items: []harness.WorkItemNote{}}})
		w := &Worker{Cfg: Config{}, Log: discardLog(), API: api}
		notes := w.workItemNotes(context.Background(), "run-1")
		if notes.on || notes.provenance != nil || *calls != 1 {
			t.Errorf("%s: notes %+v after %d calls", name, notes, *calls)
		}
	}
}

func TestLearningsInvitation_SaysNotesReachLaterRunsAtOnce(t *testing.T) {
	var on, off strings.Builder
	writeLearningsInvitation(&on, true)
	writeLearningsInvitation(&off, false)
	if !strings.Contains(on.String(), "without review") || !strings.Contains(on.String(), `"scope"`) || strings.Contains(on.String(), "A person reviews every entry") {
		t.Errorf("notes invitation:\n%s", on.String())
	}
	if !strings.Contains(off.String(), "A person reviews every entry") {
		t.Errorf("outbox invitation changed:\n%s", off.String())
	}
}
