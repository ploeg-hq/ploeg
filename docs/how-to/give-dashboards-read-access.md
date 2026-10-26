---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-26
verified_by: "Read pkg/store/migrations/0072_observer_role.sql, 0080_observer_role_reads_more_metadata.sql, cmd/ploegd/observer.go and their tests; ran them against the embedded PostgreSQL"
---

# Give dashboards and exporters read access

**Symptom:** Grafana, a postgres exporter or the KEDA scaler reads Ploeg's database with a login in `pg_read_all_data`. That login can read every Run token, every transcript, the context people attached and the full audit log.

**Goal:** those logins read only the metadata dashboards and metrics need, through one role Ploeg keeps up to date: `ploeg_observer`.

Read [Before you start](index.md#before-you-start) for names and the database session.

## What `ploeg_observer` can read

Migration [`0072_observer_role.sql`](../../pkg/store/migrations/0072_observer_role.sql) defines `ploeg_grant_observer()`, the one list of grants, and [`0080_observer_role_reads_more_metadata.sql`](../../pkg/store/migrations/0080_observer_role_reads_more_metadata.sql) extends it. ploegd calls it once at every boot, right after it applies migrations. Ploeg never creates the role: its database owner has no `CREATEROLE`. Without the role the call changes nothing.

| Granted | Not granted |
| --- | --- |
| `shifts`, `pull_requests`, `pull_request_reviews`, `pull_request_events`, `pull_request_ci_runs`, `pull_request_deployments`, `pull_request_reverts`, `deployments`, `deployment_checks`, `status_transitions`, `inference_allowances`, `forge_ci_gates`, `run_transcript_totals` | `run_transcript_entries`, `work_item_context`, `operator_notes`, `question_answers`, `work_item_note_outbox`, `note_reviews`, `claude_sessions`, `asks`, `chats`, the `operator_*` execution tables, `run_board_catch_ups`, `work_item_premise_checks`, `work_item_requeues`, `run_llm_operator_settlements` |
| `work_items` except `description` | `work_items.description` |
| `agent_runs` state, timing, team, Role, model, Harness, outcome, `failure_reason`, `usage`, `links`, `trace_alias` and take-over state (`yields`, `yield_requested_at`) | `run_token`, `summary`, `stuck_reason`, `findings`, `problem`, `solution`, `verification`, `delivery`, questions, answers and briefing columns |
| `leases` without its tokens | `leases.run_token`, `leases.forge_token_id` |
| `run_llm_accounts` alias, state and amounts; `run_llm_adjustments` amounts; `run_budget_holds.shift_id` and `reserved` | `run_token`, `gateway_key_id`, `reconciliation_evidence`, adjustment `evidence` |
| `checkpoints` phase, time, `node_name` and `pod_uid`; `gate_transitions` without `reason`; `work_item_reviews` without `body` | checkpoint `branch` and `pr_url`, transition `reason`, review `body` |
| `audit_log` `id`, `at`, `actor`, `action`, `work_item_id` | `audit_log.detail` |
| `work_item_take_overs` who, when, which Shift and Round, branch, commits and hand-back | take-over `command_id` and `yield_detail` |

A Run's token is a credential, so join a Run to its account on `run_llm_accounts.alias = agent_runs.trace_alias` (`ploeg-<first 12 hex of the token>`), never on `run_token`.

## 1. Create the role and put the logins in it

With CloudNativePG, declare the roles on the `Cluster` in the GitOps repository. `ploeg_observer` cannot log in; each reader logs in with its own role and inherits the grants:

```yaml
spec:
  managed:
    roles:
      - name: ploeg_observer
        ensure: present
        login: false
      - name: grafana_ro
        ensure: present
        login: true
        inRoles: [ploeg_observer]
        passwordSecret:
          name: ploeg-db-grafana
```

Do the same for the exporter's login and for `ploeg_scaler`, the login the chart's KEDA triggers use (`executor.scaler.userName`). Remove `pg_read_all_data` from each of them. A reader that also shows `pg_stat_statements` needs `pg_read_all_stats` besides `ploeg_observer`; that role shows query statistics, not table rows.

Without CloudNativePG, a superuser runs:

```sql
CREATE ROLE ploeg_observer NOLOGIN;
GRANT ploeg_observer TO grafana_ro;
REVOKE pg_read_all_data FROM grafana_ro;
```

## 2. Restart ploegd

Restart ploegd, for example with `kubectl -n ploeg rollout restart deployment/ploeg`. At boot it applies the grants and logs one of:

| Log line | Meaning |
| --- | --- |
| `ploeg_observer grants applied` | The role exists and has the grants |
| `ploeg_observer role absent; no read grants applied` | The role does not exist in this database cluster; go back to step 1 |
| `ploeg_observer grants not applied; dashboards keep the access they had` | The call failed, with the reason in `err`; ploegd still starts |

Calling it on every boot is harmless: a grant that is already there stays as it is. Without a restart, the database owner can apply the same grants in a [database session](index.md#open-a-database-session) with `SELECT ploeg_grant_observer();`, which returns `true` when it granted.

Verify:

```sql
SET ROLE ploeg_observer;
SELECT state, count(*) FROM work_items GROUP BY state;
SELECT run_token FROM agent_runs LIMIT 1;
RESET ROLE;
```

The first query returns counts; the second fails with `permission denied for table agent_runs`.

## 3. Adjust queries that read refused columns

A query that selects a column `ploeg_observer` cannot read fails as a whole. Two shapes that exporters used against `pg_read_all_data` need a change:

| Old query reads | Read instead |
| --- | --- |
| `'ploeg-' \|\| left(r.run_token, 12)` as the LiteLLM key alias | `r.trace_alias` |
| `stuck_reason` as a label | `outcome = 'stuck'` counts, or `failure_reason`; the stuck reason is the agent's own prose |

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `permission denied` for a table in the granted list | The role was created after ploegd last started | Restart ploegd (step 2) |
| ploegd logs `ploeg_observer grants not applied` | ploegd's database login does not own the tables, or the call timed out | Read `err`; run `SELECT ploeg_grant_observer();` as the owner |
| `permission denied for table agent_runs` on one query, others work | The query selects a refused column, often `run_token` or `stuck_reason` | Use the replacements in step 3 |
| `ploeg_grant_observer()` returns `false`, or ploegd logs `role absent` | The role does not exist in this cluster | Create it in step 1 |
| A new table is missing from dashboards after an upgrade | The migration that added it did not extend `ploeg_grant_observer()` | File a bug: every migration that adds an observable table extends the function |
