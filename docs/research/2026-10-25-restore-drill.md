# Restore drill: measured RTO and RPO, and fenced leases after a restore, 2026-10-25

Status: research record, 2026-10-25. It answers two acceptance items of [#22](https://github.com/ploeg-hq/ploeg/issues/22): restored work, leases and accounting reconcile with the backup point, and old workers cannot overwrite authority after a restore. It also records the first measured RTO and RPO.

**Question.** After Ploeg's database is destroyed and restored from a backup, does a fresh `ploegd` hold exactly what the backup held, can any worker holding a token from before the restore change that state, and how long does the restore take?

**Answer.** The restored database held the backup point's rows exactly, settled spend included. Every token from before the restore was refused on renewal, checkpoint and outcome, and changed nothing. Each item went back to `queued` or kept `needs_human`, and a new claim got a new token. On the CI runner the database and `ploegd` were serving again 0.30 s after the database was destroyed. Everything written after the backup point was lost, as expected.

## Method

The drill is `TestRestoreDrillReconcilesToTheBackupPointAndFencesEveryOlderLease` in [restore_drill_linux_test.go](../../pkg/worker/restore_drill_linux_test.go). Run it with `mise run restore-drill`. The [Restore drill workflow](../../.github/workflows/restore-drill.yml) runs it on every pull request that touches `pkg/store`, `pkg/httpapi`, `pkg/shiftengine` or `pkg/worker`, on every push to `development` and `main`, and weekly, and opens an issue when the weekly run fails.

1. **Primary.** Embedded PostgreSQL 18 in its own data directory, migrated to head. `ploegd` is the run API and Shift engine the [lease-fencing tests](../../pkg/worker/lease_fencing_linux_test.go) use (`httpapi.Server`, `shiftengine.Engine`, the sweeps `ExpireLeases`, `ExpireRuns` and `EvaluateAll`), with a 5-second lease.
2. **State before the backup.**
   - A planned item whose writer finished and whose LLM account settled at 1.2345 USD, charged to its Shift.
   - A shift-less item parked at `needs_human` by a `stuck` outcome.
   - Two queued shift-less items.
   - A planned item held by a real `Worker` (the PR #279 harness), with a checkpoint, an issued LLM account and a renewal just before the backup, so the backup holds a live lease.
3. **Backup.** A hot base backup, the low-level form of what CloudNativePG's barman-cloud takes: `pg_backup_start`, a copy of the running data directory, `pg_backup_stop`, its `backup_label`, and a second copy of `pg_wal` with the WAL written meanwhile. The drill records the moment that copy ends as the backup point.
4. **Writes after the backup point.** These are what a restore loses.
   - The worker renews again.
   - One queued item is claimed, finishes and settles 0.75 USD.
   - The other queued item is claimed and holds its lease with a checkpoint and an issued account.
   - A new item is assigned.
5. **Destruction.** The worker is cut off, PostgreSQL stops and its data directory is deleted.
6. **Restore into a clean environment.** The backup is copied into a new data directory with `recovery.signal`, `restore_command = 'false'` and `recovery_target_action = 'promote'`. A new PostgreSQL starts on a new port and recovers to the end of the copied WAL. A new store, pool, migration run, `ploegd` and first sweep follow. The RTO clock stops when that sweep has run.
7. **Assertions.**
   - Before `ploegd` starts, the restored `work_items`, `shifts`, `agent_runs`, `leases`, `run_llm_accounts`, `checkpoints` and `audit_log` equal the backup point's rows, compared as JSON.
   - Settled spend and Shift spend equal the backup point's.
   - After the in-flight lease's deadline passes, three kinds of pre-restore token get `gone` on renewal and 404 on checkpoint and outcome, and leave every table unchanged: the in-flight Run's, the token of the Run that finished in the gap, and the token of the Run that held its lease in the gap. A second, different outcome from the settled Run is refused too.
   - The sweep finishes the in-flight Run as `failed`/`lease_lost`.
   - The runbook's step 4 queries find no overdue Runs and no overdue leases.
   - No item is `active` without a live or pending Run.
   - The settled item keeps its state, the parked item stays `needs_human`, and both items claimed in the gap are `queued`. The item assigned in the gap is absent.
   - The audit log up to the backup point is unchanged.
   - New claims re-dispatch all three items under new tokens. The parked item is not dispatched.
   - The real worker that held the pre-backup lease reconnects, gets `gone` on its next renewal, stops its harness for a lost lease and has its outcome refused, and the item's authoritative state does not change.
   - The re-dispatched Run reports normally.

## Measurements

RTO is the time from destruction to the first sweep of the fresh `ploegd`. RPO is the time from the backup point to destruction. The base backup is 59 MB.

| Where | Run | RTO | Restore files | PostgreSQL start and recovery | Migrate | `ploegd` boot and first sweep | RPO | Base backup |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| CI, `ubuntu-24.04`, `-race` | [40203670247](https://github.com/ploeg-hq/ploeg/actions/runs/40203670247) attempt 1 | 297 ms | 135 ms | 122 ms | 16 ms | 25 ms | 236 ms | 247 ms |
| CI, `ubuntu-24.04`, `-race` | same run, attempt 2 | 301 ms | 134 ms | 123 ms | 19 ms | 24 ms | 227 ms | 247 ms |
| Workstation, 32 threads, `-race` | five runs | 0.67–1.39 s | 94–106 ms | 0.55–1.24 s | 7–22 ms | 13–32 ms | 0.21–0.31 s | 0.83–1.29 s |

In every run the restored database held every write before the backup point and none after it. The 0.75 USD settled in the gap was gone, and so was the item assigned in the gap. The RPO is therefore exactly the backup point. The window is a fraction of a second only because the drill destroys the database right after the backup.

## Mutation tests

Each mutation was applied to `development` at 96a8c88 plus this change, the drill run, and the mutation reverted. The unmutated drill passes, locally and on CI.

| Mutation | Result |
| --- | --- |
| `refuseExpiredLease` never refuses (the deadline check that `274c342` added) | Fails: the in-flight Run's token renews (`Gone:false`, a new 5-second lease) and its checkpoint is accepted after the restore |
| `Renew` drops `state = 'running'` from its `agent_runs` update | Fails: the real pre-backup worker's renewal of its swept Run succeeds, so its harness is never stopped |
| `ReportOutcome` drops `state = 'running'` from its compare-and-swap | Fails: the settled Run's second outcome is accepted and `agent_runs` and `audit_log` change |
| The backup omits the WAL written during the copy | Fails: the restored PostgreSQL does not start |
| The backup is taken after the gap's writes instead of before them | Fails: six of the seven compared tables differ from the backup point |

## Findings

- **A restored database is read-only until recovery ends.** The first version of the drill started `ploegd` as soon as PostgreSQL accepted connections. `Migrate` then failed with `cannot execute CREATE TABLE in a read-only transaction`, which in production makes `ploegd` exit and restart. CloudNativePG's read-write Service points at the primary only after promotion, so this shows up only if `ploegd` is pointed at the database some other way. The [runbook](../how-to/restore-after-a-database-outage.md#symptom-cause-fix) now lists it.
- **Fencing after a restore is the same fencing as after an outage.** A pre-restore token is refused because its Run is no longer `running` or its deadline has passed, and a token minted after the backup point is refused because the restored database does not know it. Nothing marks a restore. If a restore completes within one lease TTL of the backup point, the in-flight Run's lease is still live, and the worker holding it may renew. That worker is the one the restored database names as holder, so this is the same as after a plain outage. The drill waits for the deadline so that it tests the refusal.
- No defect in Ploeg was found.

## Limits

- **Embedded PostgreSQL, not CloudNativePG.** The base backup and recovery use the same PostgreSQL mechanisms that barman-cloud and CloudNativePG use. The drill does not upload to object storage, download from it, replay archived WAL, schedule a pod or bind a volume. A production RTO adds all of those, and they grow with the database and the time since the last base backup.
- **RPO with continuous archiving.** The drill restores a base backup to its end. With continuous WAL archiving, the RPO of a point-in-time restore is the archiving delay, which the drill does not measure.
- **One node, one small dataset.** The dataset has a few items and Runs in a 59 MB data directory. Recovery time grows with WAL volume.
- **`ploegd` is the test assembly.** The drill runs the run API, Shift engine and sweeps that the lease-fencing tests use. It does not run `cmd/ploegd`, so the orphan key and push-credential sweeps, LLM settlement against a gateway, and the boot gate are not part of the measured RTO. The [runbook's step 5](../how-to/restore-after-a-database-outage.md#5-after-a-restore-from-backup-find-what-was-lost) still covers spend and keys from the gap by hand.
- **Restore, not upgrade.** The restored database is migrated to the same release. The upgrade half of #22's second item rests on the worker-version handshake (`TestClaim_AWorkerOlderThanThePreviousMinorGets426NamingTheRange`), the lease-fencing tests across a `ploegd` restart, and the rollback rules in [compatibility](../ops/compatibility.md#database). No drill restores an older release's backup and upgrades it. Since 2026-10-26 the [upgrade drill](2026-10-26-upgrade-drill.md) does.
