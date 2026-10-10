---
status: proposed
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An Ask is a read-only Run outside the Shift, paid from a periodic allowance

## Context and Problem Statement

An operator consumer wants people to ask a question about an existing Work Item and get an answer in words, without the question reaching the agent doing the work and without paying for it from that Work Item's Shift Budget. Answering needs one model call, and every model call a deployment makes for its work is authorized, capped and metered by Ploeg.

Ploeg can only authorize spend inside a Shift today: [ADR-0012](0012-two-level-budgets-authorized-and-settled.md) holds a Run's authorization against its Shift's pool, and the operator API admits only a writing `operator` Run that opens a Work Item, a Shift and a Lease ([ADR-0024](0024-operator-work-uses-one-execution-authority.md)). It has no budget that spans Work Items or resets with time.

How does Ploeg authorize, cap and settle a model call about a Work Item that is not part of its delivery, and who pays for it?

## Decision Drivers

* Ploeg never runs a model or holds a prompt; it authorizes, mints a capped credential and settles ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)).
* Asking about work must not spend the work's own Shift Budget, take its Lease or start a Round.
* Every hold is released by state, not by a caller remembering to release it, and concurrent admissions cannot overspend (ADR-0012).
* The scope that pays changes: a Team now, a Client or Tenant later. The ledger must take a new scope without a migration.
* Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* An Ask Run with no Shift, authorized against a periodic allowance ledger, with the capped key returned to the consumer
* Ploeg proxies the Ask: it takes the question and calls the model itself
* Charge the Ask to the Work Item's Shift pool
* A LiteLLM team budget with a monthly reset, and no ledger in Ploeg

## Decision Outcome

Chosen option: "An Ask Run with no Shift, authorized against a periodic allowance ledger, with the capped key returned to the consumer", because it keeps every model call under Ploeg's authorization and settlement without Ploeg ever calling a model, and leaves the Work Item's delivery untouched.

**An Ask is a Run.** It is an `agent_runs` row with the Role `ask`, `writes` false, the Work Item it asks about, and no Shift, no Round and no Lease. No worker claims it and no workspace exists for it. Its run token never leaves Ploeg. It runs from admission until the consumer finishes it or its deadline passes, and the deadline is the key's TTL. A crew Role cannot be named `ask`.

**The allowance is a ledger row per scope and period.** `inference_allowances` holds one row per purpose, scope kind, scope id and period. Phase 1 has the purpose `ask`, the scope kind `team` and the period of one UTC calendar month. A period's row is created by its first Ask with the configured limit (`PLOEG_ASK_ALLOWANCE_USD`, default 2.00 USD), and its limit stays fixed for that period. Each Ask records the allowance row it was admitted against, so spend settled after the month ends still counts against the month it was asked in.

**Admission mirrors the Shift pool.** Inside one transaction Ploeg locks the period's row with `SELECT … FOR UPDATE`, sums `settled` (the reconciled spend of the period's Asks) and `held` (their `run_budget_holds`), and admits only when `limit − settled − held` covers the per-Ask Budget. A refused Ask answers 402 with the time the period resets. The per-Ask Budget is `PLOEG_ASK_BUDGET_USD` (default 0.02 USD), lowered by the `(team, ask)` inference policy's `budgetUsd` when that is smaller; the caller does not choose it. The hold is the Ask's authorization until its account settles, the same view a Shift's `reserved` reads, so a finished Ask still holds its money until the gateway's spend is read.

**Ploeg returns a capped key, not an answer.** Admission reserves the Run's inference account from the `(team, ask)` policy and mints a LiteLLM key capped at the per-Ask Budget, scoped to that policy's models and TTL. Ploeg returns the key to the operator consumer, which makes the call and then tells Ploeg the Ask is finished. Ploeg blocks the key and the managed settlement sweep settles it like any Run. A Team with no `(team, ask)` policy has no Asks: admission refuses before recording anything rather than guess a model.

**Lost responses follow the execution credential.** The consumer names each Ask with its own `askId`. Admitting the same `askId` again returns the same Ask. It carries a key only if no mint had begun; once one may have, the replay carries no key, and the consumer finishes that Ask and asks again under a new `askId`. Ploeg never stores a key and never mints a second one for an Ask.

**Ploeg keeps no question.** The Ask records the SHA-256 of the question, its asker and its consumer, never the text. The consumer stores the question and the answer.

**Ask spend is not delivery spend.** Facts and usage that report what building a Work Item cost leave Ask Runs out, and report Ask spend and Ask count beside it.

### Consequences

* Good, because a question about work costs a known, capped amount that Ploeg authorized, without touching the work's Shift, Lease or Budget.
* Good, because the allowance's hold is derived from the Asks themselves, so a crashed consumer cannot strand money: the deadline sweep blocks the key and settlement releases the hold.
* Good, because a Client or Tenant scope is a new `scope_kind` value, not a migration.
* Bad, because a Run without a Shift is a new shape every query over `agent_runs` must handle; queries that mean "delivery Runs" now leave the `ask` Role out.
* Bad, because the consumer holds a live key for up to its TTL and could make more than one call with it; the per-Ask Budget caps what that can cost.
* Bad, because a period's limit cannot yet be raised through the API; until it can, a person changes the period's row.

### Confirmation

* `go test ./pkg/store/` proves an admitted Ask creates no Shift and no Lease, that two concurrent admissions that together exceed the allowance admit exactly one, that an Ask admitted in one month does not count against the next, that a replayed `askId` returns the same Ask, and that team capacity, settling a closed tracker task, a Work Item's started flag and the context phase ignore Ask Runs.
* `go test ./pkg/httpapi/` validates every Ask and allowance response against `operator-api.v1.schema.json`, checks execute permission, the actor, team scope (404 outside it), 402 on an exhausted allowance, the refusal without a `(team, ask)` policy, that a replay mints no second key, and that finishing blocks the key, against a LiteLLM fake built on `net/http/httptest`.
* CI runs both with `mise exec -- go test ./...`.

## Pros and Cons of the Options

### Ploeg proxies the Ask

* Good, because the key never leaves Ploeg.
* Bad, because Ploeg would hold prompts and answers and call models itself, which no other path does; the execution credential already hands a capped key to the consumer.

### Charge the Ask to the Work Item's Shift pool

* Good, because it needs no new ledger.
* Bad, because asking would spend the work's own Budget, and a Work Item with no live Shift could not be asked about.

### A LiteLLM team budget with a monthly reset

* Good, because the gateway already resets budgets by period.
* Bad, because the gateway refuses spend after the fact, while Ploeg authorizes before it; Ploeg could not lock against it, and its settlement and holds would not see it.

## Re-evaluation triggers

* A consumer needs a Client or Tenant scope, or a period other than a calendar month.
* A consumer needs more than one model call or a tool call per Ask.
* An agency needs to raise a limit inside a period through the API.
* Ask settlement lags so far behind that held Asks refuse admissions an allowance could afford.

## More Information

* 2026-10-10 — The owner decided that people ask about a Work Item through a metered, read-only Ask paid from a monthly allowance, in the operator consumer's own decision record; this record is Ploeg's half.
* Contract: `docs/contracts/operator-api.v1.schema.json`, definitions `askAdmitRequest`, `askResponse` and `allowanceResponse`.
* Related: [ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [ADR-0024](0024-operator-work-uses-one-execution-authority.md), [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md).
