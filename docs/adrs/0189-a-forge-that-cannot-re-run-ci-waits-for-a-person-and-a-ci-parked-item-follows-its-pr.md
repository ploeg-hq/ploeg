---
status: proposed
date: 2026-11-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A forge that cannot re-run CI waits for a person to, and a CI-parked Work Item follows its pull request

## Context and Problem Statement

[ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) rule 5 re-runs cancelled CI once. Only the GitHub provider can re-run CI, and Forgejo 15's API has no re-run call, so on Forgejo a cancelled run sent the Work Item to `needs_human` at once. ADR-0119 accepted that as a cost. In practice people cancel Forgejo Actions runs to free busy runners, so every such cancellation parked a Work Item. That happened on 2026-11-01: a pull request's run was cancelled, every check reported "Has been cancelled", and the Shift closed `ci_not_passed` in the same evaluation.

A parked Work Item also never recovered. The review watch followed only `awaiting_review` items, so an item parked with `ci_not_passed` or `ci_not_finished` stayed `needs_human` after a person re-ran CI to green or merged the pull request, and its tracker item kept its needs-human status.

What does the gate do with cancelled CI on a forge that cannot re-run it, and what happens to a CI-parked Work Item whose pull request moves on?

## Decision Drivers

* A cancelled run is not the change's fault, and a person can re-run it in the forge in one click.
* A Work Item whose pull request is merged, or whose CI turns green on the head Ploeg delivered, should not stay in `needs_human`.
* No new Shift, Run or spend without a person asking for it.
* Items parked for other reasons (a question, budget, a person taking over) belong to a person and stay untouched.

## Considered Options

* Wait for a re-run in the forge, and let the review watch follow CI-parked Work Items
* Keep parking at once and leave recovery to a person re-assigning the item
* Dispatch the workflow again through Forgejo's `dispatches` endpoint

## Decision Outcome

Chosen option: "Wait for a re-run in the forge, and let the review watch follow CI-parked Work Items", because it recovers both cases without spend and without new forge machinery.

1. **No re-run on the forge.** When rule 5 applies, the forge cannot re-run CI and at least one excused check is cancelled, the gate holds the Shift as for pending CI and comments once on the pull request that CI was cancelled and must be re-run in the forge within the route's `ci.waitTimeout`. It waits until every cancelled check is reported again after the wait started, or the timeout passes. Checks reported again are judged by rules 2 to 5 as usual: green proceeds, a failure the change caused opens a fix round, and a check that still does not pass goes to `needs_human` with `ci_not_passed`, saying CI was re-run in the forge. On timeout the item goes to `needs_human` with `ci_not_passed`, and the message says the forge cannot re-run CI and nobody re-ran the named checks within the timeout. A check excused only because it also fails on the base commit is not waited for. GitHub keeps re-running once.
2. **A CI-parked Work Item follows its pull request.** The review watch also follows a Work Item Ploeg owns that is in `needs_human` because its last Shift closed with `ci_not_passed` or `ci_not_finished`. A merge of its pull request settles it as `done`, with the tracker write-back of an `awaiting_review` merge. A close without merge settles it as the close of an `awaiting_review` item does, and the Shift's close reason becomes `pull_request_closed`, so it settles once. While the pull request is open and the forge's CI is `success` on the head the item was delivered at (the writer's delivered head, or else the head the gate last waited on), the item returns to `awaiting_review` with an audit row, a tracker comment and the `awaiting_review` status. That starts no Shift and spends nothing. CI that passes on a head pushed afterwards does not return it, because nothing of Ploeg's reviewed that head.

### Consequences

* Good, because a cancelled run on Forgejo costs a click in the forge, not a parked Work Item.
* Good, because a CI-parked item reaches `done` or `awaiting_review` when its pull request does, and its tracker item follows.
* Bad, because on Forgejo a cancelled run keeps the Shift open, holding its budget, for up to the wait timeout.
* Bad, because a re-run is recognized by the check's update time, so a forge that reports none makes the gate wait out the timeout.

### Confirmation

`go test ./pkg/shiftengine/` in CI (`mise run verify`):

* `TestCancelledForgeCIWithoutReRunWaitsForARerunInTheForge`, `TestCancelledForgeCIRerunInTheForgeThatPassesAwaitsReview`, `TestCancelledForgeCIRerunInTheForgeThatFailsFollowsTheNormalRules`, `TestCancelledForgeCINobodyReRanParksAfterTheTimeout` and `TestCancelledForgeCIReRunsOnceThenNeedsAPerson` in `pkg/shiftengine/forge_ci_gate_test.go`;
* `TestACIParkedItemWhosePullRequestIsMergedIsDone`, `TestACIParkedItemWhoseCIPassesLaterAwaitsReview`, `TestACIParkedItemWhosePullRequestIsClosedIsSettledOnce` and `TestANeedsHumanItemParkedForAnotherReasonIsNotFollowed` in `pkg/shiftengine/review_ci_parked_test.go`.

## Pros and Cons of the Options

### Keep parking at once and leave recovery to a person re-assigning the item

* Good, because nothing changes.
* Bad, because every cancelled Forgejo run parks a Work Item, and a re-assignment starts a new, paid Shift for a pull request that may already be green or merged.

### Dispatch the workflow again through Forgejo's `dispatches` endpoint

* Good, because Ploeg could start CI itself.
* Bad, because it runs a named workflow on a ref, not the pull request's checks on its head, so its statuses need not match the cancelled ones, and it needs a workflow name per repository.

## Re-evaluation triggers

* Forgejo's API gains a call that re-runs a run's jobs; then the Forgejo provider implements the re-run and rule 1 applies only to forges without one.
* More than five Work Items a month still close as `ci_not_passed` because nobody re-ran cancelled CI within the timeout.
* A person needs a CI-parked item that turned green on a head they pushed to return to review.

## More Information

* Amends rule 5 and the "a provider that cannot re-run" consequence of [ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md); its other rules stand.
* The gate is `gateOnForgeCI` in `pkg/shiftengine/forge_ci_gate.go`; the review watch is in `pkg/shiftengine/review.go`, and the items it follows are `reviewWatchedWhere` in `pkg/store/review.go`.
