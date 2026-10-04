---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-09-28
verified_by: "Read migrations 0008 and 0012 (shifts, agent_runs.authorized, run_llm_accounts, run_budget_holds), pkg/store/{llm_accounts,llm_settlement,llm_block_queue,store,shift,operator}.go, pkg/httpapi/llm_control.go (Block, Settle), pkg/llmbroker/litellm.go, pkg/litellm/client.go and cmd/ploegd/sweep.go"
---

# Investigate a Run's spend

**Symptom:** a Shift reports more or less spend than you expect, its budget looks used up while nothing runs, or you need to show what one Run cost.

**Goal:** separate what Ploeg has **reserved** (held against the budget) from what it has **settled** (charged), trace a Run's inference account through its states, compare it with the gateway's spend logs, and tell whether the settlement sweep will finish the job or you must act.

Read [Before you start](index.md#before-you-start) for names and the database session. The rules for uncertain cost are in [Reconcile uncertainty](../ops/managed-workers.md#reconcile-uncertainty).

## How Ploeg counts money

Budgets have two levels ([ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md)):

- The **Shift** holds the pool: `shifts.budget`, and `shifts.spent`, the settled total.
- Each **Run** has an authorization, `agent_runs.authorized`, fixed when it is claimed: the smaller of the Role's cap and what the pool has left.
- A managed Run also has an **inference account**, one row in `run_llm_accounts`. It records the gateway key's alias (`ploeg-` plus the first 12 hex characters of the run token), the hashed key id (`gateway_key_id`), the last spend seen (`observed_spend`) and the settled amount (`reconciled_spend`).
- **Reserved** is never stored. The view `run_budget_holds` derives each Run's hold, and a Shift's reserved figure is their sum. The rule, from migration 0012:

  | Run | Hold |
  | --- | --- |
  | No account, `running` | `authorized` |
  | No account, not running | 0 |
  | Account not yet `reconciled` | the larger of `authorized` and `observed_spend` |
  | Account `reconciled` | `observed_spend − reconciled_spend`, at least 0 |

- **Settled** grows in two ways. A managed Run adds its reconciled amount when the sweeper settles its account. A Run without an account adds the `costUsd` its harness reported, which is the agent's own claim, when it reports its outcome.

The pool is empty when `budget − spent − reserved` is below the minimum a Run needs. Claims are then refused. When settling the holds of finished Runs could not refill it, the engine parks the Shift at `needs_human` with a reason such as `budget exhausted: pool 6.00, spent 2.10, reserved 3.95`. When it could, the Shift waits with its Run pending until settlement releases the holds. If they are still unsettled 24 hours after their Runs finished, the Shift parks with `budget held by unsettled runs: pool 8.00, spent 0.00, held 8.00` ([ADR-0048](../adrs/0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md)).

## Inference account states

| State | Meaning | What moves it on |
| --- | --- | --- |
| `reserved` | Budget held, no key requested | The mint, or settlement at zero once the Run finishes (`ploegd:mint-never-began`) |
| `minting` | Key requested; the gateway's answer is not yet recorded | The block sweep, once the Run finishes |
| `issued` | Key exists; `gateway_key_id` recorded | The block sweep, once the Run finishes |
| `unknown` | Issue or spend read failed, so the key's fate is uncertain | The block sweep, while the gateway can report the key |
| `blocked` | Key revoked; `observed_spend` recorded | The settlement sweep, after `PLOEG_LLM_SETTLE_AFTER` (15 minutes) with no change |
| `reconciled` | Settled from the gateway's spend logs; evidence stored | Nothing. Until `corrections_until`, the correction sweep reads the spend logs again and charges any late entry as an adjustment |

Every sweep interval, the controller tries to block every finished Run's account that is still `minting`, `issued` or `unknown`. It then settles every finished Run's account that is `reserved`, or `blocked` and quiet ([sweep.go](../../cmd/ploegd/sweep.go), [llm_control.go](../../pkg/httpapi/llm_control.go)). Settlement reads LiteLLM's spend logs for the recorded hashed key and any key that still carries the alias. It never settles below `observed_spend` and never treats a missing key as zero spend. A blocked account settles at `run_llm_accounts`' own 4-decimal precision: the spend-log total is rounded half-up to match `observed_spend` and `reconciled_spend` before either is compared or stored.

## 1. Find the Run and its Shift

Look the Work Item up by its tracker ticket id, then set the psql variables the later queries use:

```sql
SELECT id, provider, state, title FROM work_items WHERE external_id = '<ticket id>';
\set item 42
SELECT r.id AS run_id, r.shift_id, r.role, r.round, r.state AS run_state,
       r.outcome, r.failure_reason, r.started_at, r.finished_at
FROM agent_runs r WHERE r.work_item_id = :item
ORDER BY r.id;
\set shift 7
```

## 2. Read the Shift's ledger

```sql
SELECT s.id, s.budget, s.spent,
       COALESCE(sum(h.reserved), 0) AS reserved,
       s.budget - s.spent - COALESCE(sum(h.reserved), 0) AS remaining,
       s.closed_at, s.close_reason
FROM shifts s LEFT JOIN run_budget_holds h ON h.shift_id = s.id
WHERE s.id = :shift
GROUP BY s.id;
```

Ploeg's operator API returns the same three figures as `budgetUsd`, `spentUsd` and `reservedUsd` ([operator.go](../../pkg/store/operator.go)).

## 3. Read each Run's account and hold

```sql
SELECT r.id AS run_id, r.role, r.state AS run_state, r.authorized,
       r.usage ->> 'costUsd' AS harness_reported_usd,
       a.alias, a.state AS account_state, a.authorized AS account_authorized,
       a.observed_spend, a.reconciled_spend, h.reserved AS hold,
       now() - a.updated_at AS unchanged_for, a.reconciliation_evidence
FROM agent_runs r
LEFT JOIN run_llm_accounts a USING (run_token)
LEFT JOIN run_budget_holds h USING (run_token)
WHERE r.shift_id = :shift
ORDER BY r.id;
```

- `harness_reported_usd` is what the agent said it spent. For a managed Run it is not the charge; the account is.
- `reconciliation_evidence` tells you how the account settled: `ploegd:mint-never-began …` for zero, or `litellm:spend-logs alias=… keys=… entries=… usd=… unchanged-since=… read-at=…`.

## 4. Read the audit trail

```sql
SELECT at, actor, action, detail
FROM audit_log
WHERE work_item_id = :item AND action LIKE 'llm.%'
ORDER BY at;
```

A normal managed Run shows `llm.reserved`, `llm.minting`, `llm.issued`, one or more `llm.observed`, `llm.blocked`, then `llm.reconciled` with `spend` and `delta`. `llm.unissued_blocked` means the Run ended before a key was minted. `llm.unknown` marks the point where the controller lost certainty.

## 5. Compare with the gateway's spend logs

The settlement source is LiteLLM's per-request spend log, not the key's running total: LiteLLM writes that total asynchronously and loses it when the key is deleted. To check a settled amount yourself, sum the log entries for the account's hashed key (`gateway_key_id`).

Use a LiteLLM admin credential that you are authorized to hold. Do not copy `ploegd`'s master key onto a workstation.

```sh
kubectl -n ai port-forward svc/litellm 4000:4000
# in a second shell
curl -s -H "Authorization: Bearer $LITELLM_ADMIN_KEY" \
  "http://localhost:4000/spend/logs?api_key=<gateway_key_id>" \
  | jq '{entries: length, usd: (map(.spend) | add)}'
```

In the LiteLLM admin UI, filter the logs by the key alias `ploeg-<12 hex>` for the same answer. Spend logs must stay enabled on the gateway. With logging off, a minted key would show no entries, and only a recorded `observed_spend` stops it settling at zero.

Late entries can appear after settlement, when a request was already in flight as the key was blocked. A settlement read from the spend logs stays provisional for `PLOEG_LLM_CORRECTION_WINDOW` (default `24h`), until `run_llm_accounts.corrections_until`. Every 15 minutes in that window the correction sweep reads the spend logs again. A higher total charges only the difference to the Shift, raises `reconciled_spend`, adds a `run_llm_adjustments` row with the previous and new totals and the new evidence, and writes an `llm.reconciled` audit event with `"adjustment": true`. The first settlement stays in `settled_at`, `settled_spend` and `reconciliation_evidence`. A lower total is refused and logged as `managed settlement correction unresolved`. After the window the cost is final and nothing re-reads it. **Not implemented yet:** an operator command that records a correction after the window. Record the difference and its evidence in the operational record; do not edit `shifts.spent` or `run_llm_accounts` by hand.

A minted key whose spend logs have no entries settles at zero so its hold is released, but `cost_known` is false: the Run's cost is reported as unknown, not as zero, until a spend-log entry confirms it. An entry that costs zero does confirm zero.

```sql
SELECT a.settled_at, a.settled_spend, a.reconciled_spend, a.corrections_until, a.cost_known,
       j.previous_spend, j.spend, j.evidence, j.recorded_at
FROM run_llm_accounts a LEFT JOIN run_llm_adjustments j USING (run_token)
WHERE a.run_token = '<run_token>' ORDER BY j.id;
```

## 6. Decide whether to wait or act

1. **The account is `blocked` and `unchanged_for` is under 15 minutes.** Wait. The next sweep after the quiet period settles it.
2. **The account is `blocked` and older, but still not settled.** Look for the reason in the logs:

   ```sh
   kubectl -n ploeg logs deployment/ploeg --since=1h | grep -E 'managed (settlement|account)'
   ```

   `managed settlement unresolved … gateway spend logs unavailable` means the gateway's `/spend/logs` or `/key/list` fails. Fix the gateway; the sweep retries every interval.
3. **The account is `minting`, `issued` or `unknown` on a finished Run.** The block sweep retries it. `managed key block retry unresolved` repeating means the gateway cannot revoke the key or report its spend, often because the key is already gone. The account keeps its full hold. `GET /api/v1/operator/unsettled-accounts` lists every such Run in the consumer's Teams, oldest first, with its Work Item, account state, held amount and since when, plus the count and held total. **Not implemented yet:** a command that settles such an account from evidence you have gathered. Follow [Reconcile uncertainty](../ops/managed-workers.md#reconcile-uncertainty): keep the hold, collect the gateway and provider evidence, and record it.
4. **The Run is still `running`.** Its hold is its authorization. See [Recover a stuck Lease or Run](recover-a-stuck-lease-or-run.md) if it should have ended.

## Verify

- Every finished Run of the Shift shows `account_state = 'reconciled'` or has no account, and `hold` is 0 for each.
- `spent` in step 2 equals the sum of `reconciled_spend` over the Shift's managed Runs, plus the harness-reported `costUsd` of any Run without an account.
- For a sample Run, the spend-log total from step 5 equals `reconciled_spend`, and the `entries` count matches the evidence string, or the latest `run_llm_adjustments` evidence when the account was corrected.

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Pool looks exhausted, nothing runs, `reserved` is high | Finished Runs whose accounts are not yet `reconciled` still hold their authorization | Wait for the quiet period; otherwise work through step 6 |
| Shift parked at `needs_human` with `budget exhausted: …` | `budget − spent − reserved` fell below the minimum for a Run | Check that the holds are real. A Shift's pool is fixed when it opens: raise the team's budget in configuration for new Shifts, then re-assign the ticket |
| Shift parked at `needs_human` with `budget held by unsettled runs: …` | Finished Runs kept their holds for 24 hours because their keys could not be blocked or settled, usually because the model gateway was down | Fix the gateway and check `ploegd` for `managed key block retry unresolved` or `managed settlement unresolved`. Once the holds are released, re-assign the ticket |
| Account `blocked` for hours | Gateway spend logs or key list unavailable | Fix the gateway; the sweep retries |
| Account `unknown` for hours | Key already deleted, or the gateway cannot report it | Keep the hold; reconcile from evidence (no command yet) |
| `spent` lower than the gateway total | Entries arrived after settlement | Record the correction with evidence (no command yet) |
| `spent` differs from `harness_reported_usd` | The harness figure is self-reported; managed Runs settle from the gateway | Expected; trust the account |
| Account settled at zero with `ploegd:mint-never-began` | The Run ended before any mint began | Expected; the audit trail has no `llm.minting` for it |
| A Run has no account row | Legacy compatibility mode, or a Run from before managed accounts | Its hold is `authorized` while running; its charge is the harness-reported `costUsd` |
