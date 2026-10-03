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
