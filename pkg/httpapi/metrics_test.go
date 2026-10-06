package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q is not the Prometheus text format", ct)
	}
	return rec.Body.String()
}

func requireLines(t *testing.T, body string, lines ...string) {
	t.Helper()
	have := map[string]bool{}
	for _, l := range strings.Split(body, "\n") {
		have[l] = true
	}
	for _, l := range lines {
		if !have[l] {
			t.Errorf("missing line %q in:\n%s", l, body)
		}
	}
}

func TestMetricsExposeAlertingStateFromTheDatabase(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftID := shiftFixture(t, "901", 5, []store.Role{{Name: "builder", Writes: true, Cap: 1}})
	if _, err := testPool.Exec(ctx, `UPDATE shifts SET opened_at = now() - interval '3 hours' WHERE id = $1`, shiftID); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil || run == nil {
		t.Fatalf("claim: %v %v", run, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runs SET started_at = now() - interval '2 hours' WHERE run_token = $1`, run.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE leases SET expires_at = now() - interval '1 hour' WHERE run_token = $1`, run.RunToken); err != nil {
		t.Fatal(err)
	}

	coverage := &WebhookCoverage{}
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), TrackerWebhooks: coverage, MetricsCacheTTL: -1}
	body := scrape(t, s.Handler())
	requireLines(t, body,
		"# TYPE ploeg_shifts_open gauge",
		`ploeg_shifts_open{team="bronze"} 1`,
		"ploeg_leases_expired 1",
		`ploeg_llm_keys_past_ttl{state="issued"} 0`,
		`ploeg_llm_keys_past_ttl{state="unknown"} 0`,
		"ploeg_settled_spend_usd_last_hour 0",
		"# TYPE ploeg_runs_failed_total counter",
		`ploeg_runs_failed_total{reason="credential_leak"} 0`,
		`ploeg_runs_failed_total{reason="lease_lost"} 0`,
		`ploeg_runs_without_observed_delivery_last_day{source="legacy"} 0`,
		`ploeg_runs_without_observed_delivery_last_day{source="mismatch"} 0`,
	)
	for _, prefix := range []string{`ploeg_shift_idle_seconds_max{team="bronze"} 7`, "ploeg_lease_overdue_seconds_max 3"} {
		if !strings.Contains(body, "\n"+prefix) {
			t.Errorf("expected a sample starting %q in:\n%s", prefix, body)
		}
	}
	if strings.Contains(body, "ploeg_tracker_webhooks_missing") {
		t.Fatal("webhook coverage reported before the first check completed")
	}

	coverage.Record(4, []string{"5", "7"}, []string{"9"}, time.Unix(1700000000, 0))
	requireLines(t, scrape(t, s.Handler()),
		"# TYPE ploeg_tracker_webhooks_missing gauge",
		"ploeg_tracker_webhooks_missing 2",
		"ploeg_tracker_webhooks_unchecked 1",
		"ploeg_tracker_webhook_check_timestamp_seconds 1.7e+09",
	)
}

func TestMetricsCacheBoundsDatabaseReads(t *testing.T) {
	reset(t)
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), MetricsCacheTTL: time.Hour}
	h := s.Handler()
	requireLines(t, scrape(t, h), "ploeg_leases_expired 0")
	ingestAndLeaseExpired(t)
	requireLines(t, scrape(t, h), "ploeg_leases_expired 0")

	s.MetricsCacheTTL = -1
	requireLines(t, scrape(t, h), "ploeg_leases_expired 1")
}

func ingestAndLeaseExpired(t *testing.T) {
	t.Helper()
	shiftFixture(t, "902", 5, []store.Role{{Name: "builder", Writes: true, Cap: 1}})
	ctx := context.Background()
	if _, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE leases SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
}

func TestMetricLabelValuesAreEscaped(t *testing.T) {
	body := string(renderMetrics(store.OperationalMetrics{
		OpenShifts: map[string]int{"a\"b\\c\nd": 1},
	}, nil))
	requireLines(t, body, `ploeg_shifts_open{team="a\"b\\c\nd"} 1`)
}

func TestMetricsExposeRunOutcomesByTeamAndRole(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftFixture(t, "903", 5, []store.Role{{Name: "builder", Writes: true, Cap: 1}})
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil || run == nil {
		t.Fatalf("claim: %v %v", run, err)
	}
	reason := string(work.FailureIdle)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeFailed, "killed", "", nil, nil, &reason)); err != nil {
		t.Fatal(err)
	}
	queued, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "904", Team: "bronze", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE work_items SET updated_at = now() - interval '2 hours' WHERE id = $1`, queued); err != nil {
		t.Fatal(err)
	}

	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), MetricsCacheTTL: -1}
	body := scrape(t, s.Handler())
	requireLines(t, body,
		"# TYPE ploeg_runs_finished_total counter",
		`ploeg_runs_finished_total{team="bronze",role="builder",outcome="failed",reason="idle"} 1`,
		`ploeg_runs_finished_total{team="bronze",role="builder",outcome="failed",reason="timeout"} 0`,
		`ploeg_runs_finished_total{team="bronze",role="builder",outcome="failed",reason="infra_node"} 0`,
		`ploeg_runs_failed_total{reason="idle"} 1`,
		"# TYPE ploeg_shift_runs_without_pr_max gauge",
		`ploeg_shift_runs_without_pr_max{team="bronze"} 1`,
		"# TYPE ploeg_llm_accounts_unsettled gauge",
		`ploeg_llm_accounts_unsettled{state="blocked"} 0`,
		`ploeg_llm_accounts_unsettled{state="reserved"} 0`,
		"# TYPE ploeg_work_item_oldest_queued_seconds gauge",
	)
	if !strings.Contains(body, "\n"+`ploeg_work_item_oldest_queued_seconds{team="bronze"} 7`) {
		t.Errorf("expected the queued item's two-hour wait in:\n%s", body)
	}
}

func TestRunOutcomeSeriesAreSortedAndCarryAnEmptyReasonForNonFailures(t *testing.T) {
	body := string(renderMetrics(store.OperationalMetrics{
		FinishedRuns: map[store.RunOutcomeKey]int{
			{Team: "silver", Role: "reviewer", Outcome: "no_change_needed"}:             2,
			{Team: "bronze", Role: "builder", Outcome: "pr_opened"}:                     1,
			{Team: "bronze", Role: "builder", Outcome: "failed", Reason: "agent_error"}: 3,
		},
	}, nil))
	want := `ploeg_runs_finished_total{team="bronze",role="builder",outcome="failed",reason="agent_error"} 3
ploeg_runs_finished_total{team="bronze",role="builder",outcome="pr_opened",reason=""} 1
ploeg_runs_finished_total{team="silver",role="reviewer",outcome="no_change_needed",reason=""} 2
`
	if !strings.Contains(body, want) {
		t.Fatalf("run outcome series out of order or mislabelled:\n%s", body)
	}
}
