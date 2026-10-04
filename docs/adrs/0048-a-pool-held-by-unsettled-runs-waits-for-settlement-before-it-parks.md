---
status: accepted
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A pool held by unsettled Runs waits for settlement before it parks

A person must review this record before it is accepted. It changes when Ploeg tells the owner that a Shift ran out of money.

## Context and Problem Statement

A Shift's pool is `budget − spent − reserved`. `reserved` is the sum of `run_budget_holds` ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)). A finished Run keeps its full hold until its model-key account reaches `reconciled`. Unresolved accounting keeps its authorization until then ([ADR-0025](0025-management-authority-stays-in-the-control-plane.md)). The settlement sweep in `ploegd` moves such an account forward. `PendingLLMBlocks` blocks keys still `minting`, `issued` or `unknown`. `UnsettledLLMAccounts` reconciles accounts that are `reserved`, or that have been `blocked` and unchanged for `PLOEG_LLM_SETTLE_AFTER`. Both steps need the model gateway.

The floor sweep (`ShiftsBelowFloor`, called from `EvaluateAll`) closed every Shift whose pool fell below the 0.05 floor while a Run was pending. The reason was `budget exhausted: pool …, spent …, reserved …`, and the Work Item went to `needs_human`. It counted a hold awaiting settlement the same as money spent.

On 2026-10-01, VIK-1279 (team bronze, pool 8.00) hit this. The model gateway failed four writer Runs in a row (`infra_llm`). Each held 2.00 because its key could not be blocked or settled. The Shift parked with `budget exhausted: pool 8.00, spent 0.00, reserved 8.00`, and the fifth attempt was cancelled before it started. Later the holds settled to zero, and the operator API showed `spentUsd 0` and `reservedUsd 0`. The tracker said the budget ran out, but nothing was spent and the money came back. A short gateway outage had become a permanent stop.

## Decision Drivers

* Never authorize a Run against money that is still held. A key that may still spend must keep its hold.
* `budget exhausted` means spent, so that a person who reads it raises the budget, not fixes the gateway.
* A Shift whose holds never settle must not wait forever.
* No new configuration.
* The smallest change to the store and the engine.

## Considered Options

* **The floor sweep waits while unsettled holds could refill the pool, and parks with a separate reason after a patience**
* Release the hold of an `infra_llm` Run at once
* Keep parking, but retry parked Shifts after settlement
* Leave it as it is

## Decision Outcome

Chosen option: "**the floor sweep waits while unsettled holds could refill the pool, and parks with a separate reason after a patience**". It is the only option that keeps every hold intact and still says what happened.

1. **Unsettled hold.** An unsettled hold is the `run_budget_holds.reserved` of a Run that is `finished` and whose `run_llm_accounts.state` is not `reconciled`. That covers `reserved`, `minting`, `issued`, `unknown` and `blocked`. A running Run's hold, a reconciled account's late-observed remainder, and spend are committed money. `ShiftLedgerEntry.Unsettled` reports unsettled holds.
2. **Wait.** `ShiftsBelowFloor(ctx, patience)` leaves out a Shift whose pool is below the floor only while the unsettled holds of Runs that finished less than `patience` ago would lift it back to the floor. The Shift stays open with its pending Run. `ClaimRoleWithin` still refuses with `ErrBudgetExhausted`, so nothing is authorized against held money. Once settlement releases a hold, the next claim succeeds.
3. **Real exhaustion parks at once.** When releasing every unsettled hold would still leave less than the floor, the Shift parks as before: `budget exhausted: pool P, spent S, reserved R`, `needs_human`, with the budget notice on the tracker and the pull request. `budgetExhausted` in `publish.go` and the review loop keep matching that prefix.
4. **Patience, then a separate reason.** The engine passes `unsettledHoldPatience`, 24 hours, counted from each Run's `finished_at`. When the floor is short only because of holds older than that, the Shift parks with `budget held by unsettled runs: pool P, spent S, held H`. H is all unsettled holds. The item goes to `needs_human` with the message "the budget is still held by finished runs whose model spend was never settled; a person is asked to check the model gateway and take over". This reason does not start with `budget exhausted`, so no budget-exhausted notice is posted. 24 hours is far beyond `PLOEG_LLM_SETTLE_AFTER` (15 minutes by default) plus a sweep. Settlement that has not finished by then needs a person.
5. **Out of scope.** The fix-round budget check in `nextFixRound` (`budget_exhausted_before_fix_round`) still reads `Ledger.Remaining()`. It can close a Shift on unsettled holds in the same way. It is listed as a trigger below rather than changed here.

### Consequences

* Good, because a model gateway outage no longer turns into a "budget ran out" stop. The Shift resumes by itself once settlement releases the holds.
* Good, because no hold is released early and no claim rule changes, so the never-overspend guarantee is untouched.
* Good, because `budget exhausted` now always means the money is spent. A pool that is only held says `held`.
* Bad, because a Work Item can sit with a pending Run for up to 24 hours before a person hears about it. In Vloer it shows a pending Run and a full `reservedUsd`.
* Bad, because a Shift that waits and then settles to real spend parks one sweep later than it would have before.
* Neutral: the patience is a constant, not configuration. A deployment that needs a different value needs a code change.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store` (`TestShiftsBelowFloorWaitsForHoldsAwaitingSettlement`): a Shift with pool 8 and four finished `infra_llm` writer Runs, each holding 2 with an issued account, refuses the claim and is not returned for parking. After the accounts settle to zero, a claim is authorized 2.
* `pkg/store` (`TestShiftsBelowFloorParksHoldsUnsettledPastPatience`): the same Shift with Runs finished before the patience is returned, with `Unsettled` 8 and `SettlementCouldFund` true.
* `pkg/store` (`TestShiftsBelowFloorParksRealSpendAtOnce`): the same Shift with the four accounts settled at 2 each is returned with spend 8 and `SettlementCouldFund` false.
* `pkg/shiftengine` (`TestFloorSweepWaitsWhileHoldsAwaitSettlement`): with the engine retrying the gateway failures, `EvaluateAll` leaves the Shift open, and after settlement the fifth attempt is claimed. Against the earlier code the fifth attempt is cancelled and the Shift closes `budget exhausted`.
* `pkg/shiftengine` (`TestFloorSweepParksHoldsUnsettledPastPatienceAsHeld`, `TestFloorSweepParksRealSpendAsExhausted`): the two close reasons, word for word, and `needs_human` for both.
* `pkg/shiftengine` (`TestBelowFloorPoolParksTheItem`, `TestBudgetExhaustedRecognisesBothCloseReasons`): real exhaustion parks as before.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Release the hold of an `infra_llm` Run at once

* Good, because the pool is free again at once.
* Bad, because a Run that failed on the gateway may still have spent through a key that was never blocked. Releasing the hold could authorize money that is already gone, which breaks [ADR-0025](0025-management-authority-stays-in-the-control-plane.md).

### Keep parking, but retry parked Shifts after settlement

* Good, because the floor sweep stays as it is.
* Bad, because the tracker and Vloer would already have told a person that the budget ran out, and the retry would need a new path out of `needs_human`.

### Leave it as it is

* Good, because nothing changes.
* Bad, because every gateway outage that outlasts a Run's retries stops its Work Items with a false reason.

## Re-evaluation triggers

* A Shift closes `budget_exhausted_before_fix_round` while its holds are unsettled. Apply the same rule to `nextFixRound`.
* A Shift parks `budget held by unsettled runs` more than once a month. Look at why settlement stalls (VIK-1634) before you change the patience.
* An owner wants the patience configurable, or a deployment sets `PLOEG_LLM_SETTLE_AFTER` near 24 hours.
* Vloer needs to show that a Shift is waiting for settlement rather than pending.

## More Information

* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md): the two-level budget and the floor.
* [ADR-0025](0025-management-authority-stays-in-the-control-plane.md): unresolved accounts keep their authorization.
* [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md): why four gateway failures still leave attempts.
* [Investigate a Run's spend](../how-to/investigate-a-runs-spend.md): how to read holds and settlement.
