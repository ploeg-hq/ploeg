package httpapi

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	seededTitle   = "Rotate the payroll password SEED-TITLE"
	seededSummary = "SEED-SUMMARY the agent wrote about the customer"
	seededStuck   = "SEED-STUCK-REASON harness said ghp_SEEDsecretSEEDsecretSEEDsecretSEED1234"
	seededFinding = "SEED-FINDING the review comment"
	seededQuery   = "access_token=SEED-LINK-QUERY"
)

func assertLogsCarryNone(t *testing.T, logs string, seeded ...string) {
	t.Helper()
	for _, s := range seeded {
		if strings.Contains(logs, s) {
			t.Errorf("ploegd's log carries %q:\n%s", s, logs)
		}
	}
}

func TestAnOutcomeReportLogsNoTextTheRunWrote(t *testing.T) {
	s, logs := harnessServer(t)
	token := harnessClaim(t, s, `{"team":"bronze","role":"reviewer"}`)
	harnessOutcome(t, s, token, fmt.Sprintf(`{"outcome":"stuck","summary":%q,"stuckReason":%q,"findings":%q,
		"links":["https://forge.example/acme/app/pulls/7?%s"]}`, seededSummary, seededStuck, seededFinding, seededQuery))
	if !strings.Contains(logs.String(), `msg="outcome reported"`) {
		t.Fatalf("no outcome line logged:\n%s", logs)
	}
	assertLogsCarryNone(t, logs.String(), seededSummary, seededStuck, seededFinding, seededQuery, "SEED")
}

func TestATrackerWebhookLogsNoWorkItemTitle(t *testing.T) {
	reset(t)
	var logs bytes.Buffer
	s := withdrawalServer(nil)
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	body := fmt.Sprintf(`{"event_name":"task.assignee.created","data":{"task":{"id":4301,"title":%q,"description":"SEED-DESCRIPTION","project_id":7},"assignee":{"username":"builder"}}}`, seededTitle)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedTrackerHook("vikunja", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), `msg="work item queued"`) {
		t.Fatalf("no queue line logged:\n%s", logs.String())
	}
	assertLogsCarryNone(t, logs.String(), seededTitle, "SEED")
}
