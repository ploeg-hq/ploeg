---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-27
verified_by: "Read pkg/httpapi/{operator,operator_accounting,operator_activity,operator_settlement,llm_control}.go, pkg/store/{operator,operator_accounting,llm_accounts,llm_block_queue,llm_settlement,llm_operator_settlement}.go, migrations 0012, 0035 and 0075, cmd/ploeg-mcp/tools.go and cmd/ploegd/sweep.go; TestARunsSpendIsTracedFromAuthorizationToCorrectionWithoutTheDatabase and TestRunSpendTracesAFailedRunsAccountWithinTheConsumersTeams exercise every read below"
---

# Investigate a Run's spend

**Symptom:** a Shift reports more or less spend than you expect, its budget looks used up while nothing runs, a Run failed and you need to know what it cost, or you need to show what one Run cost.

**Goal:** separate what Ploeg has **reserved** (held against the budget) from what it has **settled** (charged), trace a Run's inference account through its states, compare it with the gateway's spend logs, tell whether the settlement sweep will finish the job or you must act, and settle an account from evidence when the gateway can no longer answer for it. Every step reads Ploeg's operator API or `ploeg-mcp`; none needs a database session.

The rules for uncertain cost are in [Reconcile uncertainty](../ops/managed-workers.md#reconcile-uncertainty).

## Before you start

You need an Operator Consumer whose `teams` include the Run's Team ([configuration](../reference/configuration.md)). A read-only consumer is enough for steps 1 to 6: spend is metadata, not Run content, so it needs no `readContent`. Step 7 needs a consumer with `execute: true`.

```sh
PLOEG=https://<ploeg>/api/v1/operator
AUTH="Authorization: Bearer $PLOEG_OPERATOR_TOKEN"
```

From an AI client with [ploeg-mcp](ask-ploeg-from-an-ai-client.md), `ploeg_get_work` answers step 1 and `ploeg_run_spend` answers steps 2 to 4 for one Run.

## How Ploeg counts money

Budgets have two levels ([ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md)):

- The **Shift** holds the pool: `budgetUsd`, and `spentUsd`, the settled total.
- Each **Run** has an authorization, `authorizedUsd`, fixed when it is claimed: the smaller of the Role's cap and what the pool has left.
- A managed Run also has an **inference account**. It records the gateway key's alias (`ploeg-` plus the first 12 hex characters of the run token), the last spend seen (`observedUsd`) and the settled amount (`reconciledUsd`).
- The **Hold** (`heldUsd`) is never stored; Ploeg derives it for each Run, and a Shift's Hold is their sum. The rule, from migration 0012:

  | Run | Hold |
  | --- | --- |
  | No account, `running` | `authorizedUsd` |
  | No account, not running | 0 |
  | Account not yet `reconciled` | the larger of `authorizedUsd` and `observedUsd` |
  | Account `reconciled` | `observedUsd − reconciledUsd`, at least 0 |

- **Settled** grows in two ways. A managed Run adds its reconciled amount when the sweeper settles its account. A Run without an account adds the `costUsd` its harness reported, which is the agent's own claim, when it reports its outcome.

The pool is empty when `budgetUsd − spentUsd − heldUsd` is below the minimum a Run needs. Claims are then refused. When settling the Holds of finished Runs could not refill it, the engine parks the Shift at `needs_human` with a reason such as `budget exhausted: pool 6.00, spent 2.10, reserved 3.95`. When it could, the Shift waits with its Run pending until settlement releases the Holds. If they are still unsettled 24 hours after their Runs finished, the Shift parks with `budget held by unsettled runs: pool 8.00, spent 0.00, held 8.00` ([ADR-0048](../adrs/0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md)).

## Inference account states

| State | Meaning | What moves it on |
| --- | --- | --- |
| `reserved` | Budget held, no key requested | The mint, or settlement at zero once the Run finishes (`ploegd:mint-never-began`) |
| `minting` | Key requested; the gateway's answer is not yet recorded | The block sweep, once the Run finishes |
| `issued` | Key exists; its hashed id is recorded | The block sweep, once the Run finishes |
| `unknown` | Issue or spend read failed, so the key's fate is uncertain | The block sweep, while the gateway can report the key |
| `blocked` | Key revoked; `observedUsd` recorded | The settlement sweep, after `PLOEG_LLM_SETTLE_AFTER` (15 minutes) with no change |
| `reconciled` | Settled from the gateway's spend logs; evidence stored | Nothing. Until `correctionsUntil`, the correction sweep reads the spend logs again and charges any late entry as an adjustment |

Every sweep interval, the controller tries to block every finished Run's account that is still `minting`, `issued` or `unknown`. It then settles every finished Run's account that is `reserved`, or `blocked` and quiet ([sweep.go](../../cmd/ploegd/sweep.go), [llm_control.go](../../pkg/httpapi/llm_control.go)). Settlement reads LiteLLM's spend logs for the recorded hashed key and any key that still carries the alias. It never settles below `observedUsd` and never treats a missing key as zero spend. A blocked account settles at four decimals: the spend-log total is rounded half-up to match `observedUsd` before either is compared or stored.

## 1. Find the Work Item, its Shifts and its Runs

Look the Work Item up by its tracker ticket, then read it:

```sh
curl -s -H "$AUTH" "$PLOEG/work-items?provider=vikunja&externalId=<ticket id>&limit=1" | jq '.items[0].id'
curl -s -H "$AUTH" "$PLOEG/work-items/<work item id>" | jq '{
  shifts: [.shifts[] | {id, round, budgetUsd, spentUsd, heldUsd, closeReason}],
  runs: [.runs[] | {id, shiftId, role, round, state, outcome, failureReason, authorizedUsd, costStatus, keyAlias}]}'
```

In ploeg-mcp: `ploeg_get_work` with `ref` set to `vikunja:<ticket id>`, the Ploeg id or the tracker task URL. Each Shift shows its `budgetUsd`, `spentUsd` and `heldUsd` ([ADR-0093](../adrs/0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md); until the deprecated forms are removed it also sends `reservedUsd`, equal to `heldUsd`). `remaining` is `budgetUsd − spentUsd − heldUsd`. A failed Run carries its `failureReason`; `costStatus` says whether its cost is `observed`, `provisional`, `settled` or `unknown`. `GET $PLOEG/runs?outcome=failed` lists failed Runs across the consumer's Teams when you start from the Run instead of the ticket.

## 2. Read the Run's account and Hold

```sh
curl -s -H "$AUTH" "$PLOEG/runs/<run id>/accounting" | jq '.accounting | {
  runState, outcome, authorizedUsd, harnessReportedUsd, heldUsd, costStatus, account}'
```

In ploeg-mcp: `ploeg_run_spend` with `runId`. Its `summary` names the account's state and what settles it.

- `harnessReportedUsd` is what the agent said it spent. For a managed Run it is not the charge; the account is.
- `account` is null for a Run without an inference account.
- `account.updatedAt` is when the account last changed; a `blocked` account settles once it has not changed for 15 minutes.
- `account.evidence` tells you how the account settled: `ploegd:mint-never-began …` for zero, `litellm:spend-logs alias=… keys=… entries=… usd=… unchanged-since=… read-at=…` from the gateway, or `operator:evidence consumer=… command=…` from step 7. It is empty before settlement.
- `account.listedUnsettled` is true when the account is one that `GET /api/v1/operator/unsettled-accounts` lists, so step 7 may settle it.

The answer never carries the gateway's hashed key id or the run token. It carries no ticket text, summary or transcript either, which is why a read-only consumer may read it ([ADR-0160](../adrs/0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)).

## 3. Read the corrections and any operator settlement

The same answer holds them:

```sh
curl -s -H "$AUTH" "$PLOEG/runs/<run id>/accounting" | jq '.accounting | {
  settledUsd: .account.settledUsd, reconciledUsd: .account.reconciledUsd,
  correctionsUntil: .account.correctionsUntil, costKnown: .account.costKnown,
  adjustments, operatorSettlement}'
```

`settledUsd` is the first settlement and `reconciledUsd` the amount including every correction. Each `adjustments` entry has the previous and new totals, the evidence of the new reading and when it was recorded. `operatorSettlement` is null unless step 7 settled the account; then it names the consumer, `commandId`, actor, the state it replaced, the amount, the Hold it released and the evidence.

## 4. Read the audit trail

```sh
curl -s -H "$AUTH" "$PLOEG/runs/<run id>/accounting" | jq '.accounting.events[] | [.at, .actor, .action, .observedUsd, .spendUsd, .deltaUsd]'
```

`events` holds the Run's newest 200 `llm.*` audit events, oldest first, each with only the amounts it recorded; `eventsTruncated` is true when older ones were left out. A normal managed Run shows `llm.reserved`, `llm.minting`, `llm.issued`, one or more `llm.observed`, `llm.blocked`, then `llm.reconciled` with `spendUsd` and `deltaUsd`. A correction is another `llm.reconciled` with `adjustment: true`, an operator settlement one with `operatorEvidence: true` and the operator as actor. `llm.unissued_blocked` means the Run ended before a key was minted. `llm.unknown` marks the point where the controller lost certainty.

For everything else that happened to the Work Item, page `GET $PLOEG/events?workItemId=<work item id>` or call `ploeg_changes_since` with `workItemId`.

## 5. Compare with the gateway's spend logs

The settlement source is LiteLLM's per-request spend log, not the key's running total: LiteLLM writes that total asynchronously and loses it when the key is deleted. To check a settled amount yourself, sum the log entries for the account's key, found by its alias `account.keyAlias`.

Use a LiteLLM admin credential that you are authorized to hold. Do not copy `ploegd`'s master key onto a workstation.

```sh
kubectl -n ai port-forward svc/litellm 4000:4000
# in a second shell
ALIAS=<keyAlias>
curl -s -G -H "Authorization: Bearer $LITELLM_ADMIN_KEY" "http://localhost:4000/spend/logs/ui" \
  --data-urlencode "key_alias=$ALIAS" --data-urlencode "start_date=2000-01-01 00:00:00" \
  --data-urlencode "end_date=<a day after the Run finished, YYYY-MM-DD HH:MM:SS>" --data-urlencode "page_size=1000" \
  | jq --arg alias "$ALIAS" '[.data[] | select(.metadata.user_api_key_alias == $alias)]
      | {entries: length, keys: (map(.api_key) | unique | length), usd: (map(.spend) | add)}'
```

This is the read settlement itself falls back to ([client.go](../../pkg/litellm/client.go), `SpendLogsByAlias`). It still answers after the key was deleted. The gateway's alias filter matches a substring, so the `select` keeps only entries of this exact alias; a result with `total_pages` above 1 needs the next `page` too. In the LiteLLM admin UI, filter the logs by the key alias for the same answer. Spend logs must stay enabled on the gateway. With logging off, a minted key would show no entries, and only a recorded `observedUsd` stops it settling at zero.

Late entries can appear after settlement, when a request was already in flight as the key was blocked. A settlement read from the spend logs stays provisional (`costStatus: provisional`) for `PLOEG_LLM_CORRECTION_WINDOW` (default `24h`), until `account.correctionsUntil`. Every 15 minutes in that window the correction sweep reads the spend logs again. A higher total charges only the difference to the Shift, raises `reconciledUsd`, adds an `adjustments` entry, and writes an `llm.reconciled` audit event with `adjustment: true`. The first settlement stays in `settledAt`, `settledUsd` and `evidence`. A lower total is refused and logged as `managed settlement correction unresolved`. After the window the cost is final (`costStatus: settled`): nothing re-reads it, and a spend reading no longer raises `observedUsd`, so it cannot hold budget again ([accounting invariants](../reference/accounting-invariants.md)). **Not implemented yet:** an operator command that records a correction after the window. Record the difference and its evidence in the operational record; do not change Ploeg's ledger by hand.

A minted key whose spend logs have no entries settles at zero so its Hold is released, but `costKnown` is false and `costStatus` is `unknown`: the Run's cost is reported as unknown, not as zero, until a spend-log entry confirms it. An entry that costs zero does confirm zero.

## 6. Decide whether to wait or act

1. **The account is `blocked` and `account.updatedAt` is under 15 minutes ago.** Wait. The next sweep after the quiet period settles it.
2. **The account is `blocked` and older, but still not settled.** Look for the reason in the logs:

   ```sh
   kubectl -n ploeg logs deployment/ploeg --since=1h | grep -E 'managed (settlement|account)'
   ```

   `managed settlement unresolved … gateway spend logs unavailable: <cause>` means reading the gateway's spend failed; the cause after the colon says which call and why (`/key/list`, `/spend/logs`, or `/spend/logs/ui` for an account whose key identity was never recorded). Fix the gateway; the sweep retries every interval.

   If `updatedAt` keeps moving, something keeps raising the account's observed spend, and each `llm.observed` in step 4 restarts the quiet period. Before 0.2.0-rc.15, every operator spend read did that even when nothing was spent, so a finished operator execution's account never settled. A `blocked` account whose Run finished more than an hour ago shows `listedUnsettled: true` and appears in `GET $PLOEG/unsettled-accounts` with its Hold.
3. **The account is `minting`, `issued` or `unknown` on a finished Run.** The block sweep retries it. `managed key block retry unresolved` repeating means the gateway cannot revoke the key or report its spend, often because the key is already gone. The account keeps its full Hold. `GET $PLOEG/unsettled-accounts` lists every such Run in the consumer's Teams, oldest first, with its Work Item, account state, held amount and since when, plus the count and held total. While the gateway can still answer, wait for the sweep. When it can no longer answer for the key, follow [Reconcile uncertainty](../ops/managed-workers.md#reconcile-uncertainty) and settle the account from your evidence (step 7).
4. **The Run is still `running`.** Its Hold is its authorization. See [Recover a stuck Lease or Run](recover-a-stuck-lease-or-run.md) if it should have ended.

## 7. Settle an account from evidence

Use this only for an account whose `listedUnsettled` is true and that the gateway can no longer answer for: its spend logs stay unreadable, or the key is gone in a way the block sweep cannot confirm. While the sweeps can still settle an account, they do, and the command refuses it.

1. Confirm the key can no longer spend. For an account in `minting`, `issued` or `unknown`, check in the gateway that no key with the alias `ploeg-<12 hex>` exists or that it is blocked. Settling stops the block sweep retrying the key.
2. Gather the amount and where it comes from: the provider's invoice or usage export for the key's requests, or a gateway export you trust. Write the evidence so someone else can find the source again, for example `provider invoice 2026-10 line 14, project ploeg, 0.42 USD`. Never put a credential in it.
3. List the accounts with an execute-capable operator consumer, then settle one by its `runId`:

   ```sh
   curl -s -H "$AUTH" "$PLOEG/unsettled-accounts" | jq '.accounts'

   curl -s -X POST -H "$AUTH" \
     -H "X-Ploeg-Actor: <your id>" -H 'Content-Type: application/json' \
     -d '{"commandId":"settle-run-1234","amountUsd":0.42,"evidence":"provider invoice 2026-10 line 14, project ploeg, 0.42 USD"}' \
     "$PLOEG/unsettled-accounts/1234/settle"
   ```

The answer is 201 with `settlement`: the amount charged (`settledUsd`, rounded to four decimals), the spend Ploeg had observed (`observedUsd`), the Hold it released (`releasedUsd`) and the state it replaced. Repeating the same request answers 200 with `replayed: true` and changes nothing, so a retry after a timeout is safe. Reusing the `commandId` for another Run, amount or evidence answers 409 `command_conflict`.

The command refuses, and changes nothing, when:

| Answer | Why |
| --- | --- |
| 409 `below_observation` | `amountUsd` is below the spend Ploeg observed while the Run ran. Ploeg never settles below its own observation; find the missing usage. |
| 409 `not_unsettled` | The account is not listed: it is `reserved`, its Run still runs, or it was blocked less than an hour ago. The sweeps still own it. |
| 409 `already_settled` | The account is `reconciled`. A correction after the window is not this command. |
| 404 `not_found` | No such Run with an account in the consumer's teams. |
| 403 `execution_forbidden` | The consumer lacks execute permission. |

The settlement is final at once: no correction sweep re-reads it. Step 2 then shows the account `reconciled`, `costKnown` true and evidence starting `operator:evidence consumer=… command=…`; step 3 shows `operatorSettlement`; step 4 gains one `llm.reconciled` event whose actor is `operator:<consumer>:<your id>`, with `operatorEvidence: true`. The Shift's `spentUsd` rises by the amount and its `heldUsd` falls by `releasedUsd`.

## Verify

- Every finished Run of the Shift shows `account.state` `reconciled`, or `account` null, and `heldUsd` 0.
- The Shift's `spentUsd` in step 1 equals the sum of `account.reconciledUsd` over its managed Runs, plus the `harnessReportedUsd` of any Run without an account.
- For a sample Run, the spend-log total from step 5 equals `account.reconciledUsd`, and the `entries` count matches `account.evidence`, or the latest `adjustments` evidence when the account was corrected.

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Pool looks exhausted, nothing runs, `heldUsd` is high | Finished Runs whose accounts are not yet `reconciled` still hold their authorization | Wait for the quiet period; otherwise work through step 6 |
| Shift parked at `needs_human` with `budget exhausted: …` | `budgetUsd − spentUsd − heldUsd` fell below the minimum for a Run | Check that the Holds are real. A Shift's pool is fixed when it opens: raise the team's budget in configuration for new Shifts, then re-assign the ticket |
| Shift parked at `needs_human` with `budget held by unsettled runs: …` | Finished Runs kept their Holds for 24 hours because their keys could not be blocked or settled, usually because the model gateway was down | Fix the gateway and check `ploegd` for `managed key block retry unresolved` or `managed settlement unresolved`. Once the Holds are released, re-assign the ticket |
| Account `blocked` for hours | Gateway spend logs or key list unavailable | Fix the gateway; the sweep retries |
| Account `unknown` for hours | Key already deleted, or the gateway cannot report it | Confirm the key is blocked or gone, then settle from evidence (step 7) |
| `spentUsd` lower than the gateway total | Entries arrived after settlement | Record the correction with evidence (no command yet) |
| `reconciledUsd` differs from `harnessReportedUsd` | The harness figure is self-reported; managed Runs settle from the gateway | Expected; trust the account |
| Account settled at zero with `ploegd:mint-never-began` | The Run ended before any mint began | Expected; step 4 has no `llm.minting` for it |
| `account` is null | Legacy compatibility mode, or a Run from before managed accounts | Its Hold is `authorizedUsd` while running; its charge is `harnessReportedUsd` |
| Run accounting answers 404 | The Run is outside the consumer's `teams`, or the Ploeg predates the route | Use a consumer that names the Team; upgrade Ploeg |

## Appendix: the same reads in the database

Use this only when the operator API is unavailable, for example while `ploegd` is down. Open a read-only session as [Before you start](index.md#before-you-start) describes; the queries select `left(run_token, 12)`, never the full run token, which is a credential.

```sql
SELECT id, provider, state, title FROM work_items WHERE external_id = '<ticket id>';
\set item 42
SELECT r.id AS run_id, r.shift_id, r.role, r.round, r.state AS run_state,
       r.outcome, r.failure_reason, r.started_at, r.finished_at
FROM agent_runs r WHERE r.work_item_id = :item ORDER BY r.id;
\set shift 7

SELECT s.id, s.budget, s.spent, COALESCE(sum(h.reserved), 0) AS reserved,
       s.budget - s.spent - COALESCE(sum(h.reserved), 0) AS remaining,
       s.closed_at, s.close_reason
FROM shifts s LEFT JOIN run_budget_holds h ON h.shift_id = s.id
WHERE s.id = :shift GROUP BY s.id;

SELECT r.id AS run_id, left(r.run_token, 12) AS alias_suffix, r.role, r.state AS run_state, r.authorized,
       r.usage ->> 'costUsd' AS harness_reported_usd,
       a.state AS account_state, a.observed_spend, a.settled_spend, a.reconciled_spend,
       a.corrections_until, a.cost_known, h.reserved AS hold,
       now() - a.updated_at AS unchanged_for, a.reconciliation_evidence
FROM agent_runs r
LEFT JOIN run_llm_accounts a USING (run_token)
LEFT JOIN run_budget_holds h USING (run_token)
WHERE r.shift_id = :shift ORDER BY r.id;

SELECT j.previous_spend, j.spend, j.evidence, j.recorded_at
FROM run_llm_adjustments j JOIN agent_runs r USING (run_token)
WHERE r.id = <run id> ORDER BY j.id;

SELECT at, actor, action, detail FROM audit_log
WHERE work_item_id = :item AND action LIKE 'llm.%' ORDER BY at;
```

Never write to these tables by hand: `shifts.spent` must stay equal to what the accounts settled ([accounting invariants](../reference/accounting-invariants.md)).
