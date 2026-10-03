---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-09-23
verified_by: "Read apps/ploeg cmd/ploegd/{main,sweep}.go (startup ping retry, Migrate, boot orphan sweeps), pkg/store/store.go (Migrate, ExpireLeases), pkg/store/shift.go (ExpireRuns), pkg/httpapi/server.go (readyz), pkg/worker/worker.go (renewLoop), ops/helm/ploeg and docs/operations/first-cutover.md"
---

# Restore Ploeg after a database outage

**Symptom:** `ploegd` reports `db unreachable` on `/readyz`, crash-loops with `database unreachable after retries`, or logs `lease sweep failed`; workers log `lease renewal failed`. Or the database was restored from a backup.

**Goal:** bring `ploegd` back on a healthy database, let the sweeper clean up the Runs the outage killed, and, after a restore from backup, find the work and spend that the restored database no longer knows about.

Read [Before you start](index.md#before-you-start) for names and the database session.

## What an outage does

All of Ploeg's state is in PostgreSQL. When it is unreachable:

- `/readyz` returns 503, so the Service stops routing tracker webhooks and worker requests to `ploegd`. `/healthz` stays 200, so Kubernetes does not restart the pod ([server.go](../../pkg/httpapi/server.go)).
- The sweeper logs errors and does nothing.
- Each worker fails to renew. After three failed renewals, about one `PLOEG_LEASE_TTL` (5 minutes in the chart), it cancels its Run to avoid a double claim. It cannot report the outcome while the database is down.
- A starting `ploegd` pings the database for two minutes, then exits. Kubernetes restarts it with backoff until the database answers ([main.go](../../cmd/ploegd/main.go)).
- The KEDA scaler cannot read the queue, so no new worker pods start.

After the database returns, `ploegd` applies any missing migrations at start (tracked in `schema_migrations`). Its sweeper then finishes every Run whose deadline passed during the outage as `failed` with `lease_lost`, and blocks its key. The Shift engine re-opens a failed writer's Round, within the retry cap.

## 1. Stop new dispatch

Pause the worker workloads as in [Drain workers, steps 1 and 2](drain-workers.md#2-stop-new-claims). A pod that starts before `ploegd` is ready cannot claim and exits. Pausing keeps the recovery quiet, so the sweeper can finish the Runs the outage killed before new ones start.

## 2. Bring the database back

The database is a CloudNativePG cluster that the GitOps repository defines. Its recovery procedure, including restore from backup, belongs to the platform, not to Ploeg. Check its state:

```sh
kubectl -n ploeg get clusters.postgresql.cnpg.io ploeg-db
kubectl -n ploeg get pods -l cnpg.io/cluster=ploeg-db
```

Before you continue, decide which case you are in:

- **Outage:** the same data came back, for example after a failover or a node restart. Skip step 5.
- **Restore from backup:** the database now reflects an earlier point in time. Everything Ploeg recorded after that point is gone. Do step 5.

Do not run an older `ploegd` image against a database that a newer one has migrated. Migrations run forward only.

## 3. Bring `ploegd` back

If `ploegd` gave up while the database was down, restart it rather than wait for the crash-loop backoff:

```sh
kubectl -n ploeg rollout restart deployment/ploeg
kubectl -n ploeg rollout status deployment/ploeg --timeout=180s
kubectl -n ploeg logs deployment/ploeg --since=10m \
  | grep -E 'database not ready|migration|orphan sweep|sweep failed'
```

A `migration <name>: …` error stops `ploegd` at start. Do not edit `schema_migrations` to get past it. Find out why the migration fails on this database.

## 4. Let the sweeper clean up

Within one sweep interval, Runs that died during the outage finish. Watch them:

```sql
SELECT count(*) FILTER (WHERE state = 'running' AND expires_at < now()) AS overdue_runs,
       count(*) FILTER (WHERE state = 'running') AS running_runs
FROM agent_runs;
SELECT count(*) AS overdue_leases FROM leases WHERE expires_at < now();
SELECT action, count(*) FROM audit_log
WHERE at > now() - interval '30 minutes' AND action IN ('run.expired', 'lease.expired', 'infra_cap')
GROUP BY action;
```

`infra_cap` means a Work Item outside a Shift reached its infrastructure-failure cap and went `stale`. The outage caused that, not the agent. Re-assign those tickets in the tracker once Ploeg is healthy.

Then check that the killed Runs' inference accounts settle. Give the settlement sweep its quiet period (15 minutes by default) before you worry:

```sql
SELECT a.state, count(*) FROM run_llm_accounts a JOIN agent_runs r USING (run_token)
WHERE r.state = 'finished' AND a.state <> 'reconciled'
GROUP BY a.state;
```

[Investigate a Run's spend](investigate-a-runs-spend.md) covers accounts that stay unsettled.

## 5. After a restore from backup: find what was lost

The restored database is missing everything after the backup point. The gateway and the forge still hold what happened in that gap. Ploeg has no tool to compare the two. **Not implemented yet:** a post-restore reconciliation command. Work through the gap by hand:

1. **Keys minted in the gap.** `ploegd` runs an orphan sweep at start and every 15 minutes. It blocks every `ploeg-*` gateway key that no unfinished Run in the database accounts for, and revokes every per-Run forge push token whose Run holds no Lease. Confirm with `orphan sweep: revoked stale keys` and `forge orphan sweep: revoked stale push credentials` in the log.
2. **Spend in the gap.** Blocking a key does not account for its spend. In the LiteLLM logs, find spend from `ploeg-*` aliases after the backup time whose alias is not in the database:

   ```sql
   SELECT alias FROM run_llm_accounts;
   ```

   That spend belongs to no Shift, and Ploeg cannot record it. **Not implemented yet:** importing it. Record it with its evidence in the operational record.
3. **Assignments in the gap.** Tracker webhooks received after the backup point are gone, and Ploeg does not replay them. Compare the tickets assigned to agent users in the tracker with `SELECT provider, external_id, state FROM work_items WHERE updated_at > '<backup time>';` and re-assign the missing ones.
4. **Branches and pull requests from the gap.** A writer that ran in the gap may have pushed an `agent/<tracker>-<ticket id>` branch or opened a pull request that Ploeg no longer knows about. Review or close them on the forge before re-assigning, so that a new Run does not start on top of an unknown branch.

## 6. Resume dispatch

Unpause the workers and resume the HelmRelease as in [Drain workers, step 6](drain-workers.md#6-resume).

## Verify

1. `/readyz` answers 200:

   ```sh
   kubectl -n ploeg port-forward svc/ploeg 8080:8080
   # in a second shell
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/readyz
   ```

2. The overdue counts in step 4 are 0, and the `ploegd` log shows no `sweep failed` for 15 minutes.
3. A newly assigned ticket reaches a worker pod and its Run reaches `running`.

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `ploegd` crash-loops with `database unreachable after retries` | Database still down or its Service not resolving | Recover the CloudNativePG cluster; then restart `ploegd` |
| `ploegd` exits with `migration <name>: …` | The schema on the restored database conflicts with a migration | Investigate that migration against the data; never edit `schema_migrations` |
| Pod running, not ready, `/readyz` says `db unreachable` | Connection string or credentials in `ploeg-db-app` no longer valid | Check the Secret the cluster generated; restart `ploegd` after it changes |
| Many `run.expired` and `lease.expired` rows after recovery | Runs cancelled themselves during the outage | Expected; the Shift engine retries writers within the cap |
| Items `stale` with `infra_cap` after recovery | The outage used up the infrastructure-failure budget | Re-assign the tickets in the tracker |
| Accounts stuck `unknown` after recovery | The gateway cannot report keys that were deleted in the meantime | [Investigate a Run's spend](investigate-a-runs-spend.md) |
| Tickets assigned during the outage never ran | Webhooks were refused while `ploegd` was unready | Re-assign them |
| An agent branch or pull request exists that Ploeg does not know | A Run in the restored-away gap pushed it | Review or close it before re-assigning |
