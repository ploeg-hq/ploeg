package operatorclient_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type readyForge struct{}

func (readyForge) InspectRepository(context.Context, string, string, string) (provider.RepositoryState, error) {
	return provider.RepositoryState{AgentsFile: true}, nil
}

func writerServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	raw, _ := json.Marshal([]map[string]any{{"name": "steward", "tokenEnv": "STEWARD_TOKEN", "teams": []string{httpapi.AllTeams}, "execute": true}})
	consumers, err := httpapi.ParseOperatorConsumers(string(raw), func(name string) (string, bool) { return token, name == "STEWARD_TOKEN" })
	if err != nil {
		t.Fatal(err)
	}
	plans, err := plan.Parse(`{"copper": {"pool": 10, "rounds": [{"roles": [{"name": "builder", "writes": true, "cap": 3}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	app := work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "app", BaseBranch: "development"}
	s := &httpapi.Server{Store: testStore, Log: log, LeaseTTL: time.Minute, RoleCaps: plans,
		Engine:         &shiftengine.Engine{Store: testStore, Plans: plans, Log: log},
		Readiness:      target.NewGate(map[string]work.Target{"app": app}, map[string]provider.RepositoryInspector{"forgejo": readyForge{}}),
		WorkerSecurity: &httpapi.WorkerSecurity{AllowLegacy: true},
		OperatorConfig: httpapi.OperatorConfig{Consumers: consumers, Teams: map[string][]string{"copper": {"builder"}, "tin": {}}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, token
}

const confirmation = "c-1:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"

var person = operatorclient.Attribution{Actor: "alice", ActingUser: "admin", Via: "mcp:Test Client 1.0", Confirmation: confirmation}

func wantAttributed(t *testing.T, c *operatorclient.Client, workItemID, action string) {
	t.Helper()
	for _, who := range []string{"alice", "admin"} {
		page, err := c.Events(context.Background(), operatorclient.EventFilter{WorkItemID: workItemID, Actor: who, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range page.Events {
			a, _ := e.Detail["attribution"].(map[string]any)
			if e.Action == action && a["consumer"] == "steward" && a["actor"] == "alice" && a["actingUser"] == "admin" && a["via"] == "mcp:Test Client 1.0" && a["confirmation"] == confirmation {
				found = true
			}
		}
		if !found {
			t.Fatalf("actor=%s: no attributed %s on Work Item %s in %+v", who, action, workItemID, page.Events)
		}
	}
}

func TestClientWritesNameThePersonAndChannel(t *testing.T) {
	ctx := context.Background()
	srv, token := writerServer(t)
	c, err := operatorclient.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}

	submission := operatorclient.Submission{RequestID: "client-submit-1", Team: "copper", Target: "app", Title: "Add a health check", Description: "Add a /healthz route."}
	first, err := c.SubmitWorkItem(ctx, person, submission)
	if err != nil || !first.Created || first.WorkItem.ShiftID == nil {
		t.Fatalf("submit = %+v, %v", first, err)
	}
	again, err := c.SubmitWorkItem(ctx, person, submission)
	if err != nil || !again.Replayed || again.WorkItem.ID != first.WorkItem.ID {
		t.Fatalf("a repeated requestId admitted twice: %+v, %v", again, err)
	}
	id := first.WorkItem.ID
	wantAttributed(t, c, id, "operator.work_item.admitted")

	note, err := c.AddNote(ctx, person, id, "client-note-1", "Keep the route unauthenticated.", "")
	if err != nil || note.Replayed {
		t.Fatalf("note = %+v, %v", note, err)
	}
	if again, err := c.AddNote(ctx, person, id, "client-note-1", "Keep the route unauthenticated.", ""); err != nil || !again.Replayed || again.ID != note.ID {
		t.Fatalf("a repeated note commandId stored twice: %+v, %v", again, err)
	}
	wantAttributed(t, c, id, "operator.note.added")

	stopped, err := c.Cancel(ctx, person, id, "client-stop-1", "")
	if err != nil || !stopped.Withdrawn {
		t.Fatalf("cancel = %+v, %v", stopped, err)
	}
	if again, err := c.Cancel(ctx, person, id, "client-stop-1", ""); err != nil || !again.Replayed {
		t.Fatalf("a repeated cancel commandId: %+v, %v", again, err)
	}
	wantAttributed(t, c, id, "work_item.withdrawn")

	stuck, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "client-stuck", Team: "copper", Title: "Stuck"})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, stuck, "copper", "ploeg/client-stuck", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.CloseShiftAndSettle(ctx, shift, "plan_exhausted", work.StateNeedsHuman, "needs a person"); err != nil {
		t.Fatal(err)
	}
	stuckID := fmt.Sprint(stuck)
	retried, err := c.Requeue(ctx, person, stuckID, operatorclient.Requeue{CommandID: "client-retry-1", FromRound: 1})
	if err != nil || retried.Replayed || retried.State != "queued" {
		t.Fatalf("requeue = %+v, %v", retried, err)
	}
	if again, err := c.Requeue(ctx, person, stuckID, operatorclient.Requeue{CommandID: "client-retry-1", FromRound: 1}); err != nil || !again.Replayed {
		t.Fatalf("a repeated requeue commandId: %+v, %v", again, err)
	}
	wantAttributed(t, c, stuckID, store.AuditWorkItemRequeued)

	proposed := proposedItems(t, srv.URL)
	decided, err := c.Approve(ctx, person, proposed[0], "looks right")
	if err != nil || !decided.Approved || decided.State != "queued" {
		t.Fatalf("approve = %+v, %v", decided, err)
	}
	if _, err := c.Approve(ctx, person, proposed[0], "again"); !errors.Is(err, operatorclient.ErrConflict) {
		t.Fatalf("approving twice: %v", err)
	}
	rejected, err := c.Reject(ctx, person, proposed[1], "duplicate")
	if err != nil || rejected.Approved || rejected.State != "done" {
		t.Fatalf("reject = %+v, %v", rejected, err)
	}
	wantAttributed(t, c, proposed[0], "work_item.approved")
	wantAttributed(t, c, proposed[1], "work_item.rejected")

	if _, err := c.Cancel(ctx, operatorclient.Attribution{Actor: "not valid"}, id, "x", ""); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("an invalid actor reached ploegd: %v", err)
	}
	if _, err := c.AddNote(ctx, person, "../teams", "x", "y", ""); err == nil {
		t.Fatal("path injection accepted")
	}
}

// ADR-0142: the client takes over a running Run for its person, attributed
// to them and the administrator acting for them, and a replay of the command
// changes nothing.
func TestClientTakesOverARunningRun(t *testing.T) {
	ctx := context.Background()
	srv, token := writerServer(t)
	c, err := operatorclient.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "client-take-over", Team: "copper", Title: "Take over"})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, id, "copper", "ploeg/client-take-over", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []store.Role{{Name: "taker", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "copper", "taker", time.Minute, 0)
	if err != nil || run == nil || run.Item.ID != fmt.Sprint(id) {
		t.Fatalf("claim: %+v, %v", run, err)
	}
	if err := testStore.RecordRunYields(ctx, run.RunToken); err != nil {
		t.Fatal(err)
	}
	taken, err := c.TakeOver(ctx, person, fmt.Sprint(id), "client-take-1", fmt.Sprint(shift))
	if err != nil || taken.State != "yielding" || len(taken.Runs) != 1 || taken.Replayed {
		t.Fatalf("take-over = %+v, %v", taken, err)
	}
	if again, err := c.TakeOver(ctx, person, fmt.Sprint(id), "client-take-1", fmt.Sprint(shift)); err != nil || !again.Replayed || again.ID != taken.ID {
		t.Fatalf("a repeated take-over commandId: %+v, %v", again, err)
	}
	wantAttributed(t, c, fmt.Sprint(id), store.AuditWorkItemTakenOver)
	if _, err := c.TakeOver(ctx, person, "../teams", "x", ""); err == nil {
		t.Fatal("path injection accepted")
	}
}

func TestClientSteersAnExecutionAsItsOwner(t *testing.T) {
	ctx := context.Background()
	srv, token := writerServer(t)
	c, err := operatorclient.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	e, _, err := testStore.AdmitOperatorExecution(ctx, "steward", "alice", store.AdmitOperatorExecution{SessionID: "client-steer", Team: "copper", Title: "Steer me",
		Objective: "Exercise the execution command client.", RepositoryID: "app", RepositoryURL: "https://forge.example/webgrip/app.git", CrewID: "crew", Demo: true}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	read, err := c.Execution(ctx, person, e.ID)
	if err != nil || read.ID != e.ID {
		t.Fatalf("read = %+v, %v", read, err)
	}
	cmd := store.OperatorExecutionCommand{ID: "client-say-1", Action: "message", ExpectedRevision: read.Revision, Generation: read.Generation, Text: "Use the helper."}
	after, err := c.ExecutionCommand(ctx, person, e.ID, cmd)
	if err != nil || after.Revision != read.Revision+1 {
		t.Fatalf("message = %+v, %v", after, err)
	}
	if again, err := c.ExecutionCommand(ctx, person, e.ID, cmd); err != nil || again.Revision != after.Revision {
		t.Fatalf("a repeated command id acted twice: %+v, %v", again, err)
	}
	wantAttributed(t, c, e.WorkItemID, store.AuditOperatorExecutionCommanded)
	stale := store.OperatorExecutionCommand{ID: "client-say-2", Action: "message", ExpectedRevision: read.Revision, Generation: read.Generation}
	if _, err := c.ExecutionCommand(ctx, person, e.ID, stale); !errors.Is(err, operatorclient.ErrConflict) {
		t.Fatalf("a stale revision: %v", err)
	}
	if _, err := c.Execution(ctx, operatorclient.Attribution{Actor: "mallory"}, e.ID); !errors.Is(err, operatorclient.ErrNotFound) {
		t.Fatalf("another person read the execution: %v", err)
	}
}

func proposedItems(t *testing.T, base string) []string {
	t.Helper()
	ctx := context.Background()
	source, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "client-source", Team: "tin", Title: "big work",
		Target: &work.Target{Owner: "webgrip", Repo: "app", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, body any) []byte {
		data, _ := json.Marshal(body)
		resp, err := http.Post(base+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, out.String())
		}
		return out.Bytes()
	}
	var claim struct {
		RunToken string `json:"runToken"`
	}
	if err := json.Unmarshal(post("/api/v1/claim", map[string]string{"team": "tin"}), &claim); err != nil || claim.RunToken == "" {
		t.Fatalf("claim: %v", err)
	}
	items := []harness.CreatedWorkItem{
		{Title: "part 1", Description: "do part", Ready: true, Kind: work.CreatedSplit},
		{Title: "part 2", Description: "do part", Ready: true, Kind: work.CreatedSplit},
	}
	post("/api/v1/runs/"+claim.RunToken+"/outcome", harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: items})
	page, _, err := testStore.OperatorItems(ctx, store.OperatorFilter{Team: "tin", States: []string{"proposed"}, Limit: 10})
	if err != nil || len(page) != 2 {
		t.Fatalf("proposed items of %d: %+v %v", source, page, err)
	}
	return []string{page[0].ID, page[1].ID}
}
