# Metrics and alerts

ploegd serves Prometheus metrics at `GET /metrics` on its HTTP port, next to `/healthz` and `/readyz`. The alerts below are an optional `PrometheusRule` in the [chart](../../ops/helm/ploeg/values.yaml). Enabling it does not change what ploegd does. It only tells you when something has gone wrong.

## How the metrics are produced

The gauges are computed from the database when Prometheus scrapes them. They are not counters kept in memory, so a ploegd restart does not reset them, and every replica reports the same values. Each result is reused for `PLOEG_METRICS_CACHE_TTL` (default `15s`) so frequent scrapes do not add database load. A negative value turns the cache off. If the database cannot be read, `/metrics` answers `503`, the scrape fails, and Prometheus sets `up` to `0` for the target. Every alert below goes quiet while that is happening, so alert on `up == 0` for this job as well.

The exposition is written by hand in the Prometheus text format (`version=0.0.4`). The Prometheus Go client is not a dependency of Ploeg, and a handful of gauges read from SQL does not need a registry, collectors or the process metrics the client would add.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `ploeg_shifts_open` | `team` | Open Shifts |
| `ploeg_shift_idle_seconds_max` | `team` | Longest time an open Shift of the team has gone without Run progress |
| `ploeg_shift_runs_without_pr_max` | `team` | The most finished Runs in any open Shift of the team that has no recorded pull request. Present at `0` for every team with an open Shift |
| `ploeg_work_item_oldest_queued_seconds` | `team` | How long the team's longest-waiting claimable queued Work Item has waited since it last became queued or its backoff ended. Absent for a team with nothing claimable |
| `ploeg_leases_expired` | | Leases past their expiry that the sweep has not released yet |
| `ploeg_lease_overdue_seconds_max` | | How long the most overdue Lease has been expired, or `0` |
| `ploeg_llm_keys_past_ttl` | `state` | Inference Accounts in `issued` or `unknown` whose key TTL has passed |
| `ploeg_llm_key_ttl_overrun_seconds_max` | `state` | How far past its TTL the oldest such account is, or `0` |
| `ploeg_llm_accounts_unsettled` | `state` | Inference Accounts of finished Runs that still hold Shift budget: `reserved`, or `blocked` and waiting for settlement. Both states are always present |
| `ploeg_settled_spend_usd_last_hour` | | Spend settled onto Shifts in the last hour, in USD |
| `ploeg_runs_finished_total` | `team`, `role`, `outcome`, `reason` | Runs ever finished, by Team, Role and outcome. `reason` is the failure reason of a `failed` Run and empty otherwise. A counter. Every Team and Role with a Run carries `outcome="failed"` for every known reason, at `0` until a Run fails that way. An outcome or reason Ploeg does not know is reported as `other` |
| `ploeg_runs_failed_total` | `reason` | Runs ever recorded failed with each failure reason. A counter: Runs are never deleted. Every known reason is present at `0`. Kept for one release beside `ploeg_runs_finished_total`, which replaces it ([ADR-0076](../adrs/0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md)); move dashboards and rules to `sum by (reason) (ploeg_runs_finished_total{outcome="failed"})` |
| `ploeg_runs_without_observed_delivery_last_day` | `source` | Runs finished in the last day whose report had no delivery record from the worker (`legacy`) or one that named another forge, repository, branch or pull request (`mismatch`) |
| `ploeg_tracker_webhooks_missing` | | Configured Vikunja projects with no assignment webhook to Ploeg |
| `ploeg_tracker_webhooks_unchecked` | | Configured Vikunja projects the webhook check could not read |
| `ploeg_tracker_webhook_check_timestamp_seconds` | | Unix time of the last webhook check |

Run progress means a Run of the Shift starting or finishing, or a checkpoint on its Work Item. A Run that ended `failed` made no progress, so neither its start nor its finish counts: a Shift whose Runs are killed one after another stays idle. Opening the Shift also counts. Lease and deadline renewals do not count, because a hung harness keeps renewing. A key's TTL is measured from the Run's start, which comes a few seconds before the key is minted. `ploeg_runs_failed_total{reason="credential_leak"}` counts Runs whose forge traffic, pushed commits or proposed learnings carried one of the Run's credentials or its canary. Every increase needs a person: rotate the credential and read the commits the Run left on its branch. [PloegCredentialLeak](#ploegcredentialleak) alerts on it. Settled spend is the sum of trusted reconciliation deltas (`llm.reconciled` in the audit log) plus the `costUsd` that finished Shift Runs without a managed Inference Account reported. A managed Run's own cost report is not counted, because it is not settlement. A Shift's pull request is recorded when Ploeg stores the forge's facts about it: a row in `pull_requests` for the Shift, or an open one for its Work Item. Every finished Run counts toward `ploeg_shift_runs_without_pr_max`, readers included. A queued Work Item's wait starts at its last state change (`work_items.updated_at`), so a Work Item that goes back to `queued` after a failed Run starts waiting again; an assignment webhook redelivery also resets it. `ploeg_llm_accounts_unsettled` uses the predicate of the settlement sweep without its quiet period, so a `blocked` account appears for up to `PLOEG_LLM_SETTLE_AFTER` in normal operation. A `legacy` Run comes from a worker older than [ADR-0059](../adrs/0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md); when the gauge has read zero for two weeks, the older-worker path can go. A `mismatch` Run is a worker or caller reporting delivery for something other than its own branch; it parks the Work Item for a person. The three tracker webhook series appear only once a webhook check has finished, and only when the Vikunja URL and token are configured. See [tracker configuration](board.md).

`/metrics` has no authentication, the same as `/readyz`. It exposes team names and aggregate spend. It exposes no tokens, Work Item titles or per-Run data. If the ploegd port is reachable from outside the cluster, block the path at that ingress.

## Enable the alerts

Both objects need the Prometheus Operator CRDs (`monitoring.coreos.com/v1`), and both are off by default:

```yaml
monitoring:
  serviceMonitor:
    enabled: true
    labels: {release: kube-prometheus-stack}   # whatever your Prometheus selects on
  prometheusRule:
    enabled: true
    labels: {release: kube-prometheus-stack}
    runbookBaseUrl: https://docs.example/ploeg/ops/alerts/
```

Every alert matches ploegd's series with `job="<release>",namespace="<release namespace>"`. The chart's ServiceMonitor produces exactly those labels. If you scrape some other way, set `monitoring.prometheusRule.selector` to your own matchers, such as `job="ploegd"`. Every alert has `enabled`, `for` and `severity`, and the thresholds are listed with each alert below. [`ci/monitoring-values.yaml`](../../ops/helm/ploeg/ci/monitoring-values.yaml) renders every alert, and its golden shows the output.

The SQL below is for `psql` against the Ploeg database. It only reads.

## PloegShiftStuck

`max by (team) (ploeg_shift_idle_seconds_max) > idleHours × 3600`. Default: 6 hours, `for: 15m`, warning.

A Shift has been open for longer than the threshold without any Run starting, finishing or checkpointing. Usually its next Run is pending and no worker claims it: the executor is not scaling for that team and Role, or pods are failing before they claim. It can also be a Run that is running and renewing but doing nothing.

A Shift whose Runs keep failing does not reset the clock, because a failed Run is not progress. [PloegRunsIdleKilled](#ploegrunsidlekilled) and [PloegRunsFailingRepeatedly](#ploegrunsfailingrepeatedly) usually fire first in that case.

**Check first:** find the Shift and the state of its Runs.

```sql
SELECT sh.id, sh.work_item_id, sh.round, r.role, r.state, r.started_at, r.expires_at
FROM shifts sh LEFT JOIN agent_runs r ON r.shift_id = sh.id
WHERE sh.closed_at IS NULL AND sh.team = '<team>'
ORDER BY sh.opened_at, r.id;
```

If the Runs are `pending`, look at that team's ScaledJob or CronJob and its recent Jobs. If one is `running`, look at the worker pod's logs. See [managed workers](managed-workers.md) for recovery.

## PloegLeaseExpired

`max(ploeg_lease_overdue_seconds_max) > overdueSeconds`. Default: 300 s, `for: 5m`, warning.

A Lease is past its expiry and has not been released. The sweep (`PLOEG_SWEEP_INTERVAL`, default `15s`) normally releases an expired Lease within one interval, blocks the Run's inference key and revokes its push credential. An overdue Lease means the sweep is not running or is failing. Until it recovers, the Work Item stays locked and the Run may keep its credentials.

**Check first:** ploegd's logs for `lease sweep failed` or `run sweep failed`, and whether the ploegd pod is running at all. Then list the stale Leases:

```sql
SELECT work_item_id, team, run_token, expires_at, renewed_at FROM leases WHERE expires_at < now() ORDER BY expires_at;
```

## PloegModelKeyPastTTL

`max by (state) (ploeg_llm_key_ttl_overrun_seconds_max) > graceSeconds`. Default: 900 s, `for: 5m`, critical.

An Inference Account is still `issued` or `unknown` after its key's TTL has passed. Normally a finished or expired Run moves its account to `blocked` and then `reconciled`. `issued` means the block was never recorded. `unknown` means Ploeg does not know whether a key was minted. The block sweep retries these accounts. It moves an account to `blocked` once the gateway confirms that the key is blocked, or that it holds no key for the account: no key carries the account's alias in `/key/list`, and `/key/info` answers `404` for the recorded key identity. The `llm.blocked` audit event records that answer as `evidence` with `gatewayKeyAbsent: true`. An account that stays in these states therefore has a key the gateway still holds, or a gateway that cannot answer. Ploeg cannot confirm that such a key is unusable. The settlement sweep never settles these states, so the account also keeps its budget hold until it is blocked or reconciled.

**Check first:** find the accounts, then check each alias on the gateway.

```sql
SELECT a.run_token, a.alias, a.state, a.gateway_key_id, a.ttl_seconds, r.started_at, r.state AS run_state
FROM run_llm_accounts a JOIN agent_runs r USING (run_token)
WHERE a.state IN ('issued', 'unknown')
  AND r.started_at + make_interval(secs => a.ttl_seconds) < now();
```

Look each alias up in LiteLLM. Then follow [reconcile uncertainty](managed-workers.md#reconcile-uncertainty): confirm the block through the controller, keep the alias and its spend logs, and do not delete the key by hand, because its spend logs are the settlement evidence.

## PloegSpendSpike

`max(ploeg_settled_spend_usd_last_hour) > usdPerHour`. Default: 20 USD, `for: 0m`, warning.

More spend was settled in the last hour than the threshold. This counts settled spend, so it lags live gateway spend by the settlement quiet period (`PLOEG_LLM_SETTLE_AFTER`, default `15m`). Use the gateway's own budget alerts for real-time limits. One late reconciliation of a large blocked account can also trigger it, and so can a correction that charges late spend-log entries inside `PLOEG_LLM_CORRECTION_WINDOW`.

**Check first:** which Runs the spend came from.

```sql
SELECT l.at, l.work_item_id, (l.detail->>'delta')::numeric AS usd, l.detail->>'evidence' AS evidence
FROM audit_log l WHERE l.action = 'llm.reconciled' AND l.at > now() - interval '1 hour'
ORDER BY usd DESC;
```

If one Work Item or team accounts for most of it, compare that Shift's `budget` and `spent` with the team's budget in the chart values. If the threshold is simply below your normal hourly load, raise `usdPerHour`.

## PloegTrackerWebhookMissing

`max(ploeg_tracker_webhooks_missing) > 0`. Default: `for: 15m`, warning.

At the last hourly check, at least one configured Vikunja project had no webhook sending `task.assignee.created` to Ploeg. This is the same count `/readyz` reports as `vikunjaWebhooks.missingProjects`. Assignments on those boards never reach Ploeg, and nothing else will tell you.

**Check first:** ploegd's warning log from the check, which names each project. Then add the webhook in Vikunja, or set `PLOEG_VIKUNJA_WEBHOOK_REGISTER=true` as described in [tracker configuration](board.md). A non-zero `ploeg_tracker_webhooks_unchecked` means the check could not read some projects. That is usually a token without access to them.

## PloegCredentialLeak

`max(increase(ploeg_runs_failed_total{reason="credential_leak"}[15m])) > 0`. Default: `for: 0m`, critical.

A Run ended `failed` with `credential_leak`: the forge proxy saw the Run's real model key, real forge token or canary in a forge API request, or the worker found one in the commits the Run pushed. The worker refused the request and every later forge write of the Run, and the Run reported no pull request. The commits it had already pushed stay on its branch.

**Check first:** ploegd's ERROR line `credential leak: the Run's output carried one of its credentials`, which names the Run's trace, the Work Item, the credential kind and the route. Rotate that Run's credentials: revoke its model key in LiteLLM if it is still live, and its forge token (a per-Run push token is revoked when the Run settles; the shared builder token is not). Then read the commits on the Run's branch and any pull request it opened before the leak, and delete or close them once you have what you need.

```sql
SELECT r.id, r.finished_at, w.external_id, r.summary
FROM agent_runs r JOIN work_items w ON w.id = r.work_item_id
WHERE r.failure_reason = 'credential_leak'
ORDER BY r.finished_at DESC LIMIT 10;
```

## PloegRunsIdleKilled

`sum by (team) (increase(ploeg_runs_finished_total{outcome="failed",reason=~"idle|timeout"}[window])) >= kills`. Default: 2 kills in `2h`, `for: 0m`, warning.

At least two Runs of the team were stopped by the harness watchdog: `idle` is `PLOEG_HARNESS_IDLE_TIMEOUT` (no output and no model call), `timeout` is `PLOEG_HARNESS_TIMEOUT` (total time). Each kill throws away the Run's work and its spend, and the Round retries the Role. In [Work Item 138](../research/2026-09-29-incident-work-item-138.md), a harness that printed nothing while it worked died at every attempt.

**Check first:** whether the killed Runs were busy or hung. Their model calls in the gateway's spend logs (the alias is the Run's trace) show a busy Run. Then decide between a longer timeout for that Team and Role, and pausing the Team.

```sql
SELECT r.id, r.work_item_id, r.role, r.failure_reason, r.started_at, r.finished_at - r.started_at AS lived, r.summary
FROM agent_runs r
WHERE r.team = '<team>' AND r.failure_reason IN ('idle', 'timeout') AND r.finished_at > now() - interval '2 hours'
ORDER BY r.finished_at;
```

## PloegRunsFailingRepeatedly

`sum by (team, role) (increase(ploeg_runs_finished_total{outcome="failed"}[window])) >= failures`. Default: 3 failures in `1h`, `for: 0m`, critical.

One Role of one team failed three or more Runs within the hour, whatever the reason. Each Shift retries a failed Role up to its attempt limit, so repeated failure burns attempts and spend across every Work Item the Role touches.

**Check first:** the reasons. `sum by (reason) (increase(ploeg_runs_finished_total{team="<team>",role="<role>",outcome="failed"}[1h]))` splits them. `infra_node`, `infra_llm` and `lease_lost` point at the cluster or the gateway; `agent_error`, `budget`, `idle` and `timeout` point at the harness, its model or its limits. Read the newest Run's summary:

```sql
SELECT r.id, r.work_item_id, r.failure_reason, r.finished_at, r.summary
FROM agent_runs r
WHERE r.team = '<team>' AND r.role = '<role>' AND r.outcome = 'failed' AND r.finished_at > now() - interval '1 hour'
ORDER BY r.finished_at DESC;
```

## PloegSandboxStartFailing

`sum by (team) (increase(ploeg_runs_finished_total{outcome="failed",reason="infra_node"}[window])) > 0`. Default: `30m`, `for: 0m`, warning.

A Run of the team failed with `infra_node`: its sandbox or pod did not become ready within `PLOEG_SANDBOX_START_TIMEOUT`, or the node failed under it. The launcher fails whichever Run of the team and Role is pending, so the failed Run may belong to another Work Item than the one that waited. These failures use the infrastructure retry budget, not the agent's attempts, so the cost is time.

**Check first:** whether the team's sandbox pods schedule. Look for `Pending` pods and `FailedScheduling` events in the worker namespace, and compare the Run's CPU and memory requests with free node capacity.

## PloegShiftNoDelivery

`max by (team) (ploeg_shift_runs_without_pr_max) >= runs`. Default: 3 Runs, `for: 15m`, warning.

An open Shift of the team has finished three or more Runs and has no recorded pull request. Retries, killed builders and reviewers of a branch that was never pushed all count. A builder that exits without a pull request is recorded `no_change_needed`, so this can also be a Shift whose Runs all look successful.

**Check first:** the Shift's Runs and their outcomes.

```sql
SELECT sh.id AS shift, sh.work_item_id, r.id, r.round, r.role, r.outcome, r.failure_reason, r.summary
FROM shifts sh JOIN agent_runs r ON r.shift_id = sh.id
WHERE sh.closed_at IS NULL AND sh.team = '<team>' AND r.state = 'finished'
  AND NOT EXISTS (SELECT 1 FROM pull_requests p WHERE p.shift_id = sh.id
      OR (p.work_item_id = sh.work_item_id AND COALESCE(p.state, 'open') = 'open'))
ORDER BY sh.id, r.id;
```

If a pull request exists on the forge but not here, the forge's facts did not reach Ploeg; check the forge webhook. Otherwise cancel the Shift or give the Work Item to a person.

## PloegQueueStalled

`max by (team) (ploeg_work_item_oldest_queued_seconds) > waitSeconds`. Default: 3600 s, `for: 10m`, warning.

A claimable Work Item of the team has waited more than an hour for a worker. Items in backoff (`next_eligible_at` in the future) and operator-owned items are not counted. Usually the team's builder slots are all taken (its ScaledJob's `maxReplicaCount`), or the executor is not scaling.

**Check first:** what the team is running and what waits.

```sql
SELECT w.id, w.external_id, w.updated_at, w.attempts,
       (SELECT count(*) FROM agent_runs r WHERE r.team = w.team AND r.state = 'running') AS team_running
FROM work_items w
WHERE w.team = '<team>' AND w.state = 'queued' AND NOT w.operator_owned
  AND (w.next_eligible_at IS NULL OR w.next_eligible_at <= now())
ORDER BY w.updated_at;
```

## PloegSettlementBacklog

`max by (state) (ploeg_llm_accounts_unsettled) > 0`. Default: `for: 1h`, warning.

Inference Accounts of finished Runs have stayed `reserved` or `blocked` for an hour. They still hold Shift budget, so the Shift's pool looks smaller than it is. The settlement sweep settles a `reserved` account at once and a `blocked` one after `PLOEG_LLM_SETTLE_AFTER`; a backlog means the sweep is failing, usually because the gateway's spend logs cannot be read.

**Check first:** ploegd's logs for settlement errors, then the accounts:

```sql
SELECT r.id, a.alias, a.state, a.updated_at, a.gateway_key_id
FROM run_llm_accounts a JOIN agent_runs r USING (run_token)
WHERE r.state = 'finished' AND a.state IN ('reserved', 'blocked')
ORDER BY a.updated_at;
```

See [reconcile uncertainty](managed-workers.md#reconcile-uncertainty) before settling anything by hand.
