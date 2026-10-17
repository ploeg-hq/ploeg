package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const testConfirmation = "confirm-7:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

var attributedHeaders = map[string][]string{
	"X-Ploeg-Actor":        {"alice"},
	"X-Ploeg-Acting-User":  {"admin"},
	"X-Ploeg-Via":          {"mcp:Test Client 1.2.3"},
	"X-Ploeg-Confirmation": {testConfirmation},
}

func attributedRequest(s *Server, method, path, token string, headers map[string][]string, body any) *httptest.ResponseRecorder {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	for name, values := range headers {
		for _, v := range values {
			r.Header.Add(name, v)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func auditAttribution(t *testing.T, workItemID int64, action string) store.Attribution {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT detail->'attribution' FROM audit_log
		WHERE work_item_id = $1 AND action = $2 ORDER BY id DESC LIMIT 1`, workItemID, action).Scan(&raw); err != nil {
		t.Fatalf("%s audit row: %v", action, err)
	}
	var a store.Attribution
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("%s attribution %s: %v", action, raw, err)
	}
	return a
}

func wantFullAttribution(t *testing.T, action string, got store.Attribution) {
	t.Helper()
	want := store.Attribution{Consumer: "workbench", Actor: "alice", ActingUser: "admin", Via: "mcp:Test Client 1.2.3", Confirmation: testConfirmation}
	if got != want {
		t.Fatalf("%s attribution = %+v, want %+v", action, got, want)
	}
}

func eventsByActor(t *testing.T, s *Server, token string, workItemID int64, actor string) []store.OperatorEvent {
	t.Helper()
	var page struct {
		Events []store.OperatorEvent `json:"events"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, fmt.Sprintf("events?workItemId=%d&actor=%s", workItemID, actor)), &page); err != nil {
		t.Fatal(err)
	}
	return page.Events
}

func wantAttributedEvent(t *testing.T, s *Server, token string, workItemID int64, action string) {
	t.Helper()
	for _, person := range []string{"alice", "admin"} {
		events := eventsByActor(t, s, token, workItemID, person)
		found := false
		for _, e := range events {
			attribution, _ := e.Detail["attribution"].(map[string]any)
			if attribution == nil {
				t.Fatalf("actor=%s returned an unattributed event %+v", person, e)
			}
			if e.Action == action && attribution["via"] == "mcp:Test Client 1.2.3" && attribution["confirmation"] == testConfirmation && attribution["consumer"] == "workbench" {
				found = true
			}
		}
		if !found {
			t.Fatalf("actor=%s: no attributed %s event in %+v", person, action, events)
		}
	}
	if events := eventsByActor(t, s, token, workItemID, "someone-else"); len(events) != 0 {
		t.Fatalf("actor=someone-else matched %+v", events)
	}
}

func TestEveryOperatorMutationRecordsOneAttributionShape(t *testing.T) {
	t.Run("submit", func(t *testing.T) {
		f := newAdmissionFixture(t)
		w := attributedRequest(f.server, "POST", admissionPath, f.token, attributedHeaders, admissionBody("req-attributed"))
		if w.Code != http.StatusCreated {
			t.Fatalf("admission: %d %s", w.Code, w.Body)
		}
		var got admissionResult
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		var id int64
		fmt.Sscan(got.WorkItem.ID, &id)
		wantFullAttribution(t, "operator.work_item.admitted", auditAttribution(t, id, "operator.work_item.admitted"))
		wantAttributedEvent(t, f.server, f.token, id, "operator.work_item.admitted")
		var shiftActor string
		if err := testPool.QueryRow(context.Background(), `SELECT actor FROM audit_log WHERE work_item_id=$1 AND action='round.opened' AND NOT detail ? 'attribution'`, id).Scan(&shiftActor); err != nil {
			t.Fatalf("a team-written row in the same request should carry no attribution: %v", err)
		}
	})

	t.Run("approve and reject", func(t *testing.T) {
		reset(t)
		consumers, token := operatorTestConsumers(t, []string{"bronze"}, true)
		s := createdServer(nil)
		s.OperatorConfig.Consumers = consumers
		h := s.Handler()
		source := ingestSource(t, "5701", "bronze")
		c := claimLegacy(t, h, "bronze")
		postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created(2, true)})
		rows := createdRows(t, source)
		if w := attributedRequest(s, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/approve", rows[0].id), token, attributedHeaders, map[string]string{"reason": "fine"}); w.Code != 200 {
			t.Fatalf("approve: %d %s", w.Code, w.Body)
		}
		if w := attributedRequest(s, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/reject", rows[1].id), token, attributedHeaders, map[string]string{"reason": "duplicate"}); w.Code != 200 {
			t.Fatalf("reject: %d %s", w.Code, w.Body)
		}
		wantFullAttribution(t, "work_item.approved", auditAttribution(t, rows[0].id, "work_item.approved"))
		wantFullAttribution(t, "work_item.rejected", auditAttribution(t, rows[1].id, "work_item.rejected"))
		var reason string
		if err := testPool.QueryRow(context.Background(), `SELECT detail->>'reason' FROM audit_log WHERE work_item_id=$1 AND action='work_item.rejected'`, rows[1].id).Scan(&reason); err != nil || reason != "duplicate" {
			t.Fatalf("existing detail keys changed: %q %v", reason, err)
		}
		wantAttributedEvent(t, s, token, rows[1].id, "work_item.rejected")
	})

	t.Run("requeue, cancel and notes", func(t *testing.T) {
		f := newRequeueFixture(t)
		stuck := f.stuckBuilder(t, "5702")
		if w := attributedRequest(f.server, "POST", requeuePath(stuck), f.token, attributedHeaders, map[string]any{"commandId": "retry-1", "fromRound": 1}); w.Code != 200 {
			t.Fatalf("requeue: %d %s", w.Code, w.Body)
		}
		wantFullAttribution(t, store.AuditWorkItemRequeued, auditAttribution(t, stuck, store.AuditWorkItemRequeued))
		wantAttributedEvent(t, f.server, f.token, stuck, store.AuditWorkItemRequeued)

		live := f.item(t, "5703")
		if w := attributedRequest(f.server, "POST", notesPath(live), f.token, attributedHeaders, map[string]any{"commandId": "note-1", "text": "Keep the API."}); w.Code != 201 {
			t.Fatalf("note: %d %s", w.Code, w.Body)
		}
		wantFullAttribution(t, store.AuditOperatorNoteAdded, auditAttribution(t, live, store.AuditOperatorNoteAdded))

		if w := attributedRequest(f.server, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/cancel", live), f.token, attributedHeaders, map[string]any{"commandId": "stop-1"}); w.Code != 200 {
			t.Fatalf("cancel: %d %s", w.Code, w.Body)
		}
		wantFullAttribution(t, "work_item.withdrawn", auditAttribution(t, live, "work_item.withdrawn"))
		wantAttributedEvent(t, f.server, f.token, live, "work_item.withdrawn")
	})

	t.Run("execution command", func(t *testing.T) {
		reset(t)
		consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
		s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
		e := executionFromResponse(t, operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", operatorHTTPInput("attributed-session")))
		path := "/api/v1/operator/executions/" + e.ID + "/commands"
		heartbeat := store.OperatorExecutionCommand{ID: "beat-1", Action: "heartbeat", ExpectedRevision: e.Revision, Generation: e.Generation}
		e = executionFromResponse(t, attributedRequest(s, "POST", path, token, attributedHeaders, heartbeat))
		message := store.OperatorExecutionCommand{ID: "say-1", Action: "message", ExpectedRevision: e.Revision, Generation: e.Generation, Text: "Use the existing helper."}
		executionFromResponse(t, attributedRequest(s, "POST", path, token, attributedHeaders, message))
		var id int64
		fmt.Sscan(e.WorkItemID, &id)
		wantFullAttribution(t, store.AuditOperatorExecutionCommanded, auditAttribution(t, id, store.AuditOperatorExecutionCommanded))
		var commands int
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action=$2`, id, store.AuditOperatorExecutionCommanded).Scan(&commands); err != nil || commands != 1 {
			t.Fatalf("only the person's message is audited, not the heartbeat: %d %v", commands, err)
		}
		wantAttributedEvent(t, s, token, id, store.AuditOperatorExecutionCommanded)
	})
}

func TestAnUnattributedActionRecordsTheConsumerAndActorOnly(t *testing.T) {
	f := newRequeueFixture(t)
	live := f.item(t, "5704")
	if w := operatorExecutionRequest(f.server, "POST", notesPath(live), f.token, "alice", map[string]any{"commandId": "note-plain", "text": "Plain."}); w.Code != 201 {
		t.Fatalf("note: %d %s", w.Code, w.Body)
	}
	if got := auditAttribution(t, live, store.AuditOperatorNoteAdded); got != (store.Attribution{Consumer: "workbench", Actor: "alice"}) {
		t.Fatalf("attribution = %+v", got)
	}
}

func TestAttributionHeadersAreValidatedBeforeAnythingChanges(t *testing.T) {
	f := newRequeueFixture(t)
	live := f.item(t, "5705")
	with := func(name string, values ...string) map[string][]string {
		h := map[string][]string{"X-Ploeg-Actor": {"alice"}}
		h[name] = values
		return h
	}
	for name, tc := range map[string]struct {
		headers map[string][]string
		code    string
	}{
		"a repeated actor":                {map[string][]string{"X-Ploeg-Actor": {"alice", "bob"}}, "actor_required"},
		"a repeated acting user":          {with("X-Ploeg-Acting-User", "admin", "admin"), "invalid_acting_user"},
		"a repeated via":                  {with("X-Ploeg-Via", "browser", "browser"), "invalid_via"},
		"an empty via":                    {with("X-Ploeg-Via", ""), "invalid_via"},
		"a via over 128 characters":       {with("X-Ploeg-Via", "mcp:"+strings.Repeat("x", 125)), "invalid_via"},
		"a via with a leading space":      {with("X-Ploeg-Via", " browser"), "invalid_via"},
		"a via with a non-ASCII rune":     {with("X-Ploeg-Via", "mcp:Cläude"), "invalid_via"},
		"a repeated confirmation":         {with("X-Ploeg-Confirmation", testConfirmation, testConfirmation), "invalid_confirmation"},
		"a confirmation without its id":   {with("X-Ploeg-Confirmation", strings.Repeat("a", 64)), "invalid_confirmation"},
		"a confirmation with upper hex":   {with("X-Ploeg-Confirmation", "c1:"+strings.Repeat("A", 64)), "invalid_confirmation"},
		"a confirmation with a short sum": {with("X-Ploeg-Confirmation", "c1:"+strings.Repeat("a", 63)), "invalid_confirmation"},
		"a via without an actor":          {map[string][]string{"X-Ploeg-Via": {"browser"}}, "actor_required"},
	} {
		w := attributedRequest(f.server, "POST", notesPath(live), f.token, tc.headers, map[string]any{"commandId": "note-" + strings.ReplaceAll(name, " ", "-"), "text": "x"})
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: %d %s, want 400 %s", name, w.Code, w.Body, tc.code)
		}
	}
	if n := countRows(t, `SELECT count(*) FROM operator_notes WHERE work_item_id=$1`, live); n != 0 {
		t.Fatalf("a refused request stored %d notes", n)
	}
	if w := attributedRequest(f.server, "POST", notesPath(live), f.token, with("X-Ploeg-Via", "mcp:Claude Code 2.1.296"), map[string]any{"commandId": "note-ok", "text": "x"}); w.Code != 201 {
		t.Fatalf("a valid via was refused: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "/api/v1/operator/events?actor=not%20valid", nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("an invalid actor filter: %d %s", w.Code, w.Body)
	}
}
