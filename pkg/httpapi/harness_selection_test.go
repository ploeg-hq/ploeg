package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

const selectionHarnesses = `{"bronze":{
	"builder":{"name":"openhands","allowed":[
		{"name":"openhands","qualification":"qualified"},
		{"name":"acp","profile":"opencode","qualification":"qualified"},
		{"name":"acp","profile":"qwen-code","qualification":"unqualified"}]},
	"reviewer":{"name":"openhands","allowed":[{"name":"openhands","qualification":"qualified"}]}}}`

const selectionPolicies = `[
	{"team":"bronze","role":"builder","budgetUsd":3,"models":["deepseek-v4","kimi-k3"],"defaultModel":"deepseek-v4","ttl":"1h"},
	{"team":"bronze","role":"reviewer","budgetUsd":1,"models":["glm-4.7"],"ttl":"1h"}]`

type selectionFixture struct {
	admissionFixture
	broker    *managedBrokerFixture
	bootstrap string
}

func newSelectionFixture(t *testing.T) selectionFixture {
	t.Helper()
	f := newAdmissionFixture(t)
	harnesses, err := ParseTeamHarnesses(selectionHarnesses)
	if err != nil {
		t.Fatal(err)
	}
	broker := &managedBrokerFixture{}
	control, err := NewLLMControl(testStore, broker, selectionPolicies)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.Repeat("b", 32)
	registry, _ := json.Marshal([]WorkerBootstrap{{Token: bootstrap, Team: "bronze", Role: "builder"}})
	security, err := NewWorkerSecurity(string(registry), strings.Repeat("s", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	f.server.OperatorConfig.TeamHarnesses = harnesses
	f.server.OperatorConfig.Teams = map[string][]string{"bronze": {"builder", "reviewer"}}
	f.server.LLMControl = control
	f.server.WorkerSecurity = security
	f.server.MetricsCacheTTL = -1
	f.engine.Selections = f.server.selections()
	return selectionFixture{admissionFixture: f, broker: broker, bootstrap: bootstrap}
}

func selectionBody(requestID, harness, model string) map[string]any {
	body := admissionBody(requestID)
	if harness != "" {
		body["harness"] = harness
	}
	if model != "" {
		body["model"] = model
	}
	return body
}

type resolvedRole struct {
	Role    string  `json:"role"`
	Harness *string `json:"harness"`
	Model   *string `json:"model"`
}

func admissionRoles(t *testing.T, w *httptest.ResponseRecorder) map[string]resolvedRole {
	t.Helper()
	var body struct {
		Roles []resolvedRole `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]resolvedRole{}
	for _, r := range body.Roles {
		out[r.Role] = r
	}
	return out
}

func wantResolved(t *testing.T, roles map[string]resolvedRole, role, harness, model string) {
	t.Helper()
	r, ok := roles[role]
	if !ok || r.Harness == nil || *r.Harness != harness || r.Model == nil || *r.Model != model {
		t.Errorf("%s resolves to %+v, want %s and %s", role, r, harness, model)
	}
}

func pendingBuilder(t *testing.T, workItemID int64) (harness, model string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(harness_requested, ''), COALESCE(model_selected, '') FROM agent_runs
		WHERE work_item_id = $1 AND role = 'builder' ORDER BY id DESC LIMIT 1`, workItemID).Scan(&harness, &model); err != nil {
		t.Fatal(err)
	}
	return harness, model
}

func (f selectionFixture) admitSelection(t *testing.T, body map[string]any, status int) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	w := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, "alice", body)
	if w.Code != status {
		t.Fatalf("admission: %d %s, want %d", w.Code, w.Body, status)
	}
	validOperatorResponse(t, w)
	var got admissionResult
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	id := int64(0)
	if got.WorkItem.ID != "" {
		if err := json.Unmarshal([]byte(got.WorkItem.ID), &id); err != nil {
			t.Fatal(err)
		}
	}
	return w, id
}

func (f selectionFixture) claim(t *testing.T, harness, profile string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"team": "bronze", "role": "builder", "harness": harness, "harnessProfile": profile})
	w := workerRequest(f.server, "POST", "/api/v1/claim", f.bootstrap, "pod", string(body))
	var out map[string]any
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, out
}

func TestAnAdmissionChoosesTheHarnessAndModelOfEachRole(t *testing.T) {
	f := newSelectionFixture(t)
	w, id := f.admitSelection(t, selectionBody("req-choice", "acp/opencode", "kimi-k3"), http.StatusCreated)
	roles := admissionRoles(t, w)
	wantResolved(t, roles, "builder", "acp/opencode", "kimi-k3")
	wantResolved(t, roles, "reviewer", "openhands", "glm-4.7")
	if harness, model := pendingBuilder(t, id); harness != "acp/opencode" || model != "kimi-k3" {
		t.Fatalf("builder Run stores %q/%q, want acp/opencode and kimi-k3", harness, model)
	}

	if code, _ := f.claim(t, "openhands", ""); code != http.StatusNoContent {
		t.Fatalf("an openhands worker claimed the acp/opencode Run: %d", code)
	}
	code, claimed := f.claim(t, "acp", "")
	if code != http.StatusOK || claimed["model"] != "kimi-k3" {
		t.Fatalf("acp worker claim = %d %v, want the Run with model kimi-k3", code, claimed)
	}
	token, control := claimed["runToken"].(string), claimed["controlToken"].(string)
	if got := workerRequest(f.server, "POST", "/api/v1/runs/"+token+"/llm/credential", control, "pod", `{}`); got.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", got.Code, got.Body)
	}
	if !slices.Equal(f.broker.request.Models, []string{"kimi-k3"}) {
		t.Fatalf("minted key allows %v, want only kimi-k3", f.broker.request.Models)
	}
	account, err := testStore.LLMAccount(context.Background(), token)
	if err != nil || !slices.Equal(account.Models, []string{"kimi-k3"}) {
		t.Fatalf("reserved account models = %v (%v), want only kimi-k3", account.Models, err)
	}
	var configured string
	if err := testPool.QueryRow(context.Background(), `SELECT configured_model FROM agent_runs WHERE run_token = $1`, token).Scan(&configured); err != nil || configured != "kimi-k3" {
		t.Fatalf("configured_model = %q (%v), want kimi-k3", configured, err)
	}
}

func TestAnAdmissionWithoutAChoiceStoresEachRolesDefault(t *testing.T) {
	f := newSelectionFixture(t)
	w, id := f.admitSelection(t, selectionBody("req-default", "", ""), http.StatusCreated)
	roles := admissionRoles(t, w)
	wantResolved(t, roles, "builder", "openhands", "deepseek-v4")
	wantResolved(t, roles, "reviewer", "openhands", "glm-4.7")
	if harness, model := pendingBuilder(t, id); harness != "openhands" || model != "deepseek-v4" {
		t.Fatalf("builder Run stores %q/%q, want the Role's defaults", harness, model)
	}
	code, claimed := f.claim(t, "openhands", "")
	if code != http.StatusOK || claimed["model"] != "deepseek-v4" {
		t.Fatalf("openhands worker claim = %d %v", code, claimed)
	}
}

func TestAnAdmissionRefusesAHarnessOrModelItCannotHonour(t *testing.T) {
	cases := []struct {
		name, harness, model, code string
		mentions                   []string
		prepare                    func(selectionFixture)
	}{
		{name: "harness outside the allowlist", harness: "acp/goose", code: "harness_not_allowed",
			mentions: []string{"builder: openhands, acp/opencode, acp/qwen-code", "reviewer: openhands"}},
		{name: "unqualified harness without the opt-in", harness: "acp/qwen-code", code: "harness_unqualified", mentions: []string{"allowUnqualifiedHarnesses"}},
		{name: "a deployment without an allowlist admits only the default", harness: "acp/opencode", code: "harness_not_allowed",
			prepare: func(f selectionFixture) {
				h, _ := ParseTeamHarnesses(`{"bronze":{"builder":{"name":"openhands"},"reviewer":{"name":"openhands"}}}`)
				f.server.OperatorConfig.TeamHarnesses = h
			}},
		{name: "model no Role lists", model: "glm-5", code: "model_not_allowed", mentions: []string{"builder: deepseek-v4, kimi-k3", "reviewer: glm-4.7"}},
		{name: "model without managed inference", model: "kimi-k3", code: "model_requires_managed_inference",
			prepare: func(f selectionFixture) {
				f.server.LLMControl = nil
				f.server.WorkerSecurity = &WorkerSecurity{AllowLegacy: true}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectionFixture(t)
			if tc.prepare != nil {
				tc.prepare(f)
			}
			w, _ := f.admitSelection(t, selectionBody("req-refused", tc.harness, tc.model), http.StatusUnprocessableEntity)
			var body struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != tc.code {
				t.Fatalf("error = %+v (%v), want %s", body.Error, err, tc.code)
			}
			for _, mention := range tc.mentions {
				if !strings.Contains(body.Error.Message, mention) {
					t.Errorf("message %q does not name %q", body.Error.Message, mention)
				}
			}
			for _, table := range []string{"work_items", "shifts", "agent_runs", "operator_work_admissions"} {
				if n := countRows(t, "SELECT count(*) FROM "+table); n != 0 {
					t.Fatalf("a refused Admission left %d rows in %s", n, table)
				}
			}
		})
	}
}

func TestAnOptedInTeamAdmitsAnUnqualifiedHarness(t *testing.T) {
	f := newSelectionFixture(t)
	builder := f.server.OperatorConfig.TeamHarnesses["bronze"]["builder"]
	builder.AllowUnqualified = true
	f.server.OperatorConfig.TeamHarnesses["bronze"]["builder"] = builder
	f.engine.Selections = f.server.selections()
	_, id := f.admitSelection(t, selectionBody("req-optin", "acp/qwen-code", ""), http.StatusCreated)
	if harness, _ := pendingBuilder(t, id); harness != "acp/qwen-code" {
		t.Fatalf("builder Run stores %q, want acp/qwen-code", harness)
	}
}

func TestAReplayWithAnotherHarnessOrModelConflicts(t *testing.T) {
	f := newSelectionFixture(t)
	_, id := f.admitSelection(t, selectionBody("req-replay", "acp/opencode", "kimi-k3"), http.StatusCreated)
	for _, changed := range []map[string]any{
		selectionBody("req-replay", "openhands", "kimi-k3"),
		selectionBody("req-replay", "acp/opencode", "deepseek-v4"),
		selectionBody("req-replay", "", "kimi-k3"),
	} {
		w, _ := f.admitSelection(t, changed, http.StatusConflict)
		if !strings.Contains(w.Body.String(), "request_conflict") {
			t.Fatalf("replay answered %s", w.Body)
		}
	}
	w, replayed := f.admitSelection(t, selectionBody("req-replay", "acp", "kimi-k3"), http.StatusOK)
	if replayed != id {
		t.Fatalf("acp replay admitted %d, want the first Work Item %d: %s", replayed, id, w.Body)
	}
	var harness, model string
	if err := testPool.QueryRow(context.Background(), `SELECT harness_requested, model_requested FROM work_items WHERE id = $1`, id).Scan(&harness, &model); err != nil ||
		harness != "acp/opencode" || model != "kimi-k3" {
		t.Fatalf("first Admission now requests %q/%q (%v)", harness, model, err)
	}
}

func TestTheOperatorExecutionAdmissionTakesNoHarnessOrModel(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	raw, _ := json.Marshal(operatorHTTPInput("selection-session"))
	for _, field := range []string{"harness", "model"} {
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		body[field] = "openhands"
		if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "alice", body); w.Code != http.StatusBadRequest {
			t.Fatalf("an execution with %s answered %d %s", field, w.Code, w.Body)
		}
	}
}

func selectionTeamsRole(t *testing.T, f selectionFixture, role string) map[string]any {
	t.Helper()
	var page struct {
		Teams []struct {
			ID    string           `json:"id"`
			Roles []map[string]any `json:"roles"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, f.server, f.token, "teams"), &page); err != nil {
		t.Fatal(err)
	}
	for _, team := range page.Teams {
		for _, r := range team.Roles {
			if team.ID == "bronze" && r["id"] == role {
				return r
			}
		}
	}
	t.Fatalf("no bronze/%s in the Teams read", role)
	return nil
}

func TestTheTeamsReadShowsWhatAPersonCanChoose(t *testing.T) {
	f := newSelectionFixture(t)
	_, id := f.admitSelection(t, selectionBody("req-teams", "acp/opencode", ""), http.StatusCreated)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET harness = 'acp', harness_profile = 'opencode', harness_version = '1.18.30', harness_recorded_at = now()
		WHERE work_item_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	builder := selectionTeamsRole(t, f, "builder")
	byKey := map[string]map[string]any{}
	for _, h := range builder["harnesses"].([]any) {
		entry := h.(map[string]any)
		byKey[entry["key"].(string)] = entry
	}
	if byKey["acp/qwen-code"]["qualification"] != "unqualified" || byKey["openhands"]["default"] != true || byKey["acp/opencode"]["default"] != false {
		t.Fatalf("builder harnesses = %v", builder["harnesses"])
	}
	if byKey["acp/opencode"]["observedVersion"] != "1.18.30" || byKey["openhands"]["observedVersion"] != nil {
		t.Fatalf("observed versions = %v", builder["harnesses"])
	}
	if !slices.Equal(anyStrings(builder["models"]), []string{"deepseek-v4", "kimi-k3"}) || builder["defaultModel"] != "deepseek-v4" || builder["model"] != "deepseek-v4" {
		t.Fatalf("builder models = %v default %v", builder["models"], builder["defaultModel"])
	}
	reviewer := selectionTeamsRole(t, f, "reviewer")
	if !slices.Equal(anyStrings(reviewer["models"]), []string{"glm-4.7"}) || reviewer["defaultModel"] != "glm-4.7" {
		t.Fatalf("reviewer models = %v default %v", reviewer["models"], reviewer["defaultModel"])
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func TestARunWhoseHarnessLeftTheAllowlistIsReportedAndKept(t *testing.T) {
	f := newSelectionFixture(t)
	_, id := f.admitSelection(t, selectionBody("req-stranded", "acp/opencode", ""), http.StatusCreated)
	narrowed, err := ParseTeamHarnesses(`{"bronze":{"builder":{"name":"openhands","allowed":[{"name":"openhands","qualification":"qualified"}]},"reviewer":{"name":"openhands"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	f.server.OperatorConfig.TeamHarnesses = narrowed

	builder := selectionTeamsRole(t, f, "builder")
	pending := builder["pendingByHarness"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pendingByHarness = %v, want one entry", pending)
	}
	if entry := pending[0].(map[string]any); entry["harness"] != "acp/opencode" || entry["pending"] != float64(1) || entry["allowed"] != false {
		t.Fatalf("pendingByHarness = %v, want one acp/opencode Run outside the allowlist", pending)
	}

	r := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	metrics, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(metrics), `ploeg_runs_pending_outside_allowlist{team="bronze",role="builder",harness="acp/opencode"} 1`) {
		t.Fatalf("metrics do not report the stranded Run:\n%s", metrics)
	}
	if harness, _ := pendingBuilder(t, id); harness != "acp/opencode" {
		t.Fatalf("the stranded Run now stores %q; ploegd must leave it", harness)
	}
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM agent_runs WHERE work_item_id = $1`, id).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("the stranded Run is %q (%v), want pending", state, err)
	}
}

func TestARequeuedWorkItemKeepsItsRequestedModel(t *testing.T) {
	f := newRequeueFixture(t)
	f.engine.Selections = RunSelections{LLM: &LLMControl{Policies: []LLMPolicy{
		{Team: "bronze", Role: "builder", Models: []string{"deepseek-v4", "kimi-k3"}, DefaultModel: "deepseek-v4"}}}}
	ctx := context.Background()
	item := work.WorkItem{Provider: "vikunja", ExternalID: "1990", Team: "bronze", Title: "t"}
	id, _, err := testStore.IngestAssigned(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE work_items SET model_requested = 'kimi-k3' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	if _, model := pendingBuilder(t, id); model != "kimi-k3" {
		t.Fatalf("first builder Run stores %q, want kimi-k3", model)
	}
	f.run(t, id, "builder", work.OutcomeStuck, "blocked", nil, nil)
	if w := operatorExecutionRequest(f.server, "POST", requeuePath(id), f.token, "alice", nil); w.Code != http.StatusOK {
		t.Fatalf("requeue: %d %s", w.Code, w.Body)
	}
	var model string
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(model_selected, '') FROM agent_runs WHERE work_item_id = $1 AND state = 'pending'`, id).Scan(&model); err != nil || model != "kimi-k3" {
		t.Fatalf("requeued builder Run stores %q (%v), want kimi-k3", model, err)
	}
}

func TestTheClaimCarriesTheRunsModelAndRecordsIt(t *testing.T) {
	f := newSelectionFixture(t)
	_, id := f.admitSelection(t, selectionBody("req-claim", "", "kimi-k3"), http.StatusCreated)
	code, claimed := f.claim(t, "openhands", "")
	if code != http.StatusOK || claimed["model"] != "kimi-k3" {
		t.Fatalf("claim = %d %v, want model kimi-k3", code, claimed)
	}
	var page struct {
		Run struct {
			ConfiguredModel *string `json:"configuredModel"`
		} `json:"run"`
	}
	var runID int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent_runs WHERE work_item_id = $1`, id).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(operatorSchemaGET(t, f.server, f.token, "runs/"+itoa(runID)), &page); err != nil {
		t.Fatal(err)
	}
	if page.Run.ConfiguredModel == nil || *page.Run.ConfiguredModel != "kimi-k3" {
		t.Fatalf("configuredModel = %v, want kimi-k3 for a Role whose policy lists two models", page.Run.ConfiguredModel)
	}
}

func TestParseTeamHarnessesRefusesAnInconsistentAllowlist(t *testing.T) {
	for _, raw := range []string{
		`{"bronze":{"builder":{"name":"openhands","allowed":[{"name":"acp","profile":"opencode"}]}}}`,
		`{"bronze":{"builder":{"name":"openhands","allowed":[{"name":"openhands","qualification":"maybe"}]}}}`,
		`{"bronze":{"builder":{"name":"openhands","allowed":[{"name":"openhands"},{"name":"openhands"}]}}}`,
	} {
		if _, err := ParseTeamHarnesses(raw); err == nil {
			t.Errorf("ParseTeamHarnesses(%s) succeeded", raw)
		}
	}
	h, err := ParseTeamHarnesses(`{"bronze":{"builder":{"name":"acp"}}}`)
	if err != nil || !h["bronze"]["builder"].Admits("acp/opencode") || h["bronze"]["builder"].Admits("openhands") {
		t.Fatalf("a Role without an allowlist must admit only its configured Harness: %+v %v", h, err)
	}
}

func TestAPolicyMustListItsDefaultModel(t *testing.T) {
	if _, err := NewLLMControl(testStore, &managedBrokerFixture{}, `[{"team":"bronze","role":"builder","budgetUsd":1,"models":["kimi-k3"],"defaultModel":"deepseek-v4","ttl":"1h"}]`); err == nil {
		t.Fatal("a policy whose default is not in its models must stop ploegd")
	}
	single := LLMPolicy{Models: []string{"deepseek-v4"}}
	if single.Default() != "deepseek-v4" {
		t.Fatalf("a single-model policy's default = %q", single.Default())
	}
	if (LLMPolicy{Models: []string{"a", "b"}}).Default() != "" {
		t.Fatal("a multi-model policy without defaultModel has no default")
	}
}
