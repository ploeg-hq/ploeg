---
status: proposed
date: 2026-10-03
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Delivery facts come from the forge, never from the agent's outcome

## Context and Problem Statement

The 2026-10-02 code-quality review (findings EXEC-01 and AUTH-04) found that an agent can decide whether its Run delivered, and where. The path today:

* The OpenHands adapter returns the agent's drop box as the whole `OutcomeReport` (`pkg/harness/adapters/openhands`). Claude Code and ACP merge it with `harness.MergeDropBox`, which takes the agent's `outcome`, `links`, `checkpoint` and `failureReason` whenever the adapter concluded no outcome itself, which is the normal clean exit (`pkg/harness/dropbox.go`).
* `resolveOutcome` (`pkg/worker/worker.go`) lets the forge win only when it finds a new pull request. When the poll finds none, finds the pull request that existed before the Run, or fails, a valid agent outcome is kept with its own `links`. An agent can report `pr_opened` with a link it wrote.
* Both callers of `findOpenChangeRequest` log a lookup error and carry on with an empty URL, so a failed forge read means "no pull request". A failed pre-run read followed by a successful post-run read turns the previous Run's pull request into this Run's `pr_opened`.
* `pr_updated` is credited when a pull request existed before the Run and the harness exited cleanly. Nothing checks that the Run pushed.
* `handleOutcome` and `handleCheckpoint` (`pkg/httpapi/server.go`) check the shape only. A checkpoint takes any `branch` and `prUrl`.
* Publication (`pullRequest` in `pkg/shiftengine/publish.go`) and the review watch (`targets` in `pkg/shiftengine/review.go`) take the number from the last link that looks like a pull request, from any host and any repository, and pair it with the Work Item's own repository. A link to `…/other/repo/pulls/7` sends findings to pull request 7 of the Work Item's repository.
* `readyForReview` (`pkg/shiftengine/engine.go`) treats any writer's `pr_opened` or `pr_updated` as delivered.

So a confused or prompt-injected agent can mark a Work Item ready for review with no pull request, and can move Ploeg's comments onto an unrelated pull request. [ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md) already says an agent must not overwrite what the adapter concluded, but a clean exit concludes nothing, so the gap stays open.

Which fields of `OutcomeReport` and `Checkpoint` may an agent set, which are facts the worker or ploegd observes, and what happens when an observation fails?

## Decision Drivers

* The pull request is the record of delivery ([ADR-0011](0011-the-pull-request-is-the-blackboard.md)). A claim about it must come from reading it.
* An agent's narrative is useful and stays. Losing a review that happened is as bad as believing a pull request that did not.
* An unknown is not a no. A forge read that failed must not settle a Work Item either way ([ADR-0045](0045-keep-run-usage-and-merge-facts.md): unknown is null, never 0).
* Workers and ploegd roll out separately. An older worker's report must still settle.
* The sibling fix for VIK-1733 (PR #152, merged as `7bfd8a3`) already moved verification into the worker-owned `verification` field and discards any value an agent writes. This record applies the same rule to delivery.

## Considered Options

* **The worker alone sets delivery facts, from forge reads it binds to the Run; ploegd checks the binding**
* Keep trusting the agent's outcome and links, with validation in ploegd
* ploegd reads the forge again on every outcome

## Decision Outcome

Chosen option: "**the worker alone sets delivery facts**", because the worker already holds the Run's forge credential and the branch, reads the forge before and after the harness, and is the only party that saw both reads. ploegd keeps one cheap check, that the facts name the Work Item's own forge and repository, and reads the forge itself only where it already does.

1. **Ownership.** Every field of `OutcomeReport` and `Checkpoint` has one owner. The worker discards an agent-owned value in a worker-owned field before it builds the report, as it does for `verification` today.

   | Field | Owner | Why |
   | --- | --- | --- |
   | `summary` | agent; the worker writes it when the agent left none | Prose for a reader, decides nothing |
   | `findings`, `problem`, `solution` | agent | Narrative ([ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md), [ADR-0042](0042-a-writing-run-reports-the-problem-and-solution-a-reviewer-reads.md)) |
   | `verdict` | agent, reading Role only | The agent's judgement, closed enum ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)) |
   | `createdWorkItems` | agent, as proposals | ploegd applies the Team's limits; the agent dispatches nothing |
   | `stuckReason` | agent when the agent reported `stuck`; worker otherwise | The agent knows why it gave up |
   | `outcome`: `stuck`, `failed`, `no_change_needed` | agent may report; the worker may override | None of them asserts delivery |
   | `outcome`: `issue_updated`, `follow_up_created` | agent may report | Neither settles a Work Item as delivered; `follow_up_created` is kept only when `createdWorkItems` is not empty |
   | `outcome`: `pr_opened`, `pr_updated` | worker only | They assert delivery; the worker sets them from `delivery`, and an agent's value becomes `no_change_needed` |
   | `delivery` (new: forge, repository, number, URL, branch, head and base commit, observation) | worker only | Observed on the forge |
   | `links` | worker only | Publication reads delivery from it today; agent links are dropped, and an agent cites a URL in its prose |
   | `verification` | worker only | Precedent, PR #152 |
   | `failureReason` | worker and adapter only | The adapter runs inside the worker; the schema already says "never by the harness" |
   | `usage` | adapter envelope or gateway | Unchanged; not a delivery fact |
   | `checkpoint.*` (`phase`, `branch`, `prUrl`, `commit`, `nodeName`, `podUid`, `instructionFiles`) | worker only | Every field is a fact about the pod, the branch or the forge |

2. **Observation.** The worker reads the forge for the Run's branch before the harness starts and again after it stops. Each read returns the pull request's number, URL, head commit and base branch, or none, or an error. `delivery.observed` is:
   * `opened`: no pull request before, one after.
   * `updated`: one before and after, and the head commit changed during the Run.
   * `none`: no pull request after, or the same head before and after.
   * `unknown`: either read failed after three tries in 30 seconds.

   The writer's outcome follows: `opened` gives `pr_opened`, `updated` gives `pr_updated`, `none` keeps the agent's non-delivery outcome. A reading Run never gets a `pr_*` outcome; its `delivery` names the pull request it reviewed, `checkpoint.commit` the head it read, and its `verdict` and `findings` stay as reported.

3. **Binding.** `delivery` names the forge instance (the Work Item's `Target.Forge`, else the default), the repository (`owner/repo` from the Target), the pull request number and URL as the forge returned them, the Run's branch, the head commit and the base branch. The Run is bound by the run token the report arrives under. ploegd accepts `delivery` only when forge, repository and branch match the claimed Work Item and the branch Ploeg derived for it. It stores the facts with the Run, and `pullRequest`, the review watch and `readyForReview` read the stored number and `observed`, never `links`.

4. **Unknown is a state.** A pre-run read that fails ends the Run before the harness starts, as `failed` with a new failure reason `infra_forge`: nothing was spent and the Round retries without using the agent's attempt budget ([ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)). A post-run read that fails gives `delivery.observed: unknown` and outcome `stuck` with failure reason `infra_forge` and the stuck reason "Ploeg could not read the forge; whether this Run pushed or opened a pull request is unknown". The Work Item goes to `needs_human`. It is never "no pull request" and never success. A `delivery` that fails the binding check is stored as `unknown` with the mismatch as the reason, with the same result.

5. **Older workers.** A report without `delivery` comes from a worker older than this change. For one minor release ploegd accepts it as today, with two limits: a `pr_*` outcome counts only when one of its links points at the Work Item's forge host and repository, and every other link is ignored for publication and the review watch. ploegd counts such reports in a metric. The release after, ploegd treats a `pr_*` outcome without `delivery` as `unknown`. `delivery` is an optional field in `outcomereport.v1`, so the contract changes additively.

### Consequences

* Good, because no text an agent writes can mark a Work Item delivered or point a comment at another pull request.
* Good, because a forge outage shows as a Work Item someone must look at, instead of a quiet "no change needed" or a false "ready for review".
* Good, because `pr_updated` now means a push happened.
* Good, because the agent keeps everything it is good at reporting: prose, verdict, proposals.
* Bad, because a forge that is slow or down now stops Work Items in `needs_human` that would have settled before, and a pre-run failure costs a claim.
* Bad, because an agent that pushed to a branch with another name, or opened a pull request against a fork, gets no credit. That is the intended rule, and it will surprise someone once.
* Bad, because the worker, ploegd, the store, the contract and three readers in `pkg/shiftengine` change together, and the older-worker window adds a code path that must be removed on schedule.

### Confirmation

When this is implemented, `go test ./...` in `apps/ploeg`, run by `.forgejo/workflows/on_pull_request.yml`, covers it with these tests, each failing on today's code:

* `pkg/harness`: a drop box that reports `pr_opened`, `links`, a `checkpoint`, a `failureReason` or a `verification` loses all of them through `MergeDropBox` and through the OpenHands adapter, and keeps its `findings`, `verdict`, `problem`, `solution` and `createdWorkItems`.
* `pkg/worker` (`resolveOutcome` table): a fake drop box cannot produce `pr_opened` when the fake forge shows no new pull request; a pre-existing pull request with an unchanged head gives no `pr_updated`; a new pull request is `pr_opened` and a moved head is `pr_updated` with the forge's number, URL and head; a forge error before the Run is `failed`/`infra_forge` with no harness started; a forge error after the Run is `stuck`/`infra_forge` with `observed: unknown`; a reader's `request_changes` verdict and findings survive.
* `pkg/httpapi`: `handleOutcome` stores `delivery` whose forge, repository and branch match the claim, and turns a mismatch into `unknown` and `needs_human`; a checkpoint with another branch or a foreign `prUrl` is refused; a report without `delivery` follows the older-worker rule and is counted.
* `pkg/shiftengine`: publication and the review watch ignore a link to another host or repository and comment only on the stored pull request; `readyForReview` is false for a writer whose delivery is `unknown` or `none`.
* `pkg/worker` against an `httptest` Forgejo and GitLab: a real new and a real updated pull request are recognised end to end.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Keep trusting the agent's outcome and links, with validation in ploegd

* Good, because it is the smallest change: ploegd checks that a `pr_*` link names the Work Item's forge and repository.
* Bad, because a valid link to an existing pull request still lets an agent claim `pr_opened` or `pr_updated` for work it did not push.
* Bad, because it keeps the failed-read-means-no-pull-request path, which validation cannot see.

### ploegd reads the forge again on every outcome

* Good, because ploegd would trust nothing the worker sends, even from a compromised worker.
* Bad, because ploegd cannot tell `opened` from `updated` without the pre-run read, which only the worker made.
* Bad, because every outcome would cost a forge read on the control plane, with a forge credential ploegd minted for the worker ([ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md)).
* Neutral, because it can be added on top of the chosen option later if a worker itself becomes a threat.

## Re-evaluation triggers

* A worker runs where its own process can be tampered with by the agent (no separate harness sandbox): then ploegd must read the forge itself.
* `infra_forge` sends more than five Work Items a week to `needs_human`: tune the retry or retry the Round instead.
* A third forge provider is added, or a harness opens pull requests through a forge API the worker does not poll.
* The older-worker metric reads zero for two weeks: remove the compatibility path.

## More Information

* Technical story: VIK-1732, from the 2026-10-02 code-quality review (EXEC-01, AUTH-04).
* 2026-10-03: proposed.
* 2026-10-03: worker side implemented while the record is still proposed (VIK-1732, first slice): the drop box and every adapter drop the agent's `pr_*` outcome, links, checkpoint, failure reason, verification and delivery; the worker sends `delivery` and refuses fork pull requests. Two choices differ from the text above until the owner decides: a failed read before the harness ends the Run `failed` with the existing `infra_node` (the reviewer's fetch failure already does), not a new `infra_forge`; a failed read after it ends `stuck` with no failure reason. `pr_updated` without a moved head, ploegd's binding check, storage and the readers in `pkg/shiftengine` are the next slice.
* 2026-10-04: ploegd side implemented while the record is still proposed (VIK-1732, second slice). `ReportOutcome` binds `delivery` to the Run it finishes: forge (when the Work Item names one), repository, the Shift's branch, base, and a URL that is the named pull request of that repository. A record that does not match is stored as `unknown` with the mismatch as its reason. Migration 0039 stores the record in `agent_runs.delivery` and marks where it came from in `delivery_source` (`worker`, `mismatch`, `legacy`); rows from before the migration stay unmarked. A writer's `pr_opened` or `pr_updated` stands only when the stored record observed `opened` or `updated`; `pr_updated` over an unmoved head is stored as `no_change_needed`; a writer's `pr_*` or `no_change_needed` with an `unknown` record is stored as `stuck`, so the Work Item goes to `needs_human`. Links to any other pull request are dropped. A checkpoint naming another branch, or a pull request outside the Work Item's repository, is refused. Publication, the review watch, the tracker message and `readyForReview` read the stored record, and for a Run without one only links to the Work Item's repository. Older workers are counted in `ploeg_runs_without_observed_delivery_last_day`. Three choices differ from the text above until the owner decides: the older-worker rule checks the link's repository path, not its host, because ploegd does not hold each forge's host; the forge id is compared only when the Work Item's Target names one, because a worker's default forge setting and ploegd's are configured separately; the stuck Run still carries no `infra_forge` reason. A worker pinned to its environment repository (`PLOEG_TARGET_SOURCE=env`) whose repository differs from the Work Item's Target now ends `stuck`.
* Refines [ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md): the drop box carries narrative only, and its rule "Outcome and Summary fill only a gap the adapter left" no longer applies to `pr_opened` and `pr_updated`.
* Precedent: PR #152 (`7bfd8a3`, VIK-1733) made `verification` worker-owned.
* Related: [ADR-0011](0011-the-pull-request-is-the-blackboard.md), [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md), [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md), [ADR-0045](0045-keep-run-usage-and-merge-facts.md).
