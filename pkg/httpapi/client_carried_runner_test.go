package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// claimedClientRun claims a client-carried writer with a Lease that has
// already lapsed, as if the client went away right after claiming.
func claimedClientRun(t *testing.T, s *Server, token, externalID string) (int64, string) {
	t.Helper()
	id := clientCarriedWork(t, s, externalID, "ryan", clientPerson)
	s.LeaseTTL = time.Millisecond
	w := clientClaim(s, token, clientPerson, "", id)
	s.LeaseTTL = time.Minute
	if w.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
	var resp clientClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	return id, resp.Credential.RunToken
}

func runnerReport(t *testing.T, s *Server, body string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/claude-sessions/report", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer sk-ant-cc-anything")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Code
}

func expireAll(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := testStore.ExpireRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ExpireLeases(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestARecordedClientCarriedRunIsNotReclaimedWhenItsClientStopsRenewing(t *testing.T) {
	reset(t)
	s, token := clientCarriedServer(t)
	s.ClaudeSessions.Verifier = fakeSessionVerifier{claims: sessionClaims("runner1")}
	held, heldToken := claimedClientRun(t, s, token, "1401")
	unrecorded, _ := claimedClientRun(t, s, token, "1402")
	if code := recordSession(t, s.Handler(), heldToken, testEnvironment, "session_runner1"); code != http.StatusNoContent {
		t.Fatalf("record session: %d", code)
	}
	expireAll(t)
	if got := runStates(t, held); got != "running" {
		t.Fatalf("the Run whose session is recorded = %q, want still running while ploegd holds it", got)
	}
	if got := runStates(t, unrecorded); got == "running" {
		t.Fatalf("the Run without a recorded session = %q, want reclaimed at its deadline as before", got)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id = $1`, held); n != 1 {
		t.Fatalf("the held Work Item has %d Runs, want no retry", n)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+heldToken+"/renew", nil))
	var renewed struct {
		Deadline time.Time `json:"deadline"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &renewed); err != nil || rec.Code != http.StatusOK || time.Until(renewed.Deadline) < time.Hour {
		t.Fatalf("a client that still renews: %d %s, want the held deadline kept", rec.Code, rec.Body)
	}
}

func TestTheRunnersReportSettlesAClientCarriedRun(t *testing.T) {
	reset(t)
	s, token := clientCarriedServer(t)
	s.ClaudeSessions.Verifier = fakeSessionVerifier{claims: sessionClaims("runner2")}
	id, runToken := claimedClientRun(t, s, token, "1403")
	if code := recordSession(t, s.Handler(), runToken, testEnvironment, "session_runner2"); code != http.StatusNoContent {
		t.Fatalf("record session: %d", code)
	}
	pr := "https://forge.example/webgrip/fixture/pulls/7"
	body, _ := json.Marshal(claudeSessionReportRequest{ExitReason: "completed", PushedCommit: "abc123", PullRequest: pr,
		FinalMessage: `Done. {"problem": "No health check.", "solution": "Added /healthz."}`})
	if code := runnerReport(t, s, string(body)); code != http.StatusNoContent {
		t.Fatalf("runner report: %d", code)
	}
	var state, outcome, problem string
	var links []string
	if err := testPool.QueryRow(context.Background(), `SELECT state, COALESCE(outcome, ''), problem, links FROM agent_runs WHERE run_token = $1`,
		runToken).Scan(&state, &outcome, &problem, &links); err != nil {
		t.Fatal(err)
	}
	if state != "finished" || outcome != "pr_opened" || problem != "No health check." || len(links) != 1 || links[0] != pr {
		t.Fatalf("Run = %s %s problem %q links %v, want pr_opened from the runner's report", state, outcome, problem, links)
	}
	if n := countRows(t, `SELECT count(*) FROM leases WHERE work_item_id = $1`, id); n != 0 {
		t.Fatalf("the settled Run still holds %d Lease(s)", n)
	}
}

func TestARunnerReportWithoutAMatchingRecordedSessionIsRefused(t *testing.T) {
	reset(t)
	s, token := clientCarriedServer(t)
	s.ClaudeSessions.Verifier = fakeSessionVerifier{claims: sessionClaims("other")}
	claimedClientRun(t, s, token, "1404")
	_, recorded := claimedClientRun(t, s, token, "1405")
	if code := recordSession(t, s.Handler(), recorded, testEnvironment, "session_mine"); code != http.StatusNoContent {
		t.Fatalf("record session: %d", code)
	}
	body := `{"exitReason":"completed","finalMessage":"{\"outcome\":\"no_change_needed\",\"summary\":\"x\"}"}`
	if code := runnerReport(t, s, body); code != http.StatusNotFound {
		t.Fatalf("a report for a session no Run recorded answered %d, want 404", code)
	}
	if got := runStates(t, mustItem(t, "1404")) + "/" + runStates(t, mustItem(t, "1405")); got != "running/running" {
		t.Fatalf("runs = %s, want both untouched", got)
	}
}

func mustItem(t *testing.T, externalID string) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM work_items WHERE external_id = $1`, externalID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunnerOutcomeFollowsTheWorkersResolution(t *testing.T) {
	pr := "https://forge.example/o/r/pulls/1"
	writer := store.ClientSessionRun{Writes: true, Title: "t", Branch: "agent/vik-1"}
	reader := store.ClientSessionRun{Title: "t", Branch: "agent/vik-1"}
	earlier := store.ClientSessionRun{Writes: true, Title: "t", PriorLinks: []string{pr}}
	for name, c := range map[string]struct {
		run   store.ClientSessionRun
		state harness.RemoteSessionState
		want  work.Outcome
	}{
		"new pull request":            {writer, harness.RemoteSessionState{ExitReason: "completed", PullRequest: pr}, work.OutcomePROpened},
		"earlier pull request":        {earlier, harness.RemoteSessionState{ExitReason: "completed", PullRequest: pr}, work.OutcomePRUpdated},
		"reader on the writer's PR":   {reader, harness.RemoteSessionState{ExitReason: "completed", PullRequest: pr}, work.OutcomeNoChangeNeeded},
		"stopped mid-turn":            {writer, harness.RemoteSessionState{ExitReason: "interrupted"}, work.OutcomeFailed},
		"stuck in the final message":  {writer, harness.RemoteSessionState{ExitReason: "completed", FinalMessage: `{"outcome":"stuck","stuckReason":"no access"}`}, work.OutcomeStuck},
		"finished with nothing to do": {writer, harness.RemoteSessionState{ExitReason: "completed"}, work.OutcomeNoChangeNeeded},
	} {
		got := runnerOutcome(c.run, c.state)
		if got.Outcome != c.want || validateOutcomeReport(got) != nil {
			t.Errorf("%s: outcome %q (%v), want %q", name, got.Outcome, validateOutcomeReport(got), c.want)
		}
	}
}
