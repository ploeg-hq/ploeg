package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/followup"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type createdRow struct {
	id, sourceItem, sourceRun, root int64
	provider, team, state, origin   string
	kind, title, forgeOwner         string
	depth                           int
	ready                           bool
	budget                          float64
}

func createdServer(policies map[string]followup.Policy) *Server {
	return &Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		OperatorConfig: OperatorConfig{Teams: map[string][]string{"bronze": {}, "silver": {}, "planning": {}}},
		CreatedWork:    policies,
	}
}

func claimLegacy(t *testing.T, h http.Handler, team string) claimResponse {
	t.Helper()
	code, c := postClaim(t, h, fmt.Sprintf(`{"team":%q}`, team))
	if code != http.StatusOK {
		t.Fatalf("claim for %s returned %d", team, code)
	}
	return c
}

func postOutcome(t *testing.T, h http.Handler, token string, rep any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(rep)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+token+"/outcome", bytes.NewReader(b)))
	return rec
}

func created(n int, ready bool) []harness.CreatedWorkItem {
	out := make([]harness.CreatedWorkItem, n)
	for i := range out {
		out[i] = harness.CreatedWorkItem{Title: fmt.Sprintf("part %d", i+1), Description: "do part", Ready: ready, Kind: work.CreatedSplit}
	}
	return out
}

func createdRows(t *testing.T, sourceID int64) []createdRow {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT id, provider, team, state, origin, created_kind, title, target_owner, depth, ready, budget_usd::float8,
			source_work_item_id, source_run_id, root_work_item_id
		FROM work_items WHERE source_work_item_id = $1 ORDER BY id`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []createdRow
	for rows.Next() {
		var r createdRow
		if err := rows.Scan(&r.id, &r.provider, &r.team, &r.state, &r.origin, &r.kind, &r.title, &r.forgeOwner,
			&r.depth, &r.ready, &r.budget, &r.sourceItem, &r.sourceRun, &r.root); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func rejections(t *testing.T, sourceID int64) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT detail->>'reason' FROM audit_log
		WHERE work_item_id = $1 AND action = 'created_work_item.rejected' ORDER BY id`, sourceID)
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

func ingestSource(t *testing.T, externalID, team string) int64 {
	t.Helper()
	id, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: team, Title: "big work",
		Target: &work.Target{Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func runID(t *testing.T, token string) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent_runs WHERE run_token = $1`, token).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOutcome_CreatesProposedWorkItemsWithSourceAndRejectsOverLimit(t *testing.T) {
	reset(t)
	h := createdServer(nil).Handler()
	source := ingestSource(t, "5001", "bronze")
	c := claimLegacy(t, h, "bronze")
	run := runID(t, c.RunToken)

	rec := postOutcome(t, h, c.RunToken, harness.OutcomeReport{
		Outcome: work.OutcomeFollowUpCreated, Summary: "split in seven", CreatedWorkItems: created(7, true),
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d %s", rec.Code, rec.Body)
	}
	rows := createdRows(t, source)
	if len(rows) != 5 {
		t.Fatalf("stored %d created Work Items, want maxCreatedPerRun 5", len(rows))
	}
	for i, r := range rows {
		if r.provider != work.ProviderPloeg || r.origin != string(work.OriginFollowUp) || r.state != string(work.StateProposed) ||
			r.team != "bronze" || r.depth != 1 || r.sourceRun != run || r.root != source || r.kind != "split" ||
			r.forgeOwner != "webgrip" || r.budget != followup.Default().ItemBudgetUSD || r.title != fmt.Sprintf("part %d", i+1) {
			t.Errorf("created row %d = %+v", i, r)
		}
	}
	reasons := rejections(t, source)
	if len(reasons) != 2 || reasons[0] != "maxCreatedPerRun 5 reached" {
		t.Fatalf("over-limit entries were not recorded: %q", reasons)
	}
	var state, outcome string
	if err := testPool.QueryRow(context.Background(), `
		SELECT w.state, r.outcome FROM work_items w JOIN agent_runs r ON r.work_item_id = w.id WHERE w.id = $1`, source).
		Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != string(work.StateDone) || outcome != string(work.OutcomeFollowUpCreated) {
		t.Fatalf("source item %s / run %s", state, outcome)
	}
	if code, _ := postClaim(t, h, `{"team":"bronze"}`); code != http.StatusNoContent {
		t.Fatalf("a proposed Work Item was claimable: %d", code)
	}
}

func TestOutcome_CreatedWorkDepthLimit(t *testing.T) {
	reset(t)
	policy := followup.Default()
	policy.AutoDispatch = true
	policy.PoolUSD = 100
	h := createdServer(map[string]followup.Policy{"bronze": policy}).Handler()
	ingestSource(t, "5101", "bronze")

	for depth := 1; depth <= 3; depth++ {
		c := claimLegacy(t, h, "bronze")
		parent, err := strconv.ParseInt(c.WorkItem.ID, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if rec := postOutcome(t, h, c.RunToken, harness.OutcomeReport{
			Outcome: work.OutcomeFollowUpCreated, Summary: "deeper", CreatedWorkItems: created(1, true),
		}); rec.Code != http.StatusNoContent {
			t.Fatalf("depth %d outcome: %d %s", depth, rec.Code, rec.Body)
		}
		rows := createdRows(t, parent)
		if depth <= policy.MaxDepth {
			if len(rows) != 1 || rows[0].depth != depth || rows[0].state != string(work.StateQueued) {
				t.Fatalf("depth %d: %+v", depth, rows)
			}
			continue
		}
		if len(rows) != 0 {
			t.Fatalf("created work beyond maxDepth: %+v", rows)
		}
		if reasons := rejections(t, parent); len(reasons) != 1 || reasons[0] != "depth 3 exceeds maxDepth 2" {
			t.Fatalf("depth rejection not recorded: %q", reasons)
		}
	}
}

func TestOutcome_CreatedWorkFloodProtection(t *testing.T) {
	reset(t)
	policy := followup.Default()
	policy.MaxOpen = 3
	policy.PoolUSD = 100
	h := createdServer(map[string]followup.Policy{"bronze": policy}).Handler()
	first := ingestSource(t, "5201", "bronze")
	second := ingestSource(t, "5202", "bronze")

	c := claimLegacy(t, h, "bronze")
	postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created(2, true)})
	c = claimLegacy(t, h, "bronze")
	postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created(2, true)})

	if got := len(createdRows(t, first)) + len(createdRows(t, second)); got != 3 {
		t.Fatalf("%d open created Work Items, want maxOpen 3", got)
	}
	reasons := append(rejections(t, first), rejections(t, second)...)
	if len(reasons) != 1 || reasons[0] != "maxOpen 3 reached: team bronze already has 3 open created Work Items" {
		t.Fatalf("flood rejection not recorded: %q", reasons)
	}
}

func TestOutcome_CreatedWorkBudgetPoolAndShiftCap(t *testing.T) {
	reset(t)
	policy := followup.Default()
	policy.AutoDispatch = true
	policy.ItemBudgetUSD = 1.5
	policy.PoolUSD = 3
	h := createdServer(map[string]followup.Policy{"bronze": policy}).Handler()
	source := ingestSource(t, "5301", "bronze")
	c := claimLegacy(t, h, "bronze")
	postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created(3, true)})

	rows := createdRows(t, source)
	if len(rows) != 2 {
		t.Fatalf("pool of 3.00 allotted %d items of 1.50", len(rows))
	}
	if reasons := rejections(t, source); len(reasons) != 1 || reasons[0] != "created-work pool exhausted: 3.00 of 3.00 USD allotted in this tree, 1.50 needed" {
		t.Fatalf("pool rejection not recorded: %q", reasons)
	}
	shift, err := testStore.OpenShift(context.Background(), rows[0].id, "bronze", "agent/ploeg-x", 6)
	if err != nil {
		t.Fatal(err)
	}
	var budget float64
	if err := testPool.QueryRow(context.Background(), `SELECT budget::float8 FROM shifts WHERE id = $1`, shift).Scan(&budget); err != nil {
		t.Fatal(err)
	}
	if budget != 1.5 {
		t.Fatalf("created Work Item's Shift pool = %.2f, want its allotment 1.50", budget)
	}
}

func TestCreatedWorkItem_OwnsItsBranch(t *testing.T) {
	reset(t)
	policy := followup.Default()
	policy.AutoDispatch = true
	h := createdServer(map[string]followup.Policy{"bronze": policy}).Handler()
	source := ingestSource(t, "5351", "bronze")
	c := claimLegacy(t, h, "bronze")
	postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created(1, true)})
	rows := createdRows(t, source)
	if len(rows) != 1 {
		t.Fatalf("created %d", len(rows))
	}
	if _, err := testStore.OpenShift(context.Background(), rows[0].id, "bronze", "agent/ploeg-created", 0); err != nil {
		t.Fatal(err)
	}
	owner, ok, err := testStore.FindBranchOwner(context.Background(), "webgrip/ploeg", "agent/ploeg-created")
	if err != nil || !ok || owner.WorkItemID != rows[0].id {
		t.Fatalf("a created Work Item's branch resolved to %+v (ok=%v, err=%v), want %d", owner, ok, err, rows[0].id)
	}
}

func TestOutcome_NotReadyRouting(t *testing.T) {
	reset(t)
	withRefinement := followup.Default()
	withRefinement.RefinementTeam = "planning"
	withRefinement.AutoDispatch = true
	h := createdServer(map[string]followup.Policy{"bronze": withRefinement}).Handler()
	source := ingestSource(t, "5401", "bronze")
	c := claimLegacy(t, h, "bronze")
	postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s",
		CreatedWorkItems: append(created(1, false), created(1, true)...)})
	rows := createdRows(t, source)
	if len(rows) != 2 || rows[0].team != "planning" || rows[0].state != "queued" || rows[0].ready ||
		rows[1].team != "bronze" || rows[1].state != "queued" {
		t.Fatalf("routing with a refinement team: %+v", rows)
	}

	reset(t)
	autoOnly := followup.Default()
	autoOnly.AutoDispatch = true
	h = createdServer(map[string]followup.Policy{"bronze": autoOnly}).Handler()
	source = ingestSource(t, "5402", "bronze")
	c = claimLegacy(t, h, "bronze")
	postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created(1, false)})
	if rows := createdRows(t, source); len(rows) != 1 || rows[0].team != "bronze" || rows[0].state != "proposed" {
		t.Fatalf("not-Ready work without a refinement target was dispatched: %+v", rows)
	}
}

func TestOutcome_RejectsMalformedCreatedWorkAtTheBoundary(t *testing.T) {
	reset(t)
	h := createdServer(nil).Handler()
	source := ingestSource(t, "5501", "bronze")
	c := claimLegacy(t, h, "bronze")
	rec := postOutcome(t, h, c.RunToken, map[string]any{
		"outcome": "follow_up_created", "summary": "s",
		"createdWorkItems": []map[string]any{{"title": "t", "description": "", "ready": true, "kind": "rewrite"}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind was accepted: %d", rec.Code)
	}
	if rows := createdRows(t, source); len(rows) != 0 {
		t.Fatalf("malformed work was stored: %+v", rows)
	}
}

func TestOperator_ApproveAndRejectProposedWorkItems(t *testing.T) {
	reset(t)
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, true)
	s := createdServer(nil)
	s.OperatorConfig.Consumers = consumers
	h := s.Handler()
	source := ingestSource(t, "5601", "bronze")
	c := claimLegacy(t, h, "bronze")
	postOutcome(t, h, c.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created(2, true)})
	rows := createdRows(t, source)
	if len(rows) != 2 {
		t.Fatalf("created %d", len(rows))
	}
	approve := fmt.Sprintf("/api/v1/operator/work-items/%d/approve", rows[0].id)
	reject := fmt.Sprintf("/api/v1/operator/work-items/%d/reject", rows[1].id)

	if w := operatorExecutionRequest(s, "POST", reject, token, "alice", map[string]string{}); w.Code != 400 {
		t.Fatalf("reject without a reason: %d %s", w.Code, w.Body)
	}
	w := operatorExecutionRequest(s, "POST", approve, token, "alice", map[string]string{"reason": "looks right"})
	if w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	w = operatorExecutionRequest(s, "POST", reject, token, "alice", map[string]string{"reason": "duplicate of part 1"})
	if w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())

	rows = createdRows(t, source)
	if rows[0].state != "queued" || rows[1].state != "done" || rows[1].budget != 0 {
		t.Fatalf("after decisions: %+v", rows)
	}
	var reason, actor string
	if err := testPool.QueryRow(context.Background(), `SELECT detail->>'reason', actor FROM audit_log
		WHERE work_item_id = $1 AND action = 'work_item.rejected'`, rows[1].id).Scan(&reason, &actor); err != nil ||
		reason != "duplicate of part 1" || actor != "operator:workbench:alice" {
		t.Fatalf("rejection audit: %q %q %v", reason, actor, err)
	}
	if w := operatorExecutionRequest(s, "POST", approve, token, "alice", map[string]string{}); w.Code != 409 {
		t.Fatalf("approving a queued item: %d %s", w.Code, w.Body)
	}
	claimed := claimLegacy(t, h, "bronze")
	if claimed.WorkItem.ID != fmt.Sprint(rows[0].id) || claimed.WorkItem.Origin != work.OriginFollowUp {
		t.Fatalf("the approved item was not dispatched: %+v", claimed.WorkItem)
	}

	readOnly, readToken := operatorTestConsumers(t, []string{"bronze"}, false)
	s.OperatorConfig.Consumers = readOnly
	if w := operatorExecutionRequest(s, "POST", reject, readToken, "alice", map[string]string{"reason": "x"}); w.Code != 403 {
		t.Fatalf("a read-only consumer decided: %d", w.Code)
	}
	other, otherToken := operatorTestConsumers(t, []string{"silver"}, true)
	s.OperatorConfig.Consumers = other
	if w := operatorExecutionRequest(s, "POST", reject, otherToken, "alice", map[string]string{"reason": "x"}); w.Code != 404 {
		t.Fatalf("a consumer outside the team decided: %d", w.Code)
	}
}

func TestClaim_MarksPlannerRoles(t *testing.T) {
	reset(t)
	plans := plan.Plans{"bronze": {Rounds: []plan.Round{{Roles: []plan.Role{{Name: "planner", Planner: true}, {Name: "analyst"}}}}}}
	h := apiServer(t, plans)
	shiftFixture(t, "5701", 0, []store.Role{{Name: "planner"}, {Name: "analyst"}})
	code, planner := postClaim(t, h, `{"team":"bronze","role":"planner"}`)
	if code != http.StatusOK || !planner.Planner {
		t.Fatalf("planner claim: %d planner=%v", code, planner.Planner)
	}
	code, analyst := postClaim(t, h, `{"team":"bronze","role":"analyst"}`)
	if code != http.StatusOK || analyst.Planner {
		t.Fatalf("analyst claim: %d planner=%v", code, analyst.Planner)
	}
}

func validateOperatorSchema(t *testing.T, body []byte) {
	t.Helper()
	schemaPath, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("response violates the published schema: %v\n%s", err, body)
	}
}
