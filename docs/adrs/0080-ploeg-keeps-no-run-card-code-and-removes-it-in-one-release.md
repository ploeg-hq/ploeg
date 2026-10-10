---
status: accepted
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg keeps no Run card code, and its card routes, tables and settings go in one release

## Context and Problem Statement

[ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moved the Run card to the operator consumer in two steps. Part 1 shipped in 0.2.0-rc.10 and rc.11: the delivery facts endpoints, the keyed pull request comment, the deprecated card legacy export and the `cards.enabled` switch. The consumer now builds cards from the facts. On every live environment it has imported the export, and Ploeg runs with `cards: {enabled: false}`.

On 2026-10-10 the owner restated the decision: "Ploeg no longer has any card logic, or it shouldn't. So this is purely an extra fun little domain on top of what Unfold is." Part 2 removes the card from Ploeg. What goes, what stays, how does a deployment upgrade without failing to boot, and how does a breaking change to `operator-api.v1` get recorded under a versioning policy that freezes v1?

## Decision Drivers

* Ploeg computes nothing a consumer can compute from facts Ploeg supplies ([ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* The facts endpoints, the keyed comment and status and gate recording must not change. Their test assertions pass unchanged.
* Rows a person or a freeze created exist only in Ploeg until the consumer imported them. That import is done on every live environment.
* A deployment whose configuration still sets a card key must keep booting for one release.
* The contract change is visible and recorded, not slipped into v1.

## Considered Options

* Remove every card route, table, package and setting in this release; retired settings warn for one release
* Keep the card routes answering, deprecated, for another release
* Publish `operator-api.v2` without the card definitions and keep v1 as it was

## Decision Outcome

Chosen option: "remove everything in this release; retired settings warn for one release", because the consumer no longer reads any card route, the card sweeps are already off on every live environment, and each release that keeps the code keeps a consumer's product inside Ploeg.

### What goes

* **Tables and columns,** by migration `0044_remove_run_cards.sql`: `card_cracks`, `card_rarity`, `card_comments`, and `pull_requests.kpis`, `pull_requests.kpis_computed_at` and `pull_requests.shape`.
* **Packages:** `pkg/rarity`, `pkg/playkpi` and `pkg/cardimage`. `pkg/flow` goes whole: status recording needed only its status name comparison, which the store now does itself. `pkg/gate` keeps the per-board status-to-gate mapping, `IsBounce` and the bounce reason it records; the journey, visits and right-first-time counts go.
* **Store:** the card, card comment, condition, flow, grade, list and rarity files, `cracks.go`, the mend half of the change facts (`RecordMends`, `ConfirmMends`), the epic sets, the legacy export, and the KPI and change-shape computation of the pull request pipeline.
* **HTTP:** `GET work-items/{id}/card`, `GET cards`, `GET work-items/{id}/crack-candidates`, `GET` and `POST work-items/{id}/cracks`, `POST work-items/{id}/evolved`, `POST cracks/{crack}/confirm|dispute|resolve` and `GET card-legacy-export`. They answer 404. The card comment posted on merge goes with them.
* **Sweeps:** the card comment, rarity and mend sweeps of `cmd/ploegd`.
* **Contract:** every `card*`, `crack*` and `legacy*` definition of `operator-api.v1.schema.json`, and their `oneOf` entries.

### What stays

* The delivery facts (`GET work-items/{id}/facts`, `GET facts`) and the keyed pull request comment, unchanged.
* Every fact they read: pull requests, reviews, conversation events, CI runs and jobs (`store.CIJob` replaces `playkpi.Job`), changed files with lines and per-file indentation, labels, changed paths, reverts, deploys, status and gate moves, tracker creation time and estimate, and epic membership.
* At a merge Ploeg still reads the diff once, only to keep each file's indentation (`RecordPullRequestIndentation`); the diff is never stored.
* A revert is still recorded. Its audit action is now `pull_request.reverted` instead of `card.reverted`.

### Status moves

Ploeg records every status move of a board with `gates:`, mapped to a gate or not. A board without gates records none. Before, an empty `statusKinds: {}` made a board without gates record status moves for the card's flow figures; with the flow figures gone, such a board needs gates to keep recording.

### Retired configuration keys

`cards`, a target's or a board's `cardStyle`, `release`, `rarity` and `cardShape`, a team's `cards` and `workingHours`, and a board's `statusKinds` are accepted and ignored in this release. ploegd logs `retired configuration key ignored` once per key at boot. The next minor release removes them from the configuration types, so the strict decoder refuses them as unknown keys. The chart's values schema and the configuration reference no longer list them.

### The contract

The versioning policy freezes v1: a removal is v2. This release removes whole routes from v1 instead, as an exception the policy now names: a route that was marked deprecated in the schema for at least one release may be removed from v1 with its definitions, provided no response that remains changes. Every card route carried that deprecation since 0.2.0-rc.10 (ADR-0079), and no remaining definition referenced a removed one. A v2 file would have duplicated sixty-nine unchanged definitions to drop fifty, and forced the one consumer to move to a new schema id for routes it no longer calls.

### Records

* This record ratifies [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md), which is now accepted.
* Rejected, as ADR-0079 planned for proposed records: [0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md), [0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md), [0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md), [0054](0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md), [0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md), [0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md) and [0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md).
* Narrowed to the facts they record, each with a dated note: [0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md) (live usage of a running Run), [0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) (gate moves and bounce reasons), [0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md) (parent membership), [0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md) (status moves and tracker facts) and [0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md) (forge activity, CI runs and per-file indentation).
* Unchanged: [0045](0045-keep-run-usage-and-merge-facts.md), [0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) and [0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md).

### Upgrade

Upgrade a deployment only after its consumer reports the legacy export imported. Migration `0044` drops the card rows; a deployment that skipped the import loses its cracks, frozen rarity tiers, card comment records and stored change shapes. Remove the retired keys from the configuration before the next minor release.

### Consequences

* Good, because Ploeg holds no card, grade, rarity, crack or flow logic, and a card change never needs a Ploeg release.
* Good, because about a fifth of the Go code under `pkg` and `cmd` goes, with its sweeps and their forge writes.
* Good, because a deployment with stale card keys still boots and is told what to remove.
* Bad, because the drop is irreversible: a consumer that did not import loses those rows.
* Bad, because a board that recorded status moves through `statusKinds` alone stops recording them until it gets `gates:`.
* Bad, because `operator-api.v1` changes shape for anyone still calling the card routes; the exception in the versioning policy is the price of not publishing a v2 for removed routes.

### Confirmation

* `go test ./pkg/store/` proves migration `0044` dropped the three tables and three columns, that the pipeline keeps events and CI runs, and that `RecordPullRequestIndentation` keeps each file's indentation and no code.
* `go test ./pkg/httpapi/` runs the facts and keyed comment tests with their assertions unchanged (one facts fixture no longer writes the dropped `kpis` column), and the webhook tests read status moves, gate moves, epics, deploys, changed paths, files, labels and reverts through the facts endpoint against `operator-api.v1.schema.json`.
* `go test ./pkg/config/ ./cmd/ploegd/` proves every retired key is accepted, listed and warned about, and that an unknown key still fails the boot.
* `go test ./internal/ledger/` gates this record and the status changes it makes.
* CI runs them through `mise run verify` and `mise run docs-check` in `.github/workflows/ci.yml`.

## Pros and Cons of the Options

### Keep the card routes answering for another release

* Good, because a consumer that had not imported would have one more release to do it.
* Bad, because every live environment has imported and runs with the card work off, so the routes would serve no one while the code stays.

### Publish `operator-api.v2`

* Good, because v1 would stay frozen to the letter.
* Bad, because v2 would copy every unchanged definition, the consumer would switch schema ids for nothing it uses, and v1 would describe routes that no longer exist.

## Re-evaluation triggers

* A Ploeg decision (admission, routing, budget or review) needs a figure the consumer computes. It returns as a Ploeg fact with its own record.
* A deployment reports a retired key it cannot remove before the next minor release.
* A second consumer needs a fact the facts endpoints do not carry.

## More Information

* 2026-10-10: decided by the owner; part 2 of ADR-0079.
* Migration: `pkg/store/migrations/0044_remove_run_cards.sql`.
* The versioning exception: `docs/contracts/README.md`, *Versioning policy*.
* The research behind the card records stays in `docs/research/` as dated records.
