---
status: proposed
date: 2026-10-25
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# An operator settles an account the gateway cannot answer for from evidence, and never below what Ploeg observed

## Context and Problem Statement

A managed Run's inference account settles only from the gateway: the block sweep revokes the key, and the settlement sweep sums LiteLLM's spend logs ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [accounting invariants](../reference/accounting-invariants.md)). When the gateway can no longer answer for a key (its spend logs are unreadable, the key was deleted outside Ploeg and `/key/info` errors instead of answering 404, or the gateway itself is gone), the account stays `minting`, `issued`, `unknown` or `blocked` and keeps its full Hold for ever. `GET /api/v1/operator/unsettled-accounts` and `PloegSettlementBacklog` make it visible, but until now the only remedy was "record it in the operational record", and the Hold kept starving the Shift ([ADR-0048](0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md)). Who may settle such an account, from what, and with which guard?

## Decision Drivers

* Ploeg errs high, never low: no path may settle below a spend Ploeg observed.
* A settlement is a money write; it must name the person, as every operator action does ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)).
* The sweeps stay the normal path. A manual settlement must not race them for an account they will still settle.
* No hand edit of `shifts.spent` or `run_llm_accounts`.
* Additive only ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)).

## Considered Options

* An operator API command that settles a listed account from evidence the operator supplies
* A `ploegd` subcommand run against the database
* Keep it manual: document SQL

## Decision Outcome

Chosen option: "An operator API command", because every other operator write already goes through the authenticated, team-scoped and attributed operator API, and a database subcommand would bypass that attribution.

`POST /api/v1/operator/unsettled-accounts/{runId}/settle` takes `{commandId, amountUsd, evidence}` from a consumer with execute permission and `X-Ploeg-Actor`. It settles only an account `unsettled-accounts` lists (the same SQL selection), in the consumer's teams. It refuses an amount below the account's `observed_spend` (409 `below_observation`). The settlement is final at once (no correction window, since the gateway cannot be re-read), records `cost_known` true, charges the Run's Shift, releases the Hold, and writes one `llm.reconciled` audit event under `operator:<consumer>:<person>` with `operatorEvidence: true`, the command id, the previous state and the attribution. The command and its evidence are kept in `run_llm_operator_settlements`, which the observer role cannot read. A repeated `commandId` with the same Run, amount, evidence and actor replays the first answer; anything else under that id is 409 `command_conflict`.

### Consequences

* Good, because a Hold the gateway can never release now has a recorded, attributed exit, and the Shift's pool returns.
* Good, because the observation floor and the list-only rule keep the sweeps authoritative wherever they can still answer.
* Bad, because settling an `issued` or `unknown` account stops the block sweep retrying its key. The operator must have confirmed the key is blocked or gone first; the how-to says so. Ploeg cannot check it, which is why the account is listed.
* Bad, because the amount is the operator's word, bounded below by Ploeg's own observation and recorded with its evidence, not verified against a provider.
* Neutral: a correction after a spend-log settlement's window closed is a different command and is not part of this decision.

### Confirmation

`go test ./pkg/store/ ./pkg/httpapi/` runs `TestOperatorSettlesAnUnsettledAccountFromEvidenceAndReleasesItsHold`, `TestOperatorSettlementNeverGoesBelowARecordedObservation`, `TestOperatorSettlementIsIdempotentPerCommand`, `TestOperatorSettlesOnlyAnAccountTheUnsettledListShows`, `TestOperatorSettlementRefusesAnAmountOrEvidenceItCannotRecord` and `TestOperatorSettleFromEvidenceEndpoint`, which validates each answer against `operator-api.v1.schema.json`. Each rule was mutation-tested on 2026-10-25: dropping the observation floor, the list-only rule, the already-settled check, the replay, the conflict check, the operator as audit actor, the Shift charge, the team scope, finality, `cost_known`, the evidence bound, the amount check or the execute permission fails at least one test.

## Pros and Cons of the Options

### A `ploegd` subcommand run against the database

* Good, because it works while `ploegd` is down.
* Bad, because it needs database credentials on an operator's machine and records no consumer or proven person.

### Keep it manual: document SQL

* Bad, because a hand edit can break the accounting invariants, and the audit log would not show who settled.

## Re-evaluation triggers

* The gateway gains a durable per-key spend export Ploeg can read after the key is gone.
* Ploeg imports provider invoices, so the amount can be checked rather than asserted.
* A correction-after-window command is designed (it may extend this endpoint).
* An operator settlement is later found to have undercharged a Run.

## More Information

[Investigate a Run's spend](../how-to/investigate-a-runs-spend.md), [Reconcile uncertainty](../ops/managed-workers.md#reconcile-uncertainty), [live gateway accounting, 2026-10-25](../research/2026-10-25-live-gateway-accounting.md).
