# Metrics and alerts

ploegd serves Prometheus metrics at `GET /metrics` on its HTTP port, next to `/healthz` and `/readyz`. The alerts below are an optional `PrometheusRule` in the [chart](../../ops/helm/ploeg/values.yaml). Enabling it does not change what ploegd does. It only tells you when something has gone wrong.

## How the metrics are produced

The gauges are computed from the database when Prometheus scrapes them. They are not counters kept in memory, so a ploegd restart does not reset them, and every replica reports the same values. Each result is reused for `PLOEG_METRICS_CACHE_TTL` (default `15s`) so frequent scrapes do not add database load. A negative value turns the cache off. If the database cannot be read, `/metrics` answers `503`, the scrape fails, and Prometheus sets `up` to `0` for the target. Every alert below goes quiet while that is happening, so alert on `up == 0` for this job as well.

The exposition is written by hand in the Prometheus text format (`version=0.0.4`). The Prometheus Go client is not a dependency of Ploeg, and a handful of gauges read from SQL does not need a registry, collectors or the process metrics the client would add.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `ploeg_shifts_open` | `team` | Open Shifts |
| `ploeg_shift_idle_seconds_max` | `team` | Longest time an open Shift of the team has gone without Run progress |
| `ploeg_leases_expired` | | Leases past their expiry that the sweep has not released yet |
| `ploeg_lease_overdue_seconds_max` | | How long the most overdue Lease has been expired, or `0` |
| `ploeg_llm_keys_past_ttl` | `state` | Inference Accounts in `issued` or `unknown` whose key TTL has passed |
| `ploeg_llm_key_ttl_overrun_seconds_max` | `state` | How far past its TTL the oldest such account is, or `0` |
| `ploeg_settled_spend_usd_last_hour` | | Spend settled onto Shifts in the last hour, in USD |
| `ploeg_runs_failed_total` | `reason` | Runs ever recorded failed with each failure reason. A counter: Runs are never deleted. Every known reason is present at `0` |
| `ploeg_runs_without_observed_delivery_last_day` | `source` | Runs finished in the last day whose report had no delivery record from the worker (`legacy`) or one that named another forge, repository, branch or pull request (`mismatch`) |
| `ploeg_tracker_webhooks_missing` | | Configured Vikunja projects with no assignment webhook to Ploeg |
| `ploeg_tracker_webhooks_unchecked` | | Configured Vikunja projects the webhook check could not read |
| `ploeg_tracker_webhook_check_timestamp_seconds` | | Unix time of the last webhook check |

Run progress means a Run of the Shift starting or finishing, or a checkpoint on its Work Item. Opening the Shift also counts. Lease and deadline renewals do not count, because a hung harness keeps renewing. A key's TTL is measured from the Run's start, which comes a few seconds before the key is minted. `ploeg_runs_failed_total{reason="credential_leak"}` counts Runs whose forge traffic or pushed commits carried one of the Run's credentials or its canary. Every increase needs a person: rotate the credential and read the commits the Run left on its branch. [PloegCredentialLeak](#ploegcredentialleak) alerts on it. Settled spend is the sum of trusted reconciliation deltas (`llm.reconciled` in the audit log) plus the `costUsd` that finished Shift Runs without a managed Inference Account reported. A managed Run's own cost report is not counted, because it is not settlement. A `legacy` Run comes from a worker older than [ADR-0059](../adrs/0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md); when the gauge has read zero for two weeks, the older-worker path can go. A `mismatch` Run is a worker or caller reporting delivery for something other than its own branch; it parks the Work Item for a person. The three tracker webhook series appear only once a webhook check has finished, and only when the Vikunja URL and token are configured. See [tracker configuration](board.md).

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
