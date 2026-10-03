---
status: proposed
date: 2026-09-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A writing Run reports the problem and solution a reviewer reads

## Context and Problem Statement

A person who opens a Work Item that is Ready for review in Vloer sees the review checklist and the tracker brief. The brief is the task as written before any work was done. It can be long, it may be a research note, and it says nothing about what the agent actually changed. To learn what a pull request fixes and how, the person has to read the brief, then the pull request description, then the diff.

The writing Run knows both halves when it finishes: the problem it addressed and the change it made. Until now it had no way to hand that to Ploeg. [ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md) gave reading Runs the outcome drop box, and writers left it absent.

How does a person see, in plain words, the problem a Work Item addressed and the solution its pull request delivers?

## Decision Drivers

* R2: an agent reports and ploegd decides. Nothing an agent writes here may move a Work Item, open a Round or change a budget.
* The forge poll stays the only source of truth for whether a pull request exists (ADR-0018).
* Vloer does not read the forge. It shows what Ploeg reports.
* A fix Round changes the solution, so the account has to follow the latest writer.
* No extra Run and no extra spend for a summary the writer can give for free.

## Considered Options

* **The writing Run reports `problem` and `solution` in the drop box, Ploeg stores them per Run, and Vloer shows the latest**
* Vloer extracts a Problem section from the tracker brief
* Vloer reads the pull request description from the forge
* A separate summarizing Role runs after the review
* The writer puts both in its `summary`

## Decision Outcome

Chosen option: "**the writing Run reports `problem` and `solution` in the drop box, Ploeg stores them per Run, and Vloer shows the latest**", because the agent that did the work is the one that can say what it did, and the drop box already reaches Ploeg from every harness.

1. `outcomereport.v1` gains two optional markdown strings, `problem` and `solution`. `worker.ComposePrompt` tells every writing Run to write only these two to `PLOEG_OUTCOME_FILE` as its last step, and to start a pull request it opens with the same two sections.
2. `harness.MergeDropBox` keeps both whatever outcome the adapter concluded, as it keeps findings. `execbin` accepts a drop box without an outcome. `worker.resolveOutcome` carries both through every outcome branch.
3. ploegd stores them on `agent_runs` for a writing Run and drops them from a reading Run, as it drops a writer's verdict. They decide nothing: no state transition, Round or budget reads them.
4. The operator API returns `problem` and `solution` on every Run, each capped at 4096 characters.
5. Vloer shows a "Problem and solution" card directly under the Work Item title. It takes the newest writing Run that reported either, from the latest Shift when one there did, names the Role and Round that wrote it, and tells the reader to check it against the pull request. Without a report the card is absent and the brief stays the only description.

### Consequences

* Good, because a reviewer reads what was wrong and what changed before the checklist, without opening the brief or the forge.
* Good, because a fix Round replaces the account, so the card describes the pull request as it stands.
* Good, because the pull request description and the card carry the same text.
* Bad, because the account is the agent's own word and can overstate or misdescribe the change. Accepted: the card says who wrote it and to check it against the pull request, and the diff, the agent reviewer's findings and the checklist stay the evidence.
* Bad, because a harness or model that ignores the instruction leaves the card empty. Accepted: the page then looks as it did before, and the conformance property keeps every adapter able to carry the report.
* Bad, because every writing prompt grows by about ten lines.

### Confirmation

* `WritingRunProblemAndSolutionSurviveTheAdapter` in `pkg/harness/harnesstest` runs for `openhands`, `claudecode`, `execbin` and `acp`. It fails against `execbin` as it stood before this record.
* `TestMergeDropBox_ProblemAndSolutionSurviveTheAdapter`, the "problem and solution survive every branch" case of `TestResolveOutcome_Precedence` and `TestComposePrompt_WriterReportsItsProblemAndSolution` pin the worker half.
* `TestOutcome_WriterProblemAndSolutionReachTheOperatorDetail` in `pkg/httpapi` and `TestOperatorReadsAreScopedAndCredentialFree` in `pkg/store` pin storage, the reader rule and the published schema.
* In Vloer, `test/ploeg-view.test.mjs` pins the card's source and placement, and `test/ploeg.test.ts` pins that a Ploeg without the fields still parses.
* Gate: `go test ./...` and Vloer's `npm test` in CI, run locally through `mise run verify`.

## Pros and Cons of the Options

### Vloer extracts a Problem section from the tracker brief

* Good, because it needs no change to Ploeg.
* Bad, because it depends on how a person wrote the brief, and most briefs have no such section.
* Bad, because it gives no solution: the brief predates the work.

### Vloer reads the pull request description from the forge

* Good, because the description is what a human reviewer on the forge reads.
* Bad, because Vloer would need forge credentials and a forge client for every dialect Ploeg supports.
* Bad, because the description is free-form, so Vloer could not reliably find a problem and a solution in it.

### A separate summarizing Role runs after the review

* Good, because a reader that did not write the change may describe it more neutrally.
* Bad, because it costs a Run and model spend for every Work Item, and it knows less than the writer about why the change was made.

### The writer puts both in its `summary`

* Good, because `summary` already exists.
* Bad, because `summary` is one line, and `worker.resolveOutcome` replaces a writer's summary with its own when the forge poll decides the outcome.

## Re-evaluation triggers

* After the first 20 writer accounts: compare each against its diff. If more than a few misdescribe the change, have the reviewing Role confirm or correct the account in its findings.
* When Vloer gains read access to the forge.
* When a Team's writers regularly finish without an account.

## More Information

* Extends [ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md): the drop box now carries a writer's account as well as a reader's review, with the same merge rule.
* Related: [ADR-0011](0011-the-pull-request-is-the-blackboard.md) for findings, which stay a reading Run's contribution, and [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) for the verdict, which stays the only field an agent can use to influence what runs next.
* Contracts: [outcomereport.v1](../contracts/outcomereport.v1.schema.json) and the Run in [operator-api.v1](../contracts/operator-api.v1.schema.json).
* The Vloer card: [browser UI](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/vloer/docs/browser-ui.md).
