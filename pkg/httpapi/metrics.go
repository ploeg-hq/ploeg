package httpapi

import (
	"bytes"
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/apilimit"
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
		s.metrics.body = renderMetrics(m, s.TrackerWebhooks, s.harnessAllowed)
		s.metrics.at = time.Now()
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(s.metrics.body)
	_, _ = w.Write(renderPoolMetrics(s.Store.PoolStat()))
	_, _ = w.Write(renderProviderAPIMetrics(apilimit.Snapshot()))
}

func renderProviderAPIMetrics(counts []apilimit.Counts) []byte {
	var b metricWriter
	b.family("ploeg_provider_api_requests_total", "counter", "Tracker and forge API calls this process sent, by provider, retries included.")
	for _, c := range counts {
		b.sample("ploeg_provider_api_requests_total", float64(c.Requests), "provider", c.Provider)
	}
	b.family("ploeg_provider_api_rate_limited_total", "counter", "Tracker and forge API calls the upstream answered with 429, by provider.")
	for _, c := range counts {
		b.sample("ploeg_provider_api_rate_limited_total", float64(c.RateLimited), "provider", c.Provider)
	}
	b.family("ploeg_provider_api_retries_total", "counter", "Tracker and forge API calls sent again after a 429 or a 503 with Retry-After, by provider.")
	for _, c := range counts {
		b.sample("ploeg_provider_api_retries_total", float64(c.Retries), "provider", c.Provider)
	}
	b.family("ploeg_provider_api_throttled_total", "counter", "Tracker and forge API calls held by the client-side rate limit, by provider; outcome is delayed (waited for a slot) or refused (the wait was too long and the call failed).")
	for _, c := range counts {
		b.sample("ploeg_provider_api_throttled_total", float64(c.Delayed), "provider", c.Provider, "outcome", "delayed")
		b.sample("ploeg_provider_api_throttled_total", float64(c.Throttled), "provider", c.Provider, "outcome", "refused")
	}
	return b.Bytes()
}

func renderPoolMetrics(st *pgxpool.Stat) []byte {
	var b metricWriter
	b.family("ploeg_db_pool_connections", "gauge", "Connections in this process's database pool, by state; their sum is the pool's open connections.")
	b.sample("ploeg_db_pool_connections", float64(st.AcquiredConns()), "state", "acquired")
	b.sample("ploeg_db_pool_connections", float64(st.IdleConns()), "state", "idle")
	b.sample("ploeg_db_pool_connections", float64(st.ConstructingConns()), "state", "constructing")
	b.family("ploeg_db_pool_max_connections", "gauge", "Most connections this process's database pool may open.")
	b.sample("ploeg_db_pool_max_connections", float64(st.MaxConns()))
	b.family("ploeg_db_pool_acquires_total", "counter", "Successful connection acquisitions from the pool.")
	b.sample("ploeg_db_pool_acquires_total", float64(st.AcquireCount()))
	b.family("ploeg_db_pool_acquire_seconds_total", "counter", "Time spent in successful acquisitions, including waits for a free connection.")
	b.sample("ploeg_db_pool_acquire_seconds_total", st.AcquireDuration().Seconds())
	b.family("ploeg_db_pool_empty_acquires_total", "counter", "Successful acquisitions that had to wait because no idle connection was free.")
	b.sample("ploeg_db_pool_empty_acquires_total", float64(st.EmptyAcquireCount()))
	b.family("ploeg_db_pool_empty_acquire_wait_seconds_total", "counter", "Time successful acquisitions spent waiting for a connection to come free.")
	b.sample("ploeg_db_pool_empty_acquire_wait_seconds_total", st.EmptyAcquireWaitTime().Seconds())
	b.family("ploeg_db_pool_canceled_acquires_total", "counter", "Acquisitions abandoned because their context ended first.")
	b.sample("ploeg_db_pool_canceled_acquires_total", float64(st.CanceledAcquireCount()))
	b.family("ploeg_db_pool_new_connections_total", "counter", "Connections the pool has opened.")
	b.sample("ploeg_db_pool_new_connections_total", float64(st.NewConnsCount()))
	b.family("ploeg_db_pool_destroyed_connections_total", "counter", "Connections the pool closed because they reached their maximum lifetime or idle time.")
	b.sample("ploeg_db_pool_destroyed_connections_total", float64(st.MaxLifetimeDestroyCount()), "reason", "lifetime")
	b.sample("ploeg_db_pool_destroyed_connections_total", float64(st.MaxIdleDestroyCount()), "reason", "idle")
	return b.Bytes()
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

func sortedRunOutcomes(m map[store.RunOutcomeKey]int) []store.RunOutcomeKey {
	keys := make([]store.RunOutcomeKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b store.RunOutcomeKey) int {
		return cmp.Or(cmp.Compare(a.Team, b.Team), cmp.Compare(a.Role, b.Role),
			cmp.Compare(a.Outcome, b.Outcome), cmp.Compare(a.Reason, b.Reason))
	})
	return keys
}

func sortedBriefedRuns(m map[store.BriefedRunsKey]int) []store.BriefedRunsKey {
	keys := make([]store.BriefedRunsKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b store.BriefedRunsKey) int {
		return cmp.Or(cmp.Compare(a.Team, b.Team), cmp.Compare(a.Role, b.Role), cmp.Compare(a.Notes, b.Notes))
	})
	return keys
}

func sortedShadowReviews(m map[store.ShadowReviewKey]int) []store.ShadowReviewKey {
	keys := make([]store.ShadowReviewKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b store.ShadowReviewKey) int {
		return cmp.Or(cmp.Compare(a.Team, b.Team), cmp.Compare(a.Role, b.Role),
			cmp.Compare(a.Primary, b.Primary), cmp.Compare(a.Shadow, b.Shadow))
	})
	return keys
}

func (s *Server) harnessAllowed(team, role, harness string) bool {
	h, ok := s.OperatorConfig.TeamHarnesses[team][role]
	return ok && h.Allows(harness)
}

func renderMetrics(m store.OperationalMetrics, webhooks *WebhookCoverage, harnessAllowed func(team, role, harness string) bool) []byte {
	var b metricWriter

	b.family("ploeg_shifts_open", "gauge", "Open Shifts per team.")
	for _, team := range sortedKeys(m.OpenShifts) {
		b.sample("ploeg_shifts_open", float64(m.OpenShifts[team]), "team", team)
	}
	b.family("ploeg_shift_idle_seconds_max", "gauge", "Longest time an open Shift of this team has gone without Run progress (a Run start, finish or checkpoint; a Run that ended failed is no progress).")
	for _, team := range sortedKeys(m.ShiftIdleSeconds) {
		b.sample("ploeg_shift_idle_seconds_max", m.ShiftIdleSeconds[team], "team", team)
	}
	b.family("ploeg_shift_runs_without_pr_max", "gauge", "Most finished Runs in any open Shift of this team that has no recorded pull request.")
	for _, team := range sortedKeys(m.ShiftRunsWithoutPRMax) {
		b.sample("ploeg_shift_runs_without_pr_max", float64(m.ShiftRunsWithoutPRMax[team]), "team", team)
	}
	b.family("ploeg_work_item_oldest_queued_seconds", "gauge", "How long the longest-waiting claimable queued Work Item of this team has waited since it last became queued.")
	for _, team := range sortedKeys(m.OldestQueuedSeconds) {
		b.sample("ploeg_work_item_oldest_queued_seconds", m.OldestQueuedSeconds[team], "team", team)
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

	b.family("ploeg_llm_accounts_unsettled", "gauge", "Inference accounts of finished Runs still holding Shift budget, by account state (reserved, or blocked and awaiting settlement).")
	for _, state := range sortedKeys(m.UnsettledLLMAccounts) {
		b.sample("ploeg_llm_accounts_unsettled", float64(m.UnsettledLLMAccounts[state]), "state", state)
	}

	b.family("ploeg_settled_spend_usd_last_hour", "gauge", "Spend settled onto Shifts during the last hour, in USD.")
	b.sample("ploeg_settled_spend_usd_last_hour", m.SettledSpendLastHourUSD)

	b.family("ploeg_runs_failed_total", "counter", "Runs that ended failed, by failure reason. A credential_leak Run carried one of its credentials or its canary to the forge.")
	for _, reason := range sortedKeys(m.FailedRuns) {
		b.sample("ploeg_runs_failed_total", float64(m.FailedRuns[reason]), "reason", reason)
	}
	b.family("ploeg_runs_finished_total", "counter", "Runs that finished, by team, Role, outcome and, for a failed Run, failure reason.")
	for _, k := range sortedRunOutcomes(m.FinishedRuns) {
		b.sample("ploeg_runs_finished_total", float64(m.FinishedRuns[k]), "team", k.Team, "role", k.Role, "outcome", k.Outcome, "reason", k.Reason)
	}
	b.family("ploeg_runs_without_observed_delivery_last_day", "gauge", "Runs finished in the last day whose report carried no delivery record from the worker (legacy) or one that did not match the Run (mismatch).")
	for _, source := range sortedKeys(m.RunsWithoutObservedDeliveryLastDay) {
		b.sample("ploeg_runs_without_observed_delivery_last_day", float64(m.RunsWithoutObservedDeliveryLastDay[source]), "source", source)
	}
	b.family("ploeg_shadow_reviews_total", "counter", "Finished shadow Runs (ADR-0096), by team, shadow Role, the verdict the Round's acting readers gave (primary) and the shadow's own verdict (shadow); each verdict is approve, request_changes or none.")
	for _, k := range sortedShadowReviews(m.ShadowReviews) {
		b.sample("ploeg_shadow_reviews_total", float64(m.ShadowReviews[k]), "team", k.Team, "role", k.Role, "primary", k.Primary, "shadow", k.Shadow)
	}
	b.family("ploeg_runs_pending_outside_allowlist", "gauge", "Pending Runs that store a Harness the current allowlist of their team and Role no longer contains, so no workload claims them (ADR-0099).")
	for _, p := range m.PendingRunsByHarness {
		if harnessAllowed == nil || !harnessAllowed(p.Team, p.Role, p.Harness) {
			b.sample("ploeg_runs_pending_outside_allowlist", float64(p.Pending), "team", p.Team, "role", p.Role, "harness", p.Harness)
		}
	}
	b.family("ploeg_runs_briefed_total", "counter", "Finished Runs that recorded their briefing (ADR-0124), by team, Role and whether the briefing held any Work Item Note (notes is with or without).")
	for _, k := range sortedBriefedRuns(m.BriefedRuns) {
		b.sample("ploeg_runs_briefed_total", float64(m.BriefedRuns[k]), "team", k.Team, "role", k.Role, "notes", k.Notes)
	}
	b.family("ploeg_run_briefed_notes_total", "counter", "Work Item Notes the finished Runs of a team and Role were briefed with (ADR-0124).")
	for _, k := range sortedBriefedRuns(m.BriefedNotes) {
		b.sample("ploeg_run_briefed_notes_total", float64(m.BriefedNotes[k]), "team", k.Team, "role", k.Role)
	}
	b.family("ploeg_awaiting_review_with_unsuccessful_ci", "gauge", "Work Items in awaiting_review whose pull request's head CI, as last read from the forge, is not success (ADR-0119); stays at 0.")
	b.sample("ploeg_awaiting_review_with_unsuccessful_ci", float64(m.AwaitingReviewWithUnsuccessfulCI))
	b.family("ploeg_work_item_notes_unwritten", "gauge", "Work Item Notes the outbox has not yet written to the Tenant's notes graph.")
	b.sample("ploeg_work_item_notes_unwritten", float64(m.UnwrittenNotes))
	b.family("ploeg_work_item_note_oldest_unwritten_seconds", "gauge", "How long the oldest unwritten Work Item Note has waited for the notes graph; 0 when none waits.")
	b.sample("ploeg_work_item_note_oldest_unwritten_seconds", m.OldestUnwrittenNoteSeconds)

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
