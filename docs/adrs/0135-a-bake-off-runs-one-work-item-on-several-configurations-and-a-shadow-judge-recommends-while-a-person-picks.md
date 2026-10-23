---
status: proposed
date: 2026-10-23
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A bake-off runs one Work Item on several configurations and a shadow judge recommends while a person picks

## Context and Problem Statement

A Team picks a Harness and a model per Role from its policy ([ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [ADR-0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md)), and every Run records what it was configured with and what it cost ([ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md), [ADR-0097](0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md)). Yet the only way to compare two configurations on the same real Work Item is to admit it twice and read two Shifts by hand, which opens two pull requests on one tracker item and reserves no budget for the pair. Editors now ship this comparison for their own agents: VS Code 1.143's Compare Models runs one prompt on several models in parallel, has a judge model recommend one and shows time and tokens, but no cost, and its judge reports through the editor's own tools, so no outside host can take part. How does an Operator Consumer run one Work Item on several configurations at once, under one budget known before anything spends, and get a comparable, costed result with a recommendation that never decides?

## Decision Drivers

* Money is bounded before it is spent ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)): the whole bake-off is reserved as one amount, and an admission that cannot be covered is refused before any Run starts.
* A recommendation never acts ([ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md)): the judge records, a person chooses, and nothing is published until they do.
* One Executor for all work ([ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)): attempts are ordinary Runs that `ploeg-worker` claims, with the same Leases, budgets and recovery.
* One tracker item ends with one pull request ([ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md)): attempts produce Delivery Candidates, and only the chosen one enters the review loop.
* Ploeg publishes facts and consumers render them ([ADR-0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md)): cost, time, tokens and the verdict are stored per attempt; no score, grade or ranking is computed here.
* Off unless an admission asks for it.

## Considered Options

* A bake-off Shift: one admission names two or more configurations, the Shift runs one writing Run per configuration in parallel, a shadow judge Role reads every Outcome Report and records a recommendation, and the Shift waits for a person to pick the attempt that continues
* The consumer admits the Work Item once per configuration and compares the Shifts itself
* Sequential attempts: requeue the Work Item on another configuration after each one ends
* The judge picks the winner and the Shift continues without a person

## Decision Outcome

Chosen option: "**A bake-off Shift**", because it is the only option that reserves the budget as one amount, keeps one pull request per tracker item, and gives the judge every attempt to read in one place while leaving the choice to a person.

**1. The admission.** `POST /api/v1/operator/work-items` ([ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)) takes an optional `bakeOff` object: `attempts`, a list of two to six `{harness, model}` configurations, and an optional `judge` `{model}`. Each `harness` must pass ADR-0098's allowlist and each `model` ADR-0103's Role policy, with the same refusals and messages; two attempts may name the same configuration. `bakeOff` is part of the admission's fingerprint, so a replay never starts a second bake-off and a changed replay is a conflict. A tracker-originated Work Item is never a bake-off; a Team may not configure one in its plan. This is a consumer's deliberate, priced request.

**2. The reservation.** The Shift pool reserves, before any Run starts, the writing Role's per-Run authorization times the number of attempts plus the judge's reading authorization. When that exceeds the admission's `budgetUsd`, the consumer's `maxBudgetUsd` or the Team's cap, the admission answers `400 pool_above_limit` and records nothing. The Team read already exposes each Role's per-Run authorization, so a consumer can state the total to its person before admitting. The response carries `bakeOff.reservedUsd`.

**3. The attempts.** The Shift's first writing Round runs one Run per configuration, claimed by `ploeg-worker` like any other, each in its own workspace and on its own branch (`<shift branch>/attempt-<n>`), each briefed identically and each storing its configuration as ADR-0091 and ADR-0097 require. Each attempt ends with a Delivery Candidate and an Outcome Report and opens no pull request. An attempt that fails or is stopped ends like any Run; the bake-off continues with the attempts that finished. The Team's `maxRunning` still bounds parallelism, so attempts may queue behind one another.

**4. The judge.** When every attempt has ended, or the Shift's time cap passes, one Run of a shadow Role (ADR-0096, `shadow: true`) named `judge` reads every finished attempt's Outcome Report, diff and the Work Item's acceptance conditions, and records a verdict: the recommended attempt, a note per attempt of at most 1024 characters, and whether it found the attempts equivalent. The verdict is a stored fact on the Shift and nothing else: it opens no Round, publishes nothing, briefs no later Run except as item 5 says, and is paid from the pool as a reading Run. Without a `judge` the Shift records no verdict and still waits for the person. The judge's model comes from the admission, else from the `judge` Role's policy default.

**5. The choice.** The Work Item then waits as `needs_human` with Shift close reason `bake_off_choice`. `POST /api/v1/operator/work-items/{id}/bake-off/choose` takes `{attempt, commandId}`, requires execute permission and `X-Ploeg-Actor` ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)), and is audited as `bake_off.chosen` with the attempt, the verdict's recommendation and whether they agree. The chosen attempt's branch becomes the Shift branch and its Delivery Candidate enters the review loop as a normal first writing Round's result ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)). The unchosen attempts' workspaces are released and their branches deleted; their Runs, costs and notes stay on the record. A choice may name an attempt the judge did not recommend. A consumer that wants the chosen attempt improved with the others' ideas adds the verdict's notes as an operator note ([ADR-0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md)) and requeues; Ploeg adds no synthesis concept. When nobody chooses within the Team's pause bound ([ADR-0101](0101-a-paused-run-keeps-its-pod-and-session-for-a-bounded-time-and-is-never-retried-automatically.md)), the Shift ends `bake_off_unchosen`, nothing is published, and the unspent reservation is released.

**6. The facts.** Work Item detail and the Run list carry `bakeOff`: each attempt's number, configuration, Run, wall time, tokens, settled cost, and whether it was chosen; the judge's Run and verdict; the reservation and what was released. Consumers compare and render; Ploeg ranks nothing.

### Consequences

* Good, because a Team can answer "which configuration is worth its price on our work" on real Work Items, under one reserved budget, with the result and the cost on the record.
* Good, because the judge changes nothing it measures: the same Shift, with or without a judge, waits for the same choice.
* Good, because every attempt is an ordinary Run: Leases, recovery, transcript, limits and telemetry apply unchanged.
* Bad, because a bake-off costs the attempts' sum plus a judge: a two-attempt bake-off at bronze's writer price is about twice a normal Shift's first Round. Accepted, because the amount is reserved and refused before spend, and because the alternative, admitting twice by hand, costs the same without the reservation or the comparison.
* Bad, because the unchosen attempts' work is thrown away, and the judge's note is the only thing that carries over. Accepted, because merging two agents' diffs mechanically is not Ploeg's job and a requeue with the notes is the honest form of it.
* Bad, because parallel attempts take `maxRunning` slots from other Work Items of the Team. Accepted, because the Team's own bound applies and the consumer asked.

### Confirmation

* `go test ./...` in `.forgejo/workflows/on_pull_request.yml`: the admission tests refuse a `bakeOff` whose reservation exceeds `budgetUsd`, `maxBudgetUsd` or the Team cap with `pool_above_limit` and record nothing; a replay with the same `bakeOff` queues nothing; a changed `bakeOff` conflicts; the judge Role's Run is `shadow: true` and its verdict changes no Shift state; `bake-off/choose` on an unrecommended attempt succeeds and is audited with `agreesWithJudge: false`; an unchosen bake-off ends `bake_off_unchosen` after the pause bound and releases the reservation.
* The contract bundle: `docs/contracts/operator-api.v1.schema.json` describes `bakeOff` on `workItemAdmissionRequest`, `workItemAdmissionResponse` and Work Item detail, and the `bake-off/choose` route; the schema check in the same workflow fails when the shapes drift from the handlers.
* `go test ./internal/ledger/` keeps this record in the index.

## Pros and Cons of the Options

### The consumer admits the Work Item once per configuration

* Good, because it needs no change to Ploeg.
* Bad, because one tracker item gets several Shifts and several pull requests, the budget is reserved per Shift and never as a pair, and no judge can read the attempts together.

### Sequential attempts through requeue

* Good, because it exists today ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)).
* Bad, because the second attempt starts after the first ended, so the comparison takes twice the wall time, and the second attempt's briefing already knows the first.

### The judge picks the winner

* Good, because the Shift never waits for a person.
* Bad, because it makes a shadow reading decide what is published, which ADR-0096 forbids, and because a judge's verdict is not proof that an attempt is correct.

## Re-evaluation triggers

* Twenty bake-offs have ended with a choice; then compare how often the person's choice agreed with the judge.
* A Team asks for more than six attempts, for attempts that differ in prompt, skills or effort rather than Harness and model, or for a bake-off from a tracker-originated Work Item.
* A judge's reading costs more than a writing attempt on the same Work Item.
* A person asks for the unchosen attempts' diffs to be merged rather than described.
* An editor or protocol lets an outside host supply attempts or a judge to its own comparison with cost shown.
