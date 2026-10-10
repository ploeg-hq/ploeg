package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func notesServer(t *testing.T) (*Server, string) {
	t.Helper()
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"bronze", "silver"}, true)
	return &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, OperatorConfig: OperatorConfig{Consumers: consumers}}, token
}

func notesPath(id int64) string {
	return fmt.Sprintf("/api/v1/operator/work-items/%d/notes", id)
}

func shiftOf(t *testing.T, id int64) int64 {
	t.Helper()
	var shift int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM shifts WHERE work_item_id=$1 AND closed_at IS NULL`, id).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	return shift
}

func shiftFixtureItem(t *testing.T, externalID string, roles []store.Role) (int64, int64) {
	t.Helper()
	shift := shiftFixture(t, externalID, 0, roles)
	id, _, err := testStore.TrackerWorkItemID(context.Background(), "vikunja", externalID)
	if err != nil {
		t.Fatal(err)
	}
	return id, shift
}

type noteResult struct {
	ID         string  `json:"id"`
	WorkItemID string  `json:"workItemId"`
	CommandID  string  `json:"commandId"`
	ShiftID    *string `json:"shiftId"`
	Consumed   bool    `json:"consumed"`
	Replayed   bool    `json:"replayed"`
}

func decodeNote(t *testing.T, w *httptest.ResponseRecorder) noteResult {
	t.Helper()
	validOperatorResponse(t, w)
	var body struct {
		Note noteResult `json:"note"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Note
}

func operatorNotes(findings []harness.Finding) []harness.Finding {
	var out []harness.Finding
	for _, f := range findings {
		if f.Role == "operator note" {
			out = append(out, f)
		}
	}
	return out
}

func TestOperatorNoteIsIdempotentPerCommand(t *testing.T) {
	s, token := notesServer(t)
	id, shift := shiftFixtureItem(t, "5101", []store.Role{{Name: "reviewer"}})
	body := map[string]any{"commandId": "note-1", "text": "  Keep the public API unchanged.  ", "expectedShiftId": strconv.FormatInt(shift, 10)}
	w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", body)
	if w.Code != 201 {
		t.Fatalf("note: %d %s", w.Code, w.Body)
	}
	first := decodeNote(t, w)
	if first.Replayed || first.Consumed || first.ShiftID == nil || *first.ShiftID != strconv.FormatInt(shift, 10) || first.CommandID != "note-1" {
		t.Fatalf("note: %+v", first)
	}
	w = operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", body)
	if w.Code != 200 {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	if replay := decodeNote(t, w); !replay.Replayed || replay.ID != first.ID {
		t.Fatalf("replay: %+v", replay)
	}
	body["text"] = "Something else entirely."
	if w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", body); w.Code != 409 || errorCode(t, w) != "command_conflict" {
		t.Fatalf("reused commandId: %d %s", w.Code, w.Body)
	}
	if n := countRows(t, `SELECT count(*) FROM operator_notes WHERE work_item_id=$1`, id); n != 1 {
		t.Fatalf("notes stored = %d", n)
	}
	var text, actor string
	if err := testPool.QueryRow(context.Background(), `SELECT text, actor FROM operator_notes WHERE work_item_id=$1`, id).Scan(&text, &actor); err != nil || text != "Keep the public API unchanged." || actor != "operator:workbench:alice" {
		t.Fatalf("stored note %q by %q: %v", text, actor, err)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='operator.note.added'`, id); n != 1 {
		t.Fatalf("operator.note.added events = %d", n)
	}
}

func TestOperatorNoteRefusesAStaleShiftSettledWorkAndBadInput(t *testing.T) {
	s, token := notesServer(t)
	id, shift := shiftFixtureItem(t, "5201", []store.Role{{Name: "reviewer"}})
	stale := map[string]any{"commandId": "stale", "text": "Use the old plan.", "expectedShiftId": strconv.FormatInt(shift+1000, 10)}
	if w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", stale); w.Code != 409 || errorCode(t, w) != "shift_changed" {
		t.Fatalf("stale shift: %d %s", w.Code, w.Body)
	}
	for name, body := range map[string]map[string]any{
		"no command":  {"text": "x"},
		"empty text":  {"commandId": "empty", "text": "   "},
		"long text":   {"commandId": "long", "text": strings.Repeat("é", store.MaxOperatorNoteRunes+1)},
		"bad shift":   {"commandId": "badshift", "text": "x", "expectedShiftId": "01"},
		"extra field": {"commandId": "extra", "text": "x", "priority": "high"},
	} {
		if w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", body); w.Code != 400 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", map[string]any{"commandId": "max", "text": strings.Repeat("é", store.MaxOperatorNoteRunes)}); w.Code != 201 {
		t.Fatalf("a note at the bound: %d %s", w.Code, w.Body)
	}
	if _, err := testStore.WithdrawWorkItem(context.Background(), id, nil, "test", store.CloseReasonWithdrawnByOperator); err != nil {
		t.Fatal(err)
	}
	if w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", map[string]any{"commandId": "late", "text": "Too late."}); w.Code != 409 || errorCode(t, w) != "work_item_terminal" {
		t.Fatalf("withdrawn item: %d %s", w.Code, w.Body)
	}
	other, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "5202", Team: "gold", Title: "outside scope"})
	if err != nil {
		t.Fatal(err)
	}
	if w := operatorExecutionRequest(s, "POST", notesPath(other), token, "alice", map[string]any{"commandId": "foreign", "text": "x"}); w.Code != 404 {
		t.Fatalf("cross-team note: %d %s", w.Code, w.Body)
	}
	e, _, err := testStore.AdmitOperatorExecution(context.Background(), "workbench", "alice", operatorHTTPInput("note-owned"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/work-items/"+e.WorkItemID+"/notes", token, "alice", map[string]any{"commandId": "owned", "text": "x"}); w.Code != 409 || errorCode(t, w) != "operator_owned" {
		t.Fatalf("operator-owned note: %d %s", w.Code, w.Body)
	}
	if n := countRows(t, `SELECT count(*) FROM operator_notes WHERE command_id <> 'max'`); n != 0 {
		t.Fatalf("refused notes stored = %d", n)
	}
}

func TestOperatorNoteIsInjectedIntoTheNextClaimedRunExactlyOnce(t *testing.T) {
	s, token := notesServer(t)
	id, _ := shiftFixtureItem(t, "5301", []store.Role{{Name: "reviewer", Cap: 1}, {Name: "security", Cap: 1}, {Name: "docs", Cap: 1}})
	for i, text := range []string{"First instruction.", "Second instruction."} {
		if w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", map[string]any{"commandId": fmt.Sprintf("n%d", i), "text": text}); w.Code != 201 {
			t.Fatalf("note %d: %d %s", i, w.Code, w.Body)
		}
	}
	h := s.Handler()
	status, first := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	if status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	notes := operatorNotes(first.Briefing)
	if len(notes) != 2 || !strings.Contains(notes[0].Findings, "First instruction.") || !strings.Contains(notes[1].Findings, "Second instruction.") || !strings.Contains(notes[0].Findings, "operator:workbench:alice") {
		t.Fatalf("first claim briefing: %+v", first.Briefing)
	}
	if n := countRows(t, `SELECT count(*) FROM operator_notes WHERE work_item_id=$1 AND consumed_at IS NOT NULL AND consumed_by_run=(SELECT id FROM agent_runs WHERE run_token=$2)`, id, first.RunToken); n != 2 {
		t.Fatalf("notes consumed by the first Run = %d", n)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='operator.note.consumed'`, id); n != 2 {
		t.Fatalf("operator.note.consumed events = %d", n)
	}
	status, second := postClaim(t, h, `{"team":"bronze","role":"security"}`)
	if status != http.StatusOK {
		t.Fatalf("second claim: %d", status)
	}
	if notes := operatorNotes(second.Briefing); len(notes) != 0 {
		t.Fatalf("a consumed note reached a second Run: %+v", notes)
	}
	if w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", map[string]any{"commandId": "n2", "text": "Third instruction."}); w.Code != 201 {
		t.Fatalf("third note: %d %s", w.Code, w.Body)
	}
	status, third := postClaim(t, h, `{"team":"bronze","role":"docs"}`)
	if status != http.StatusOK {
		t.Fatalf("third claim: %d", status)
	}
	if notes := operatorNotes(third.Briefing); len(notes) != 1 || !strings.Contains(notes[0].Findings, "Third instruction.") {
		t.Fatalf("third claim briefing: %+v", third.Briefing)
	}
	w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", map[string]any{"commandId": "n0", "text": "First instruction."})
	if replay := decodeNote(t, w); w.Code != 200 || !replay.Consumed {
		t.Fatalf("replay of a consumed note: %d %s", w.Code, w.Body)
	}
}

func TestOperatorNoteReachesAPreShiftClaim(t *testing.T) {
	s, token := notesServer(t)
	id, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "5401", Team: "bronze", Title: "pre-shift"})
	if err != nil {
		t.Fatal(err)
	}
	w := operatorExecutionRequest(s, "POST", notesPath(id), token, "alice", map[string]any{"commandId": "pre", "text": "Mind the migration."})
	if w.Code != 201 {
		t.Fatalf("note: %d %s", w.Code, w.Body)
	}
	if note := decodeNote(t, w); note.ShiftID != nil {
		t.Fatalf("a note without a Shift reported one: %+v", note)
	}
	status, claim := postClaim(t, s.Handler(), `{"team":"bronze"}`)
	if status != http.StatusOK {
		t.Fatalf("claim: %d", status)
	}
	if notes := operatorNotes(claim.Briefing); len(notes) != 1 || !strings.Contains(notes[0].Findings, "Mind the migration.") {
		t.Fatalf("pre-shift claim briefing: %+v", claim.Briefing)
	}
}

func cancelPath(id int64) string {
	return fmt.Sprintf("/api/v1/operator/work-items/%d/cancel", id)
}

type cancelResult struct {
	State     string  `json:"state"`
	Withdrawn bool    `json:"withdrawn"`
	ShiftID   *string `json:"shiftId"`
	CommandID *string `json:"commandId"`
	Replayed  bool    `json:"replayed"`
}

func decodeCancel(t *testing.T, w *httptest.ResponseRecorder) cancelResult {
	t.Helper()
	validOperatorResponse(t, w)
	var body struct {
		Cancellation cancelResult `json:"cancellation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Cancellation
}

func TestOperatorCancelRefusesAStaleShift(t *testing.T) {
	s, token := notesServer(t)
	id, shift := shiftFixtureItem(t, "5501", []store.Role{{Name: "reviewer"}})
	stale := map[string]any{"commandId": "cancel-stale", "expectedShiftId": strconv.FormatInt(shift+1000, 10)}
	if w := operatorExecutionRequest(s, "POST", cancelPath(id), token, "alice", stale); w.Code != 409 || errorCode(t, w) != "shift_changed" {
		t.Fatalf("stale cancel: %d %s", w.Code, w.Body)
	}
	if got := snapshotItem(t, id); got.state != "queued" || got.liveShifts != 1 {
		t.Fatalf("a stale cancel withdrew work: %+v", got)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='work_item.withdrawn'`, id); n != 0 {
		t.Fatalf("a stale cancel was audited as a withdrawal: %d", n)
	}
}

func TestOperatorCancelWithAMatchingShiftWithdrawsOnceAndReplays(t *testing.T) {
	s, token := notesServer(t)
	id, shift := shiftFixtureItem(t, "5601", []store.Role{{Name: "reviewer"}})
	guarded := map[string]any{"commandId": "cancel-1", "expectedShiftId": strconv.FormatInt(shiftOf(t, id), 10)}
	w := operatorExecutionRequest(s, "POST", cancelPath(id), token, "alice", guarded)
	if w.Code != 200 {
		t.Fatalf("guarded cancel: %d %s", w.Code, w.Body)
	}
	c := decodeCancel(t, w)
	if !c.Withdrawn || c.Replayed || c.State != "withdrawn" || c.ShiftID == nil || *c.ShiftID != strconv.FormatInt(shift, 10) || c.CommandID == nil || *c.CommandID != "cancel-1" {
		t.Fatalf("guarded cancel: %+v", c)
	}
	w = operatorExecutionRequest(s, "POST", cancelPath(id), token, "alice", guarded)
	if w.Code != 200 {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	if r := decodeCancel(t, w); r.Withdrawn || !r.Replayed || r.ShiftID == nil || *r.ShiftID != strconv.FormatInt(shift, 10) {
		t.Fatalf("replay: %+v", r)
	}
	if n := countRows(t, `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='work_item.withdrawn' AND detail->>'command_id'='cancel-1'`, id); n != 1 {
		t.Fatalf("withdrawals audited = %d", n)
	}
	if w := operatorExecutionRequest(s, "POST", cancelPath(id), token, "alice", map[string]any{"expectedShiftId": "1"}); w.Code != 400 {
		t.Fatalf("a body without commandId: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", cancelPath(id), token, "alice", nil); w.Code != 200 {
		t.Fatalf("an unguarded cancel: %d %s", w.Code, w.Body)
	} else if r := decodeCancel(t, w); r.Withdrawn || r.Replayed || r.CommandID != nil {
		t.Fatalf("an unguarded cancel of a withdrawn item: %+v", r)
	}
}
