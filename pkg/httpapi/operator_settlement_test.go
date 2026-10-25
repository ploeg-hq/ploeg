package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type settleCall struct {
	path, body, actor string
	execute           bool
	teams             []string
}

func settleFromEvidence(t *testing.T, call settleCall) (int, map[string]any) {
	t.Helper()
	consumers, token := operatorTestConsumers(t, call.teams, call.execute)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	r := httptest.NewRequest("POST", call.path, strings.NewReader(call.body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if call.actor != "" {
		r.Header.Set("X-Ploeg-Actor", call.actor)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %d %s", w.Code, w.Body)
	}
	return w.Code, body
}

func settlementErrorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestOperatorSettleFromEvidenceEndpoint(t *testing.T) {
	reset(t)
	ctx := context.Background()
	token, runID := seedOperatorUnsettled(t, "801", "silver", "unknown", true, 2, 3*time.Hour)
	if _, err := testPool.Exec(ctx, `UPDATE run_llm_accounts SET observed_spend=0.3 WHERE run_token=$1`, token); err != nil {
		t.Fatal(err)
	}
	_, goldRun := seedOperatorUnsettled(t, "802", "gold", "unknown", true, 1, 3*time.Hour)

	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	settlePath := fmt.Sprintf("/api/v1/operator/unsettled-accounts/%d/settle", runID)
	valid := `{"commandId":"invoice-2026-10","amountUsd":0.42,"evidence":"provider invoice 2026-10 line 14"}`
	silver := []string{"silver"}

	refusals := []struct {
		name   string
		call   settleCall
		status int
		code   string
	}{
		{"read-only consumer", settleCall{settlePath, valid, "ryan", false, silver}, 403, "execution_forbidden"},
		{"no actor", settleCall{settlePath, valid, "", true, silver}, 400, "actor_required"},
		{"no commandId", settleCall{settlePath, `{"amountUsd":0.42,"evidence":"x"}`, "ryan", true, silver}, 400, "command_required"},
		{"no amount", settleCall{settlePath, `{"commandId":"c","evidence":"x"}`, "ryan", true, silver}, 400, "invalid_settlement"},
		{"negative amount", settleCall{settlePath, `{"commandId":"c","amountUsd":-1,"evidence":"x"}`, "ryan", true, silver}, 400, "invalid_settlement"},
		{"unknown field", settleCall{settlePath, `{"commandId":"c","amountUsd":1,"evidence":"x","force":true}`, "ryan", true, silver}, 400, "invalid_command"},
		{"below observation", settleCall{settlePath, `{"commandId":"low","amountUsd":0.29,"evidence":"x"}`, "ryan", true, silver}, 409, "below_observation"},
		{"another team", settleCall{fmt.Sprintf("/api/v1/operator/unsettled-accounts/%d/settle", goldRun), valid, "ryan", true, silver}, 404, "not_found"},
	}
	for _, c := range refusals {
		status, body := settleFromEvidence(t, c.call)
		if status != c.status || settlementErrorCode(body) != c.code {
			t.Fatalf("%s: %d %v, want %d %s", c.name, status, body, c.status, c.code)
		}
	}

	validate := func(body map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("settlement violates published schema: %v\n%s", err, raw)
		}
	}
	status, body := settleFromEvidence(t, settleCall{settlePath, valid, "ryan", true, silver})
	if status != 201 {
		t.Fatalf("settle: %d %v", status, body)
	}
	validate(body)
	settlement := body["settlement"].(map[string]any)
	if settlement["runId"] != fmt.Sprint(runID) || settlement["settledUsd"] != 0.42 || settlement["observedUsd"] != 0.3 ||
		settlement["releasedUsd"] != 2.0 || settlement["previousState"] != "unknown" || settlement["replayed"] != false {
		t.Fatalf("settlement: %v", settlement)
	}

	status, body = settleFromEvidence(t, settleCall{settlePath, valid, "ryan", true, silver})
	if status != 200 || body["settlement"].(map[string]any)["replayed"] != true {
		t.Fatalf("replay: %d %v", status, body)
	}
	validate(body)
	for name, call := range map[string]settleCall{
		"conflicting replay": {settlePath, `{"commandId":"invoice-2026-10","amountUsd":0.5,"evidence":"provider invoice 2026-10 line 14"}`, "ryan", true, silver},
		"second command":     {settlePath, `{"commandId":"again","amountUsd":0.5,"evidence":"x"}`, "ryan", true, silver},
	} {
		status, body := settleFromEvidence(t, call)
		want := map[string]string{"conflicting replay": "command_conflict", "second command": "already_settled"}[name]
		if status != 409 || settlementErrorCode(body) != want {
			t.Fatalf("%s: %d %v, want 409 %s", name, status, body, want)
		}
	}

	var actor, attributedActor string
	var events int
	if err := testPool.QueryRow(ctx, `SELECT min(actor),min(detail->'attribution'->>'actor'),count(*) FROM audit_log
		WHERE action='llm.reconciled' AND (detail->>'run')::bigint=$1`, runID).Scan(&actor, &attributedActor, &events); err != nil {
		t.Fatal(err)
	}
	if events != 1 || actor != "operator:workbench:ryan" || attributedActor != "ryan" {
		t.Fatalf("audit: %d events, actor %q, attributed %q", events, actor, attributedActor)
	}
}
