# Accounting invariants

These are the rules Ploeg keeps for the money of managed inference: what a Shift may hold back, what a Run's gateway key may spend, and what each Shift is charged once the gateway has recorded that spend. Each rule names where the code enforces it and which test breaks when it stops holding. [Investigate a Run's spend](../how-to/investigate-a-runs-spend.md) shows how to check them on a live database.

## Terms

- **Authorization** is the most one Run may spend, stored in `agent_runs.authorized`.
- **Inference account** is a managed Run's row in `run_llm_accounts`. It carries the account's own `authorized`, the highest `observed_spend` Ploeg read while the Run was live, and `reconciled_spend` once it settles.
- **Hold** is budget kept back for a Run that has not settled. It is computed by the `run_budget_holds` view and never stored ([ADR-0093](../adrs/0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md)).
- **Gateway truth** is the total of the LiteLLM spend-log entries of a Run's own keys.

## Invariants

1. **Admission never overcommits a Shift.** A Run's authorization is `min(cap, budget − spent − held)`, computed while the Shift row is locked `FOR UPDATE`, so two concurrent claims cannot both take the same remainder. A Shift with less than the minimum a Run needs refuses the claim.
   Enforced in `startPendingRun` ([shift.go](../../pkg/store/shift.go)). Tested by `TestAuthorizeIsAtomicUnderConcurrency` and `TestAControlledLedgerSettlesEveryRunToItsOwnAccountExactlyOnce`, which checks `spent + held ≤ budget` with eight Runs live at once.

2. **A key never carries more than its Run may spend.** An account's authorization is the smaller of the Run's authorization and the role's policy budget. The gateway key is minted with exactly that `max_budget`, and a mint without a positive cap is refused, never sent.
   Enforced in `ReserveLLMAccount` ([llm_accounts.go](../../pkg/store/llm_accounts.go)) and `LiteLLM.Mint` ([litellm.go](../../pkg/llmbroker/litellm.go)). Tested by `TestManagedReservationIsIdempotentAndBoundedByRunAuthority` and the controlled-ledger test, which compares every minted `max_budget` with its account.

3. **A Run is charged only for its own keys.** Settlement reads the spend logs of the key id Ploeg recorded and of keys whose alias is exactly the Run's `ploeg-<12 hex>`. A spend-log answer that carries an entry of another key fails the read, so the account stays unsettled rather than charged for it. The charge goes to the Run's own Shift only. Aliases are the first 12 hex characters of the run token, a 48-bit space, so isolation also rests on those prefixes not colliding.
   Enforced in `SettledSpendForRun` ([litellm.go](../../pkg/llmbroker/litellm.go)), `SpendLogs` ([client.go](../../pkg/litellm/client.go)) and `SettleLLMAccount`. Tested by `TestAControlledLedgerSettlesEveryRunToItsOwnAccountExactlyOnce`, which settles eight Runs of two teams against one gateway and checks every account and every Shift against gateway truth.

4. **A Shift's `spent` is the sum of what its Runs settled.** For a managed Run that is `reconciled_spend`. Only a Run without an inference account adds the `costUsd` it reported itself; a managed Run's self-reported cost is never charged.
   Enforced in `SettleLLMAccount` and `ReportOutcome`. Checked after every step by `assertAccountingInvariants` ([accounting_gateway_test.go](../../pkg/httpapi/accounting_gateway_test.go)).

5. **Settlement happens once and only goes up.** A settlement that reads the same total writes nothing. A lower total is refused, and no settlement goes below `observed_spend`. The first settlement stays in `settled_spend` and `settled_at`. Each later raise inside the correction window charges only the difference and adds a `run_llm_adjustments` row that chains from the previous total. Every charge has one `llm.reconciled` audit event carrying its `delta`.
   Enforced in `SettleLLMAccount`, `SettleLLMAccountFromEvidence` ([llm_operator_settlement.go](../../pkg/store/llm_operator_settlement.go)) and the `run_llm_adjustments` check constraint. Tested by `TestCorrectionNeverLowersASettlement`, `TestControllerSettlesBlockedAccountFromSpendLogsOnce`, `TestOperatorSettlementNeverGoesBelowARecordedObservation`, `TestOperatorSettlementIsIdempotentPerCommand` and the replay test below.

6. **Only a finished Run with a blocked key settles, unless an operator settles it from evidence.** The sweeps never settle an account in `minting`, `issued` or `unknown`. It reaches `blocked` first, either through the block sweep, which revokes the key and reads its spend, or through a confirmed report that the gateway has no key for it. A minted key with no spend-log entries settles at zero with `cost_known` false. An account whose mint never began settles at zero on that evidence.
   The one exception is an account the gateway can no longer answer for: once `GET /api/v1/operator/unsettled-accounts` lists it, an execute-capable operator may settle it from evidence, at no less than its observation, final at once and audited under their name ([ADR-0155](../adrs/0155-an-operator-settles-an-account-the-gateway-cannot-answer-for-from-evidence-and-never-below-what-ploeg-observed.md)). An account the list does not show is refused, so the command never races a sweep.
   Enforced in `LLMControl.Block` and `LLMControl.Settle` ([llm_control.go](../../pkg/httpapi/llm_control.go)) and `SettleLLMAccountFromEvidence`. Tested by `TestACrashAtEveryMintBoundarySettlesToGatewayTruth` and `TestOperatorSettlesOnlyAnAccountTheUnsettledListShows`.

7. **Every hold is released by settlement, and only by settlement.** An unsettled account holds the larger of its authorization and `observed_spend`. A settled account holds `observed_spend − reconciled_spend`, at least zero, which is non-zero only while a correction is due. A reading after the correction window has closed is not recorded, because the cost is final and no sweep would release what it held.
   Enforced in the `run_budget_holds` view ([0012_run_llm_accounts.sql](../../pkg/store/migrations/0012_run_llm_accounts.sql)) and `RecordLLMObserved`. Tested by `TestAReadingAfterTheCorrectionWindowNeverHoldsBudget` and `TestAReadingInsideTheCorrectionWindowHoldsUntilCorrected`.

## What Ploeg does not guarantee

**Settled spend can exceed authorization.** The gateway checks a key's `max_budget` before a request, not after, so requests already in flight when the key reaches its cap or is blocked still land. Ploeg settles the true total and charges it rather than clipping it to the authorization; see *Operating limits* in [managed workers](../ops/managed-workers.md). The tests let in-flight entries land after a key is blocked and expect them charged.

**An operator settlement is the operator's figure.** Ploeg bounds it below by its own observation and keeps the evidence and the person with it, but does not check it against a provider invoice.

**A late entry after the correction window is not charged.** Once `corrections_until` has passed, the cost is final. An entry that the gateway records later is not read again; [investigate a Run's spend](../how-to/investigate-a-runs-spend.md) says how to record such a difference.

## How the tests check them

The accounting tests in [pkg/httpapi](../../pkg/httpapi/) run the real controller and store against PostgreSQL and a fake LiteLLM gateway. The fake mints capped keys, refuses requests past a key's cap or after it is blocked, keeps per-key spend logs, filters keys by alias the way LiteLLM 1.103 does, and can lose a mint response.

- `TestAControlledLedgerSettlesEveryRunToItsOwnAccountExactlyOnce` runs eight Runs of two teams over four Shifts at once, lets entries land after their keys are blocked, and runs settlement and correction sweeps three at a time. Every account must settle to its own gateway truth, every Shift to the sum of its Runs, with one first settlement per Run.
- `TestReplayedOutOfOrderEventsSettleEveryRunToGatewayTruth` builds each Run's events (charges, observations, block, finish, in-flight entries, a stale earlier reading) and sweeps that act on a listing taken earlier. It shuffles them, replays some twice, and applies them one at a time with fixed seeds 19, 20 and 21. After every event it checks the invariants and that no observed, settled or spent value went down. After a final drain, every account must equal gateway truth.
- `TestACrashAtEveryMintBoundarySettlesToGatewayTruth` stops issuance before the gateway is called, after the gateway minted but before Ploeg recorded the key, and when the mint response is lost. Each Run must still be blocked and settled to what its key spent.

`assertAccountingInvariants` expresses invariants 2 to 7 as SQL queries that list the rows breaking them, so a failure names the account or Shift. Each test was checked by breaking the code it guards: charging another Shift, matching keys by alias prefix, charging the whole total on a correction instead of the difference, and recording readings after the window. Each break fails at least one of these tests.

The fake is not the only check. On 2026-10-25 every reconciled account of a live LiteLLM gateway over four weeks (200 accounts) was compared with the sum of its spend logs: 199 matched within 0.0001 USD, the storage precision, and one settled 0.000837 USD above its logs at its higher observation, as invariant 5 requires ([live gateway accounting](../research/2026-10-25-live-gateway-accounting.md)).
