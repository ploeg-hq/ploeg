# Live gateway accounting: Ploeg's settlements against LiteLLM's spend logs, 2026-10-25

Status: research record, 2026-10-25. It answers [#20](https://github.com/ploeg-hq/ploeg/issues/20): the settlement path had only been qualified against a fake gateway ([accounting invariants](../reference/accounting-invariants.md#how-the-tests-check-them)), and no tolerance between Ploeg's figures and the gateway's was declared.

**Question.** On a live gateway, does what Ploeg settles for each Run match what the gateway recorded, and within what tolerance?

**Answer.** Yes. Of 200 settled accounts over four weeks, 199 match the gateway's spend logs to the last stored digit, and the one that does not is 0.000837 USD above its logs, settled at a spend Ploeg had itself observed. The totals differ by +0.000423 USD (+0.0010 %). Ploeg never settled below the gateway.

## Supported path

This is the only path this record qualifies:

- **Gateway:** LiteLLM, with spend logs enabled.
- **Keys:** one virtual key per Run, minted by `ploegd` with the Run's authorization as `max_budget` and the alias `ploeg-<12 hex>`; Ploeg records the key's hashed id (`gateway_key_id`, the SHA-256 LiteLLM stores as `api_key`).
- **Settlement:** after the Run finishes, the block sweep revokes the key; after 15 quiet minutes the settlement sweep sums `LiteLLM_SpendLogs.spend` for the key and settles at that total, rounded half-up to four decimals, and never below the highest spend Ploeg observed while the Run ran ([Reconcile uncertainty](../ops/managed-workers.md#reconcile-uncertainty)).
- **Correction window:** 24 hours (`PLOEG_LLM_CORRECTION_WINDOW`), during which late entries raise the settlement and are charged as adjustments.

## Declared tolerance

**Per account: Ploeg's settled amount is at least the spend-log total, and at most 0.0001 USD above it, unless Ploeg observed a higher spend while the Run ran, in which case it equals that observation.** Ploeg is never below the logs by more than the rounding of the last digit.

Why this tolerance:

- `run_llm_accounts` stores money as `numeric(12,4)`. A spend-log total such as 0.32296301 USD is stored as 0.3230, so 0.0001 USD is the finest difference Ploeg can represent. A tighter tolerance would require changing the column, and nothing billed is that precise.
- Ploeg errs high by design. LiteLLM keeps a running spend counter on each key that it writes asynchronously; Ploeg reads it while the Run is live as `observed_spend`. When that counter is ahead of the logs at settlement, Ploeg settles at the observation and records both figures in the evidence (`usd=…` and `settled-at-observation=…`). Undercharging a Run would let a Shift spend more than its pool; overcharging by a fraction of a cent keeps the pool conservative.

## Method

Read-only queries on 2026-10-25 around 14:30Z against the maintainer's homelab: `ploegd` 0.2.0-rc.68 to rc.71 (the accounts were settled by the releases deployed during the window) and its LiteLLM gateway, each on its own PostgreSQL database.

1. From Ploeg: every `run_llm_accounts` row in state `reconciled` with a non-empty `gateway_key_id`, with its `reconciled_spend` (the settled amount including any correction), `settled_at` and `reconciliation_evidence`. 200 accounts, settled 2026-09-27 to 2026-10-25.
2. From LiteLLM: `sum(spend)` and `count(*)` of `LiteLLM_SpendLogs` grouped by `api_key`.
3. Joined on `gateway_key_id = api_key`. Accounts whose `gateway_key_id` is empty (the mint never began) are excluded, and so are LiteLLM rows whose `api_key` is empty: joined on equality, the two would match each other and charge unrelated requests to those accounts. Neither side's empty value names a key.
4. Compared per account and in total.

No key value was read or copied: the comparison uses hashed key ids, and this record reports them only in aggregate. No credential, Work Item text or tracker identifier appears here.

## Results

| Measure | Value |
| --- | --- |
| Settled accounts compared | 200 |
| Accounts with spend-log entries | 152 (9,930 entries) |
| Accounts without entries | 48, each settled at 0 |
| Ploeg settled total | 43.2281 USD |
| Spend-log total | 43.227677 USD |
| Difference | +0.000423 USD (+0.0010 %) |
| Accounts within 0.0001 USD | 199 of 200 |
| Accounts below the logs by more than 0.0001 USD | 0 |

**The one exception.** An account settled on 2026-10-10 at 0.3238 USD, while its 149 spend-log entries sum to 0.322963 USD (+0.000837 USD). Its evidence reads `entries=149 usd=0.32296301700000013 … settled-at-observation=0.3238`: the key's running counter, observed while the Run was live, was ahead of its logs, and Ploeg settled at the observation as the rule above requires. This is LiteLLM's counter and logs disagreeing, not a Ploeg error, and it falls on the high side.

**Accounts without entries.** The 48 accounts with no spend-log entries settled at zero, which matches the logs. Where a key had been minted, the account carries `cost_known=false`, so the operator API reports the Run's `costStatus` as `unknown` and its `settledUsd` as null rather than a zero cost, until an entry confirms it.

**A fresh sample.** Runs 451 to 453 on 2026-10-25 (a builder at 0.0855 USD, a reviewer and a shadow reviewer, part of the F-04 requalification in [#282](https://github.com/ploeg-hq/ploeg/pull/282)) settled the same way, and the usage report Ploeg posted on the pull request showed them as settled and reconciled.

## Missing or delayed usage

Usage that is late or missing is visible, not silently zero:

- A late entry inside the 24-hour window raises the settlement; the difference is charged to the Shift and kept as a `run_llm_adjustments` row.
- A minted key with no entries settles with its cost reported as `unknown`.
- An account the gateway cannot answer for keeps its full Hold, appears in `GET /api/v1/operator/unsettled-accounts`, and raises `PloegSettlementBacklog` after an hour.
- The operator's action for such an account is `POST /api/v1/operator/unsettled-accounts/{runId}/settle`, which settles it from evidence they gathered, never below Ploeg's observation, and audits it under their name ([ADR-0154](../adrs/0154-an-operator-settles-an-account-the-gateway-cannot-answer-for-from-evidence-and-never-below-what-ploeg-observed.md), [how-to](../how-to/investigate-a-runs-spend.md#7-settle-an-account-from-evidence)).

## Test cost

No additional spend. The 200 accounts were the deployment's normal work in the window, and the comparison was two read-only queries.

## Evidence window

Settlements from 2026-09-27 to 2026-10-25; queries on 2026-10-25.

## Limitations

- **One gateway.** Only LiteLLM was compared. Another gateway would need its own qualification.
- **Gateway, not provider.** Provider invoices were not compared. The gateway's spend is LiteLLM's price calculation per request; whether it equals what the model providers bill is outside this record.
- **One deployment.** The maintainer's homelab, one tenant, about 200 Runs. No multi-tenant or high-volume deployment was measured.
- **The counter and the logs.** Why LiteLLM's key counter ran ahead of its logs for one key is internal to LiteLLM and was not investigated. Ploeg's rule makes the gap a slight overcharge, never an undercharge.
- **No missing-usage incident occurred in the window.** The operator settlement command was qualified by its tests, not by a live account the gateway could not answer for.
