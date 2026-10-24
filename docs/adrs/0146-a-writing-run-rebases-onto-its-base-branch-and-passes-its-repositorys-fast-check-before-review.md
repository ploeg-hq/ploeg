---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A writing Run rebases onto its base branch and passes its repository's fast check before review

## Context and Problem Statement

The owner runs many writing Runs in parallel on the same repositories. Each opens a pull request from a branch cut at the moment the Run cloned, and the base branch keeps moving while the Run works and while the pull request waits for review. Pull requests reach `awaiting_review` already conflicting with their base, and the owner resolves the conflicts by hand, one pull request at a time.

Nothing in a Run's procedure brings its branch up to date. The writer's delivery contract says to branch from the base and push, and the worker's checks ([ADR-0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md), [ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md)) verify the pushed commit, not whether it merges. The review reconcile already records a conflict on a pull request in review ([ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md), Show), but no part of Ploeg acts on it.

When does a Run bring its branch up to date with its base, who resolves a conflict, what check proves the result still works, and what happens when a conflict cannot be resolved?

## Decision Drivers

* A pull request in `awaiting_review` should merge into its base. A reviewer's time on a pull request that cannot merge is wasted.
* A conflict that needs a choice between two behaviours is a decision for a person ([ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md)). It must reach one as `needs_human` with the conflicting paths named, not as a red review.
* What Ploeg reports comes from the forge and from its own git work, never from the agent's claim ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)).
* Only the Run's own branch may be rewritten, and never over a push someone else made in the meantime ([ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0013](0013-push-rights-are-minted-per-run.md)).
* The check after a rebase must take seconds to a few minutes. The full verification and the forge's CI still run afterwards.
* A repository that declares no fast check must keep working.
* Code from the agent's branch must never run where it can reach the credential the worker pushes with ([ADR-0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md)).

## Considered Options

* The writer rebases inside the Run, the worker checks the result and rebases a clean branch itself, and a confirmed conflict in review queues a rebase Follow-Up
* Only the worker rebases, mechanically, after the agent exits
* Merge the base into the branch instead of rebasing
* File every conflict as a ticket, as ADR-0040 proposes for any pull request

## Decision Outcome

Chosen option: the first, because the agent is the only party that can resolve a conflict with judgement, the worker is the only party whose observation Ploeg trusts, and the review reconcile already detects the conflicts that appear after the hand-off.

1. **The fast check is declared per repository.** A target or a tracker project in ploegd's configuration sets `fastCheck:`, a list of shell command lines run in order from the repository root. ploegd refuses to boot when two routes give one repository different fast checks. ploegd sends the fast check of the Work Item's repository on the claim of a writing Run only (`claimResponse.fastCheck`). It is distinct from the executor's `verify` commands, which stay the worker's full verification.
2. **The writer rebases before its last push.** The delivery contract tells every writer to fetch the base branch, rebase onto it, resolve each conflict so that both sides' intent survives, regenerate generated files with the repository's generator, renumber a record or migration whose number the base has since used, run the fast check (the script `$PLOEG_FAST_CHECK_SCRIPT` when the repository declares one) and the checks, and push with `--force-with-lease` to its own branch. When a conflict needs a choice between two behaviours, the writer aborts the rebase, pushes nothing more, names the conflicting paths and exits non-zero.
3. **The worker checks every pull request against its base before the hand-off.** After a writing Run opened or updated a pull request, and before the worker's verification, the worker fetches the pull request's head and its base branch, as the forge names them, into a git directory private to the worker:
   * **Current.** The head contains the base's head. Nothing changes.
   * **Behind and clean.** The worker rebases the head onto the base. When the repository declares a fast check, it runs on the rebased tree, which carries no repository metadata, so nothing the check does can reach the push. Only when every command passed does the worker push the rebased commit with a lease on the head it read. The verification and the forge CI gate then run on the new head.
   * **No fast check declared.** The worker pushes the clean rebase and records the fast check as skipped.
   * **Conflicted.** The rebase stops on conflicts the agent left. The worker aborts it, pushes nothing, and the Run ends `stuck` with the conflicting paths in its summary and stuck reason. The Shift closes and the Work Item goes to `needs_human`, never to `awaiting_review`.
   * **Fast check failed.** The rebase was clean, but the fast check failed on the rebased tree. The worker pushes nothing, and the Run ends `stuck` naming the failing command.
   * **Unchecked.** The forge or git could not be read, the branch moved meanwhile, or the push lost its lease. The outcome is left as it was and the reason is recorded; the later gates still apply.

   Each result is written under "Ploeg base sync" in the Run's findings, which reach the pull request.
4. **A conflict after the hand-off is rebased by a Follow-Up.** The review reconcile confirms a conflict with ADR-0040's two-poll rule. Each time a pull request in `awaiting_review` enters `conflicted`, ploegd hands it to the Team that owns the branch. A Team that sets `forgeFollowUps.rebaseOnConflict` gets a rebase Follow-Up on the same branch, built like a repair Follow-Up: one open per pull request, and capped together with failed-check repairs by `maxRepairs`. Its Run follows rules 2 and 3. A Team without the setting keeps today's behaviour, and the conflict stays visible on the review lane.
5. **Only Ploeg's branches.** Rules 3 and 4 act only on a branch a Ploeg Shift worked. A pull request someone else opened ([ADR-0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md)) is never rebased, and ADR-0040 keeps "merge, never rebase" for a person's branch.

Rebasing rather than merging keeps the pull request a short series of the Run's own commits on top of the base, which is what the reviewer reads and what a squash merge lands. It rewrites the branch, so review comments on earlier commits show as outdated. That is accepted: a conflicting pull request cannot merge, and the forge compares the force-pushed head with the previous one.

### Consequences

* Good, because a pull request reaches `awaiting_review` only when it contained its base at the hand-off, so the owner stops resolving routine conflicts by hand.
* Good, because a conflict that needs a person reaches `needs_human` naming the paths, and no review round is spent on it.
* Good, because a clean rebase costs no model spend: the worker does it with git.
* Good, because a target without a fast check works unchanged, and the skip is visible on the pull request.
* Bad, because the worker clones the branch and its base with full history once per writing Run. Large repositories pay for it in time and disk.
* Bad, because the fast check runs on a tree without `.git`, so a check that reads git history cannot be a fast check.
* Bad, because a rebase Follow-Up costs model spend for every conflict after the hand-off. `maxRepairs` and the opt-in bound it.
* Bad, because the base can still move between the worker's push and the person's merge. Rule 4 covers the conflicting case; a base that moved without conflicting is left alone.

### Confirmation

`go test ./...` in CI (`mise run verify`):

* `pkg/worker/basesync_test.go`: `TestAWriterRebasesOntoAConflictingBaseAndPassesTheFastCheckBeforeReview`, `TestTheWorkerRebasesABranchTheBaseMovedUnderAndRunsTheFastCheckBeforePushing`, `TestAConflictTheAgentCouldNotResolveEndsTheRunForAPersonNamingThePaths`, `TestARebaseWhoseFastCheckFailsIsNotPushedAndReachesAPerson`, `TestARepositoryWithoutAFastCheckIsStillRebasedAndTheSkipIsRecorded` and `TestABranchThatAlreadyContainsItsBaseIsLeftAlone`, against a real git server;
* `pkg/worker/prompt_test.go`: `TestComposePrompt_WriterRebasesOntoTheBaseBeforeItsLastPush` and `TestFastCheckSection_NamesTheScriptAndItsCommandsOnlyWhenDeclared`;
* `pkg/config/fast_check_test.go`, and `pkg/httpapi/fast_check_test.go` for the claim;
* `TestReviewWatch_AConfirmedConflictAsksForARebaseOncePerConflict` and `TestReviewWatch_AnUnconfirmedConflictAsksForNothing` in `pkg/shiftengine/review_merge_test.go`, and `pkg/httpapi/rebase_conflict_test.go` for the Follow-Up.

In production, the worker logs `brought the pull request up to date with its base` with the result for every writing Run that delivered a pull request, and the count of Work Items whose merge state is `conflicted` while in `awaiting_review` should fall to the conflicts that appeared after the hand-off on Teams without `rebaseOnConflict`.

## Pros and Cons of the Options

### Only the worker rebases, mechanically, after the agent exits

* Good, because no prompt change is needed and the result is deterministic.
* Bad, because every real conflict then reaches a person, including the ones the agent could have resolved in the Run that wrote the change.

### Merge the base into the branch instead of rebasing

* Good, because nothing is force-pushed and review comments keep their commits.
* Bad, because every update adds a merge commit, the reviewer reads the base's changes in the branch's history, and repeated updates pile up merges in long-running reviews.

### File every conflict as a ticket, as ADR-0040 proposes

* Good, because the owner sees each conflict on the board.
* Bad, because a conflict on Ploeg's own branch, found before the hand-off, is part of the Run's work, not new work. A ticket adds a tracker round trip, an assignment and a new Shift to do what the Run can do before it exits.

## Re-evaluation triggers

* More than five Work Items a month end `stuck` with conflicting paths that a person resolves without a product decision. The writer's conflict instructions then need work, or the conflict-prone files a generator.
* A repository's fast check needs git history, so the export without `.git` blocks it.
* A route's branch protection requires branches to be up to date with the base before merging, so a base that moved without conflicting also needs a rebase after the hand-off.
* ADR-0040's conflict tickets are built, so the in-place mode for a person's pull request and the rebase Follow-Up must not both act on one pull request.

## More Information

* [ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md) supplies the two-poll conflict rule this reuses; its Detect, File and Resolve parts are unchanged.
* [ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) and ADR-0070 gate the commit this record produces.
* Board: VIK-2397.
