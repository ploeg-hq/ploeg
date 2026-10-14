package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func harnessServer(t *testing.T) (*Server, *bytes.Buffer) {
	t.Helper()
	reset(t)
	shiftFixture(t, "harness-version", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	var logs bytes.Buffer
	return &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.NewTextHandler(&logs, nil)),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}}, &logs
}

func harnessClaim(t *testing.T, s *Server, body string) string {
	t.Helper()
	code, claim := postClaim(t, s.Handler(), body)
	if code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	return claim.RunToken
}

func harnessOutcome(t *testing.T, s *Server, token, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+token+"/outcome", strings.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("outcome = %d %s", rec.Code, rec.Body)
	}
}

func recordedHarness(t *testing.T, token string) store.OperatorRun {
	t.Helper()
	run, err := testStore.OperatorRun(context.Background(), runIDOf(t, token), nil)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestARunKilledAfterItsClaimKeepsTheClaimedHarnessVersion(t *testing.T) {
	s, _ := harnessServer(t)
	token := harnessClaim(t, s, `{"team":"bronze","role":"reviewer","harness":"acp","harnessProfile":"opencode","harnessVersion":"opencode 1.18.30"}`)
	execSQL(t, `UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE run_token = $1`, token)
	expired, err := testStore.ExpireRuns(context.Background())
	if err != nil || len(expired) != 1 {
		t.Fatalf("expired = %+v, %v", expired, err)
	}
	run := recordedHarness(t, token)
	if run.State != "finished" || run.FailureReason == nil || *run.FailureReason != "lease_lost" {
		t.Fatalf("expired Run = state %s failure %v, want recovered as lease_lost", run.State, run.FailureReason)
	}
	if strOf(run.HarnessVersion) != "opencode 1.18.30" || run.HarnessAgent != nil {
		t.Fatalf("killed Run reads version %s agent %+v, want the claimed version and no agent", strOf(run.HarnessVersion), run.HarnessAgent)
	}
}

func TestTheOutcomeReportAddsTheAgentAndKeepsTheClaimedVersion(t *testing.T) {
	s, logs := harnessServer(t)
	token := harnessClaim(t, s, `{"team":"bronze","role":"reviewer","harness":"acp","harnessProfile":"opencode","harnessVersion":"1.18.30"}`)
	harnessOutcome(t, s, token, `{"outcome":"no_change_needed","summary":"read it",
		"harness":{"name":"acp","profile":"opencode","version":"1.19.0","agent":{"name":"opencode","version":"1.19.0"}}}`)
	run := recordedHarness(t, token)
	if strOf(run.HarnessVersion) != "1.18.30" || run.HarnessAgent == nil || run.HarnessAgent.Name != "opencode" || strOf(run.HarnessAgent.Version) != "1.19.0" {
		t.Fatalf("recorded version %s agent %+v", strOf(run.HarnessVersion), run.HarnessAgent)
	}
	if run.Outcome == nil || *run.Outcome != string(work.OutcomeNoChangeNeeded) {
		t.Fatalf("outcome = %v", run.Outcome)
	}
	if !strings.Contains(logs.String(), "recorded=1.18.30 reported=1.19.0") {
		t.Fatalf("the different version was not logged:\n%s", logs)
	}
}

func TestARecordingFailureNeverChangesTheOutcome(t *testing.T) {
	s, logs := harnessServer(t)
	token := harnessClaim(t, s, `{"team":"bronze","role":"reviewer"}`)
	execSQL(t, `CREATE FUNCTION refuse_harness_agent() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.harness_agent IS DISTINCT FROM OLD.harness_agent THEN RAISE EXCEPTION 'harness write refused'; END IF; RETURN NEW; END $$`)
	execSQL(t, `CREATE TRIGGER refuse_harness_agent BEFORE UPDATE ON agent_runs FOR EACH ROW EXECUTE FUNCTION refuse_harness_agent()`)
	t.Cleanup(func() {
		execSQL(t, `DROP TRIGGER refuse_harness_agent ON agent_runs`)
		execSQL(t, `DROP FUNCTION refuse_harness_agent()`)
	})
	harnessOutcome(t, s, token, `{"outcome":"no_change_needed","summary":"read it","harness":{"name":"acp","agent":{"name":"opencode"}}}`)
	run := recordedHarness(t, token)
	if run.Outcome == nil || *run.Outcome != string(work.OutcomeNoChangeNeeded) || run.Summary != "read it" || run.State != "finished" {
		t.Fatalf("outcome = %v summary %q state %s", run.Outcome, run.Summary, run.State)
	}
	if run.HarnessAgent != nil || run.HarnessVersion != nil {
		t.Fatalf("facts after a failed write = %+v %s", run.HarnessAgent, strOf(run.HarnessVersion))
	}
	if !strings.Contains(logs.String(), "reported harness not recorded") {
		t.Fatalf("the failure was not logged:\n%s", logs)
	}
}

func TestClaimAndOutcomeAcceptFieldsTheyDoNotKnow(t *testing.T) {
	s, _ := harnessServer(t)
	token := harnessClaim(t, s, `{"team":"bronze","role":"reviewer","harness":"openhands"}`)
	harnessOutcome(t, s, token, `{"outcome":"no_change_needed","summary":"read it"}`)
	if run := recordedHarness(t, token); run.HarnessVersion != nil || run.HarnessAgent != nil {
		t.Fatalf("older worker's Run = %s %+v, want unknown", strOf(run.HarnessVersion), run.HarnessAgent)
	}

	shiftFixture(t, "harness-future", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	token = harnessClaim(t, s, `{"team":"bronze","role":"reviewer","harnessVersion":"2.0","futureField":{"x":1}}`)
	harnessOutcome(t, s, token, `{"outcome":"no_change_needed","summary":"read it","futureField":true,
		"harness":{"name":"openhands","futureField":"y"}}`)
	if run := recordedHarness(t, token); strOf(run.HarnessVersion) != "2.0" {
		t.Fatalf("version = %s", strOf(run.HarnessVersion))
	}
}

func TestTheTeamsReadShowsEachRolesHarnessModelAndObservedVersion(t *testing.T) {
	s, _ := harnessServer(t)
	token := harnessClaim(t, s, `{"team":"bronze","role":"reviewer","harness":"acp","harnessProfile":"opencode","harnessVersion":"1.18.30"}`)
	harnessOutcome(t, s, token, `{"outcome":"no_change_needed","summary":"read it","harness":{"name":"acp","agent":{"name":"opencode","version":"1.18.30"}}}`)

	consumers, operator := operatorTestConsumers(t, []string{"bronze"}, false)
	harnesses, err := ParseTeamHarnesses(`{"bronze":{"reviewer":{"name":"acp","profile":"opencode"},"builder":{"name":"openhands"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewLLMControl(testStore, &managedBrokerFixture{}, `[{"team":"bronze","role":"reviewer","budgetUsd":1,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s.LLMControl = control
	s.OperatorConfig = OperatorConfig{Consumers: consumers, Teams: map[string][]string{"bronze": {"builder", "reviewer"}}, TeamHarnesses: harnesses}

	type role struct {
		ID      string `json:"id"`
		Harness *struct {
			Name    string  `json:"name"`
			Profile *string `json:"profile"`
		} `json:"harness"`
		Model           *string                        `json:"model"`
		ObservedHarness *store.OperatorObservedHarness `json:"observedHarness"`
	}
	read := func() map[string]role {
		t.Helper()
		raw := operatorSchemaGET(t, s, operator, "teams")
		var body struct {
			Teams []struct {
				Roles []role `json:"roles"`
			} `json:"teams"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Teams) != 1 {
			t.Fatalf("teams = %s, %v", raw, err)
		}
		for _, field := range []string{`"harness":`, `"model":`, `"observedHarness":`} {
			if strings.Count(string(raw), field) < len(body.Teams[0].Roles) {
				t.Fatalf("a Role lacks %s: %s", field, raw)
			}
		}
		out := map[string]role{}
		for _, r := range body.Teams[0].Roles {
			out[r.ID] = r
		}
		return out
	}
	roles := read()
	reviewer, builder := roles["reviewer"], roles["builder"]
	if reviewer.Harness == nil || reviewer.Harness.Name != "acp" || strOf(reviewer.Harness.Profile) != "opencode" || strOf(reviewer.Model) != "trusted-model" {
		t.Fatalf("reviewer = %+v", reviewer)
	}
	o := reviewer.ObservedHarness
	if o == nil || o.Version != "1.18.30" || strOf(o.Name) != "acp" || strOf(o.Profile) != "opencode" || o.Agent == nil || o.Agent.Name != "opencode" ||
		time.Since(o.RecordedAt) > time.Minute {
		t.Fatalf("reviewer observed = %+v", o)
	}
	if builder.Harness == nil || builder.Harness.Name != "openhands" || builder.Harness.Profile != nil || builder.Model != nil || builder.ObservedHarness != nil {
		t.Fatalf("builder = %+v, want openhands, no profile, no model and nothing observed", builder)
	}

	s.OperatorConfig.TeamHarnesses = nil
	if r := read()["reviewer"]; r.Harness != nil || r.ObservedHarness == nil {
		t.Fatalf("without PLOEG_TEAM_HARNESSES reviewer = %+v, want no configured harness and the observed one", r)
	}
}

func TestParseTeamHarnessesRefusesWhatIsNotAHarness(t *testing.T) {
	if h, err := ParseTeamHarnesses(""); err != nil || h != nil {
		t.Fatalf("empty = %v, %v", h, err)
	}
	for _, raw := range []string{`[]`, `{"bronze":{"":{"name":"Not A Harness"}}}`, `{"bronze":{"":{"name":"acp","profile":"x y"}}}`, `{"bronze":{"":{}}}`} {
		if _, err := ParseTeamHarnesses(raw); err == nil {
			t.Errorf("%s parsed", raw)
		}
	}
	h, err := ParseTeamHarnesses(`{"bronze":{"":{"name":"exec"}}}`)
	if err != nil || h["bronze"][""].Name != "exec" || h["bronze"][""].Profile != nil {
		t.Fatalf("plan-less team = %+v, %v", h, err)
	}
}
