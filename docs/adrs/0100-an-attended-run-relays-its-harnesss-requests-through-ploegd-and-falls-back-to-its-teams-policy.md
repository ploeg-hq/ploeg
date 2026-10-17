---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An attended Run relays its Harness's requests through ploegd and falls back to its Team's policy

## Context and Problem Statement

A Run is not steerable: its Harness adapter answers permission requests itself, refuses questions, and runs one prompt turn ([ADR-0006](0006-ahp-is-the-wrong-layer.md) accepted this as a consequence). The owner wants sessions executed by Ploeg's worker to stay interactive. How do a Harness's questions reach a person, and the person's answers and messages reach the Harness, without breaking crash-safety or the worker's network boundary?

## Decision Drivers

* Crash-safety never depends on an agent or a person (R2); a person who never answers must not hang a Run.
* The worker reaches only the model gateway, the forge and ploegd; nothing connects into the pod.
* Execution state lives in PostgreSQL (R6, [ADR-0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md)); commands are idempotent (R13).
* Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)); the interface belongs to the consumer.

## Considered Options

* The worker posts requests to ploegd and long-polls an inbox; consumers read and answer through the operator API; unanswered requests fall back to the Team's policy
* A consumer attaches to the agent's ACP session in the pod
* Steering only between Runs, as notes for the next Round (Unfold ADR-0023)

## Decision Outcome

Chosen option: "relay through ploegd with policy fallback", because it is the only option that is interactive within a Run, keeps the pod pull-only, and degrades to today's unattended behaviour.

* Only attended Runs relay. A Work Item is attended when it is submitted so through the operator work-item route (`POST /api/v1/operator/work-items`, [ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)); every Run its Shifts create, retries and re-opened Rounds included, inherits it. Operator Execution admissions and tracker Runs never are.
* The worker posts each permission request and question to ploegd and long-polls the Run's inbox for answers, messages, cancellation, pause and resume. ploegd stores every entry, redacted per [ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md), before acknowledging it.
* Consumers read a Run's interactions and send idempotent `answer`, `message`, `pause` and `resume` commands per Run. The first valid answer wins.
* An attended Run carries `autoApprove`, set at admission with `attended` and kept on retries, re-opened Rounds and requeues; it is part of the admission's fingerprint. With `autoApprove: true`, the adapter decides every permission request at once by the Team's permission mode, exactly as an unattended Run does, and relays none; questions, messages, pause and resume still reach the person. With `autoApprove: false` (the default), every permission request is relayed, unless the Team's mode is `deny_all`: a person cannot widen what the Team forbids. `autoApprove: true` without `attended` is refused.
* Every request has a deadline from the Team's answer timeout (default 10 min). An expired permission resolves by the Team's permission mode, an expired question is refused.
* A message is delivered at the next prompt-turn boundary, or interrupts the turn on request. One not acknowledged before the Run ends becomes an operator note for the next Run. [ADR-0104](0104-a-message-to-an-attended-run-reaches-it-at-the-next-prompt-turn-and-context-items-still-wait.md) narrows [ADR-0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md) for this: messages to attended Runs only; context items still reach only the next Run.
* When the Run expires, open requests expire and later answers are refused. Interactions never carry over to another Run.
* The ACP adapter implements this first. Other Harnesses report no interaction support and turn messages into notes.

### Consequences

* Good, because a person can approve, answer and steer a running agent from any consumer.
* Good, because an absent person costs at most the answer timeout, and the Run then behaves as an unattended one.
* Bad, because a pod waits idle while a person decides, costing compute and a slot under `maxRunning`.
* Bad, because it adds a long-poll per attended Run on ploegd and a new table that grows with interactions.

### Confirmation

* `go test ./pkg/store/ ./pkg/httpapi/` covers request, inbox order, first-answer-wins, replay, deadline expiry, cancel withdrawal, carry-over on Run end, refusal after Lease expiry and team scope, against both schemas in `docs/contracts/`.
* `go test ./pkg/harness/adapters/acp/` runs a scripted ACP agent through `ask` mode with `autoApprove` false (every request relayed) and true (decided by the Team's mode, none relayed), elicitation, a message at a turn boundary, an interrupting message and a storm while relaying.
* `go test ./pkg/worker/` kills the inbox mid-wait and checks that the Run finishes by policy.

## Pros and Cons of the Options

### Attach to the ACP session

* Good, because it gives full control with no relay.
* Bad, because it needs inbound access to a sandboxed pod and hands a consumer the worker's credential-bearing channel; ADR-0006 and Unfold ADR-0023 both reject it.

### Steering between Runs only

* Good, because it needs no new channel.
* Bad, because a running Run cannot be helped or stopped from asking, which the owner rejected on 2026-10-13.

## Re-evaluation triggers

* ACP v2 adds multi-client sessions or `session/resume` with replay, which could replace the relay.
* AHP's second gate in backlog #101 is met and a consumer serves AHP over Ploeg's relay.
* Attended Runs' median wait for an answer passes the answer timeout.
* Long-polling attended Runs measurably loads ploegd.

## More Information

* 2026-10-14 — The owner confirmed that a Team mode of `deny_all` refuses a permission request without relaying it, also when `autoApprove` is false: a person cannot widen what the Team forbids.
* OpenSpec change `interactive-runs`. Depends on ADR-0102 (proposed, OpenSpec change `submit-work-items`) for the attended flag. Relates to ADR-0006 (not superseded; one of its re-evaluation triggers has fired, see the change's Open Question 1), ADR-0068 (narrowed by [ADR-0104](0104-a-message-to-an-attended-run-reaches-it-at-the-next-prompt-turn-and-context-items-still-wait.md), not superseded; Open Question 8, resolved) and backlog #101.
* 2026-10-13: the owner rejected losing interactivity and decided that interaction runs inside Ploeg: Ploeg relays permission requests, questions and streamed events to the operator consumer and takes answers, messages, pause and resume back. This diverges from Unfold ADR-0023. Proposed.
* 2026-10-13 — The owner decided that "Approve tool use automatically" is a per-Run `autoApprove` flag: true decides permissions by the Team's permission mode without relaying them, false relays every permission request and lets the Team's mode decide when its deadline passes. Questions, messages and pause reach the person either way. Unattended Runs are unchanged.
