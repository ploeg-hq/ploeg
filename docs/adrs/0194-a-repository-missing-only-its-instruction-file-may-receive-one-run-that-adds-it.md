---
status: proposed
date: 2026-11-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-03-31
---

# A repository missing only its instruction file may receive one kind of work: a Run whose only deliverable is a pull request that adds it

## Context and Problem Statement

The readiness gate ([ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md), [`pkg/target/readiness.go`](../../pkg/target/readiness.go)) refuses a Work Target that is archived, is a mirror, or has no `AGENTS.md` at the root of its base branch. It refuses at ingest (`target_not_ready` in route refusals), at operator Admission (`422 target_not_ready`, [ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)) and again at claim, where the Run ends `stuck` ([route a multi-repo board](../how-to/route-a-multi-repo-board.md#4-check-that-each-target-is-ready)). The instruction file is required for good reason: every writing Run is told to read it and run the verify command it names, and every reviewing Run judges the work against the base branch's copy ([ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md), [ADR-0163](0163-a-reviewer-reads-the-base-branchs-instruction-files-and-a-team-can-require-a-reviewer-unlike-its-writer.md)).

The gate also refuses the one piece of work that would make the repository ready. A person connecting a repository that has never had an instruction file must leave the onboarding flow, write one by hand, get it merged, and come back. With runtime Work Target registration ([ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md)) the rest of onboarding becomes self-serve; this is the step that stays manual, and most repositories fail readiness on exactly this.

How can a repository that is unready only because its instruction file is missing become ready through Ploeg, without loosening the gate for any other work?

## Decision Drivers

* **The gate keeps its meaning.** Ordinary work still needs a ready target. The exception must be one fixed deliverable, not a bypass a description can widen.
* **Quality.** An instruction file is read by every later Run. A wrong build command or an invented convention costs more than a missing file, so the file must be written from the repository's actual code and say where each command came from.
* **No change reaches a base branch without a person.** The pull request is reviewed like any other, and a reviewing Role never approves a change to an instruction file on its own (ADR-0030, ADR-0163).
* **Spend is authorised and capped** like any Shift ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [ADR-0162](0162-a-team-spends-at-most-its-cap-per-period-and-a-claim-over-it-starts-nothing.md)).
* **Untrusted input never starts it.** Tracker text and labels cannot ([ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md)); only a proven person can.

## Considered Options

* **A prepare-repository Work Item kind: one Run, one deliverable (a pull request adding `AGENTS.md`), started only by a proven person, accepted by the gate when the instruction file is the only failure**
* Ploeg commits a template instruction file, without a model
* A person adds the instruction file by hand before the repository can be used (status quo)
* Drop the instruction file from the readiness gate

## Decision Outcome

Chosen option: "**A prepare-repository Work Item kind**", because it is the only option that produces an instruction file written from the repository itself, through the same budget, Run, review and human merge as every other change, while the gate's exception is narrow enough to be checked mechanically: one Work Item kind, one readiness reason, one changed path.

Proposed; built on 2026-11-18 as described here and in the amendment of that date. Each section states the contract the implementation meets.

### 1. The kind

A Work Item gains `kind`: `change` (every existing Work Item, and the default) or `prepare_repository`. The kind is set at Admission, stored on the Work Item, part of the Admission fingerprint, and never changes.

A `prepare_repository` Work Item:

* has exactly one deliverable: a pull request against the target's base branch that **adds `AGENTS.md` at the repository root and changes no other path**. A short "fast check" section in that file, naming the quickest command that checks a change, is part of the same file, not a second deliverable;
* takes its title and description from Ploeg, not from the caller. The Admission refuses `title` and `description` for this kind (`400 invalid_admission`), so the deliverable cannot be widened by text;
* is limited to one open `prepare_repository` Work Item per target (`409 prepare_in_progress`, naming the open one).

### 2. Who starts it

Only an operator Admission, `POST /api/v1/operator/work-items` with `kind: "prepare_repository"`, a `team` and a `target`, by a consumer with `execute` permission and the Team in scope. The request must carry a verified person token ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)) **in every attribution mode**: asserted attribution answers `403 person_token_required` even where [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)'s mode is `asserted`.

Tracker intake never produces this kind. A tracker item routed to such a target is refused with `target_not_ready` exactly as today. The route refusal and the Admission refusal gain `prepareAvailable: true` when the only readiness reason is the missing instruction file, so a consumer can offer a person the prepare step.

### 3. The gate's exception, and why it ends the chicken-and-egg

`target.Gate` returns structured reasons instead of one sentence: `archived`, `mirror`, `instruction_file_missing`, plus `Unknown` when the forge could not be asked. The sentence Ploeg comments with is built from them, unchanged.

* **At Admission** a `prepare_repository` Work Item is accepted only when the reasons are exactly `[instruction_file_missing]`. A ready target answers `409 target_already_ready`; any other reason, alone or together with the missing file, answers `422 target_not_ready` as today. Archived repositories and mirrors stay refused for every kind.
* **At claim** the gate is asked again. If the reasons are still exactly `[instruction_file_missing]`, the Run starts. If the target became ready in the meantime (someone merged an instruction file), no Run starts: the Work Item is withdrawn with reason `already_prepared`, its hold is released and `work_item.prepare_not_needed` is written. Any other reason ends the Run `stuck` as today.
* **For every other kind** nothing changes: `instruction_file_missing` refuses `change` Work Items at ingest, Admission and claim.

ADR-0193 uses the same structured reasons: a runtime target whose only reason is `instruction_file_missing` may be registered, and reports `prepareAvailable: true`.

### 4. The Role and the brief

The Work Item runs on the named Team's plan, like any Admission. Its first writing Round uses the plan's writing Role; its reviewing Rounds use the plan's reviewing Roles. What differs is the brief.

The writer receives a Ploeg-owned brief, `prepare_repository`, kept in [`pkg/taskprompt`](../../pkg/taskprompt/prompt.go) beside the other prompts and covered by its golden tests, instead of a tracker or Admission description. It keeps the delivery contract first (ADR-0030) and asks the Run to:

* read the repository before writing: its layout, manifests and lockfiles, build files, task runners, CI workflows, README and contributing documents;
* write a root `AGENTS.md` that says what the project is, how to set it up, how to build, test and lint it, its conventions, and where things live, in at most about 150 lines;
* name only commands it found in the repository, and cite for each the file it came from (a `Makefile` target, a `package.json` script, a CI job);
* run each command the sandbox allows and list the rest under "Checks left to CI", as every writer does;
* state anything it could not determine as unknown, and never invent a command, a convention or a person;
* write no secret, host, or credential into the file;
* open the pull request with a body that lists every command and its source.

The writer's general rule that changing `AGENTS.md` "is outside the Work Item unless the Work Item asks for it" is satisfied: this Work Item asks for it, and for nothing else.

**Enforcement is the worker's, not the model's.** Before pushing or opening the pull request, the worker computes the branch's changed paths against the base and refuses delivery unless the set is exactly `{AGENTS.md}`, added. A Run that changed anything else ends `stuck` with `outside_deliverable`, names the paths, pushes nothing and opens nothing; the Work Item moves to `needs_human`. The forge proxy's existing branch fence ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)) still applies.

### 5. Budget

The Shift pool is the lowest of the Team plan's pool, the consumer's `maxBudgetUsd`, the Admission's `budgetUsd`, and the Team's prepare cap: `teams.<team>.prepareRepository.maxBudgetUsd`, default **USD 2.00**. The cap is a default, not a fixed price: a large repository may need it raised. Holds, settlement and the Team's period cap (ADR-0162) apply unchanged. The review loop ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)) does not run for this kind (section 6), so the pool pays for one writing Round and one pass of the plan's reviewing Rounds. Spend is what the gateway meters; nothing is estimated or invented.

### 6. Review and merge

The pull request is reviewed like any other, but **no reviewer's verdict reopens the writer**. Under ADR-0030 and ADR-0163 a reviewing Run treats any change to an instruction file as a finding and never approves it ([`pkg/taskprompt/prompt.go`](../../pkg/taskprompt/prompt.go) tells it to omit the verdict when the Work Item asked for the change). Under ADR-0017 a `request_changes` verdict reopens the plan's writing Round up to `maxFixRounds` (default 2). Together they would spend the pool on fix rounds whose only possible outcome is another change to the same file that the reviewer may again not approve, and then park the Work Item. So for `kind: prepare_repository`:

* the Shift runs one writing Round and the plan's reviewing Rounds once, as if the Team's `maxFixRounds` were 0, whatever the Team plan says;
* a reviewer's findings (wrong or uncited commands, invented conventions, missing sections) are posted on the pull request as usual, and a `request_changes` verdict is recorded with them but neither reopens the writer nor holds the pull request back from review;
* the Shift closes with the pull request at `awaiting_review` for a person, provided the one-path check passed and the forge's CI gate ([ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md)) lets it through.

A person who wants the file changed edits the branch, asks for changes in a pull request review, which reaches Ploeg as on any pull request, or closes it and starts a new prepare. A person merges, as for every Work Item; Ploeg never merges on its own.

### 7. After the merge

When the forge reports the pull request merged, the Work Item closes `done` as any delivered Work Item does, and Ploeg re-inspects the target at once instead of waiting for the next ingest. The gate's verdict turns ready, `target.readiness_changed` is written to the event feed and `/connections` reports the target ready, so a consumer can move the person on. If the pull request is closed unmerged, the Work Item closes as any other and a new `prepare_repository` Admission is allowed.

**Cancelled after the pull request opened.** When a person cancels a `prepare_repository` Work Item (`POST /api/v1/operator/work-items/{id}/cancel`) after its pull request opened, Ploeg closes that pull request through the forge's `PullRequestCloser` capability, as it does for a credential leak ([ADR-0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md)): the branch is kept, a comment says the prepare was cancelled and by whom, and `work_item.prepare_cancelled` is written. The target can then take a new prepare at once. If the forge cannot close it (the adapter lacks the capability or the call fails), the cancel still lands, the response names the pull request as still open, and a new prepare Admission answers `409 prepare_in_progress` naming that pull request until a person closes it. Closing was chosen over refusing a second prepare while the first pull request is open, because a cancelled Work Item should not leave a pull request that looks live and blocks the next attempt.

### Consequences

* Good, because a repository without an instruction file can be made ready from inside the onboarding flow, in one proven action, and the gate never has to be weakened for ordinary work.
* Good, because the file is written from the repository's own code, cites where each command came from, and is reviewed and merged by a person, so every later Run starts from instructions someone checked.
* Good, because the exception is checked by code at three points (Admission, claim, delivery) against one kind, one reason and one path, not by a model following a description.
* Bad, because preparing a repository costs a Run's spend, up to the cap, where a hand-written file costs nothing in model spend.
* Bad, because a model can still write a plausible but wrong file. Accepted: the citation rule and the reviewer's finding make errors checkable, and a person merges.
* Bad, because a reviewer's findings are not fixed by a Run: a person fixes them on the branch or starts a new prepare. Accepted, because a fix round could only change the same file again, and the reviewer may not approve that either.
* Bad, because the Work Item model gains a `kind` that every reader of Work Items, the operator API and the Admission fingerprint must carry.
* Neutral, because a repository whose instruction file lives elsewhere (only `CLAUDE.md`, or a nested `AGENTS.md`) is still not ready, and this kind adds a root `AGENTS.md` beside it. A Run may read the existing file as input.

### Confirmation

`mise run verify` runs them through `go test ./...` (the pull request-level tests use fakes or an `httptest` Forgejo; the worker's delivery test pushes through the forge proxy to a real `git-http-backend`):

* `pkg/target` tests: the gate returns structured reasons; `[instruction_file_missing]` alone allows a `prepare_repository` Work Item and nothing else; `archived` or `mirror`, alone or with the missing file, refuses every kind.
* `pkg/httpapi` Admission tests: `prepare_repository` accepted for a target missing only its instruction file; `409 target_already_ready` for a ready target; `422 target_not_ready` for an archived target and a mirror; `400 invalid_admission` with a `title` or `description`; `409 prepare_in_progress` for a second open one; `403 person_token_required` under both attribution modes; the pool capped at `prepareRepository.maxBudgetUsd`.
* A route-refusal test: a tracker item routed to such a target is refused `target_not_ready` with `prepareAvailable: true`, and no `prepare_repository` Work Item is created by any tracker path.
* A claim test: a target that became ready before claim withdraws the Work Item with `already_prepared` and starts no Run; one that became archived ends the Run `stuck`.
* A `pkg/worker` delivery test with an `httptest` forge: a branch adding only `AGENTS.md` opens a pull request; a branch that also changes another path, modifies an existing `AGENTS.md`, or adds a nested one ends `stuck` with `outside_deliverable` and pushes nothing.
* A `pkg/taskprompt` golden test for the `prepare_repository` brief.
* A `pkg/shiftengine` review-loop test: a `prepare_repository` Shift on a Team plan with `maxFixRounds: 2` whose reviewer returns `request_changes` opens no fix round, posts the findings, spends nothing further and settles at `awaiting_review`; the same verdict on a `change` Work Item still reopens the writer.
* A cancel test with an `httptest` forge: cancelling a `prepare_repository` Work Item whose pull request is open closes it without deleting the branch and writes `work_item.prepare_cancelled`, after which a new prepare Admission is accepted; with a forge that cannot close, the cancel lands and a new prepare Admission answers `409 prepare_in_progress` naming the open pull request.
* A merge test: the forge's merged event closes the Work Item `done`, re-inspects the target and writes `target.readiness_changed`.
* `go test ./internal/ledger/` keeps this record well-formed, and `go test ./internal/boundary/` keeps it free of consumer names.

## Pros and Cons of the Options

### Ploeg commits a template instruction file, without a model

* Good, because it is free, instant and deterministic.
* Bad, because a template cannot know the repository's build, test or lint commands, so it either leaves them blank or guesses, and every later Run is briefed with that.
* Bad, because it turns the gate into a checkbox: a repository passes readiness with a file that tells an agent nothing, which defeats the reason the gate exists.

### A person adds the instruction file by hand (status quo)

* Good, because no model is involved and the person knows their repository.
* Bad, because it is the one step of onboarding that leaves the flow, and many people do not know what the file should contain.
* Neutral, because it stays possible: a person can still add the file by hand, and a prepare Work Item already open is withdrawn as `already_prepared` when they do.

### Drop the instruction file from the readiness gate

* Good, because the chicken-and-egg disappears.
* Bad, because writing and reviewing Runs would then work without the commands and conventions ADR-0030 and ADR-0163 rely on, and the first sign would be a failed or wrongly reviewed change.

## Re-evaluation triggers

* ADR-0193 is accepted or rejected, which decides how often unprepared repositories reach Ploeg.
* More than one in five merged prepare pull requests needs a follow-up fix to a command it named, or a person reports an invented command.
* A prepare Work Item routinely runs into its cap, or settles under a tenth of it.
* The readiness gate gains another reason, or accepts another instruction file name, so the exception's one reason needs restating.
* A consumer asks to prepare other files (a CI workflow, a `CLAUDE.md`) through the same kind.

## More Information

* Related: [ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md), [ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md), [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md), [ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md), [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0163](0163-a-reviewer-reads-the-base-branchs-instruction-files-and-a-team-can-require-a-reviewer-unlike-its-writer.md), [ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md), [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md).
* Open questions for the owner:
  * The default cap. Recommendation: USD 2.00, raised per Team; revisit after the first ten prepare Runs with their settled spend.
  * Whether the Admission may carry a short person's note ("our tests need Postgres") for the writer. Recommendation: not at Admission; a person can add one through the existing notes route once the Work Item is live, which reaches the next Run as a finding while the one-path check still holds.
* 2026-11-17 — Amended after an independent review, before anything was built: section 6 now runs no fix round for this kind (a reviewer may never approve an instruction-file change, so ADR-0017's reopened writer could only spend the pool and park the Work Item); section 5 no longer applies the review loop's caps; section 7 says a cancel after the pull request opened closes it. Two Confirmation tests added.
* 2026-11-18 — Amended while building it, to the shape its first consumer was built against: the Teams read (`GET /api/v1/operator/teams`) reports each Team's cap as `prepareRepository: {maxBudgetUsd}`; a cancel of a prepare Work Item whose pull request had opened answers `cancellation.pullRequest: {url, state}`, `closed` when the forge closed it and `open` when it could not; `GET /api/v1/operator/work-items/{id}` of a prepare Work Item carries `pullRequest.state` read from the forge at the read (`open`, `merged`, `closed` or `unknown`), so a consumer sees when a pull request a cancel left open was closed and a new prepare may start; `409 prepare_in_progress` names the open Work Item in `error.workItemId` and a still-open pull request in `error.pullRequestUrl`; `422 target_not_ready` on an Admission carries `error.prepareAvailable`, and `GET /api/v1/operator/connections` carries `prepareAvailable` per repository, true only for a registered target. The worker enforces the one deliverable at its forge proxy, against the base branch it fetches from the forge itself, so a prepare Run needs `PLOEG_FORGE_TOKEN_ISOLATION=proxy` and ends `stuck` without it. Cancel withdraws a prepare Work Item in any state but `done` and `withdrawn`, so a pull request awaiting review can be cancelled and closed.
