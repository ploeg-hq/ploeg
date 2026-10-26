---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-26
verified_by: "Ran the upgrade drill (mise run upgrade-drill) from v0.2.0-rc.77 and v0.2.0-rc.1, and once from v0.1.0; read pkg/store/store.go (Migrate, applyMigration), pkg/httpapi/worker_version.go, docs/ops/compatibility.md and ADR 0158"
---

# Recover from a failed upgrade

**Symptom:** after an upgrade, `ploegd` exits with `migration <name>: …` and crash-loops, or it starts but the new release misbehaves, or the database had to be restored from a backup taken before the upgrade.

**Goal:** get Ploeg serving again with the least data lost, and know before you start whether you can roll back or must restore.

Read [Before you start](index.md#before-you-start) for names and the database session. Upgrade in the order [compatibility](../ops/compatibility.md#version-pairs) gives: database backup, then `ploegd`, then workers, then consumers. Every recovery below assumes you took that backup.

## Decide: rerun, roll back or restore

| What happened | Recovery | Data lost | Measured in the [upgrade drill](../research/2026-10-26-upgrade-drill.md) |
| --- | --- | --- | --- |
| `ploegd` exits with `migration <name>: … lock timeout`, or its pod or node died during the upgrade | **Rerun.** Restart `ploegd` (step 1). It applies the interrupted migration and the rest | Nothing | 33 to 49 ms from restart to serving, after the 5 s the failed attempt waited. In a cluster the crash-loop backoff (10 s, doubling to 5 minutes) dominates |
| PostgreSQL crashed during the upgrade | **Rerun** once the database is back | Nothing; crash recovery keeps every committed migration | 44 to 52 ms after PostgreSQL is back |
| `ploegd` exits with the same `migration <name>: …` error on every start, not a lock timeout | **Restore** (step 3). The migration fails on this data, so rerunning cannot help | Everything after the pre-upgrade backup | 0.27 to 0.66 s on a 76 MB backup, plus your restore time |
| The upgrade finished but the new release misbehaves, and you came from the previous release | **Roll back** (step 2) to the previous release, on the upgraded database | Nothing | The previous release was serving 21 ms after its process started |
| The upgrade finished and you need a release older than the previous one | **Restore** (step 3). An older release on a newer schema is not supported | Everything after the pre-upgrade backup | as above |
| The database was lost after the upgrade, and the newest backup predates it | **Restore and upgrade.** Restore the backup and run the current release; it migrates the backup forward on start | Everything after the backup | 0.29 to 1.03 s from destruction to serving, 39 migrations included |

The drill measured these on the CI runner and a workstation with embedded PostgreSQL and a 77 MB backup. They time PostgreSQL and `ploegd`, not a CloudNativePG restore, object storage or pod scheduling. Time a real restore of `ploeg-db` before you promise an operator target.

## 1. Rerun the upgrade

First stop new dispatch, as in [Drain workers, steps 1 and 2](drain-workers.md#2-stop-new-claims), so the restarted `ploegd` migrates without Runs competing for table locks. Then:

```sh
kubectl -n ploeg logs deployment/ploeg --previous | grep -E 'migration|ploegd exiting'
kubectl -n ploeg rollout restart deployment/ploeg
kubectl -n ploeg rollout status deployment/ploeg --timeout=300s
```

Each migration and its row in `schema_migrations` commit together, so an interrupted migration left nothing behind and the restart applies it from the start. Check that every migration of the release is recorded:

```sql
SELECT count(*), max(name) FROM schema_migrations;
```

The count is the number of files in the release's `pkg/store/migrations/`, and `max(name)` the newest. Never insert into or delete from `schema_migrations` by hand.

If the error comes back with `lock timeout` every time, something holds a table for more than 5 seconds. Find it before you restart again:

```sql
SELECT pid, state, wait_event_type, now() - xact_start AS open_for, left(query, 80)
FROM pg_stat_activity WHERE datname = 'app' AND xact_start IS NOT NULL ORDER BY xact_start;
```

## 2. Roll back to the previous release

Only to the release directly before the one you upgraded to. Every migration expands the schema, so the previous release keeps working on it ([compatibility](../ops/compatibility.md#database)).

1. In the GitOps repository, set the HelmRelease's chart version back to the previous release and let Flux reconcile. The chart pins `ploegd` and the workers by digest, so both go back together.
2. Watch `ploegd` start. It runs its own migrations, finds them all applied, and serves:

   ```sh
   kubectl -n ploeg rollout status deployment/ploeg --timeout=300s
   ```

3. Resume dispatch as in [Drain workers, step 6](drain-workers.md#6-resume).

Upgrading again later is the normal upgrade: the newer `ploegd` finds its migrations already applied.

## 3. Restore the pre-upgrade backup

1. Stop dispatch and scale `ploegd` to zero, as in [Drain workers](drain-workers.md), steps 1 to 5.
2. Pin the HelmRelease to the release that took the backup, so nothing newer starts on the restored database.
3. Restore `ploeg-db` from the backup taken before the upgrade, as in [Restore after a database outage, step 2](restore-after-a-database-outage.md#2-bring-the-database-back). Wait until `SELECT pg_is_in_recovery()` answers `false`.
4. Scale `ploegd` back up. It holds exactly the backup's Work Items, Runs, Leases, accounts and audit trail.
5. Work through [what was lost](restore-after-a-database-outage.md#5-after-a-restore-from-backup-find-what-was-lost): keys minted, spend, assignments and branches after the backup point.
6. Resume dispatch.

## What the database backup does not hold

Ploeg's credentials are not in PostgreSQL. Every original is in OpenBao and reaches the `ploeg` namespace as a Secret ([where each credential lives](rotate-credentials.md#where-each-credential-lives)). Restoring the database neither restores nor loses them, and rolling back or restoring needs no credential step. If a vault original itself is lost, issue a new one as [Rotate credentials](rotate-credentials.md) describes. A new worker signing key ends every Run in flight, because a capability signed with the old key stops working; drain first.

Gateway keys and per-Run forge tokens minted after the backup point are in the gateway and the forge, not in the restored database. The orphan sweeps after `ploegd` starts block and revoke them ([what was lost](restore-after-a-database-outage.md#5-after-a-restore-from-backup-find-what-was-lost)).

## Workers during and after the upgrade

A worker of the older release that kept running through the upgrade cannot change anything with the token it held. Once its Lease's deadline has passed, its renewal gets `gone`, its checkpoint and outcome get 404, and the sweeper finishes its Run as `lease_lost`. Its next claim is served under a new token if its release is the same minor as `ploegd` or the one before; a worker further behind gets **426 Upgrade Required** and claims nothing. A worker released before `v0.2.0-rc.67` sends no version and is not judged, so for those only the Lease protects you. Drain such workers before the upgrade.

## Verify

1. `ploegd` is ready and logs no `migration` error:

   ```sh
   kubectl -n ploeg logs deployment/ploeg --since=10m | grep -E 'migration|sweep failed'
   ```

2. `schema_migrations` holds every migration of the running release (step 1).
3. The overdue counts of [Restore after a database outage, step 4](restore-after-a-database-outage.md#4-let-the-sweeper-clean-up) are 0.
4. A newly assigned ticket reaches a worker pod and its Run reaches `running`.

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `migration <name>: … canceling statement due to lock timeout` | A long transaction or live traffic held a table the migration alters | Drain, find the holder in `pg_stat_activity`, restart `ploegd` |
| `migration <name>: … terminating connection` or `conn closed` | `ploegd`'s connection or PostgreSQL died during the migration | Restart `ploegd` once the database answers |
| The same `migration <name>: …` error on every start, with a constraint, type or syntax error | The migration does not fit this database | Restore the pre-upgrade backup and run the release that took it; report the migration |
| `migration <name>: … relation … already exists` | Someone ran part of a migration by hand, or edited `schema_migrations` | Restore the pre-upgrade backup; do not drop objects by hand to get past it |
| The previous release logs errors on the upgraded schema | You rolled back further than one release | Restore the pre-upgrade backup |
| Workers exit with `ploegd does not serve this worker version` | Workers two minors behind `ploegd`, or newer than it | Upgrade the side the message names |
