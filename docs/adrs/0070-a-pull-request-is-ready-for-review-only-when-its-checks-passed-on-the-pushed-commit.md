---
status: accepted
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A pull request is ready for review only when its checks passed on the pushed commit

## Context and Problem Statement

[ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) has the worker run a team's configured checks after a writing Run opens or updates a pull request. It ran them in the agent's own checkout, after the agent finished, and a failed result changed nothing: the Work Item still reached `awaiting_review`. Two gaps followed from that:

* The checks described the agent's working tree, not what it pushed. An agent that pushed a broken commit and then fixed the file without committing got a pass, marked only as "dirty".
* `awaiting_review` asks a person to review and merge. A pull request whose checks failed, or never finished, reached that state the same way a passing one did.

The owner decided on 2026-10-03 that ready for review requires passing checks bound to the exact commit. This is one of the triggers ADR-0035 names. When is a Run's pull request ready for review, and what happens when it is not?

## Decision Drivers

* `awaiting_review` is a claim to a person that the work is worth their time. It must not cover failing code.
* The result has to describe the commit on the forge, the one a person merges, and nothing the agent left behind locally.
* A pull request that fails its checks is still work worth keeping. It gets fixed, not discarded.
* A Run without checks configured must keep working while routes adopt them.

## Considered Options

* Verify a fresh checkout of the pushed commit and hold back `awaiting_review` until the checks pass
* Keep verifying the agent's checkout and only label a failure on the pull request
* Leave verification to the forge's CI and wait for its status

## Decision Outcome

Chosen option: "Verify a fresh checkout of the pushed commit and hold back `awaiting_review` until the checks pass", because only the worker can run the checks right after the Run with the route's toolchain, and only a fresh checkout ties the result to the pushed commit.

1. **What is verified.** After a writing Run opens or updates a pull request, the worker clones the Run's branch into a new directory. The clone must be at the pull request's head as the forge reported it. The worker runs the checks there, never in the agent's checkout. It reads the branch again afterwards. If the clone is at another commit, or the branch moved while the checks ran, the verification is `incomplete` and says why. The record's `commit` is the commit the checks ran on.
2. **What is ready.** A Work Item reaches `awaiting_review` only when the last writing Run that opened or updated its pull request has no verification (no checks configured) or one that `passed`. The outcome, `pr_opened` or `pr_updated`, stays as the forge observed it ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)), and the pull request stays open.
3. **Where it goes instead.** In a configured plan with fix rounds left and budget for them, a failed or incomplete verification re-opens the writing Round, just like a reviewer's `request_changes` ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)). A reviewer's approval does not override it. The writer's verification findings are in its briefing. Otherwise the item goes to `needs_human`: the Shift closes as `checks_not_passed` when the plan allows no fix round, and keeps `fix_round_cap_reached` or the budget reason, with a message naming the checks, when the fix loop ran out. A plan-less team's Shift and a Run without a Shift also go to `needs_human`.

Not yet implemented (proposed, to be decided in later work):

* A route that has no checks configured still reaches `awaiting_review`. Under the owner's rule it should do so only when the route explicitly opts into "tests left to CI", and the item should then show that state as such.
* A push after the Run, such as a later human or reviewer push, does not yet invalidate the last writer's verification. Binding a reviewer's verdict and the close to the verified commit is the next step.
* The record does not yet carry the tree object or an identity for the check policy and toolchain.

### Consequences

* Good, because a pull request in `awaiting_review` passed its checks on the commit a person will merge.
* Good, because an agent cannot pass verification with uncommitted local changes, and a commit replaced mid-verification is reported, not trusted.
* Bad, because the worker clones the branch a second time, which costs time and forge traffic once per verified Run. A clone limited to 50 commits keeps that small.
* Bad, because a flaky check now sends a Run back to its writer or to a person. Fix the check, or leave it out of `verify`.

### Confirmation

`go test ./pkg/worker/ ./pkg/shiftengine/ ./pkg/store/` in CI (`mise run verify`):

* `TestVerificationRunsOnThePushedCommitNotOnTheAgentsCheckout`, `TestAPullRequestHeadThatIsNotTheBranchIsNotVerified` and `TestAPushWhileTheChecksRunLeavesTheVerificationIncomplete` in `pkg/worker/verify_pushed_test.go`;
* `TestFailedChecksSendTheWriterBackEvenWhenTheReviewerApproves`, `TestChecksThatDidNotPassNeedAPersonWhenNoFixRoundIsAllowed`, `TestFailedChecksStillNeedAPersonWhenTheFixRoundsRunOut`, `TestPassedChecksAwaitReview` and `TestUniform_FailedChecksNeedAPerson` in `pkg/shiftengine/checks_gate_test.go`;
* `TestAShiftlessPullRequestAwaitsReviewOnlyWhenItsChecksPassed` in `pkg/store/checks_gate_test.go`.

## Pros and Cons of the Options

### Keep verifying the agent's checkout and only label a failure

* Good, because nothing changes for a deployment.
* Bad, because the result can describe code that was never pushed, and a failing pull request still asks a person to merge it.

### Leave verification to the forge's CI

* Good, because CI already runs every gate with the repository's own setup.
* Bad, because Ploeg would wait on a status it does not control, which may never come for a repository without CI. It also could not send the writer back in the same Shift.

## Re-evaluation triggers

* A route needs its pull requests reviewed while its checks cannot run in the worker sandbox. That calls for the "tests left to CI" opt-in.
* A flaky check sends more than five Work Items a month to `needs_human`.
* The forge's own check status becomes available to Ploeg for every supported forge.

## More Information

* [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) introduced worker verification. [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) is the fix loop this reuses, and [ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) supplies the pull request's head commit.
* Board: VIK-1738.
