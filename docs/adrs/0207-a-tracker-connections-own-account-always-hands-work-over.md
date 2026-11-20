---
status: accepted
date: 2026-11-20
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-20
---

# A tracker connection's own account always hands work over

## Context and Problem Statement

[ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) section 9 made a tracker connection's own account a hand-over agent only when the connection set `handOverAgent: true`. A tracker is connected for one reason: so that work assigned to that account reaches Ploeg. With the option off, a binding through the connection accepts no work at all, because [ADR-0205](0205-only-an-assignment-of-ploegs-own-tracker-account-hands-work-over.md) ignores every other assignee. Its first operator consumer already sends `true` on every ClickUp connection. Why is it an option?

## Decision Drivers

* A tracker connection has one purpose; a setting that can only disable that purpose is configuration nobody needs.
* The account behind a connection is a dedicated member, never a person ([ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) rule 4).

## Considered Options

* Remove the option: every connection of a kind that recognises it hands work over
* Keep the option, default it to true

## Decision Outcome

Chosen option: "remove the option", because a default nobody should change is still a field to read, document and get wrong.

* The connection's own account, by the vendor user id its probe read, hands work over on the Scopes bound through that connection, for every connection of a kind whose adapter recognises it (ClickUp today). ADR-0195 section 9 still describes how; only the switch is gone.
* `handOverAgent` is removed from the start and patch requests and from the connection view. A request that still sends it is refused as an unknown field, `400 invalid_request`. The break is recorded in `docs/contracts/breaking-changes.yaml`.
* The `hand_over_agent` column stays, unread and unwritten, because migrations are append-only and dropping it buys nothing.

### Consequences

* Good, because connecting ClickUp is the whole setup: assign the account and the work arrives.
* Bad, because a consumer that sends `handOverAgent` is refused until it stops. A consumer stops sending it when it moves to this release.
* Bad, because a connection made with a person's own account would hand over that person's own assignments. ADR-0184 rule 4 already forbids such a connection.

### Confirmation

* `go test ./cmd/ploegd/` runs `TestEveryClickUpConnectionsOwnAccountHandsWorkOver`: a ClickUp connection made with no option hands work over for its account and for no other; it fails on the code before this record.
* `go test ./pkg/httpapi/` runs `TestHandingOverIsNoOptionOfAConnection`: `handOverAgent` is refused on start and patch and absent from the view.
* `go test ./internal/compat/` accepts the removal only through its recorded exceptions.
* `go test ./internal/ledger/` keeps this record well-formed.

## Pros and Cons of the Options

### Keep the option, default it to true

* Good, because no consumer breaks.
* Bad, because the field stays in the contract with no case where turning it off is right.

## Re-evaluation triggers

* A tracker connection is needed that only reads and writes back, for example a second account per Workspace for write-backs.
* Another tracker kind's adapter learns to recognise an assignment to the connection's account.

## More Information

* 2026-11-20 — The owner decided the option goes: a ClickUp account is connected for one reason.
* Related: [ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) section 9, [ADR-0205](0205-only-an-assignment-of-ploegs-own-tracker-account-hands-work-over.md), [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md).
