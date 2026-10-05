---
status: proposed
date: 2026-10-05
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A stopped writing Run leaves a checkpoint its retry starts from

## Context and Problem Statement

[ADR-0019](0019-a-failed-writing-run-reopens-its-round.md) re-opens a Round when its writing Run fails, and the retry starts again. Today it starts from nothing:

* The worker deletes and re-clones the repository onto an `emptyDir` (`pkg/worker/worker.go:245-251`).
* The worker accepts a checkpoint field in its contract but does not populate it ([architecture](../architecture.md) §9).

In [Work Item 138](../research/2026-09-29-incident-work-item-138.md), Run 200 started with `briefing=0` and paid again to read the code Run 196 had already read. Its dirty tree, its plan and its last messages were lost with the pod.

What should a writing Run leave behind when it is stopped by a timeout, an idle kill, a failure, or a clean exit with no pull request, so that its retry starts where it ended?

## Decision Drivers

* **Paid work is not thrown away.** A retry is cheaper and likelier to finish from evidence than from zero.
* **Durable state lives only in Postgres and the forge,** never in a pod (R6, [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md)).
* **Only the writer may push** ([ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0013](0013-push-rights-are-minted-per-run.md)). The checkpoint must be written with the writer's own Lease and push right.
* **A checkpoint must not leak a secret.**
* **Harness-native state is not portable across harnesses** ([architecture](../architecture.md) §6), but the same harness and image may resume it.

## Considered Options

* The writer pushes a WIP ref and stores its last messages, and the retry is briefed from them
* Snapshot the workspace volume and restore it for the retry
* Share one workspace volume across a Shift's Runs
* Keep starting retries from zero

## Decision Outcome

Chosen option: "**The writer pushes a WIP ref and stores its last messages, and the retry is briefed from them**". It uses only Git and Postgres and the push right the writer already holds, and it works on every executor.

When a writing Run ends without a pull request (timeout, idle kill, harness failure, or exit 0 with no pull request), the worker does the following before it reports its Outcome:

1. Runs the existing leak scan on the working tree (`pkg/worker/worker.go`). Only a clean tree is checkpointed.
2. Commits the dirty tree and pushes it to `refs/ploeg/wip/<shift>/<run>`, a ref outside branch namespaces, so no pull request or CI run starts from it.
3. Reports a Checkpoint through the existing run API with:
   * the WIP ref and commit;
   * the last N agent messages, bounded in size;
   * the harness name and image.

   The worker stores them in the next migration.

The retry is briefed from the previous attempt's checkpoint through the context path of [ADR-0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md):
* it fetches the WIP ref;
* it receives the diff and the last messages as part of its briefing;
* when the harness and image match, it may also receive the harness session archive for a native resume.

WIP refs are deleted when the Shift closes.

### Consequences

* **Good:**
  * A retry starts from the previous attempt's diff and plan. On WI-138 that is an estimated 20–30 minutes and US$ 0,20–0,40 saved per retry.
  * A stopped Run's work becomes visible to a person, as a ref and messages on the Work Item.
  * Nothing new is stored outside Postgres and the forge.
* **Bad:**
  * The forge holds extra refs until the Shift closes. A failed cleanup leaves them behind; the sweep removes WIP refs of closed Shifts.
  * The worker spends a few seconds and a push at the end of a stopped Run. A Run killed with SIGKILL, or one that loses its node, leaves no checkpoint, and its retry still starts from zero.
  * A retry briefed with a failed attempt's plan can repeat its mistake. The briefing labels it as a failed attempt's evidence, not instructions.

### Confirmation

* **Worker.** A `pkg/worker` test, using an `httptest` run API and a local bare repository, proves:
  * a timed-out writing Run pushes `refs/ploeg/wip/<shift>/<run>`;
  * it reports a Checkpoint carrying the ref and the last messages;
  * a tree that fails the leak scan is not pushed.
* **Briefing.** A `pkg/shiftengine` test proves the retry's briefing carries the previous checkpoint's ref and messages.
* **Cleanup.** A sweep test proves the WIP refs of a closed Shift are deleted.
* **Deployed.** On the next stopped writing Run, `git ls-remote <repo> 'refs/ploeg/wip/*'` lists its ref, and the retry's sandbox log shows a non-zero briefing.

## Pros and Cons of the Options

### Snapshot the workspace volume and restore it for the retry

* Good, because the whole tree is preserved, caches included.
* Bad, because it needs CSI snapshots (Longhorn copies the data, and the clone is not ready at once) and a stateful volume per Run.
* Bad, because it is not available on every executor and leaves state outside Postgres and the forge.

### Share one workspace volume across a Shift's Runs

* Good, because nothing needs restoring.
* Bad, because a reader could write the writer's tree, which breaks ADR-0010's rule that readers never write the branch.

### Keep starting retries from zero

* Good, because nothing is added.
* Bad, because paid exploration is thrown away on every retry, as in WI-138.

## Re-evaluation triggers

* agent-sandbox ships a pause and resume or snapshot API for Sandboxes.
* A forge rejects or garbage-collects refs outside `refs/heads` and `refs/tags`.
* Retries briefed from a checkpoint fail more often than retries from zero, over 20 or more retries.

## More Information

* Evidence: [Substrate, language and Run bottlenecks](../research/2026-10-05-substrate-language-and-run-bottlenecks.md), §1 and §8. Also the [incident record](../research/2026-09-29-incident-work-item-138.md), tickets 5 and 6.
* Related: [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md), [ADR-0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md), and the [checkpoint contract](../contracts/checkpoint.v1.schema.json).
* 2026-10-05: proposed from the bottleneck spike.
