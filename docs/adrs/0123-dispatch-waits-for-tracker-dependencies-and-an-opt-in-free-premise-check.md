---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Dispatch waits for tracker dependencies and an opt-in free premise check, and a briefing names overlapping active work

## Context and Problem Statement

Agents never talk to each other directly. They coordinate through what Ploeg, the forge, the tracker and the knowledge graph record, and Ploeg assembles each Run's view of it. Three gaps sit before and between Work Items:

1. **A false or unclear premise costs a writing Round.** In VIK-573 ([ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md)) the Work Item asked to extend an end-to-end test that did not exist, and a paid writer spent a Round finding that out. ADR-0036 proposed a premise check (T0) but did not build it.
2. **Work that waits for other work starts anyway.** A tracker can say "blocked by" or "follows", and Ploeg ignored it. A writer then builds on a schema or an API another Work Item has not delivered yet.
3. **Two Shifts in one repository do not know about each other.** Each writer finds out about the other's change only as a merge conflict.

On 2026-10-18 the owner delegated these decisions ("fix it all, get to the best result"). The constraint that came with it: no paid model spend for this. The only model the premise check may call is one the gateway prices at zero, such as the homelab's Hetzner-hosted Qwen.

## Decision Drivers

* A check that runs on every Work Item must cost nothing, or it costs more than the Rounds it saves at a few bounces a month (ADR-0036's volume argument).
* "Never a paid model" has to hold even when the configuration is wrong. A gateway alias with a paid fallback, or a model renamed to a paid one, must not get through.
* The tracker owns intent and acceptance criteria, so the tracker's own relation decides whether a Work Item waits. Ploeg holds only its newest reading, with when it read it.
* The webhook fast path must not wait for a model call.
* A person who answers the check's question stays in charge. The check must never be able to keep a Work Item from a person who says go.
* Ploeg knows no consumer. What a person needs to see goes into the operator contract.

## Considered Options

* **A**: a ploegd classifier call on a free gateway model before the first Shift opens, a tracker-dependency hold on the same gate, and a store read of other live Shifts in the same repository that a briefing calls
* **B**: ADR-0036's T0 as written, a reading Role with `preflight: true` in a Round before the writer
* **C**: build none of it; leave false premises to the writer's `stuck` and dependencies to people

## Decision Outcome

Chosen option: **A**. A free classifier call costs one gateway round trip and no money. A Role Run costs a pod, a clone and a per-Run cap, and would be paid on a paid Team model. The dependency hold and active-work read reuse what Ploeg already records: tracker relations, pull request paths and live Shifts.

### 1. Premise check

* **Opt-in per Team.** `teams.<name>.premiseCheck.model` in the `PLOEG_CONFIG` file (chart `config.teams.<name>.premiseCheck`) names one gateway model. A Team without it is not checked. This applies to Work Items a person assigned or admitted (`origin: assignment`), never to Follow-Ups or pull request reads.
* **When.** Before a Work Item's first Shift opens, from the sweeper only. At most three checks run per sweep. The webhook path never calls a model. It leaves the Work Item queued, and the next sweep checks it.
* **What it reads.** The Work Item's title and description and its target repository name, each between tags as untrusted data. It does not read the repository.
* **What it answers.** One JSON object: `proceed`, `ask` with the one question a person must answer, or `too_big` with a suggested split, plus a one-line reason. A reply of any other shape is a failed attempt.
* **Never a paid model.** Before every call ploegd reads the model group's deployments from LiteLLM `/model/info` and refuses unless every deployment states an input and an output price of exactly zero. It mints a key with `models: [<that model>]`, a US$0.01 budget, a five-minute expiry and `disable_fallbacks` in its metadata, sends `disable_fallbacks: true` with the call, and revokes the key afterwards. A reply whose `x-litellm-response-cost` is not zero, or whose `x-litellm-model-group` names another group, is refused and its verdict discarded. A model that is not free records the check as `skipped` at once, without a call.
* **Verdicts.** `proceed` opens the Shift. `ask` and `too_big` move the Work Item to `needs_human` with reason `premise check: <verdict>: <question>`, open no Shift and no Run, and post the question on the tracker task. A failed attempt is retried on the next sweep; after three the check is `skipped` and the Shift opens.
* **One check per Work Item.** Once a Work Item has a verdict it is not checked again. A person who assigns or requeues a parked Work Item sends it on: answering the question is the person's decision, not the classifier's.
* **Visible.** Every verdict is a `work_item.premise_checked` audit row. The operator API's Work Item carries `premiseCheck` with the verdict, question, reason, model, attempts and time.

### 2. Dependencies

* A `provider.DependencyReader` lists the items a tracker item waits for, with whether each is done. Vikunja reads the `blocked` and `follows` relations of `GET /tasks/{id}`. GitHub Issues reads `GET .../issues/{n}/dependencies/blocked_by`, where a closed issue is done. Other trackers hold nothing.
* Before a Work Item's first Shift opens, ploegd reads its dependencies, stores them in `work_item_dependencies` and holds the Shift while any is not done. While it waits, the sweep reads them again every `PLOEG_DEPENDENCY_RECHECK` (default five minutes). The hold ends when the tracker marks each one done. Ploeg's own state for the other Work Item does not count, because Ploeg never decides that work is done.
* A tracker read that fails keeps the last reading. A Work Item that was never read is not held, so a tracker outage stops nothing new.
* The operator API's Work Item lists its open dependencies under `waitingFor`, each with the tracker id and title and Ploeg's Work Item id when Ploeg has one. `PloegQueueStalled` does not count a Work Item held this way. `PLOEG_DEPENDENCY_HOLD=false` switches the hold off.

### 3. Active-work awareness

* Each live Shift's repository is its Work Item's Work Target. Its touched paths are the paths its open pull request changes at the captured head (`pull_request_paths`, from the pushed diff). Its planned paths are the newest `plannedPaths` a Run of the Shift sent on a checkpoint: an additive, optional field of `checkpoint.v1`, at most 100 paths, kept on `shifts.planned_paths`. No worker sends it yet.
* `store.ActiveWork` lists the other live Shifts on the same repository, and `pkg/activework.For` compares their paths with the Work Item's own: equal paths, or a directory (ending in `/`) that holds the other. Its `Report.Markdown` renders the overlapping Shifts first, with the shared paths, and quotes tracker titles as data. The briefing calls it; this record does not decide where in the briefing it goes.

### Choices a person should re-check

These were decided under the owner's delegation, and each is cheap to change:

1. **Which Teams run the check.** It ships off for every Team. ADR-0036's owner decision 3 put it on for `bronze`; that is a homelab configuration change after the release (`premiseCheck.model: hetzner-qwen3-8-27b`).
2. **The classifier sees only the Work Item text.** It cannot catch VIK-573's kind of false premise, a file or test that does not exist, without a repository index. Feeding it the briefing's repository facts is the next step, and is not built.
3. **Fail open.** Three failed attempts, a model that is not free, or a charged reply skip the check and dispatch goes on. The alternative is holding work whenever the free endpoint is down (Hetzner's experimental inference allows 10 requests a minute).
4. **A requeue skips the check.** A person who reassigns a parked Work Item is trusted to have answered, even if the description did not change.
5. **"Follows" holds like "blocked by"** on Vikunja. Only the tracker's done flag ends the hold, so a dependency Ploeg delivered but nobody closed in the tracker still holds.
6. **The parked question lives on the premise check, not on ADR-0121's questions.** ADR-0121 (in progress) parks a Shift with close reason `question_for_person` when a Run asks a person, keeps the question on that Run, and briefs the next Shift with the person's answer by question id. The premise check asks before any Shift or Run exists, so its question is stored on `work_item_premise_checks`, carried in the `needs_human` reason, the tracker comment and the operator API's `premiseCheck`, and answered by assigning the Work Item again. The shape matches (one question to a person, with its text, a person answers, the Work Item goes on); once both are merged, the premise question should be listed and answered with ADR-0121's questions so a person answers in one place.
7. **Planned paths have no producer yet.** The field is there for a harness or a planning Role that knows its paths before it pushes.

### Consequences

* Good, because a stale or unclear Work Item on an opted-in Team reaches a person before any paid Round, and costs nothing.
* Good, because "never a paid model" is enforced three times, by price check, by key scope without fallbacks, and by refusing a charged reply, and holds when an alias is misconfigured.
* Good, because a tracker's own "blocked by" now means what a person meant by it, and the operator API shows what a Work Item waits for.
* Good, because a briefing can say "VIK-40 is changing `pkg/store/store.go` too" instead of a merge conflict saying it later.
* Bad, because an opted-in Team's first dispatch waits for the next sweep. Accepted: a sweep interval is seconds to a minute, and a writer Round is many minutes.
* Bad, because a classifier that reads only text will both miss false premises and ask needless questions. The false-bounce rate (ADR-0036's P1) is what decides whether a Team keeps it.
* Bad, because a held Work Item costs one tracker read per recheck interval while it waits.

### Confirmation

`go test ./...` in CI (`mise run verify`) covers it, with fakes only and no model call:

* `pkg/premise`: a check on a free model mints a key scoped to that model with `disable_fallbacks`, sends `disable_fallbacks`, and revokes the key; a paid, partly paid or missing model is never minted or called; a charged reply and a reply from another model group are refused; the reply parser is strict.
* `pkg/shiftengine` (`dispatch_gate_test.go`): the fast path never calls the check; a `proceed` opens the Shift; an `ask` parks at `needs_human` with no Shift and no Run, comments the question and audits the reason; a requeue proceeds without a second check; failures retry then skip; a sweep runs at most three checks; an open dependency holds the Shift until the tracker marks it done; a failed read never holds a Work Item that was never held; the switch turns the hold off.
* `pkg/store`: dependencies are replaced and name Ploeg's Work Item; active work lists only other live Shifts in the same repository with their paths; a dependency-held Work Item is not a stalled queue.
* `pkg/httpapi`: the operator Work Item read carries `waitingFor` and `premiseCheck` and validates against `operator-api.v1.schema.json` and `tracker-execution.v1.schema.json`.
* `pkg/provider/vikunja`, `pkg/provider/githubissues`, `pkg/litellm`, `pkg/config` and `pkg/activework` unit tests.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### B: ADR-0036's T0, a reading Role before the writer

* Good, because the Role reads the repository and could catch a missing test.
* Bad, because every check costs a pod start, a clone and a Run on the Team's model, which is a paid model on most Teams.
* Bad, because a Role's `stuck` closes the Shift through the normal path, so the question reaches a person as a stuck close rather than as a question.

### C: build nothing

* Good, because nothing changes.
* Bad, because the VIK-573 cost recurs, blocked work starts, and parallel Shifts meet only as merge conflicts.

## Re-evaluation triggers

* A Team's premise check bounces more than 20 % of its Work Items that a person then requeues unchanged, or bounces nothing in 30 Work Items: switch it off for that Team (ADR-0036's P1).
* The free endpoint's terms, price or rate limit change, or LiteLLM stops reporting `x-litellm-response-cost`.
* ADR-0121's typed questions land: move the premise question onto them.
* A worker or Role starts sending `plannedPaths`, or two active Shifts on one repository conflict in a month despite the briefing.
* A tracker without a done flag on its relations is connected.

## More Information

* [ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md): the ladder whose T0 this record decides.
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md): an agent reports and ploegd decides; the classifier's verdict is a closed enum ploegd acts on.
* [ADR-0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md): tracker parents read through the same relation seam.
* [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md): LiteLLM is the credential and metering seam.
* Code: `pkg/premise`, `pkg/shiftengine/dispatch_gate.go`, `pkg/activework`, `pkg/store/dependencies.go`, `pkg/store/premise.go`, `pkg/store/active_work.go`, migration `0061_dispatch_holds.sql`.
