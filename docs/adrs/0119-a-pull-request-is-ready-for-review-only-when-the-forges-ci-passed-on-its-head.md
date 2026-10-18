---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: 0070
review-by: 2027-01-31
---

# A pull request is ready for review only when the forge's CI passed on its head

## Context and Problem Statement

[ADR-0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md) holds back `awaiting_review` while the worker's own checks did not pass on the pushed commit. It rejected waiting for the forge's CI, because Ploeg could not read it on every forge. The gate in `checksHoldBackReview` therefore reads only the worker's verification. A route without worker checks, or whose worker checks pass while the forge's CI fails, still asks a person to review a pull request with red CI.

That happened on 2026-10-18. A one-line documentation change on homelab-cluster (pull request #784, VIK-2200) reached `awaiting_review` while all seven required checks on its head commit reported `failure`, each with the description "Has been cancelled". CI never finished, and the change was not what broke it. Unfold listed the item under "Ready for your review".

Ploeg now reads the combined commit status at the pull request's head (`pkg/forgefacts.CI`, stored as `store.PullRequestCI`), which is ADR-0070's third re-evaluation trigger. When is a pull request ready for review, and what happens when the forge's CI is red, still running, or was cancelled?

## Decision Drivers

* `awaiting_review` tells a person the work is worth their time. Red or unfinished CI makes that claim false, whichever checks went red.
* The result has to describe the commit a person merges: the pull request's head on the forge.
* A fix round costs money. It should only open when the writer can fix the failure, so a cancelled or infrastructure failure must not open one.
* A repository without CI must keep working.

## Considered Options

* Gate `awaiting_review` on the worker's checks and the forge's CI at the pull request head, sorting failures by cause
* Keep the worker-only gate from ADR-0070 and only show red CI in consumers
* Gate on the forge's CI and treat every red status as the writer's failure

## Decision Outcome

Chosen option: "Gate `awaiting_review` on the worker's checks and the forge's CI at the pull request head, sorting failures by cause", because it is the only option where `awaiting_review` means the merged commit passed CI and no fix round is spent on a failure the writer cannot fix.

1. **The worker's checks.** ADR-0070's rules 1 to 3 stand unchanged. The worker verifies a fresh clone at the pull request's head. A failed or incomplete verification reopens the writing Round while fix rounds and budget last, and otherwise goes to `needs_human`.
2. **The forge's CI.** A Work Item reaches `awaiting_review` only when the combined status at the pull request's head SHA is `success`, or when the forge reports no statuses at all for that commit. The SHA is the one the forge reports for the pull request ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)), never one an agent reports.
3. **Pending.** While the combined status is `pending`, the Shift does not settle. It waits for CI up to the route's `ci.waitTimeout` (default 60 minutes). On timeout the item goes to `needs_human` with close reason `ci_not_finished`, and the message names the contexts that are still pending.
4. **Failed because of the change.** A context in `failure` or `error` that the provider does not mark as cancelled reopens the writing Round as a fix round, the same way a failed verification does ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) caps and budget). The writer's briefing names each failing context with its target URL and, where the forge serves one, the tail of its log. A reviewer's approval does not override it. When the fix loop runs out, the item goes to `needs_human` with the existing cap or budget reason, and the message names the contexts.
5. **Cancelled or infrastructure.** A context is not the writer's to fix when the provider reports it cancelled (on Forgejo, `failure` with the description "Has been cancelled"; on GitHub, a check run with conclusion `cancelled`), or when the same context is also failing on the pull request's base commit. Ploeg asks the forge to re-run CI on the head once and goes back to rule 3. If the result is still not `success` and no context is the writer's to fix, the item goes to `needs_human` with close reason `ci_not_passed`, naming the contexts and why each was not sent to the writer. That path spends no fix round.
6. **After the hand-off.** When the head CI of an item already in `awaiting_review` stops being `success` (a later push, a re-run, a refreshed fact), the item leaves `awaiting_review` under rules 3 to 5. It reaches `needs_human` only after those rules have run, never by re-dispatching on its own.
7. **What consumers read.** The close reason and its message name the CI contexts. Consumers such as Unfold show an item whose head CI is not `success` as not ready for review, even before this is implemented.

Implementation is tracked as VIK-2209. Until it ships, rule 7 is the only part in effect, and ADR-0070's worker gate remains the only gate.

### Consequences

* Good, because a pull request in `awaiting_review` passed CI on the commit a person merges.
* Good, because a cancelled run or a red base branch costs one CI re-run, not a model-paid fix round.
* Good, because the writer's fix round now gets the forge's failing checks, not only the worker's.
* Bad, because a Shift on a slow CI stays open longer and holds its budget while it waits. The wait timeout bounds that.
* Bad, because ploegd now needs a re-run call per supported forge, and a provider that cannot re-run sends cancelled CI straight to `needs_human`.
* Bad, because cancellation is recognized from a forge-specific description on Forgejo. A Forgejo release that changes the wording turns cancelled runs into writer failures until the provider is updated.

### Confirmation

Once VIK-2209 ships, `go test ./pkg/shiftengine/ ./pkg/store/ ./pkg/provider/...` in CI (`mise run verify`):

* `TestRedForgeCIOpensAFixRound`, `TestCancelledForgeCIReRunsOnceThenNeedsAPerson`, `TestFailingBaseContextIsNotTheWritersToFix`, `TestPendingForgeCIHoldsTheShiftUntilTheTimeout`, `TestGreenForgeCIAwaitsReview` and `TestNoForgeStatusesAwaitReview` in `pkg/shiftengine/forge_ci_gate_test.go`;
* `TestAnAwaitingReviewItemWhoseCIWentRedLeavesReview` in `pkg/store/forge_ci_gate_test.go`;
* the Forgejo and GitHub provider tests that classify a cancelled context.

ADR-0070's confirmation tests keep passing. In production, `ploeg_awaiting_review_with_unsuccessful_ci` (the number of `awaiting_review` items whose head CI is not `success`) stays at 0.

## Pros and Cons of the Options

### Keep the worker-only gate and only show red CI in consumers

* Good, because nothing changes in ploegd.
* Bad, because `awaiting_review` keeps asking a person to review red CI, and every consumer has to work around it.

### Gate on the forge's CI and treat every red status as the writer's failure

* Good, because it is the simplest rule.
* Bad, because a cancelled run or a red base branch pays for fix rounds the writer cannot win, then reaches a person anyway, later and at greater cost.

## Re-evaluation triggers

* Forgejo exposes a distinct `cancelled` commit status state or a job-level API, so cancellation no longer has to be recognized from a description.
* A route's CI regularly takes longer than its wait timeout, so more than five Work Items a month close as `ci_not_finished`.
* More than five Work Items a month close as `ci_not_passed` because the base branch was red. That calls for gating dispatch on a green base instead.
* A route needs its pull requests reviewed while its CI cannot run.

## More Information

* Supersedes [ADR-0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md), whose worker verification rules this keeps.
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) is the fix loop, [ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) supplies the head commit.
* Board: VIK-2209 (implementation), VIK-2200 (the Work Item that showed the gap).
* 2026-10-18, implementation (VIK-2209): the gate is `gateOnForgeCI` in `pkg/shiftengine/forge_ci_gate.go`. A held Shift stays open and the sweeper's next evaluation reads the CI again; its wait and its one re-run are kept per Shift and head commit in `forge_ci_gates`, so a new head starts both again. Rule 6 runs in the review reconcile (`ReviewWatch.gateOnForgeCI` in `pkg/shiftengine/review.go`). Where the build departs from the text above: only the GitHub provider can re-run CI (the failed and cancelled jobs of the commit's workflow runs), so Forgejo and GitLab take rule 5's no-re-run path straight to `ci_not_passed`; the writer's briefing names each failing check with its target URL and no log tail, because no provider serves one; under rule 6 a check the writer could fix also ends at `ci_not_passed`, because the Shift that wrote the pull request has closed and no fix round can run without a new dispatch; a Work Item that reads a pull request someone else opened ([ADR-0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md)) is not gated, since nothing of Ploeg's can fix its CI. The base commit is the one each forge names for the pull request: `base.sha` on Forgejo and GitHub, `diff_refs.base_sha` on GitLab, whose provider marks a `canceled` job cancelled.
