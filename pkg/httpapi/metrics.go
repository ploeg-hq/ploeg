package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// DefaultMetricsCacheTTL bounds how often a scrape reaches the database when
// Server.MetricsCacheTTL is zero.
const DefaultMetricsCacheTTL = 15 * time.Second

type metricsCache struct {
	mu   sync.Mutex
	at   time.Time
	body []byte
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ttl := s.MetricsCacheTTL
	if ttl == 0 {
		ttl = DefaultMetricsCacheTTL
	}
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	if s.metrics.body == nil || ttl < 0 || time.Since(s.metrics.at) >= ttl {
		m, err := s.Store.OperationalMetrics(r.Context())
		if err != nil {
			s.Log.Error("metrics collection failed", "err", err)
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		s.metrics.body = renderMetrics(m, s.TrackerWebhooks)
		s.metrics.at = time.Now()
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(s.metrics.body)
}

type metricWriter struct{ bytes.Buffer }

func (b *metricWriter) family(name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (b *metricWriter) sample(name string, value float64, labels ...string) {
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(escapeLabel(labels[i+1]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	b.WriteByte('\n')
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(v string) string { return labelEscaper.Replace(v) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func renderMetrics(m store.OperationalMetrics, webhooks *WebhookCoverage) []byte {
	var b metricWriter

	b.family("ploeg_shifts_open", "gauge", "Open Shifts per team.")
	for _, team := range sortedKeys(m.OpenShifts) {
		b.sample("ploeg_shifts_open", float64(m.OpenShifts[team]), "team", team)
	}
	b.family("ploeg_shift_idle_seconds_max", "gauge", "Longest time an open Shift of this team has gone without Run progress (a Run start, finish or checkpoint).")
	for _, team := range sortedKeys(m.ShiftIdleSeconds) {
		b.sample("ploeg_shift_idle_seconds_max", m.ShiftIdleSeconds[team], "team", team)
	}

	b.family("ploeg_leases_expired", "gauge", "Leases whose expiry has passed and that the sweep has not yet released.")
	b.sample("ploeg_leases_expired", float64(m.ExpiredLeases))
	b.family("ploeg_lease_overdue_seconds_max", "gauge", "How long the most overdue Lease has been past its expiry; 0 when none is.")
	b.sample("ploeg_lease_overdue_seconds_max", m.LeaseOverdueSeconds)

	b.family("ploeg_llm_keys_past_ttl", "gauge", "Inference accounts in the issued or unknown state whose key TTL, measured from the Run's start, has passed.")
	for _, state := range sortedKeys(m.KeysPastTTL) {
		b.sample("ploeg_llm_keys_past_ttl", float64(m.KeysPastTTL[state]), "state", state)
	}
	b.family("ploeg_llm_key_ttl_overrun_seconds_max", "gauge", "Largest amount by which an issued or unknown inference account has outlived its key TTL; 0 when none has.")
	for _, state := range sortedKeys(m.KeyTTLOverrunSeconds) {
		b.sample("ploeg_llm_key_ttl_overrun_seconds_max", m.KeyTTLOverrunSeconds[state], "state", state)
	}

	b.family("ploeg_settled_spend_usd_last_hour", "gauge", "Spend settled onto Shifts during the last hour, in USD.")
	b.sample("ploeg_settled_spend_usd_last_hour", m.SettledSpendLastHourUSD)

	b.family("ploeg_runs_failed_total", "counter", "Runs that ended failed, by failure reason. A credential_leak Run carried one of its credentials or its canary to the forge.")
	for _, reason := range sortedKeys(m.FailedRuns) {
		b.sample("ploeg_runs_failed_total", float64(m.FailedRuns[reason]), "reason", reason)
	}

	if webhooks != nil {
		if c, ok := webhooks.snapshot(); ok {
			b.family("ploeg_tracker_webhooks_missing", "gauge", "Configured Vikunja projects whose assignment webhook was not found at the last check.")
			b.sample("ploeg_tracker_webhooks_missing", float64(c.missing))
			b.family("ploeg_tracker_webhooks_unchecked", "gauge", "Configured Vikunja projects the last webhook check could not read.")
			b.sample("ploeg_tracker_webhooks_unchecked", float64(c.failed))
			b.family("ploeg_tracker_webhook_check_timestamp_seconds", "gauge", "Unix time of the last completed webhook coverage check.")
			b.sample("ploeg_tracker_webhook_check_timestamp_seconds", float64(c.at.Unix()))
		}
	}
	return b.Bytes()
}
