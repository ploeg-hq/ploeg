---
status: proposed
date: 2026-10-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A reviewer reads the base branch's instruction files, and a Team can require a reviewer unlike its writer

## Context and Problem Statement

A reading Run checks out the branch it reviews, and the writing Run controlled that branch. Its harness then loads the repository's agent instruction files (`AGENTS.md`, `CLAUDE.md`, `.cursorrules`, `.mcp.json`, `.claude/`, `.agents/`, `.openhands/`) from the checkout, so a writer could add or edit the instructions the reviewer follows and steer the review of its own change (F-43, issue #353). The files were recorded and scanned for hidden characters, which caught nothing written in plain text. Nothing made the reviewer differ from the writer either: both Roles could run the same model on the same harness, so a weakness one has, the other shares. How does a reviewer stay independent of the work it judges?

## Decision Drivers

* The writer must not choose what the reviewer is told to do.
* The reviewer must still see the writer's change to those files, because the change is part of the work under review.
* A Team that wants a second opinion must be able to prove it at load, not trust a reading of values.
* A Team without the requirement must behave exactly as before.

## Considered Options

* **Restore the base branch's instruction files in the reviewer's working tree, name the changed ones in its briefing, and let a Team require different models and harnesses, checked when ploegd starts**
* Refuse to review a branch that changes an instruction file
* Strip every instruction file from the reviewer's checkout
* Compare only the default models of writer and reviewer

## Decision Outcome

Chosen option: "**Restore the base branch's instruction files, name them in the briefing, and check a Team's `independentReview` at start**", because the reviewer then follows the instructions a person merged, still judges the writer's change to them, and a Team's demand for a different model is refused at boot when the configuration could break it.

* Before checking out the branch under review, the worker records the base commit. After the checkout, every path the branch adds, changes or removes relative to that commit whose name or a directory of which is in the instruction set is put back to its base version in the working tree, or deleted where the base has none. So is every changed file an instruction symlink resolves to. The commit under review is untouched, so `git diff <base> HEAD -- <path>` still shows the writer's version.
* The paths it put back are named in a briefing entry from `instruction files` for the reviewer's Round, as data, with that command. The hidden-character scan and the checkpoint's instruction-file record then run on the restored tree, so they record what the harness reads.
* A Team plan may carry `independentReview: {model: bool, harness: bool}`. A reviewing Role is a reading Role, not a shadow and not a planner, in a Round after the plan's first writing Round. With `model`, ploegd refuses to start when any model a reviewing Role's managed inference policy admits is admitted by a writing Role's policy; with `harness`, when any Harness key a reviewing Role's admission may choose is one a writing Role's admission may choose. The refusal names the Team, both Roles and the shared model or Harness. A Role without a policy or a Harness configuration is refused, because nothing then proves the difference. `{}` is refused as asking for nothing. The field is omitted from a plan's encoding when unset, so the plan hash of every existing Team is unchanged.

### Consequences

* Good, because a writer can no longer give its reviewer instructions, and the reviewer is told where the writer tried.
* Good, because a Team's independence rule is checked against the same policies and Harness allowlists that admission uses, so an admission cannot pick its way around it.
* Bad, because the reviewer's working tree differs from the commit it reviews in those paths, and `git status` shows them modified. Accepted: the briefing says so and names the command that shows the writer's version.
* Bad, because source files, tests and documentation on the branch still reach the reviewer, and any of them can carry text aimed at a model. Accepted: they are the work under review; human merge stays the final control (AT4).
* Bad, because `independentReview` is opt-in, so a Team that leaves it out may still review a writer with the same model. Accepted: some Teams have one model, and the threat model records the residual.

### Confirmation

`go test ./pkg/worker/ -run 'TestReviewerReadsTheBaseBranchsInstructionFilesAndIsToldTheWriterChangedThem|TestReviewerOfABranchThatChangesNoInstructionFileReviewsAsBefore|TestPinBaseInstructionFiles'`, `go test ./pkg/httpapi/ -run IndependentReview`, `go test ./pkg/plan/ -run 'ReviewPairs|IndependentReview'` and `go test ./pkg/config/ -run TestLoad_PlanCarriesIndependentReview`, all run by `mise run verify`. `TestTheChartsIndependentReviewReachesPloegdAndHolds` reads the `executor-harness-selection` golden, so a chart change that drops the field fails it.

## Pros and Cons of the Options

### Refuse to review a branch that changes an instruction file

* Good, because nothing a writer wrote reaches the reviewer as instructions.
* Bad, because a legitimate change to `AGENTS.md` could never be reviewed by an agent, and a writer could stall its own review on purpose.

### Strip every instruction file from the reviewer's checkout

* Good, because it is simpler than restoring.
* Bad, because the reviewer loses the instructions a person merged, such as the repository's test commands and conventions.

### Compare only the default models of writer and reviewer

* Good, because it needs only the plan.
* Bad, because an admission may choose any model a Role's policy lists (ADR-0103), so two Roles with different defaults can still run the same model.

## Re-evaluation triggers

* A supported harness reads instructions from a path outside the instruction set, or from the user's home directory in the checkout.
* A reviewer approves a change whose instruction-file edit the briefing named and a person later finds it steered the review.
* A Team asks for independence by model family or provider rather than by model name.
* The worker gains a read-only checkout of the base branch beside the branch under review.

## More Information

* Source issue: [#353](https://github.com/ploeg-hq/ploeg/issues/353) (F-43). Related: F-26 ([#309](https://github.com/ploeg-hq/ploeg/issues/309)), F-28 ([#311](https://github.com/ploeg-hq/ploeg/issues/311)).
* 2026-10-28: proposed by the maintainer. It takes effect when the owner merges it into `development`.
