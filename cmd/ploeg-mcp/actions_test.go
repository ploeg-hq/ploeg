package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// personNamespace is the subject namespace the acting remote is configured
// with, and signedInPerson the person id of its token's subject "subject-1" under it:
// the first 32 hex characters of sha256("https://id.example.com/application/o/ploeg|subject-1").
const (
	personNamespace = "https://id.example.com/application/o/ploeg/"
	signedInPerson  = "oidc-6435b71f32dd50c4582f6f3f1c0e55bf"
)

var actionTools = []string{"ploeg_submit_work", "ploeg_approve_work", "ploeg_reject_work", "ploeg_retry_work", "ploeg_stop_work", "ploeg_add_note", "ploeg_steer_execution"}

type readyForge struct{}

func (readyForge) InspectRepository(context.Context, string, string, string) (provider.RepositoryState, error) {
	return provider.RepositoryState{AgentsFile: true}, nil
}

type acting struct {
	*remote
	ploegd http.Handler
}

// newActingRemote serves ploeg-mcp http in front of a real ploegd: the group
// ploeg-stewards maps to the execute-capable consumer mcp-steward, the group
// ploeg-readers to the read-only consumer mcp-reader.
func newActingRemote(t *testing.T) acting {
	t.Helper()
	return newActingRemoteWith(t, actingOptions{})
}

// actingOptions let a test give mcp-steward clientIds, ploegd person tokens
// and ploeg-mcp more environment.
type actingOptions struct {
	stewardClientIDs []string
	personTokens     func([]httpapi.OperatorConsumer) *httpapi.PersonTokens
	env              map[string]string
	configure        func(*httpConfig)
}

func newActingRemoteWith(t *testing.T, o actingOptions) acting {
	t.Helper()
	stewards, readers, admitter := randomToken(t), randomToken(t), randomToken(t)
	steward := map[string]any{"name": "mcp-steward", "tokenEnv": "STEWARD", "teams": []string{"pewter", "brass"}, "execute": true}
	if o.stewardClientIDs != nil {
		steward["clientIds"] = o.stewardClientIDs
	}
	raw, _ := json.Marshal([]map[string]any{
		steward,
		{"name": "mcp-reader", "tokenEnv": "READER", "teams": []string{"pewter"}, "execute": false},
		{"name": "another-consumer", "tokenEnv": "ADMITTER", "teams": []string{"pewter", "tin"}, "execute": true},
	})
	tokens := map[string]string{"STEWARD": stewards, "READER": readers, "ADMITTER": admitter}
	consumers, err := httpapi.ParseOperatorConsumers(string(raw), func(name string) (string, bool) {
		v, ok := tokens[name]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := plan.Parse(`{"pewter": {"pool": 10, "rounds": [{"roles": [{"name": "builder", "writes": true, "cap": 3}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	var personTokens *httpapi.PersonTokens
	if o.personTokens != nil {
		personTokens = o.personTokens(consumers)
	}
	log := slog.New(slog.DiscardHandler)
	app := work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "app", BaseBranch: "development"}
	ploegd := (&httpapi.Server{Store: testStore, Log: log, LeaseTTL: time.Minute, RoleCaps: plans,
		Engine:         &shiftengine.Engine{Store: testStore, Plans: plans, Log: log},
		Readiness:      target.NewGate(map[string]work.Target{"app": app}, map[string]provider.RepositoryInspector{"forgejo": readyForge{}}),
		WorkerSecurity: &httpapi.WorkerSecurity{AllowLegacy: true},
		OperatorConfig: httpapi.OperatorConfig{Consumers: consumers, PersonTokens: personTokens, Teams: map[string][]string{"pewter": {"builder"}, "brass": {}, "tin": {}}}}).Handler()
	env := map[string]string{"PLOEG_MCP_SUBJECT_NAMESPACE": personNamespace}
	for k, v := range o.env {
		env[k] = v
	}
	r := newRemoteTokens(t, func(*remote) http.Handler { return ploegd },
		`[{"group":"ploeg-stewards","tokenEnv":"ADMINS_TOKEN","execute":true},{"group":"ploeg-readers","tokenEnv":"READERS_TOKEN"}]`, stewards, readers, env, func(cfg *httpConfig) {
			if o.configure != nil {
				o.configure(cfg)
			}
		})
	return acting{remote: r, ploegd: ploegd}
}

type confirmer struct {
	mu       sync.Mutex
	answer   *mcp.ElicitResult
	messages []string
}

func (c *confirmer) elicit(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, req.Params.Message)
	return c.answer, nil
}

func (c *confirmer) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.messages) == 0 {
		return ""
	}
	return c.messages[len(c.messages)-1]
}

func (a acting) client(t *testing.T, groups []string, c *confirmer) *mcp.ClientSession {
	t.Helper()
	opts := &mcp.ClientOptions{}
	if c != nil {
		opts.ElicitationHandler = c.elicit
	}
	transport := &mcp.StreamableClientTransport{Endpoint: a.url, HTTPClient: &http.Client{Transport: bearer{a.token(groups, "ploeg:read")}}, DisableStandaloneSSE: true, MaxRetries: -1}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "Test Client", Version: "1.2.3"}, opts).Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func accept() *confirmer {
	return &confirmer{answer: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
}

func act(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (actionOut, *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out actionOut
	if !res.IsError {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &out)
	}
	return out, res
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		out[tool.Name] = tool
	}
	return out
}

func lastAttribution(t *testing.T, workItemID, action string) (store.Attribution, int) {
	t.Helper()
	page, _, err := testStore.OperatorEvents(context.Background(), store.OperatorFilter{WorkItemID: atoi(t, workItemID), Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var a store.Attribution
	n := 0
	for _, e := range page {
		if e.Action != action {
			continue
		}
		n++
		raw, _ := json.Marshal(e.Detail["attribution"])
		a = store.Attribution{}
		_ = json.Unmarshal(raw, &a)
	}
	return a, n
}

func atoi(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatalf("id %q: %v", s, err)
	}
	return n
}

func confirmed(message string) string {
	sum := sha256.Sum256([]byte(message))
	return hex.EncodeToString(sum[:])
}

func wantConfirmedBy(t *testing.T, got store.Attribution, message string) {
	t.Helper()
	if got.Consumer != "mcp-steward" || got.Actor != signedInPerson || got.ActingUser != "" || got.Via != "mcp:Test Client 1.2.3" || !strings.HasSuffix(got.Confirmation, ":"+confirmed(message)) || !strings.HasPrefix(got.Confirmation, "confirm-") {
		t.Fatalf("attribution = %+v, want mcp-steward, `+signedInPerson+`, mcp:Test Client 1.2.3 and the hash of %q", got, message)
	}
}

func stuckItem(t *testing.T, externalID string) string {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "pewter", Title: "Stuck " + externalID})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, id, "pewter", "ploeg/"+externalID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.CloseShiftAndSettle(ctx, shift, "plan_exhausted", work.StateNeedsHuman, "needs a person"); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(id)
}

func itemState(t *testing.T, id string) string {
	t.Helper()
	d, err := testStore.OperatorItem(context.Background(), atoi(t, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	return d.Item.State
}

func TestStdioModeOffersNoActionTools(t *testing.T) {
	handler, token := ploegd(t)
	cs := connect(t, handler, token)
	for name, tool := range toolNames(t, cs) {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || strings.Contains(strings.Join(actionTools, ","), name) {
			t.Fatalf("stdio offers %s, which is not a read tool", name)
		}
	}
}

func TestAReadOnlyCallerSeesAndCallsNoActionTools(t *testing.T) {
	a := newActingRemote(t)
	cs := a.client(t, []string{"ploeg-readers"}, accept())
	tools := toolNames(t, cs)
	if len(tools) != 6 {
		t.Fatalf("a reader sees %d tools", len(tools))
	}
	for _, name := range actionTools {
		if tools[name] != nil {
			t.Fatalf("a reader sees %s", name)
		}
	}
	id := stuckItem(t, "mcp-reader-1")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ploeg_stop_work", Arguments: map[string]any{"ref": id}})
	if err == nil && !res.IsError {
		t.Fatal("a reader called an action tool")
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("a reader's call changed the Work Item")
	}
}

func TestAnExecuteCallerSeesEveryActionToolAsAWrite(t *testing.T) {
	a := newActingRemote(t)
	tools := toolNames(t, a.client(t, []string{"ploeg-stewards"}, accept()))
	if len(tools) != 6+len(actionTools) {
		t.Fatalf("tools = %d", len(tools))
	}
	for _, name := range actionTools {
		tool := tools[name]
		if tool == nil || len(name) > 30 || tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || tool.Title == "" || !strings.Contains(tool.Description, "confirm") {
			t.Fatalf("%s: %+v", name, tool)
		}
	}
	if !*tools["ploeg_stop_work"].Annotations.DestructiveHint || *tools["ploeg_submit_work"].Annotations.DestructiveHint {
		t.Fatal("stop must be destructive and submit not")
	}
	if len(instructions) > 500 || !strings.Contains(instructions, "confirm") {
		t.Fatalf("instructions are %d characters or do not mention confirmation", len(instructions))
	}
}

func TestEachActionCallsPloegOnlyAfterThePersonConfirms(t *testing.T) {
	a := newActingRemote(t)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)

	submitted, res := act(t, cs, "ploeg_submit_work", map[string]any{"team": "pewter", "target": "app", "title": "Add a health check", "description": "Add /healthz.", "budgetUsd": 2})
	if res.IsError || !submitted.Done || submitted.WorkItemID == "" || !strings.Contains(yes.last(), "Team pewter") || !strings.Contains(yes.last(), "$2.00") {
		t.Fatalf("submit: %s %+v, asked %q", textOf(res), submitted, yes.last())
	}
	got, _ := lastAttribution(t, submitted.WorkItemID, "operator.work_item.admitted")
	wantConfirmedBy(t, got, yes.last())

	noted, res := act(t, cs, "ploeg_add_note", map[string]any{"ref": submitted.WorkItemID, "text": "Keep the route unauthenticated."})
	if res.IsError || !noted.Done || !strings.Contains(yes.last(), "Keep the route unauthenticated.") {
		t.Fatalf("note: %s %+v", textOf(res), noted)
	}
	got, _ = lastAttribution(t, submitted.WorkItemID, store.AuditOperatorNoteAdded)
	wantConfirmedBy(t, got, yes.last())

	stopped, res := act(t, cs, "ploeg_stop_work", map[string]any{"ref": submitted.WorkItemID})
	if res.IsError || !stopped.Done || stopped.State != "withdrawn" || !strings.Contains(yes.last(), `"Add a health check"`) {
		t.Fatalf("stop: %s %+v, asked %q", textOf(res), stopped, yes.last())
	}
	got, _ = lastAttribution(t, submitted.WorkItemID, "work_item.withdrawn")
	wantConfirmedBy(t, got, yes.last())

	stuck := stuckItem(t, "mcp-retry-1")
	retried, res := act(t, cs, "ploeg_retry_work", map[string]any{"ref": stuck, "note": "Try the smaller fix."})
	if res.IsError || !retried.Done || retried.State != "queued" || !strings.Contains(yes.last(), "Round 1") {
		t.Fatalf("retry: %s %+v", textOf(res), retried)
	}
	got, _ = lastAttribution(t, stuck, store.AuditWorkItemRequeued)
	wantConfirmedBy(t, got, yes.last())

	proposed := proposedItems(t, a.ploegd)
	approved, res := act(t, cs, "ploeg_approve_work", map[string]any{"ref": proposed[0]})
	if res.IsError || !approved.Done || approved.State != "queued" {
		t.Fatalf("approve: %s %+v", textOf(res), approved)
	}
	got, _ = lastAttribution(t, proposed[0], "work_item.approved")
	wantConfirmedBy(t, got, yes.last())
	if _, res := act(t, cs, "ploeg_reject_work", map[string]any{"ref": proposed[1]}); !res.IsError {
		t.Fatal("a rejection without a reason went ahead")
	}
	rejected, res := act(t, cs, "ploeg_reject_work", map[string]any{"ref": proposed[1], "reason": "duplicate"})
	if res.IsError || !rejected.Done || rejected.State != "done" {
		t.Fatalf("reject: %s %+v", textOf(res), rejected)
	}
	got, _ = lastAttribution(t, proposed[1], "work_item.rejected")
	wantConfirmedBy(t, got, yes.last())

	e, _, err := testStore.AdmitOperatorExecution(context.Background(), "mcp-steward", signedInPerson, store.AdmitOperatorExecution{SessionID: "mcp-steer", Team: "pewter", Title: "Steer me",
		Objective: "Exercise steering through ploeg-mcp.", RepositoryID: "app", RepositoryURL: "https://forge.example/webgrip/app.git", CrewID: "crew", Demo: true}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	steered, res := act(t, cs, "ploeg_steer_execution", map[string]any{"executionId": e.ID, "action": "message", "text": "Use the helper."})
	if res.IsError || !steered.Done || steered.ExecutionID != e.ID || !strings.Contains(yes.last(), "Use the helper.") {
		t.Fatalf("steer: %s %+v", textOf(res), steered)
	}
	got, _ = lastAttribution(t, e.WorkItemID, store.AuditOperatorExecutionCommanded)
	wantConfirmedBy(t, got, yes.last())
	if _, res := act(t, cs, "ploeg_steer_execution", map[string]any{"executionId": e.ID, "action": "explode"}); !res.IsError {
		t.Fatal("an unknown steering action went ahead")
	}
}

func TestAPersonSteersTheirExecutionAdmittedThroughAnotherConsumer(t *testing.T) {
	a := newActingRemote(t)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)
	ctx := context.Background()
	admit := func(session, actor, team string) store.OperatorExecution {
		e, _, err := testStore.AdmitOperatorExecution(ctx, "another-consumer", actor, store.AdmitOperatorExecution{SessionID: session, Team: team, Title: "Started elsewhere",
			Objective: "Exercise steering an execution another consumer admitted.", RepositoryID: "app", RepositoryURL: "https://forge.example/webgrip/app.git", CrewID: "crew", Demo: true}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		e, err = testStore.CommandOperatorExecution(ctx, e.ID, "another-consumer", actor, store.OperatorExecutionCommand{ID: "start-" + session, Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	mine := admit("elsewhere-mine", signedInPerson, "pewter")

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ploeg_get_work", Arguments: map[string]any{"ref": mine.WorkItemID}})
	if err != nil || res.IsError {
		t.Fatalf("get work: %v %s", err, textOf(res))
	}
	var work getOut
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &work)
	if len(work.Executions) != 1 || work.Executions[0].ID != mine.ID || !work.Executions[0].Yours || work.Executions[0].Owner != signedInPerson || work.Executions[0].State != "running" || !strings.Contains(work.Summary, mine.ID) {
		t.Fatalf("get work lists %+v, summary %q", work.Executions, work.Summary)
	}

	messaged, res := act(t, cs, "ploeg_steer_execution", map[string]any{"executionId": mine.ID, "action": "message", "text": "Prefer the small fix."})
	if res.IsError || !messaged.Done || messaged.State != "running" {
		t.Fatalf("message: %s %+v", textOf(res), messaged)
	}
	got, _ := lastAttribution(t, mine.WorkItemID, store.AuditOperatorExecutionCommanded)
	wantConfirmedBy(t, got, yes.last())
	paused, res := act(t, cs, "ploeg_steer_execution", map[string]any{"executionId": mine.ID, "action": "pause"})
	if res.IsError || !paused.Done || paused.State != "pause_requested" {
		t.Fatalf("pause: %s %+v", textOf(res), paused)
	}
	got, n := lastAttribution(t, mine.WorkItemID, store.AuditOperatorExecutionCommanded)
	wantConfirmedBy(t, got, yes.last())
	if n != 2 {
		t.Fatalf("%d commands audited, want 2", n)
	}

	for name, e := range map[string]store.OperatorExecution{
		"another person's": admit("elsewhere-theirs", "oidc-00000000000000000000000000000000", "pewter"),
		"another Team's":   admit("elsewhere-tin", signedInPerson, "tin"),
	} {
		if _, res := act(t, cs, "ploeg_steer_execution", map[string]any{"executionId": e.ID, "action": "pause"}); !res.IsError || !strings.Contains(textOf(res), "nothing changed") {
			t.Fatalf("%s execution was steered: %s", name, textOf(res))
		}
		after, err := testStore.OperatorExecution(ctx, e.ID, "another-consumer", e.Actor)
		if err != nil || after.State != "running" || after.Revision != e.Revision {
			t.Fatalf("%s execution changed: %+v %v", name, after, err)
		}
	}
}

func TestADeclinedOrCancelledConfirmationChangesNothing(t *testing.T) {
	a := newActingRemote(t)
	for _, answer := range []*mcp.ElicitResult{
		{Action: "decline"},
		{Action: "cancel"},
		{Action: "accept", Content: map[string]any{"confirm": false}},
	} {
		asked := &confirmer{answer: answer}
		cs := a.client(t, []string{"ploeg-stewards"}, asked)
		id := stuckItem(t, "mcp-decline-"+answer.Action+fmt.Sprint(len(answer.Content)))
		out, res := act(t, cs, "ploeg_stop_work", map[string]any{"ref": id})
		want := notConfirmed
		if answer.Action == "cancel" {
			want = notAnswered
		}
		if res.IsError || out.Done || out.Summary != want || asked.last() == "" {
			t.Fatalf("%s: %s %+v", answer.Action, textOf(res), out)
		}
		if state := itemState(t, id); state != "needs_human" {
			t.Fatalf("%s: the Work Item became %s", answer.Action, state)
		}
		if _, n := lastAttribution(t, id, "work_item.withdrawn"); n != 0 {
			t.Fatalf("%s: ploegd was called", answer.Action)
		}
	}
}

func TestAClientWithoutElicitationIsRefused(t *testing.T) {
	a := newActingRemote(t)
	cs := a.client(t, []string{"ploeg-stewards"}, nil)
	id := stuckItem(t, "mcp-no-elicit")
	_, res := act(t, cs, "ploeg_stop_work", map[string]any{"ref": id})
	if !res.IsError || !strings.Contains(textOf(res), "cannot ask you to confirm") {
		t.Fatalf("refusal: %s", textOf(res))
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("ploegd was called without a confirmation")
	}

	token := a.token([]string{"ploeg-stewards"}, "ploeg:read")
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ploeg_stop_work","arguments":{"ref":%q}}}`, id)
	resp, out := a.post(token, "2025-11-25", body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(out, []byte("cannot ask you to confirm")) || itemState(t, id) != "needs_human" {
		t.Fatalf("a 2025-11-25 stateless call: %d %s", resp.StatusCode, out)
	}
}

func TestARepeatedCommandIDActsOnce(t *testing.T) {
	a := newActingRemote(t)
	cs := a.client(t, []string{"ploeg-stewards"}, accept())
	args := map[string]any{"team": "pewter", "target": "app", "title": "Once", "description": "Only once.", "commandId": "mcp-client-retry-1"}
	first, res := act(t, cs, "ploeg_submit_work", args)
	if res.IsError || !first.Done || first.Replayed || first.CommandID != "mcp-client-retry-1" {
		t.Fatalf("first: %s %+v", textOf(res), first)
	}
	second, res := act(t, cs, "ploeg_submit_work", args)
	if res.IsError || !second.Replayed || second.WorkItemID != first.WorkItemID {
		t.Fatalf("second: %s %+v", textOf(res), second)
	}
	if _, n := lastAttribution(t, first.WorkItemID, "operator.work_item.admitted"); n != 1 {
		t.Fatalf("admitted %d times", n)
	}

	stopArgs := map[string]any{"ref": first.WorkItemID, "commandId": "mcp-client-stop-1"}
	if out, res := act(t, cs, "ploeg_stop_work", stopArgs); res.IsError || !out.Done || out.Replayed {
		t.Fatalf("stop: %s %+v", textOf(res), out)
	}
	if out, res := act(t, cs, "ploeg_stop_work", stopArgs); res.IsError || !out.Replayed {
		t.Fatalf("repeated stop: %s %+v", textOf(res), out)
	}
	if _, n := lastAttribution(t, first.WorkItemID, "work_item.withdrawn"); n != 1 {
		t.Fatalf("withdrawn %d times", n)
	}

	fresh, _ := act(t, cs, "ploeg_submit_work", map[string]any{"team": "pewter", "target": "app", "title": "Fresh", "description": "A new call."})
	if !fresh.Done || fresh.Replayed || !strings.HasPrefix(fresh.CommandID, "mcp-") || fresh.WorkItemID == first.WorkItemID {
		t.Fatalf("a call without commandId got %q", fresh.CommandID)
	}
}

func proposedItems(t *testing.T, ploegd http.Handler) []string {
	t.Helper()
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "mcp-source", Team: "brass", Title: "big work",
		Target: &work.Target{Owner: "webgrip", Repo: "app", BaseBranch: "development"}}); err != nil {
		t.Fatal(err)
	}
	post := func(path string, body any) []byte {
		data, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		ploegd.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data)))
		if w.Code < 200 || w.Code > 299 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	var claim struct {
		RunToken string `json:"runToken"`
	}
	if err := json.Unmarshal(post("/api/v1/claim", map[string]string{"team": "brass"}), &claim); err != nil || claim.RunToken == "" {
		t.Fatalf("claim: %v", err)
	}
	items := []harness.CreatedWorkItem{
		{Title: "part 1", Description: "do part", Ready: true, Kind: work.CreatedSplit},
		{Title: "part 2", Description: "do part", Ready: true, Kind: work.CreatedSplit},
	}
	post("/api/v1/runs/"+claim.RunToken+"/outcome", harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: items})
	page, _, err := testStore.OperatorItems(ctx, store.OperatorFilter{Team: "brass", States: []string{"proposed"}, Limit: 10})
	if err != nil || len(page) != 2 {
		t.Fatalf("proposed items: %+v %v", page, err)
	}
	return []string{page[0].ID, page[1].ID}
}
