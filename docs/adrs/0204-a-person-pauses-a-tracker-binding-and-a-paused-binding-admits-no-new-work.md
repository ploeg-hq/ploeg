---
status: accepted
date: 2026-11-19
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-19
---

# A person pauses a tracker binding, and a paused binding admits no new work

## Context and Problem Statement

A runtime tracker binding ([ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md)) can be created, replaced and removed, but not paused. A consumer that shows a binding as a route a person can switch off has nothing to call: removing the binding loses its targets, statuses and webhook, and deleting the Team removes every route and Role it has. The operator Teams read carries a `paused` field, but nothing in Ploeg writes it: `OperatorTeam.Paused` is always nil, a runtime Team returns `"paused": nil`, the schema types it `null`, and `pkg/store/operator_test.go` asserts nil. How does a person stop a binding taking new work, and keep everything else about it, with Ploeg's authorization and an audit event?

## Decision Drivers

* Each write maps to one binding, one permission and one audit event ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)).
* A pause must stop new work for certain. An assignment in a Scope no rule routes is not refused today: it is queued with no target and the worker falls back to its own repository (`resolveTarget` in `pkg/httpapi/server.go`). Taking a paused binding out of the routing table would therefore move its work, not stop it.
* Work already admitted keeps its Shift, its write-back and its connection.
* No field in the contract that nothing writes.

## Considered Options

* Pause and resume per binding, refused at intake
* Pause per Team
* Do not offer pause, and only remove the dead `paused` field

## Decision Outcome

Chosen option: "pause and resume per binding, refused at intake", because it touches exactly the route a person switched off and cannot leak its work elsewhere.

* `POST /api/v1/operator/teams/{team}/tracker-bindings/{id}/pause` and `…/resume` take no body. They need what `DELETE` on the binding needs: `bindScopes`, the Team in the consumer's scope, an actor, and a proven person where [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md) requires one. Both answer `200` with the binding and `changed`, which is false when the binding already was in the asked state, so a retry is safe. A change takes a new revision, so every replica rebuilds within its poll, and writes `tracker_binding.paused` or `tracker_binding.resumed` with the person, consumer and channel. A binding that is not live is `404`. A static route cannot be paused: change the file.
* A paused binding stays in the routing table with its pin, its statuses and its connection, so work it admitted before the pause runs and writes back as before, and queued work stays queued. At intake, an assignment whose Scope and Team match a routed, paused binding is refused before routing, as the route refusal `binding_paused` with a comment on the task. It never falls through to another route or the worker's repository. A re-assignment of an item already live is refused the same way and leaves the item and its Shift alone.
* A resume admits again from the next assignment. Assignments refused during the pause are not replayed; a person assigns them again. Ploeg does not re-read the board, and offers no "run now": intake is driven by webhooks, and a run-now would only mean re-reading the board.
* A replacement keeps the pause. A removal of a paused binding removes it.
* The binding read and list routes report `paused`, `pausedAt` and `pausedBy`, additively. The row gains `paused_at` and `paused_by`; the observer role reads `paused_at` and not `paused_by`.
* The Team's `paused` is deprecated, not given a meaning: a pause per Team would be a second, wider switch. It stays `null` until the compatibility window of [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) lets a later minor remove it; a consumer reads the binding's `paused` instead.

### Consequences

* Good, because a person can switch one route off and on without losing its configuration or webhook.
* Good, because a paused route cannot send work anywhere else; the refusal is visible in the route-refusals read and on the task.
* Bad, because work assigned during a pause must be assigned again after it. Accepted: replaying would mean reading the board, which no webhook-driven route needs otherwise.
* Bad, because the deprecated Team field stays in the contract for a deprecation window.

### Confirmation

* `go test ./pkg/httpapi/` runs `TestTrackerBindingPauseRefusesNewWorkAndResumeAdmitsAgain` and `TestTrackerBindingPauseNeedsWhatRemovalNeeds`: pause and resume, their repeats, the list's `paused`, a refused assignment with `binding_paused` and no Work Item, a replacement keeping the pause, admission after resume, the audit events, and the gates of removal.
* `go test ./pkg/store/` keeps `paused_by` out of the observer role's reach.
* `go test ./internal/compat/` checks the schema change is additive.
* `go test ./internal/ledger/` keeps this record well-formed.

## Pros and Cons of the Options

### Pause per Team

* Good, because it would give the existing Team `paused` field a meaning.
* Bad, because it stops every route of the Team, and a Team's other intake, when a person meant one route.

### Do not offer pause

* Good, because nothing is built.
* Bad, because the only way to stop a route is to remove it and bind it again later.

## Re-evaluation triggers

* A consumer asks for assignments refused during a pause to be admitted on resume.
* A pause per Team is asked for, for example to stop all intake of a Team during an incident.
* Ploeg gains a board re-read for runtime bindings, which would make replay on resume cheap.
* The deprecation window for the Team's `paused` closes.

## More Information

* 2026-11-19 — The owner decided to add pause and resume per binding, to use the existing binding removal for remove, and not to offer run now. The Team's `paused` field was found unwritten and is deprecated.
* 2026-11-19 — Accepted by the owner. The pause and resume routes, the `binding_paused` refusal and the deprecation of the Team's `paused` shipped in PR #468.
* Related: [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) (bindings), [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md) (route refusals), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) (attribution), [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) (deprecation).
