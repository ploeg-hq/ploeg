# Observability, and decisions without code

Date: 2026-10-05. Source: ploeg `development` at `34e6d57`, unfold `development` at `e7d1588`, homelab-cluster `main`. Read-only; the live cluster was not queried.

The owner asked:
1. Do we have proper observability?
2. What else would add useful features or remove bottlenecks?

The observability decision is [ADR-0076](../adrs/0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md). The other findings are ranked for the board, because priorities live on the tracker.

## 1. Observability: partly

Spend, keys, Leases and credentials are well covered. Run liveness and outcome quality are not, and those are what Work Item 138 needed.

### What exists

**Metrics**
* **ploegd** exports 12 hand-rendered families (`pkg/httpapi/metrics.go:86-131`, `pkg/store/metrics.go:55`). It has:
  * shift idle time, lease and key overruns;
  * settled spend in the last hour;
  * `ploeg_runs_failed_total{reason}`, with no `team` or `role` label;
  * tracker webhook counters.

  It has no HTTP latency, claim, launch or outcome counters. Scraping goes through the chart ServiceMonitor.
* **A second source** is a postgres-exporter in homelab-cluster (`kubernetes/apps/ploeg/ploeg/app/exporter/`). It runs SQL for Work Item counts and ages, Run counts by Team and outcome, stuck reasons, Run durations and checkpoints. Its header comment, which says ploegd ships no `/metrics`, is stale. Lease state is exported twice, once from each source.

**Alerts**
* **The chart PrometheusRule** covers:
  * Shift stuck over 6 h;
  * Lease expired;
  * key past TTL (disabled in the homelab);
  * a spend spike over US$ 20 an hour;
  * a missing tracker webhook;
  * a credential leak.
* **Grafana-managed rules** cover a stuck Lease, the oldest queued item over 900 s, failed worker Jobs, and blocked or outliving keys.
* **Routing:** both go through vmalertmanager to ntfy, with critical and warning topics, a 24 h digest for failed Jobs, and a healthchecks deadman.

**Logs**
* ploegd and the worker log with slog in logfmt. Alloy ships every pod's stdout, Kata sandboxes included, to VictoriaLogs for 30 days.

**Traces**
* Ploeg has none.
* LiteLLM exports OTel spans to VictoriaTraces. Its spans carry no Run key, so per-Run trace filtering is a known gap in the run-explorer dashboard.

**Dashboards**
* `glide.generated`, `glide-runs.generated`, `glide-loop` and six `dark-factory-*` dashboards.
* Every `ploeg_*` and `litellm_*` metric they query exists. ploegd's own families are barely used, and no panel plots `ploeg_runs_failed_total`.

**SLOs**
* Sloth is installed, but no Ploeg or LiteLLM SLO is defined. The Grafana "SLO" rules are thresholds, with no objective or burn rate.

**Other components**

| Component | State |
| --- | --- |
| LiteLLM | Scraped; no error-rate, latency or served-model alert |
| agent-sandbox controller | Scraped; no alerts |
| KEDA | Scraped; no alerts |
| CNPG (ploeg-db) | Shared backup, WAL and restore-test rules |
| Unfold | `/healthz`, `/readyz` and JSON error lines; no metrics, no logs around Ploeg calls, no frontend error capture |

### Gaps, ranked

1. **Nothing catches repeated idle or timeout kills.**
   * `PloegShiftStuck` counts a finished Run as progress (`pkg/store/metrics.go:83-87`), so twelve kills reset it every 45 minutes.
   * The failed-Job rule never fires, because a killed Run reports its Outcome and its Job exits 0.
   * `ploeg_runs_failed_total{reason="idle"}` has no `team` label, so "two idle kills per Team" (incident ticket 11) cannot be written.
2. **"Ended without a pull request" looks like success.** There is no outcome metric by Role, so a builder exiting 0 with no PR is invisible.
3. **Phase latency can only be computed in SQL.** Each phase has a timestamp source, but none is exported as a metric:
   * queued: `work_items.created_at`;
   * claimed: `agent_runs.started_at`;
   * sandbox ready: `kube_pod_status_ready_time`;
   * first model call: LiteLLM;
   * pull request: `pull_requests.first_seen_at`;
   * merge: `merged_at`.

   The 56-minute wait behind one builder slot in WI-138 is invisible.
4. **Correlation is broken.**
   * Alloy maps `trace_id` from a `trace-id` key, but the worker logs `trace=` (`pkg/worker/worker.go:172,532-587`).
   * Only the "claimed" line carries trace, Shift, Round and Role; "run finished" (`worker.go:197`) does not.
   * ploegd names the Work Item `id` in some lines and `work_item` in others, and carries no Run id on its claim and outcome lines.
5. **A Run's last messages live only in the logs of a deleted pod** (incident ticket 5).
6. **LiteLLM error rate and served-model mismatch are not alerted** (VIK-1408). `agent_runs.usage.models` exists but is not exported.
7. **Sandbox start failures and unschedulable pods are not alerted.** Panels exist; rules do not.
8. **Some backlogs are not exported.** `UnsettledLLMAccounts` (`pkg/store/llm_settlement.go:29`) has no gauge, nor do publication operations in `reserved` or `unknown`. Webhook lag and failures are not measured.
9. **Spend has only an absolute spike alert.** It has no burn rate against a Shift pool or an operator budget.
10. **Unfold has no observability of its own.**

### Alerts that would have caught WI-138

| Alert | Expression sketch | Would have fired |
| --- | --- | --- |
| PloegRunsIdleKilled | `sum by (team) (increase(ploeg_runs_finished_total{outcome="failed",reason=~"idle\|timeout"}[2h])) >= 2`. Stopgap today, without `team`: `increase(ploeg_runs_failed_total{reason=~"idle\|timeout"}[2h]) >= 2` | about 07:20 |
| PloegSandboxStartFailing | `increase(ploeg_runs_failed_total{reason="infra_node"}[30m]) >= 1`, or `max(kube_pod_status_unschedulable{namespace="ploeg"}) > 0` for 5m. After ADR-0072: a reservation waiting for capacity past a threshold | about 06:09 |
| PloegShiftNoDelivery | `ploeg_shift_runs_without_pr_max{team}`: a Shift open over 2 h with 3 or more finished Runs and no PR | about 08:10 |
| PloegWriterNoPR | `increase(ploeg_runs_finished_total{role="builder",outcome="no_change_needed"}[6h]) >= 1`, to the digest | at Run 200 |
| PloegQueueWaitHigh | `max by (team) (ploeg_work_items_oldest_age_seconds{state="queued"}) > 3600`, replacing the noisier 900 s rule | about 06:58 |
| LiteLLMErrorRate | failed over total requests above 5% for 15 m (confirm the native metric names live) | — |
| PloegServedModelMismatch | `ploeg_runs_model_mismatch_last_day{team} > 0` | — |
| PloegSettlementBacklog | `ploeg_llm_accounts_unsettled{state="finished"} > 0` for 1 h | — |

One live check is worth making: did the Grafana "queue not draining" rule fire during WI-138, while the item sat queued from 05:58 to 07:25? If it fired and was ignored, the problem is noise, not coverage.

### OpenTelemetry

Not first.

* The worker sandbox's egress is limited to the model gateway and the forge. The `ploeg` namespace has no route to the OTLP collector, so tracing the worker would mean widening sandbox egress or relaying through ploegd.
* Fixing log keys, adding labelled counters, and building alerts on them gets most of the value for little work.
* OTel in ploegd comes second: one span per Run with the `ploeg-<12hex>` alias as an attribute, and spans for claim, launch and publish. LiteLLM spans tagged with the same alias then join in VictoriaTraces.

## 2. Decisions accepted but without code

| Decision | State | Value | Effort |
| --- | --- | --- | --- |
| [ADR-0044](../adrs/0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md): restart from a chosen Round | Accepted; no route (only cancel, approve and reject exist) | Shift 118 re-ran the builder (US$ 0,58, 35 min) to get a review worth at most US$ 0,40. It also unblocks ADR-0036's "answer and continue" | S–M |
| [ADR-0070](../adrs/0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md): ready for review only when checks passed | Accepted. It reads the worker's own verification (`pkg/shiftengine/engine.go:271-284`), which in the homelab is gofmt only | `awaiting_review` today means "gofmt passed" | S–M |
| Repair on failed checks (`repairFailedChecks`) | Configured (`maxRepairs: 2`), but it waits for a `ForgeCheckFailed` webhook Forgejo cannot send (`pkg/httpapi/forge_followup.go:15,38`) | The repair loop never fires on Forgejo. Polling commit status on the heads of open Ploeg pull requests (`provider.ReadCommitStatus` exists) would make it work and would hold review until CI finishes | S–M |
| [ADR-0037](../adrs/0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md): registry egress proxy | Accepted; nothing in the chart | Real tests inside the Run, instead of gofmt; reviewers stop reporting egress failures as defects | M–L |
| Unfold ADR-0011: read-first MCP server | Accepted; not built | The owner's coding sessions can ask "what is stuck and why" without the UI | S–M |
| Planner Role | Exists (`pkg/plan/plan.go:34-37`), but can only split or clarify, never "ready, carry on" | No Team can put a cheap readiness check in front of the builder | M |
| Ploeg Bench | The board and route exist; there is no trial driver or grader | No regression gate for prompt or harness changes | M |

## 3. Other opportunities, ranked

1. **Finish ADR-0044 and give stuck Runs an answer path.** Covered in §2. Ploeg owns the route; Unfold owns the button.
2. **Gate review on forge CI and poll status where webhooks are missing.** Covered in §2. Ploeg.
3. **A readiness verdict before paid builder work.**
   * Give the planner a `proceed` verdict, and add a per-board dispatch guard on the `agent-ready` label (backlog #122).
   * A flash-model read costs about US$ 0,05–0,10. It replaces builder Runs of US$ 0,42–0,58 and 35–45 minutes that end in a question, as Run 200 did after 201 messages.
   * Enable it per board. Ploeg.
4. **Keep each Run's last messages, and pause a Team after two idle kills.** Incident tickets 5 and 11. Ploeg, plus a rule in homelab-cluster.
5. **A scorecard per Team, Role and model.**
   * It shows pass rate, fix rounds caused, cost per merged PR and time to PR, from the existing Run list (`runListItem` carries models, outcome, verdict, cost and duration).
   * It is a ranking only below about 20 samples.
   * Unfold, under ADR-0074.
6. **The benchmark's free part as a CI gate.**
   * The L1 conformance check is 17 SQL assertions on the `exec` adapter, with no model calls, in `go test`.
   * Mode B nightly (seeded defects, n=5) follows, at about €5–10 a night.
7. **A GitHub forge and Issues provider.**
   * Ploeg's home and backlog are on GitHub ([ADR-0062](../adrs/0062-github-is-the-independent-project-home.md)), but no provider exists, so Ploeg cannot work on itself. Since 2026-09-01, 87 of 90 commits are the owner's.
   * The GitLab change shows the worker's forge gap is two places: the PR poll and the briefing.
   * Ploeg.
8. **A cost forecast at admission, and a daily digest.**
   * Forecast: median and p90 of settled spend and duration per Team and Role.
   * Digest: one ntfy message a day with ready PRs, items needing a person, spend against pools, and failures.
   * Unfold, after RFC-0004.
9. **The read-first MCP server.** Covered in §2. Unfold.
10. **The registry egress proxy.** Covered in §2. It overlaps with the per-repository seed image for known repositories. Ploeg chart and homelab-cluster.

Ranked lower:
* **Alerts filed as tickets.** In homelab-cluster, never auto-assigned.
* **Ploeg repairing Renovate or other pull requests it did not open.** Declined: Follow-Ups resolve only Ploeg's own pull requests, which limits prompt injection (backlog #9).
* **Auto-merge or a merge queue.** Declined: Unfold never merges. A forge-native auto-merge switched on by the approving person keeps the human gate.
* **Preview environments.** Large; mostly homelab-cluster work for the agency phase.
* **Steering a running unattended Run.** After ADR-0075 and the ADR-0044 route.

## Homelab-cluster follow-ups

These are not in this repository. All are GitOps edits:
* Map `trace_id` from the `trace` key, and promote `run`, `shift`, `role` and `work_item` (`kubernetes/apps/observability/alloy-agent/app/helmrelease.yaml:153-174`).
* Add rules for LiteLLM, agent-sandbox and KEDA, and the panels in §1.
* Remove the stale exporter header and the duplicate lease queries.
