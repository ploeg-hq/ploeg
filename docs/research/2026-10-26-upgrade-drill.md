# Upgrade drill: an older release's backup upgraded to head, interrupted migrations, and fenced older workers, 2026-10-26

Status: research record, 2026-10-26. It completes the upgrade half of [#22](https://github.com/ploeg-hq/ploeg/issues/22), which the [restore drill](2026-10-25-restore-drill.md) left open: restore a representative dataset written by an older release into a clean environment, upgrade it through the supported path, exercise an interrupted migration and worker reconnection, and measure recovery time and data loss.

**Question.** When a backup taken by an older release is restored and the current `ploegd` migrates it, do the older release's Work Items, Runs, Leases, accounts and audit trail come through unchanged? Can a worker of the older release, holding a token from before the upgrade, change anything afterwards? When a migration is interrupted, does rerunning it give the same database as an uninterrupted upgrade? And how long does each recovery take?

**Answer.** Yes, for each of the three releases tried. A backup written by `v0.2.0-rc.77` (the previous release), by `v0.2.0-rc.1` (the oldest release the policy expects to upgrade), and by `v0.1.0` (which the policy does not cover) restored into a clean PostgreSQL and migrated to head. Every row of every table kept every column that head still has, except the two changes migrations make on purpose. Settled spend and Shift spend matched the backup point to the cent. The older release's worker process, reconnecting after the upgrade, got `gone` on renewal and 404 on checkpoint and outcome, and changed nothing. Its next claim was served under a new token. A migration interrupted three ways (a lock timeout, `ploegd` killed, PostgreSQL crashed) left the interrupted migration unapplied, and the next start finished the upgrade with a schema and rows identical to an uninterrupted one. Under `-race`, a restore plus upgrade served again in 0.29 to 0.42 s on the CI runner and 0.6 to 1.0 s on a workstation, and an interrupted upgrade served again 33 to 52 ms after the rerun began.

## Which older releases

[ADR 0158](../adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md) decides that going from 0.1 to 0.2 is a fresh install and that the upgrade promise starts at `v0.2.0` (Q8). No `v0.2.0` exists yet. Every 0.2 candidate is held to [compatibility](../ops/compatibility.md): a candidate breaks nothing in the release before it, upgrades may skip releases, and the previous release must run on the new schema. Two releases follow from that:

| Base | Why | Promise it tests |
| --- | --- | --- |
| `previous`: the newest `v*` tag reachable from the commit, the base `go test ./internal/compat/` uses. `v0.2.0-rc.77` on 2026-10-26 | A rolling update and a rollback pair head with this release | Upgrade, and rollback to it on the upgraded schema without a restore |
| `v0.2.0-rc.1` | The oldest 0.2 release. Upgrading from it skips every candidate since, which compatibility allows | Upgrade across skipped releases. Rollback to it is not promised, so the drill restores instead |

When `v0.2.0` is released, the oldest base becomes `v0.2.0`, because the 0.1 line and candidates before a stable are then outside the promise. That is a one-word change to the `upgrade-drill` task in [mise.toml](../../mise.toml).

`v0.1.0` was run once by hand (`PLOEG_UPGRADE_DRILL_BASES=v0.1.0 mise run upgrade-drill`) to see what the database half of the reinstall exception costs. It passed: its 37 migrations are unchanged at head, so its backup migrates forward like any other. ADR 0158 still treats 0.1 to 0.2 as a reinstall, for the configuration and chart changes, not the schema.

## Method

The drill is two tests in [pkg/worker](../../pkg/worker/): `TestUpgradeDrillRestoresAnOlderReleasesBackupMigratesItToHeadAndFencesItsWorkers` and `TestUpgradeDrillAnInterruptedMigrationRecoversByRerunningOrByRestoringTheOlderRelease`. They are opt-in because they build older releases. Run them with `mise run upgrade-drill`, which sets `PLOEG_UPGRADE_DRILL_BASES="previous v0.2.0-rc.1"`. The [Upgrade drill workflow](../../.github/workflows/upgrade-drill.yml) runs them on every pull request that touches `pkg/store`, `pkg/httpapi`, `pkg/shiftengine`, `pkg/worker`, `go.mod` or `mise.toml`, on every push to `development` and `main`, and weekly, and opens an issue when the weekly run fails.

### The older release is its own code

For each base, the drill takes the tag's tree with `git archive`, drops one program into it, [testdata/upgrade-drill-seed/main.go](../../pkg/worker/testdata/upgrade-drill-seed/main.go), and builds that program against the tag's own `go.mod`, so it links the older release's `pkg/store`, `pkg/httpapi`, `pkg/shiftengine` and `pkg/worker`. Every row in the backup is written by the older release's code, and the database starts on the older release's migrations. The program has these commands:

| Command | What it runs, from the older release |
| --- | --- |
| `serve` | `Store.Migrate`, then the run API (`httpapi.Server`) with its Shift engine, as `ploegd` assembles them |
| `ingest` | `Store.IngestAssigned`, and `Engine.EnsureShift` for a planned item |
| `run` | A claim, checkpoint and outcome through the release's own `worker.APIClient`; an LLM account reserved, minted and issued through its store |
| `settle` | `RecordLLMBlocked` and `SettleLLMAccount` |
| `hold` | A worker process: it claims, checkpoints, renews every second through the release's own client, and keeps renewing through any outage. When its token is gone it tries a checkpoint and an outcome with that token, then claims again |
| `bulk` | 1,000 shift-less items, each claimed, checkpointed, reported (`no_change_needed`, `stuck` or `pr_opened` in turn) and settled at 0.01 USD |

Two signatures changed between `rc.1` and head: `store.New` gained a pool configuration and `APIClient.Renew` gained a context and returns a struct. The program calls both through reflection, so one source builds against every 0.2 release.

### The upgrade drill

1. **Primary.** Embedded PostgreSQL 18, migrated by the older release's `serve`, behind a front that plays the Kubernetes Service: workers and clients reach whichever `ploegd` sits behind it.
2. **State before the backup, all written by the older release.** The restore drill's set: a planned item finished and settled at 1.2345 USD against its Shift, an item parked at `needs_human`, two queued items, and a planned item held by the older release's `hold` worker with a checkpoint and an issued account. Plus the 1,000 bulk items: 13,037 rows in all, a 76 to 77 MB base backup.
3. **Backup.** The front holds renewals for the moment of the snapshot, and the drill takes the restore drill's hot base backup (`pg_backup_start`, a copy of the data directory, `pg_backup_stop`, the WAL written meanwhile).
4. **Writes after the backup point, still by the older release.** A renewal, an item claimed, finished and settled at 0.75 USD, an item claimed and held with a checkpoint, and a new assignment.
5. **Destruction.** The older `ploegd` is killed, PostgreSQL stops and its data directory is deleted. The front answers 503, as a Service with no ready pod does. The `hold` worker keeps renewing and failing.
6. **Restore and upgrade.** The backup is recovered into a new data directory and a new PostgreSQL. Before anything migrates, every table equals the backup point. Then head's `Store.Migrate` runs (no migrations from `rc.77`, 39 from `rc.1`), and head's `ploegd` boots and sweeps once. The RTO is the sum of these steps: restoring the files, starting and recovering PostgreSQL, migrating, booting with the first sweep. It leaves out the drill's own checks.
7. **Assertions.**
   - Every column of every table the older release created and head still has holds the backup point's values. Two changes are allowed by name: [0052](../../pkg/store/migrations/0052_work_item_state_active.sql) renames `leased` to `active` (ADR 0093), and [0044](../../pkg/store/migrations/0044_remove_run_cards.sql) drops the card tables and three `pull_requests` columns (ADR 0080). Any other dropped table or column, or any other changed value, fails the drill.
   - `schema_migrations` holds exactly head's migrations.
   - Settled spend and Shift spend equal the backup point's.
   - The in-flight lease is still live when head serves, so the next step tests the fence and not only the sweeper.
   - After that lease's deadline, the tokens minted in the gap get `gone` and 404, and the settled Run's second, different outcome is refused. The front then routes to head. The older release's `hold` worker reconnects: its renewal gets `gone`, its checkpoint and its outcome get 404, and no table changes.
   - The sweep finishes the in-flight Run as `failed`/`lease_lost`. No Run or Lease is overdue, no item is `active` without a live Run, the parked item stays parked, both items claimed in the gap are `queued`, and the gap's assignment is absent. The audit trail up to the backup point is unchanged.
   - The `hold` worker's next claim gets item 7 under a new token and its outcome is accepted. Head re-dispatches both queued items under new tokens; one of them settles 0.5 USD, and the ledger moves by exactly that amount.
   - **Version handshake.** A second `ploegd` reporting a release two minors ahead (`0.4.0`) refuses the `rc.77` worker's claim with 426 and starts no Run. The `rc.1` worker predates the handshake (it sends no `workerVersion`), so it is served, as compatibility says. See the findings.
   - **Rollback, for the previous release only.** Head's `ploegd` is taken out of the front and the older `serve` starts on the upgraded database. Its `Migrate` finds nothing to do, it serves, and it takes a new item through claim and outcome. Head then starts again on the same schema.

### The interrupted-migration drill

From the older release's backup, the drill picks a migration about two thirds into the pending ones that touches a table none of the pending migrations before it touches, and that has at least one statement before that touch. For `rc.1` and `v0.1.0` that is [0071](../../pkg/store/migrations/0071_lookup_indexes.sql) (34th of 39 pending for `rc.1`), whose earlier `CREATE INDEX` statements succeed before it reaches `operator_executions`. A session holds `ACCESS EXCLUSIVE` on that table while head's `Migrate` runs, and each case restores the backup afresh:

| Case | How it is interrupted |
| --- | --- |
| Lock timeout | Nothing more: the migration waits `lock_timeout` (5 s) and fails, as [`Store.Migrate`](../../pkg/store/store.go) intends when live traffic holds a table |
| `ploegd` killed | While the migration waits, its backend is terminated with `pg_terminate_backend`, as when the pod or node dies |
| PostgreSQL crash | While the migration waits, `pg_ctl stop -m immediate`; a new PostgreSQL starts on the same data directory and runs crash recovery |
| Restore instead | The lock-timeout case, then the partly migrated database is discarded, the pre-upgrade backup restored and the older release started on it |

After each interruption, `schema_migrations` holds every migration before the interrupted one and none from it on. For the first three cases a new `Store.Migrate`, as the next `ploegd` start runs it, must finish: `schema_migrations` equals head's, the catalogue (every column with its type, nullability and default, every constraint, index, trigger, function and view) equals that of an uninterrupted upgrade of the same backup, and so does every row. Head's `ploegd` then serves and takes a queued item through claim and outcome. For the restore case the database must equal the backup, hold only the older release's migrations, and serve a Run on the older release.

## Measurements

Embedded PostgreSQL 18 and the dataset above (13,037 rows, 76 to 77 MB base backup), under `-race`, on two machines: the CI runner (`ubuntu-24.04`, [run 40294384069](https://github.com/ploeg-hq/ploeg/actions/runs/40294384069), the workflow's first run, on this change) and a 32-thread workstation (three runs of the final code per base). The RTO is the time the recovery steps take, with the drill's checks left out. The RPO is the time from the backup point to destruction, and each run lost exactly the writes after the backup point.

### Restore and upgrade

| From | Where | Migrations to head | RTO | Restore files | PostgreSQL start and recovery | Migrate | `ploegd` boot and first sweep | RPO | Rollback served again |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `v0.2.0-rc.77` | CI | 0 | 287 ms | 138 ms | 121 ms | 16 ms | 11 ms | 1.18 s | 21 ms |
| `v0.2.0-rc.1` | CI | 39 | 424 ms | 128 ms | 122 ms | 164 ms | 9 ms | 0.54 s | not promised; restore |
| `v0.2.0-rc.77` | workstation | 0 | 0.60–0.67 s | 107–115 ms | 459–546 ms | 7–8 ms | 6–16 ms | 0.65–1.12 s | 21 ms |
| `v0.2.0-rc.1` | workstation | 39 | 0.77–1.03 s | 101–106 ms | 507–730 ms | 162–175 ms | 5–16 ms | 0.49–0.76 s | not promised; restore |
| `v0.1.0`, once | workstation | 41 | 0.76 s | 100 ms | 486 ms | 170 ms | 5 ms | 0.49 s | not promised |

### Interrupted upgrade from `v0.2.0-rc.1`

| Case | Recovery | Served again after, CI | Served again after, workstation |
| --- | --- | --- | --- |
| Lock timeout | Restart `ploegd`; `Migrate` resumes at 0071 | 47 ms (rerun 38 ms, boot 9 ms) | 33–44 ms (rerun 28–39 ms, boot 5 ms) |
| `ploegd` killed | Same | 49 ms | 33–43 ms |
| PostgreSQL crash | PostgreSQL crash recovery, then the same | 52 ms after PostgreSQL is back | 44–49 ms |
| Restore instead | Restore the pre-upgrade backup, run `rc.1` | 266 ms (files 124 ms, PostgreSQL 121 ms, `ploegd` start 21 ms) | 0.59–0.66 s (files about 0.1 s, PostgreSQL about 0.5 s, `ploegd` start 10–11 ms) |

The lock-timeout times start after the 5 s the failed attempt waited. PostgreSQL starts faster on the CI runner than on the workstation, as in the restore drill. In a cluster the rerun is not immediate: a `ploegd` that fails a migration exits, and Kubernetes restarts it after its crash-loop backoff, 10 s doubling to 5 minutes. That delay, not the rerun, sets the RTO of an interrupted upgrade.

## Mutation tests

Each mutation was applied to `development` at 8e98500 plus this change, the drill run, and the mutation reverted. The unmutated drill passes for both bases.

| Mutation | Drill | Result |
| --- | --- | --- |
| `refuseExpiredLease` never refuses | upgrade | Fails: the `rc.77` and `rc.1` workers' renewals of their expired leases succeed after the upgrade and they never see `gone` |
| `workerVersionRefusalFor` serves every version | upgrade | Fails: ploegd `0.4.0` gives the `rc.77` worker item 41 |
| A new migration multiplies `settled_spend` by 0.9 | upgrade | Fails for both bases: `run_llm_accounts` differs from the backup point |
| A new migration drops `checkpoints.branch` | upgrade | Fails: "the upgrade dropped column checkpoints.branch, which no migration retires on record" |
| 0052 no longer rewrites `leased` | upgrade | Fails for `rc.1`: `work_items` differs, item 7 stays `leased` |
| `applyMigration` runs 0071's statements one by one outside its transaction | interrupted | Fails in the three rerun cases: the index 0071 created before the lock stays behind, and the rerun stops on `relation "shifts_by_work_item" already exists` |
| `applyMigration` records the migration in `schema_migrations` before applying it | interrupted | Fails in all four cases: after the interruption `schema_migrations` already names 0071 |

The first version of the drill used the restore drill's 5-second lease. Under `-race` the restore and its checks took longer than that, so the lease had expired before head's first sweep, the sweeper finished the Run, and the worker was refused for that reason. The `refuseExpiredLease` mutation then passed. The drill now uses a 20-second lease and fails if the lease is not live when head starts serving.

## Findings

- **No defect found.** An older release's data survives the upgrade, with only the two recorded changes. A token from before the upgrade cannot renew, checkpoint or report, and accounting continues from the backup point. Rolling back to the previous release on the upgraded schema works without a restore.
- **Interrupted migrations rerun exactly** because [`applyMigration`](../../pkg/store/store.go) applies a migration and records it in one transaction. The two mutations above show the drill fails if either half of that is lost.
- **A worker from before `rc.67` is never judged by version.** The handshake came with `v0.2.0-rc.67`. A worker from an earlier release sends no `workerVersion`, and compatibility says such a claim is not judged, so even a `0.4.0` ploegd served the `rc.1` worker. Only the Lease and the Run token protect authority from such a worker, and the drill shows they do. The window closes on its own: every `0.2` stable will send its version.
- **The handshake judges claims, not tokens.** A worker refused with 426 claims nothing, but a token it already holds is fenced by its Lease and Run state alone, as after any restore.

## Limits

- **Embedded PostgreSQL, not CloudNativePG.** As in the restore drill, there is no object storage, WAL archive, pod scheduling or volume binding. A production restore adds all of these.
- **One node, a modest dataset.** 13,037 rows and a 77 MB backup. Migration time grows with the tables a migration rewrites or indexes; 0052's `UPDATE` and the index migrations are the ones that scale with data.
- **`ploegd` and the worker are assembled, not deployed.** The older release runs as its `Store` and run API, not its `cmd/ploegd` binary, so its config loading, orphan sweeps and gateway settlement are not part of it. Head runs the lease-fencing tests' assembly, as in the restore drill. The older worker is the release's `APIClient` in a loop that never gives up, not `ploeg-worker`: the real worker cancels its harness after three failed renewals, so it is less persistent than the drill's.
- **Not the chart.** Stable channel gate G5 ([stable channel plan](../ops/stable-channel-plan.md)) upgrades and rolls back a whole installation on a kind cluster. This drill covers the database and API half of that. The chart, its values and the images are not exercised.
- **A blocked migration, not a broken one.** Each interruption stops a correct migration. A migration whose SQL fails on the data cannot be fixed by rerunning; the restore case covers that recovery, and the runbook says so.
