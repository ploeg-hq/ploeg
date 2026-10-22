---
status: proposed
date: 2026-10-19
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg reports how each board and repository it routes to is wired

## Context and Problem Statement

Connecting a repository and a tracker board to Ploeg takes several acts across several systems: a `targets` entry and a board rule in the configuration file, the tracker webhook, the Team users shared into the board, the bot's access to the repository, an `AGENTS.md` on the base branch and the forge webhook that brings reviews back ([onboarding friction](../research/2026-08-28-onboarding-friction.md)). Ploeg already checks some of these, but it only says so in places a person does not look:

* target readiness ([ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)) is a boot log line and, for an item, a refusal;
* the Vikunja webhook check is a log line and a count in `/readyz`;
* nobody checks that a Team's tracker user can see a board; the assignment silently does nothing;
* a repository whose forge webhook is missing looks exactly like one where nobody reviews.

The person who handed a task over learns that a connection is broken only when the task is refused or never starts. How does an operator consumer show a person whether every board and repository is wired, and what to fix when one is not?

## Decision Drivers

* A broken connection must be visible before a task fails on it, not after.
* Ploeg owns tracker and forge I/O; a consumer must not need tracker or forge credentials to check wiring.
* Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)), and an operator read stays inside the consumer's team scope.
* Configuration stays a file read at boot. This decision reports on it; it does not make it writable.
* A check must not slow every read down or hammer the tracker and forge.

## Considered Options

* An operator read, `GET /api/v1/operator/connections`, built from the configuration and the checks ploegd already runs
* Expand `/readyz` with the per-board and per-repository detail
* Leave the checks to the consumer, with its own tracker and forge credentials

## Decision Outcome

Chosen option: "An operator read built from the configuration and the checks ploegd already runs", because it reuses checks Ploeg runs anyway, needs no new credentials anywhere, and stays inside the operator API's scope and contract rules.

**The view.** `GET /api/v1/operator/connections` answers `connectionsResponse`: every board in `trackers.*.projects` and every repository a board or a `repo/<key>` label can reach.

* A **repository** is a registered target, keyed by its target key, or a repository a board names directly, keyed `owner/name@branch`. It carries its readiness (`ready`, `not_ready` with the reason, or `unknown` when the forge could not be asked), the boards that route to it, and `lastForgeEventAt`: when Ploeg last recorded a forge webhook event for it in the last 30 days, null when none.
* A **board** carries its tracker, name, project id, its rule (`repository`, `default`, `allow`, pinned `team`), the state of its tracker webhook (`present`, `missing`, `unreadable`, `unresolved`, or `unchecked` for a tracker Ploeg does not check), and every Team in scope with each of its tracker users and whether the board is shared with that user (`true`, `false`, or null when the share list could not be read).

**Where the facts come from.**

* Readiness comes from a readiness gate over every repository in the view. It inspects them all at boot and every hour; a read re-inspects only repositories last seen not ready, within 10 seconds.
* Board facts come from the hourly Vikunja webhook check, which now also reads each board's direct shares (`GET /projects/{id}/users`). A user who reaches a board only through a Vikunja team shows as not shared.
* `lastForgeEventAt` is read from the audit log; migration `0067_forge_events_by_time.sql` indexes forge events by time.

**Scope.** Boards pinned to a Team outside the consumer's scope are left out, and only Teams in scope are listed. Repositories are listed for every consumer, because a repository can be reached by every Team that can be assigned on its board.

### Consequences

* Good, because a missing webhook, an unshared Team user, a repository without `AGENTS.md` and a repository whose forge never reports reviews each become a visible fact with a known fix, before a task fails on it.
* Good, because the consumer needs no tracker or forge credentials, and no check runs more often than it does today, apart from the hourly readiness refresh.
* Bad, because `lastForgeEventAt` is a proxy: a quiet repository and a repository whose forge webhook is missing look the same. Ploeg's forge credentials cannot read a repository's webhooks without admin rights.
* Bad, because only Vikunja boards are checked; ClickUp and GitHub Issues boards answer `unchecked`.
* Bad, because the board facts are up to an hour old; `checkedAt` says how old.

### Confirmation

* `go test ./pkg/httpapi/` validates the response against `operator-api.v1.schema.json`, and checks readiness, the share and webhook states, the forge-event time, team scope, and that query parameters, other methods and anonymous reads are refused.
* `go test ./cmd/ploegd/` checks that the webhook check records each board's webhook state and shares under its name or pinned id, and that the view lists registered targets and direct board repositories once each.
* `go test ./pkg/provider/vikunja/` checks the share-list read.
* CI runs all three with `mise exec -- go test ./...`.

## Pros and Cons of the Options

### Expand `/readyz` with the per-board and per-repository detail

* Good, because it needs no authentication change.
* Bad, because `/readyz` is unauthenticated and for probes; repository and board names do not belong there, and a probe must stay cheap.

### Leave the checks to the consumer, with its own tracker and forge credentials

* Good, because Ploeg changes nothing.
* Bad, because every consumer would need tracker admin and forge read rights and would duplicate Ploeg's resolution of board names, labels and targets, which is exactly where the two drift today.

## Re-evaluation triggers

* Ploeg gains runtime registration of boards or targets, or a Team manifest ([ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md)) replaces the routing file.
* Ploeg's forge credentials can read repository webhooks, so a missing forge webhook can be checked directly.
* A second tracker provider is used for production work, so `unchecked` hides real gaps.
* A deployment has enough boards that the hourly share check costs noticeably in tracker requests.

## More Information

* 2026-10-19 — The owner agreed to make connection health visible first, before reshaping how connections are declared. Prompted by a task refused because its `repo/` label named no target.
* Contract: `docs/contracts/operator-api.v1.schema.json`, definitions `connectionsResponse`, `connectionRepository`, `connectionBoard`.
* Related: [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md), [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md).
