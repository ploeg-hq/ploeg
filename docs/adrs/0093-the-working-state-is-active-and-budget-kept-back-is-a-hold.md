---
status: accepted
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# The working state is active, budget kept back is a Hold, and every cost has one Cost Status

## Context and Problem Statement

Three words in Ploeg's operator API describe the wrong thing or mean two things.

* The Work Item state `leased` is named for the Lease it used to imply. Since [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) a Work Item in that state has a Shift and may hold no Lease at all, because a Round of readers takes none. The domain model recorded this as an open ambiguity, "the `leased` Work Item state", and recommended deciding with the implementing change.
* Money kept back for started work that has not settled is called `reserved` in the Shift and the summary (`reservedUsd`), `held` in the Ask Allowance and the unsettled-accounts list (`heldUsd`), and "authorization" in places. `authorizedUsd` meanwhile names the ceiling one Run may spend. A reader cannot tell whether `reserved` and `authorized` are the same figure.
* Cost status has a value set per place: Run detail answers `observed` or `unknown`, an Ask answers `provisional`, `settled` or `unknown`, and an Ask's `provisional` covers both an unsettled cost and a settled one inside its correction window. Run detail's `observed` also covers a final cost.

The owner decided on 2026-10-13 how to name these. The operator API is read by consumers pinned to Ploeg releases, and v1 is frozen ([contracts](../contracts/README.md), *Versioning policy*). How does Ploeg rename them without breaking a consumer silently?

## Decision Drivers

* One word per concept, the same in the API, the store, the MCP tools and the domain model.
* `authorized` keeps exactly one meaning: the ceiling a budget allows.
* A consumer pinned to an older Ploeg release keeps working until it moves, and a consumer is told what changes, in the schema and in the release notes.
* A rolling update runs the outgoing ploegd beside the new one for a moment, and both write `work_items.state`.
* A Cost Status value a place cannot reach is not listed for that place.

## Considered Options

* Rename in two steps: new names now with deprecated aliases, removal in a later release
* Publish `operator-api.v2` with the new names and keep v1 unchanged
* Keep the old names and document what they mean

## Decision Outcome

Chosen option: "**Rename in two steps: new names now with deprecated aliases, removal in a later release**", because it changes no field a consumer reads without notice and keeps every consumer working through one upgrade, while the alternative v2 file would duplicate more than eighty unchanged definitions to rename a state and four fields.

### The words

* **active** is the Work Item state in which a Team is working the item. `leased` is retired. A Lease stays what it was: the right to write a Shift's branch.
* A **Hold** is budget kept back against a Shift pool or an Ask Allowance for work that started and has not settled. The verb and adjective are "held". API fields say `held…`: `heldUsd`. `authorized` keeps one meaning, the ceiling a Run's budget allows (`authorizedUsd`), and a Hold is how much of that ceiling is still kept back.
* **Cost Status** is one value set owned by Ploeg, `work.CostStatus` and `$defs/costStatus` in the operator schema:

| Value | Meaning |
| --- | --- |
| `observed` | Spend seen so far that can still move: the Run or Ask is live, or its account has not settled |
| `provisional` | Settled once, still inside the correction window, so a late gateway charge may raise it |
| `settled` | Final |
| `unknown` | Ploeg has no trustworthy figure, which is never zero |

Each place uses the subset it can reach:

| Place | Values |
| --- | --- |
| Run detail `costStatus` | all four. With a gateway account it follows the account; without one a finished Run with a reported cost is `settled` |
| Ask `spend.costStatus` and facts `asks[].costStatus` | all four. `unknown` before any spend was observed and when settlement found no spend-log entries |
| Operator execution spend `costStatus` | `observed` or `unknown`, from the gateway's live reading; a deterministic demo answers `demo`, which is not a Cost Status and says no money was spent |
| Worker `GET /runs/{token}/llm/spend` `status` | `observed` only |

### This release

* The store writes `active`. Migration `0052` rewrites every stored `leased` to `active`, narrows `work_items_state_known` to the new name, rebuilds the `work_items_created_open` index, and adds a trigger that rewrites a `leased` write to `active`. During a rolling update the outgoing ploegd may still write `leased`; the trigger stores it as `active`, so readers of this release never see the retired value.
* Responses report `active` where they reported `leased`. The Work Item state enums in `operator-api.v1`, `tracker-execution.v1` and `taskspec.v1` list `active` and keep `leased` so payloads from an older release still validate.
* `heldUsd` is added beside `reservedUsd` on the Shift (operator and tracker-execution), the summary spend and the facts `runBudgetHolds`, and `active` beside `leased` in the summary counts. The old fields stay, equal to the new ones, and are marked `"deprecated": true` in the schema. ploegd's own client (`pkg/store`, used by ploeg-mcp) reads either name, so it also reads an older ploegd.
* Inputs accept the retired name: the `state` filter and requeue's `expectedState` read `leased` as `active`, and `state=active,leased` is one state, not a repeat.
* Cost statuses draw from the one set, as in the table above. Run detail reports `provisional` and `settled` where it said `observed`; an Ask reports `observed` while its account is unsettled where it said `provisional`, and `unknown` before its first observation.
* ploeg-mcp says `active` and `heldUsd`, and explains a Hold and the four Cost Status values in its instructions.

This is a change to the frozen v1, named as an exception in the contracts' versioning policy like [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)'s. The state value and the Cost Status values cannot have aliases, because a field has one value. A consumer therefore must read `active` and all four Cost Status values before it runs against this release.

### The removal release

A later release, never this one, cut once the operator consumers in use read only the new forms. It removes `reservedUsd` and the `leased` count from v1, makes `heldUsd` and `active` required, drops `leased` from the state enums and answers 400 to it as input, and a migration drops the trigger. It is a `feat!` with a `BREAKING CHANGE:` footer and a record of its own that names this one.

### Left as they are

* The inference account state `reserved` and the audit action `llm.reserved` name a step of the account's lifecycle (an account row exists before a key is minted), not money. `keyState: reserved` in the Ask spend and the Run list's `reservedModels` (the models the account was reserved for) keep their names.
* The internal view column `run_budget_holds.reserved` and Go locals keep their names; no consumer reads them.

### Consequences

* Good, because the API, store, MCP and domain model use one word for each of the three concepts, and the open ambiguity is resolved.
* Good, because no consumer field disappears in this release, and an older ploegd stays readable by Ploeg's own client.
* Good, because a consumer can show a cost's state without combining `costFinal`, `settledAt` and the account state itself.
* Bad, because a consumer that matched `leased` or treated `observed` as "a cost exists" breaks on upgrade unless it moved first. The contract, the schema and the release notes say so, and the one consumer moves in the same change that bumps its pin.
* Bad, because responses carry two names for the same figure until the removal release.

### Confirmation

* `go test ./pkg/work/` proves `ParseState` reads `leased` as `active`.
* `go test ./pkg/store/` proves migration `0052` and its trigger store a `leased` write as `active`, that the deprecated aliases are emitted equal to the new fields, and that an older ploegd's `reservedUsd` and `leased` still decode.
* `go test ./pkg/httpapi/` validates every operator response against `operator-api.v1.schema.json`, proves the `state` filter reads `leased`, `active` and both together as one state, that the summary's `leased` equals `active`, and that `$defs/costStatus` equals `work.CostStatuses`.
* `go test ./internal/ledger/` gates this record, and `mise run docs-check` regenerates and checks the domain pages.
* CI runs them through `mise run verify` and `mise run docs-check`.

## Pros and Cons of the Options

### Publish `operator-api.v2`

* Good, because v1 would stay frozen to the letter.
* Bad, because v2 would copy every unchanged definition, every consumer would switch schema ids, and ploegd would serve both shapes for the rename of a state and four fields.

### Keep the old names and document what they mean

* Good, because nothing changes for a consumer.
* Bad, because `leased` keeps describing a Lease a Work Item may not hold, `reserved` and `held` keep naming one figure twice, and an Ask's `provisional` keeps meaning two things.

## More Information

* 2026-10-13: decided by the owner. The operator consumer records the same vocabulary for its own screens.
* Resolves the domain ambiguity "the `leased` Work Item state" in `docs/domain/model.yaml`, which also gains the terms Hold, Cost Status and Plan and retires `leased` and Reservation.
* Migration: `pkg/store/migrations/0052_work_item_state_active.sql`.
* Budgets: [ADR-0012](0012-two-level-budgets-authorized-and-settled.md). Ask Allowance: [ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md). Consumers are not named: [ADR-0069](0069-ploeg-names-none-of-its-consumers.md).
* The versioning exception: `docs/contracts/README.md`, *Versioning policy*.
