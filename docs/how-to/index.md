---
type: landing
audience: [operator]
owner: ploeg
last_verified: 2026-09-23
verified_by: "Read ops/helm/ploeg/{values.yaml,templates/*}, pkg/store/migrations/0001-0016 and cmd/ploegd/{main,sweep}.go"
---

# Operate Ploeg: runbooks

These runbooks cover the recurring operator tasks for a cluster deployment of Ploeg. Each one starts from a symptom, gives the commands, says how to verify the result and ends with a symptom → cause → fix table.

| Runbook | Use it when |
| --- | --- |
| [Recover a stuck Lease or Run](recover-a-stuck-lease-or-run.md) | A Work Item stays `leased`, a Run stays `running` or `pending`, or a new writer cannot start |
| [Rotate credentials](rotate-credentials.md) | You replace the LiteLLM master key, a forge token, a worker bootstrap token or the worker signing key |
| [Drain workers before maintenance](drain-workers.md) | You need a quiet Ploeg before an upgrade, a gateway change or database work |
| [Investigate a Run's spend](investigate-a-runs-spend.md) | A Shift's budget looks wrong, a hold is not released, or you need to prove what a Run cost |
| [Restore after a database outage](restore-after-a-database-outage.md) | PostgreSQL was unavailable or was restored from a backup |
| [Give Runs a toolchain and checks](give-runs-a-toolchain-and-checks.md) | Agents open pull requests that fail checks they could not run in the sandbox |
| [Route a board that serves several repositories](route-a-multi-repo-board.md) | One board holds tickets for more than one repository, or a ticket was refused with a `repo/*` label reason |
| [Send deploys from a pipeline to Ploeg](send-deploys-from-a-pipeline.md) | An operator consumer should see where a merged change is live |
| [Map tracker statuses to delivery gates](map-tracker-statuses-to-gates.md) | An operator consumer should see every board status, the gate a ticket stands in, and bounces with a reason |
| [Ask Ploeg from an AI client over MCP](ask-ploeg-from-an-ai-client.md) | A person wants to ask Claude Code, Codex or Cursor what waits for them, why, and what it cost |

For accounts whose final cost is uncertain, [Reconcile uncertainty](../ops/managed-workers.md#reconcile-uncertainty) sets the rules these runbooks follow.

## Before you start

The commands assume the names the current GitOps layout gives the [Helm chart](../../ops/helm/ploeg/README.md)'s release, database and gateway; the chart itself names no database or gateway. If your deployment differs, substitute the names:

| Thing | Default name |
| --- | --- |
| Namespace | `ploeg` |
| Controller Deployment, Service and HelmRelease | `ploeg` (the Helm release name) |
| Worker workloads (ScaledJob or CronJob) | `ploeg-worker-<team>` or `ploeg-worker-<team>-<role>` |
| Worker pods | label `app.kubernetes.io/name=ploeg-worker`, plus `ploeg.webgrip.dev/team` and `ploeg.webgrip.dev/role` |
| PostgreSQL cluster (CloudNativePG) | `ploeg-db`, database `app` |
| LiteLLM gateway | Service `litellm` in namespace `ai`, port 4000 |

Terms used throughout: a **Work Item** is one unit of work from a tracker. A **Shift** is one Team's engagement with it, holding the budget pool. A **Run** is one Role working once, as one worker pod. A **Lease** is the exclusive right to write the Shift's branch; only writing Runs hold one. The **sweeper** is the loop in `ploegd` ([sweep.go](../../cmd/ploegd/sweep.go)) that expires overdue Leases and Runs, blocks their gateway keys and settles spend. The [glossary](../domain/glossary.md) defines each term.

Desired state lives in Git and Flux reconciles it. The runbooks use `kubectl` for observation and for short, deliberate interventions. When a step needs a lasting change, make it in the GitOps repository.

### Open a database session

Ploeg has no operator CLI. You inspect its state with `psql` on the primary database pod:

```sh
PRIMARY=$(kubectl -n ploeg get pods \
  -l cnpg.io/cluster=ploeg-db,cnpg.io/instanceRole=primary -o name)
kubectl -n ploeg exec -it "$PRIMARY" -c postgres -- psql -d app
```

Older CloudNativePG versions label the primary `role=primary` instead of `cnpg.io/instanceRole=primary`.

Start every investigation read-only, so a mistyped statement cannot change state:

```sql
SET default_transaction_read_only = on;
```

Leave read-only mode only for a write that a runbook names, and run that write in its own transaction. The queries select `left(run_token, 12)`, never the full token: the full run token is a credential. The first 12 characters are the suffix of the Run's LiteLLM key alias `ploeg-<12 hex>`.
