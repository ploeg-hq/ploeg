---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A paused Run keeps its pod and session for a bounded time and is never retried automatically

## Context and Problem Statement

Pause exists only on Operator Executions, whose Executor is the consumer; ploegd records what the consumer reports. Once Ploeg's worker executes sessions, a person needs to pause a running Run and resume it. What does a pause do to the Harness, the Lease, the budget, and a pod that dies while paused?

## Decision Drivers

* A pause requires a confirmed stop before an explicit resume, and a deliberate stop survives every recovery boundary (R14, R3).
* Native Harness state is opaque and cannot be resumed in another pod (`docs/architecture.md` §9; backlog #67, #70).
* Crash-safety never waits for a person (R2); an unresolved Hold stays held (R15).

## Considered Options

* Pause cancels the current prompt turn and keeps the session in the same pod for a bounded time; past the bound the Run ends stuck
* Pause ends the Run and a resume starts a new Run from a pushed checkpoint ([ADR-0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md))
* No pause; cancel and requeue only (Unfold ADR-0023)

## Decision Outcome

Chosen option: "cancel the turn, keep the session for a bounded time", because it is the only option that resumes the same agent conversation, and the bound keeps it from holding a pod indefinitely.

* A pause is in effect when the worker confirms the agent stopped its turn (for ACP, the stop reason after `session/cancel`). Resume is accepted only after that confirmation and starts a new turn in the same session.
* While paused, the worker keeps renewing the Run (and a writing Run's Lease) and the Run keeps its Inference Account's Hold. Agent watchdogs stop; the harness timeout and the Job deadline keep counting.
* A pause longer than the Team's pause bound (default 30 min), measured from the stored confirmation, ends the Run `stuck` with reason `paused_too_long` (R4); so does the harness timeout or the Job deadline ending a paused Run. ploegd records the Outcome, puts a stop in the Run's inbox and settles the Hold through the existing settlement path. A paused Run whose pod dies ends with reason `paused_run_lost` and its Work Item goes to `needs_human`. Neither is retried automatically; a person requeues ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)).
* Harnesses that cannot pause refuse it with `not_interactive`.

### Consequences

* Good, because a short pause resumes with the agent's full context.
* Good, because every way a pause can end is explicit and needs a person, never a silent retry.
* Bad, because a pause costs an idle pod and its slot until resumed or bounded.
* Bad, because a long pause loses the agent's context; the next Run starts from the branch.

### Confirmation

* `go test ./pkg/store/ ./pkg/httpapi/` checks resume refused before confirmation, `paused_too_long` at the bound, `paused_run_lost` on Lease expiry and no retry in either case.
* `go test ./pkg/harness/adapters/acp/` pauses and resumes a scripted agent in one session and checks the watchdogs stop while paused.

## Pros and Cons of the Options

### End the Run and resume from a checkpoint

* Good, because no pod is held.
* Bad, because the agent's conversation is lost on every pause, and ADR-0075 is proposed and covers writing Runs only.

### No pause

* Good, because it adds nothing.
* Bad, because the owner rejected losing interactivity on 2026-10-13.

## Re-evaluation triggers

* agent-sandbox ships pause and resume of a sandbox, so a paused pod can release its node.
* An ACP profile supports `session/load` reliably across pods, so a pause could end the pod.
* More than a fifth of pauses in a month end `paused_too_long`.

## More Information

* OpenSpec change `interactive-runs`. Builds on [ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md).
* 2026-10-13: the owner decided that interactive sessions, including pause and resume, are supported in Ploeg, diverging from Unfold ADR-0023's "no pause in v1". Proposed.
