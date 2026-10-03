---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run card is assembled per Work Item from stored facts

## Context and Problem Statement

The proposed Run cards show what Unfold did for one ticket: who worked it, what it cost, which pull requests it opened and how they ended. [ADR-0045](0045-keep-run-usage-and-merge-facts.md) made Ploeg keep the usage, merge and review facts a card needs. Three questions remain open:

* What is one card? A Run, a Shift and a Work Item are all candidates. A ticket that bounced (pull request closed, reworked, opened again) has two pull requests and often two Shifts.
* Who draws it? Vloer renders, but it has no forge access and no database. Ploeg holds every fact.
* Which facts are still missing? A card shows the size of each change and whether CI passed. Ploeg reads neither: `pull_requests` has no diff figures and no commit status, and an open pull request is only recorded once a review, merge or close arrives.

The owner decided on 2026-10-01: one card per ticket, its pull requests as plays, card style set on the Work Target with per-client themes on top of skins, and rarity still open.

## Decision Drivers

* Facts only. Ploeg never sends presentation, display percentages or moment tiers; Vloer owns how a card looks.
* Unknown stays unknown. A figure nobody reported is absent, never zero ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)).
* No forge read when a card is read. A card is read far more often than a pull request changes.
* `operator-api.v1` changes only additively (`docs/contracts/README.md`).
* Rarity, grade, finish and condition are undecided and must not leak into stored data.

## Considered Options

* **One card per Work Item, assembled from stored facts when read, with diff and CI captured when pull request facts are recorded**
* One card per Run
* One card per Shift
* Store an assembled card and update it on every event
* Vloer assembles the card from the existing work-item detail

## Decision Outcome

Chosen option: "**one card per Work Item, assembled when read**", because the ticket is what a person recognises, a bounced pull request is part of that ticket's story, and assembling from stored facts keeps one source of truth.

1. **One card per Work Item.** Its pull requests are its plays, oldest number first. A closed pull request and its merged successor are two plays on one card. An epic's set card is out of scope.
2. **Ploeg sends facts, Vloer renders.** `GET /api/v1/operator/work-items/{id}/card` returns `{"schemaVersion": 1, "card": …}` under the existing operator authentication and team scope, and 404 outside it. `pkg/store.OperatorCard` assembles it in one read-only transaction: identity, target, state, steward, roster, crew, plays, totals and events. The schema is the `card` definition in `operator-api.v1.schema.json`.
3. **Derived, not stored.** State: `withdrawn` for a withdrawn Work Item; `drafting` with no play; `in_review` while a play is open or its state is unknown; `merged` when the latest play merged, or a play merged and the Work Item is done; `closed` otherwise. The steward is whoever merged the latest merged play, else the last approver. The roster lists the humans who merged or reviewed. Forge logins Ploeg acts as (`PLOEG_FORGEJO_BOT`, `PLOEG_GITLAB_BOT`) are never a person on a card.
4. **Totals.** Every started Run counts. A usage figure sums the Runs that reported it and is absent when none did. `usageComplete` is false when cost, tokens or a figure present in the totals misses a started Run. `costStatus` is `reserved` while budget is still held, `observed` once a cost is recorded, `not_reported` otherwise.
5. **Diff and CI are captured with the pull request facts.** Migration `0024_pull_request_diff_and_ci.sql` adds `additions`, `deletions`, `changed_files`, and `ci_state`, `ci_checks`, `ci_head_sha` and `ci_captured_at` to `pull_requests`. When the forge webhook records facts for a Ploeg pull request (opened, reopened, synchronized, review, merge, close, no longer mergeable), Ploeg then reads the pull request once for the diff and any missing fact, and the combined commit status at its head. The review poller reads the same for every `awaiting_review` pull request, open ones included. Each read is best-effort: a failure is logged and leaves the figure unknown. A newer reading replaces the diff figures it reports and the whole CI reading. A pull request on no Ploeg branch costs no read.
6. **Forge parity.** Forgejo reports the diff from `GET /repos/{o}/{r}/pulls/{n}` and CI from `GET /repos/{o}/{r}/commits/{sha}/status`. GitLab reports only an exact `changes_count` (not "1000+") and CI from the commit statuses, combined as any failure, then any error, then any pending, else success. Checks outside that vocabulary (Forgejo `warning`, GitLab `skipped` and `manual`) are left out. Line counts stay unknown on GitLab. Reading statuses is the optional `provider.CommitStatusReader`; a forge without it leaves CI unknown.
7. **Card style on the Work Target.** `cardStyle: {skin, theme}` on a registered target or on a project's inline `repo` names the skin and an optional theme. The default is skin `vloer-native` and no theme. A repository styled differently in two places fails at boot.
8. **Deferred.** `rarity`, `grade` and `condition` are always `null`, `finish` is always `"matte"` and `demo` is always `false` from Ploeg, until those decisions are made. Proposed, not implemented: the steward becoming the developer carrying the ticket (the tracker assignee), once assignees are ingested.

### Consequences

* Good, because a card and the work-item detail can never disagree: both read the same rows.
* Good, because a bounced ticket shows its whole history on one card.
* Good, because diff and CI are on record before anyone opens a card, and reading a card costs no forge call.
* Bad, because every recorded webhook event on a Ploeg pull request now costs up to two forge reads, and every poll of an open pull request costs a second one for the commit status.
* Bad, because pull requests recorded before this lands have no diff or CI until their next event or poll, and a Work Item whose pull request predates [ADR-0045](0045-keep-run-usage-and-merge-facts.md) shows no play at all.
* Bad, because the card is assembled on each read; a Work Item with many Runs costs a larger query. Runs are bounded at 1000, plays at 50 and events at 1000 per card.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store`: migration 0024 applies with nullable columns; diff and CI never erase a known value with an unknown one and a newer reading replaces them; the card of a Work Item without a Shift is a draft with nothing zero-filled; an open pull request is in review with its diff, CI, crew and totals; a bounced pull request and its merged successor are two plays with the merger as steward and the bot left out; failed and stuck Runs are counted; partial usage is summed and flagged; the steward falls back to the last approver; a withdrawn Work Item and the team scope.
* `pkg/provider/forgejo`, `pkg/provider/gitlab`: the diff and commit status reads, unknown values left out, and the opened and synchronized webhooks.
* `pkg/httpapi`: an opened and a synchronized webhook capture the diff and CI from a fake forge; a failed read keeps the webhook's facts; a human pull request costs no read; the card endpoint validates against `operator-api.v1`, applies the configured card style and answers 404 outside the consumer's scope.
* `pkg/shiftengine`: the poller records an open pull request's diff and CI, and keeps the rest when the status read fails.
* `pkg/config`, `cmd/ploegd`: `cardStyle` validation, conflicts and the default skin.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### One card per Run

* Good, because a Run already has its usage and outcome.
* Bad, because a ticket becomes a stack of near-identical cards, and a reviewer Run has no pull request of its own.

### One card per Shift

* Good, because a Shift owns the branch and the budget pool.
* Bad, because a bounced ticket is split over two cards, and the person who reads the board thinks in tickets.

### Store an assembled card

* Good, because a read is one row.
* Bad, because every event must update a second copy of facts that already exist, and a missed update leaves the card wrong with no way to tell.

### Vloer assembles the card

* Good, because Ploeg changes less.
* Bad, because Vloer would re-derive state and totals from the detail response, which bounds Runs and events at 200 and carries no diff or CI.

## Re-evaluation triggers

* The owner decides rarity, grade or finish. A new record states which stored facts they derive from.
* Tracker assignees are ingested. Revisit the steward fallback.
* A card read takes more than 200 ms at p95, or a Work Item passes 1000 Runs.
* A third forge provider is added. Check that `CommitStatusReader` and the diff fields fit it.
* The forge read rate from fact capture shows up in forge rate limiting or in the forge's own metrics.

## More Information

* [ADR-0045](0045-keep-run-usage-and-merge-facts.md): the usage, merge and review facts this card reads.
* [ADR-0011](0011-the-pull-request-is-the-blackboard.md): the pull request stays the forge's record; Ploeg only notes what the forge said.
* [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md): registered targets, where `cardStyle` is set.
* Vloer renders the card under its own record (Vloer ADR-0026, proposed).
