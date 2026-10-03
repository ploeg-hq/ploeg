---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-09-23
verified_by: "Read apps/ploeg pkg/store/{store,shift,operator_execution}.go (Claim, ClaimRole, Renew, ExpireLeases, ExpireRuns, CloseShift), pkg/shiftengine/engine.go, pkg/worker/worker.go (renewLoop), cmd/ploegd/{main,sweep}.go, migrations 0001, 0008, 0013 and ops/helm/ploeg"
---

# Recover a stuck Lease or Run

**Symptom:** a Work Item stays `leased` long after its worker pod is gone, a Run stays `running` or `pending` for hours, or a writing Run in a new Round never starts.

**Goal:** get every Run to a finished state through Ploeg's own sweeper, so the Lease is dropped, the gateway key is blocked, the push credential is revoked and the Work Item moves on. Edit rows by hand only where this page says to.

Read [Before you start](index.md#before-you-start) for names and the database session.

## How recovery normally happens

You rarely need this runbook, because the sweeper in `ploegd` does the work every `PLOEG_SWEEP_INTERVAL` (15 seconds in the chart) ([sweep.go](../../cmd/ploegd/sweep.go)):

- A worker renews its Run's deadline every third of `PLOEG_LEASE_TTL` (5 minutes in the chart). After three failed renewals it cancels its own Run ([worker.go](../../pkg/worker/worker.go)).
- `ExpireRuns` finishes every `running` Run whose `expires_at` has passed: outcome `failed`, `failure_reason = 'lease_lost'`. It drops the writer's Lease, blocks the Run's gateway key and revokes its push credential ([shift.go](../../pkg/store/shift.go)). Runs that belong to an operator execution (a Vloer session) are skipped here; operator execution reconciliation handles them.
- `ExpireLeases` does the same for Leases outside a Shift (`shift_id IS NULL`), then re-queues the Work Item with backoff, or marks it `stale` after repeated infrastructure failures ([store.go](../../pkg/store/store.go)).
- The Shift engine then advances the Round. A failed writer re-opens its own Round, up to a cap ([ADR-0019](../adrs/0019-a-failed-writing-run-reopens-its-round.md)).

So a "stuck" Run usually means one of three things: the sweeper is not running, the worker is alive and still renewing, or nothing ever claimed a `pending` Run.

## 1. Check that the sweeper runs

```sh
kubectl -n ploeg get deployment ploeg
kubectl -n ploeg logs deployment/ploeg --since=15m \
  | grep -E 'sweep failed|lease expired|run deadline expired|evaluate failed'
```

`lease sweep failed` or `run sweep failed` means the sweep cannot write to the database. Go to [Restore after a database outage](restore-after-a-database-outage.md). If `ploegd` is not ready, fix that first: nothing below works without the sweeper.

## 2. Find overdue and long-running work

Overdue Leases and Runs. With a healthy sweeper this returns nothing for longer than one interval:

```sql
SELECT 'lease' AS kind, l.work_item_id, l.shift_id, r.id AS run_id, l.team, r.role,
       l.expires_at, now() - l.expires_at AS overdue_by
FROM leases l LEFT JOIN agent_runs r USING (run_token)
WHERE l.expires_at < now()
UNION ALL
SELECT 'run', r.work_item_id, r.shift_id, r.id, r.team, r.role,
       r.expires_at, now() - r.expires_at
FROM agent_runs r
WHERE r.state = 'running' AND r.expires_at < now()
ORDER BY overdue_by DESC;
```

Every running Run, oldest first:

```sql
SELECT r.id AS run_id, r.work_item_id, w.state AS item_state, r.shift_id,
       r.team, r.role, r.round, r.writes, r.started_at,
       now() - r.started_at AS running_for, r.expires_at,
       l.renewed_at AS lease_renewed_at,
       EXISTS (SELECT 1 FROM operator_executions e WHERE e.run_id = r.id) AS operator_run
FROM agent_runs r
JOIN work_items w ON w.id = r.work_item_id
LEFT JOIN leases l ON l.run_token = r.run_token
WHERE r.state = 'running'
ORDER BY r.started_at;
```

Pending Runs that no worker has claimed:

```sql
SELECT r.team, r.role, count(*) AS pending, min(s.opened_at) AS oldest_shift_opened
FROM agent_runs r JOIN shifts s ON s.id = r.shift_id
WHERE r.state = 'pending' AND s.closed_at IS NULL
GROUP BY r.team, r.role
ORDER BY oldest_shift_opened;
```

Inconsistent rows. Both queries should return nothing; no sweep repairs what they find:

```sql
-- A Shift Lease whose Run is no longer running.
SELECT l.work_item_id, l.shift_id, r.id AS run_id, r.state AS run_state
FROM leases l LEFT JOIN agent_runs r USING (run_token)
WHERE l.shift_id IS NOT NULL AND (r.id IS NULL OR r.state <> 'running');

-- A Work Item marked leased with no Lease, no live Run and no live Shift.
SELECT w.id, w.team, w.updated_at
FROM work_items w
WHERE w.state = 'leased' AND NOT w.operator_owned
  AND w.updated_at < now() - interval '10 minutes'
  AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.work_item_id = w.id)
  AND NOT EXISTS (SELECT 1 FROM agent_runs r
                  WHERE r.work_item_id = w.id AND r.state IN ('pending', 'running'))
  AND NOT EXISTS (SELECT 1 FROM shifts s
                  WHERE s.work_item_id = w.id AND s.closed_at IS NULL);
```

## 3. Match the Run to its pod

The queries below use psql variables. Set them from the rows you found, for example `\set item 42` for the Work Item id and `\set run 1234` for the Run id. The latest checkpoint records the pod and node that wrote it:

```sql
SELECT phase, branch, pr_url, pod_uid, node_name, created_at
FROM checkpoints WHERE work_item_id = :item
ORDER BY created_at DESC LIMIT 1;
```

List the worker pods of the Run's team and compare UIDs:

```sh
kubectl -n ploeg get pods -l app.kubernetes.io/name=ploeg-worker,ploeg.webgrip.dev/team=<team> \
  -o custom-columns=POD:.metadata.name,UID:.metadata.uid,JOB:.metadata.labels.job-name,PHASE:.status.phase,STARTED:.status.startTime
kubectl -n ploeg logs <pod> -c worker --tail=100
```

## 4. Apply the fix for what you found

### A live Run that should stop

The worker is alive and renewing, so the sweeper leaves it alone. This happens with a harness that loops without making progress. The harness limits (`PLOEG_HARNESS_TIMEOUT` 100 minutes, idle 15 minutes) and the Job's `activeDeadlineSeconds` (7200) will end it eventually. To end it now, delete its Job:

```sh
kubectl -n ploeg delete job <job>
```

The worker receives SIGTERM and has `terminationGracePeriodSeconds` (90 seconds) to stop the harness, revoke its key and report an outcome. Check that the Run is finished afterwards. If it is still `running` with a future `expires_at`, continue with the next section.

### A Run or Lease whose worker is gone but whose deadline is in the future

Wait for the deadline: at most one `PLOEG_LEASE_TTL` after the last renewal. To act sooner, first confirm with step 3 that no pod for the Run exists: a live worker renews and moves the deadline forward again. Then move the deadline to now and let the sweeper do the rest. **Not implemented yet:** Ploeg has no operator command for this, so you change the deadline in SQL. The change touches only the deadline. The sweeper still records the outcome, drops the Lease, blocks the key and revokes the push credential.

```sql
SET default_transaction_read_only = off;
-- A Shift Run (expires_at is set): writers and readers.
UPDATE agent_runs SET expires_at = now()
WHERE id = :run AND state = 'running' AND expires_at IS NOT NULL;
-- A Lease outside a Shift (a Run from before Shifts, or PLOEG_SHIFTS_UNIFORM=false).
UPDATE leases SET expires_at = now()
WHERE work_item_id = :item AND shift_id IS NULL;
```

Within one sweep interval the logs show `run deadline expired, run reclaimed` or `lease expired, item released`.

### A Run from a Vloer session

If `operator_run` is true, `ExpireRuns` does not touch the Run and moving its deadline does nothing. Stop or cancel the session in Vloer. Ploeg's operator execution reconciliation expires sessions past their own deadline and blocks their keys.

### A pending Run that never starts

Nothing claimed it. Check the workload for its team and Role:

```sh
kubectl -n ploeg get scaledjobs,cronjobs
kubectl -n ploeg describe scaledjob ploeg-worker-<team>-<role>
kubectl -n keda logs deployment/keda-operator --since=30m | grep -i ploeg
```

- **No workload for the Role:** the chart renders one workload per team and Role from `executor.teams[].plan`. A Role that `ploegd` knows but the chart does not render is never claimed. Make the plan in the GitOps values match.
- **Scaler errors:** the KEDA PostgreSQL trigger connects as `executor.scaler.userName` with the `ploeg-scaler` Secret. Fix its credentials or host.
- **Budget exhausted:** `ploegd` logs `claim refused: shift budget exhausted`. The Shift's pool cannot fund another Run, often because unsettled holds are still counted. The engine parks such a Shift at `needs_human` with reason `budget exhausted: pool …`. See [Investigate a Run's spend](investigate-a-runs-spend.md).
- **Workers paused on purpose:** see [Drain workers](drain-workers.md#6-resume).

### A Shift Lease whose Run is not running

The first inconsistency query found a Lease that no sweep removes: `ExpireLeases` skips Shift Leases, and `ExpireRuns` only sees running Runs. The next writer in that Shift cannot take the Lease. **Not implemented yet:** there is no repair command. After confirming no pod for the Run exists, delete the Lease in SQL:

```sql
SET default_transaction_read_only = off;
BEGIN;
DELETE FROM leases l
WHERE l.work_item_id = :item AND l.shift_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM agent_runs r
                  WHERE r.run_token = l.run_token AND r.state = 'running');
INSERT INTO audit_log (actor, action, work_item_id, detail)
VALUES ('operator:<your name>', 'lease.removed_manually', :item,
        '{"reason": "run no longer running"}');
COMMIT;
```

The push credential minted with that Lease is no longer live in the database. The forge orphan sweep, every 15 minutes, revokes it.

### A Work Item left `leased` with nothing running

The second inconsistency query found an item that no sweep re-queues. Re-assigning it in the tracker does not help: ingest refreshes a `leased` item without re-queuing it. **Not implemented yet:** there is no re-queue command. Re-queue it in SQL:

```sql
SET default_transaction_read_only = off;
BEGIN;
UPDATE work_items SET state = 'queued', next_eligible_at = NULL, updated_at = now()
WHERE id = :item AND state = 'leased' AND NOT operator_owned;
INSERT INTO audit_log (actor, action, work_item_id, detail)
VALUES ('operator:<your name>', 'work_item.requeued_manually', :item,
        '{"reason": "leased with no lease and no live run"}');
COMMIT;
```

The Shift engine opens a Shift for it on its next pass.

### A Work Item at `stale` or `needs_human`

These are end states, not stuck states. `stale` means the attempt or infrastructure-failure cap was reached ([ADR-0021](../adrs/0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)); `needs_human` means a Run reported `stuck`, the plan ran out, or the budget did. Read the reason, fix the cause, then re-assign the ticket in the tracker. Re-assignment re-queues the item and resets its attempt counters ([store.go](../../pkg/store/store.go)).

## Verify

1. Both overdue queries from step 2 return nothing.
2. The inconsistency queries return nothing.
3. The item's audit trail shows the transition:

   ```sql
   SELECT at, actor, action, detail FROM audit_log
   WHERE work_item_id = :item ORDER BY at DESC LIMIT 10;
   ```

   Expect `run.expired` or `lease.expired`, then a Shift or queue action.

4. `SELECT state FROM run_llm_accounts a JOIN agent_runs r USING (run_token) WHERE r.id = :run;` moves to `blocked` and later `reconciled`. If it does not, continue with [Investigate a Run's spend](investigate-a-runs-spend.md).

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Overdue Leases or Runs pile up; logs show `lease sweep failed` or `run sweep failed` | `ploegd` cannot reach or write the database | [Restore after a database outage](restore-after-a-database-outage.md) |
| Overdue rows, no sweep errors, `ploegd` not ready | Controller down or crash-looping | Fix `ploegd`; the sweep catches up on start |
| Run `running` for hours, deadline keeps moving | Worker alive and renewing; harness not progressing | Delete the Job; the worker reports on SIGTERM |
| Run `running`, pod gone, deadline in the future | Pod killed within the last TTL | Wait one TTL, or set `expires_at = now()` |
| Run `running` for hours, `operator_run` true | Vloer session; skipped by `ExpireRuns` | Stop or cancel the session in Vloer |
| Runs `pending`, no worker pods | No workload for the Role, KEDA scaler error, or workers paused | Align the plan in values, fix scaler credentials, or resume workers |
| Runs `pending`, log `claim refused: shift budget exhausted` | Pool spent or held by unsettled accounts | [Investigate a Run's spend](investigate-a-runs-spend.md) |
| Next writer never starts; a Shift Lease exists for a finished Run | Orphan Shift Lease | Delete it in SQL (not implemented as a command yet) |
| Item `leased`, nothing running, no live Shift | Orphan item state | Re-queue it in SQL (not implemented as a command yet) |
| Item `stale` or `needs_human` | Retry cap, `stuck` report, plan end or budget | Fix the cause, re-assign in the tracker |
