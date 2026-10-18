---
status: proposed
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A writing Run shows its change in a walkthrough a person can repeat

## Context and Problem Statement

A writing Run reports the problem it addressed and the solution it delivered in words ([ADR-0042](0042-a-writing-run-reports-the-problem-and-solution-a-reviewer-reads.md)). How to check the change sits under a folded `### For developers` heading. A person who approves the work without reading code, such as a product owner, a tester or an agency's client, gets a paragraph to trust and nothing to look at or follow.

The owner asked, on 2026-10-18, for something tangible a non-technical person can look at, and for a clear test path that person can follow as the human in the loop.

How does a writing Run show what it made, and give a person steps to check it themselves, without Ploeg trusting the agent's word for more than it does today?

## Decision Drivers

* R2: an agent reports and ploegd decides. Nothing in a walkthrough moves a Work Item, opens a Round or changes a budget.
* A screenshot must come from the change itself. A staged picture, or a terminal capture standing in for a screen that does not exist, misleads the person it is meant to help.
* The agent that wrote the change knows how to show it, so asking it costs no extra Run.
* A second agent, the reviewing Run, should check the pictures, so they are not only the writer's word.
* Additive and compatible: an older worker, a harness without a browser, or a Team that has not opted in keeps today's behaviour exactly.
* Ploeg serves what it stores to any authenticated consumer and shows nothing itself.

## Considered Options

* **The writing Run walks its own test path, screenshots each step, and reports the steps with the screenshots in the drop box; ploegd stores them, and the reviewing Run checks each picture**
* A free-text "how to test" section in the solution
* Ploeg starts the change in a preview environment and captures screenshots itself
* Ploeg uploads the screenshots to the pull request as attachments

## Decision Outcome

Chosen option: "**the writing Run walks its own test path, screenshots each step, and reports both; ploegd stores them, and the reviewing Run checks each picture**". The steps a person follows and the screenshots that show them are one record, so a picture is always the outcome of a step the agent actually took.

1. **Contract.** [outcomereport.v1](../contracts/outcomereport.v1.schema.json) gains an optional `walkthrough`:
   * `commit`: the commit the agent walked.
   * `steps[]`, at most 8: `{do, expect, screenshot?}`. `do` and `expect` are one plain sentence each, written for a person who does not read code, at most 280 characters. `screenshot` is a path relative to the workspace.
   * `noVisibleChange`, at most 600 characters: the plain reason a person cannot see the change anywhere and why that is fine. With it, `steps` may be empty.
2. **Prompt.** For a Team with `teams.<team>.walkthrough.enabled: true`, `worker.ComposePrompt` tells a writing Run to finish by walking the change as a person would and reporting the walkthrough. When the change shows anywhere, in the application, a dashboard or a timeline, the agent screenshots it there. When it shows nowhere, the agent writes `noVisibleChange` and takes no screenshot. It never screenshots a terminal or test output to stand in for a screen. A Run that cannot start what it changed leaves `screenshot` out of a step and says why in `expect`. The option is false by default, and without it the prompt is unchanged.
3. **Worker.** After the harness exits, the worker reads each listed screenshot. The path must resolve inside the workspace without following a symlink, the file must be PNG, JPEG or WebP by its magic bytes, at most 1 MiB, and at most 8 files per Run. A file that fails is dropped and its step keeps its text, with the reason recorded. The worker marks the walkthrough `stale` when `commit` is not the commit the Run pushed. Like findings and the problem and solution, the walkthrough always survives the adapter's own conclusion (`harness.MergeDropBox`).
4. **Store.** ploegd stores the steps on the writing Run and the image bytes in a table keyed by Run and step, as it stores context files ([`0038_work_item_context.sql`](../../pkg/store/migrations/0038_work_item_context.sql)). It drops a walkthrough from a reading Run. A fix Round's writer reports its own walkthrough, which replaces the earlier one as the newest.
5. **Operator API.** The operator API returns `walkthrough` on every writing Run: the steps, `stale`, `noVisibleChange`, and per step a screenshot URL. `GET /operator/v1/runs/{id}/walkthrough/{step}` serves the image to an authenticated consumer with its media type and `Content-Security-Policy: default-src 'none'`. Ploeg uploads nothing to the forge and shows nothing itself.
6. **Review.** When the Shift's writing Run reported a walkthrough, the worker writes its steps and screenshots into the reviewing Run's workspace and the prompt asks the reviewer to look at each screenshot. The reviewer raises a finding by id ([ADR-0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md)) when a screenshot does not show its step's `expect`, when a step has no screenshot and no reason, when the walkthrough is stale, or when a screenshot shows a credential or personal data. A reviewer whose model cannot read images reviews the steps as text and says so in its summary. A screenshot costs the reviewer about 1,500 input tokens, so at most about 12,000 per review.

### Consequences

* Good, because a person who does not read code sees what changed, and has steps to repeat that the agent has already walked once.
* Good, because the reviewing Run checks the pictures against the steps before a person spends time on them.
* Good, because a change with nothing to show says so plainly instead of showing a picture that means nothing.
* Bad, because the screenshots are still the agent's own capture. Accepted: the reviewer checks them, a stale walkthrough is marked, the steps let a person check for themselves, and the diff and the forge's CI stay the evidence ([ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md)).
* Bad, because a Run can only screenshot what it can start. That needs a headless browser in the harness image and an application that runs in the workspace, which is the deployment's concern. Without it, the walkthrough is steps without pictures.
* Bad, because a screenshot can show a secret or personal data on the screen, and the worker cannot scan an image for one as it scans learnings ([ADR-0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md)). Accepted: the prompt says to use demo data, the reviewer looks for it, images are served only to authenticated consumers and never posted to the forge.
* Bad, because walking the change adds minutes to a writing Run and image tokens to a review.
* Bad, because images grow the database. At 8 MiB per writing Run at most, this is accepted until object storage replaces bytea for context files too.

### Confirmation

`mise exec -- go test ./...`, run in CI and locally through `mise run verify`:

* A `harnesstest` property, `WritingRunWalkthroughSurvivesTheAdapter`, for every adapter.
* Worker tests: a path outside the workspace, a symlink, a file that is not an image by its bytes, a file over 1 MiB and a ninth file are each dropped with a reason; a commit other than the pushed one marks the walkthrough stale; the walkthrough survives every outcome branch.
* `TestComposePrompt_WriterReportsItsWalkthrough`: the instruction appears only for an opted-in Team and names `noVisibleChange` and the ban on terminal screenshots.
* `pkg/store` and `pkg/httpapi` tests: a writing Run's walkthrough reaches the operator detail, a reading Run's is dropped, the image route needs authentication, returns the stored media type and its policy header, and answers 404 for another Run's step.
* A reviewer prompt test: the walkthrough's files reach the reviewing Run's workspace and the prompt names the findings to raise.

## Pros and Cons of the Options

### A free-text "how to test" section in the solution

* Good, because it needs only a prompt change.
* Bad, because a consumer cannot tell steps from prose, so it cannot offer a checklist or match a picture to a step.
* Bad, because it gives nothing to look at.

### Ploeg starts the change in a preview environment and captures screenshots itself

* Good, because the screenshots would not be the agent's capture.
* Good, because a person could click through the change themselves.
* Bad, because Ploeg would have to know how to start every repository's application, which is the repository's and the deployment's business ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)).
* Bad, because it needs a cluster, a URL and a teardown per pull request. The owner chose screenshots first (2026-10-18), and this record leaves room for a preview environment later.

### Ploeg uploads the screenshots to the pull request as attachments

* Good, because reviewers on the forge would see them without a consumer.
* Bad, because GitHub has no API for pull request attachments, so it works on Forgejo and GitLab only.
* Bad, because a forge comment is readable by everyone who can read the repository, and a screenshot can show data the diff does not.
* Bad, because the last image-on-the-pull-request design ([ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md)) was rejected.

## Re-evaluation triggers

* Ten opted-in writing Runs have reported a walkthrough: check how many screenshots the reviewer rejected and how many walkthroughs were stale.
* A reviewer finds a credential or personal data in a screenshot.
* A consumer or a Team asks for a preview environment per pull request, or for the screenshots on the forge.
* Object storage replaces bytea for context files.

## More Information

* Ticket VIK-2228 (this record) and VIK-2229 (the review half), under epic VIK-2226.
* 2026-10-18: proposed, from the owner's request and answers the same day.
