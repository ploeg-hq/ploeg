---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A card list finds cards by roster login, newest activity first

## Context and Problem Statement

Vloer's binders and packs show the cards a person played a part in. The owner's card contract addendum (P4), section "Card list for binders and packs", asks Ploeg for `GET /api/v1/operator/cards?member=<login>…`: full cards for the Work Items whose roster names any given login, team-scoped by operator auth, newest activity first, with a `since` filter and a cursor. Ploeg assembles a card per Work Item when it is read ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)) and keeps no roster table, so a list cannot filter on the assembled roster in SQL. Assembling every card of a team to filter it would not fit the operator API's five-second request timeout. The question is how candidates are found, how they are ordered and paged, and how many cards one request assembles.

## Decision Drivers

* A listed card is exactly the single card ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)): one assembler, no second projection that drifts.
* Membership is the roster's: merger, reviewer, qa, acceptor and cosigner ([ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md), [ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md)), and the steward. Ploeg's own forge logins are never members.
* Forge and tracker logins differ in case between systems, so a login matches without case.
* One request does bounded work.

## Considered Options

* **Find candidate Work Items through lowercased login indexes on the stored person facts, order them by a computed activity time, and confirm each against its assembled roster**
* Keep a roster table written whenever a fact changes
* Let Vloer page the Work Item list and read each card

## Decision Outcome

Chosen option: "**Candidates from login indexes, ordered by activity, confirmed against the assembled roster**", because it reuses the card assembler unchanged and adds only indexes.

1. **Candidates.** Migration 0029 indexes `lower()` of `pull_requests.merged_by`, `pull_request_reviews.reviewer`, `gate_transitions.actor` and `card_cracks.mended_by`, and adds `agent_runs (work_item_id, started_at)`. A Work Item is a candidate when a member merged one of its plays, reviewed one, moved its ticket out of the test or acceptance gate, or merged a fix that mended one of its confirmed cracks without being its steward. These are the facts the roster is built from; the steward is always a merger or an approving reviewer.
2. **Confirmation.** Each candidate is assembled by `OperatorCard` and listed only when its roster or steward names a member, compared without case. Logins of the configured forge bots are dropped from the request first.
3. **Activity.** A card's activity time is the latest of: the first Run's start (minted), a play first seen or merged, a merged play's first deploy to its release environment ([ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)), and a confirmed crack's confirmation, mend or mend confirmation. A Work Item with none of these uses its creation time. A play's opening is Ploeg's first record of it, which can be later than the card's `pr_opened` event when a checkpoint saw the link first. `since` keeps cards whose activity is at or after it.
4. **Order and cursor.** Cards come newest activity first, ties by Work Item id descending. `nextBefore` is an opaque cursor (`c1.` and base64url of the activity time in microseconds and the Work Item id) of the last candidate the page examined. A card whose activity moves after it was paged past does not come back on a later page; it leads the next first page.
5. **Bounds.** `limit` is 1 to 50 (default 20), `member` 1 to 20 logins of at most 256 bytes. A page examines at most twice its limit, so a page can hold fewer cards, or none, while `nextBefore` is set. Gateway reads for running Runs share one three-second deadline per request ([ADR-0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md)).
6. **Response.** `{schemaVersion: 1, cards: [card…], nextBefore: string|null}` (`cardsResponse` in `operator-api.v1`). `schemaVersion` is the number 1, as the single card sends, not the older operator responses' string `"1.0"`.

### Consequences

* Good, because a listed card cannot disagree with the single card.
* Good, because the candidate query reads indexes only, and the work per request is bounded by the limit.
* Bad, because each listed card is a separate read transaction, so a page is not a single snapshot.
* Bad, because a card's activity can move between pages; paging is stable for cards that do not change, not a changefeed.
* Bad, because a member named only in a play beyond the card's first 50, or a review beyond its first 500, is a candidate the assembled card does not confirm; that page then holds fewer cards.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store`: membership by merger, reviewer, approving steward, qa, acceptor and cosigner, case-insensitive, with bots and a developer who only moved a ticket into test left out (`TestOperatorCardsListsEveryRosterRoleAndTheSteward`); team scope, order and `since` (`TestOperatorCardsAreTeamScopedNewestFirstAndFilteredBySince`); a release to the release environment and a confirmed crack count as activity and a staging deploy does not (`TestOperatorCardsActivityCountsReleaseAndCracks`); cursor paging without repeats (`TestOperatorCardsPageWithAStableCursor`); the 50-card cap and foreign cursors (`TestOperatorCardsCapsThePageAndRejectsForeignCursors`); and the candidate query plan uses the migration 0029 indexes (`TestOperatorCardsFindCandidatesThroughTheLoginIndexes`).
* `pkg/httpapi`: responses validate against `operator-api.v1`, including the empty list, and bad queries answer 400, a team outside scope 403 and no credential 401.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Keep a roster table written whenever a fact changes

* Good, because membership becomes one indexed lookup with no confirmation step.
* Bad, because every writer of a person fact (forge webhooks, gate moves, crack decisions) must keep it in step with the assembler's rules, and the two drift.

### Let Vloer page the Work Item list and read each card

* Bad, because Vloer would read every card of a team to find one person's, which does not scale past a small team.

## More Information

* The owner's card contract addendum (P4), section "Card list for binders and packs", gives the endpoint and response shape.
* [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) defines the card this list returns.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. This decision stays in force while the card endpoints are deprecated and is removed with them in the next minor release.

## Re-evaluation triggers

* A card list request passes one second at p95: keep a roster or activity table instead of assembling each card.
* Pages with fewer cards than their limit while `nextBefore` is set become common: raise the examine bound or fix the truncation that causes them.
* Tracker users are linked to forge logins: a member may be one person with several logins resolved by Ploeg.
