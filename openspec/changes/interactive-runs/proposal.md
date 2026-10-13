## Why

The owner decided on 2026-10-13 that sessions started from an operator consumer stay interactive once Ploeg's worker executes them, and rejected losing interactivity. Today no Run is interactive. The adapter seam is `Run(ctx, spec, env) → OutcomeReport` (`pkg/harness/adapter.go:36-40`) and `RunEnv` has no input channel (`:43-68`). The ACP adapter answers every permission request itself from the Team's permission mode (`pkg/harness/adapters/acp/client.go:65-103`, default `allow_always`, `permission.go:27-46`, storm caps `:79-82`), refuses `fs/*`, `terminal/*` and elicitation (`client.go:17-37`), sends one ACP prompt turn (`acp.go:244-258`), and streams at most 15 Checkpoints plus a 5-minute heartbeat (`acp.go:397-402`). A person cannot answer a question, approve a tool call, send a message to a running Run, or pause it.

## What Changes

- A Run MAY be **attended**: a submission through `POST /api/v1/operator/work-items` (the `submit-work-items` change) marks its Work Item so, and every Run its Shifts create, including retries and re-opened Rounds, inherits the flag. An attended Run's worker relays to ploegd the permission requests and questions its Harness raises, and takes back answers, messages, pause and resume. Unattended Runs, including all tracker-originated Runs, behave exactly as today.
- **Approve tool use automatically (owner decision 2026-10-13).** An attended Run carries `autoApprove`, set on the same submission and kept on retries and requeues. `true`: permission requests are decided at once by the Team's permission mode and never relayed; questions, messages and pause still reach the person. `false` (default): every permission request is relayed, and when its deadline passes the Team's permission mode decides. It is part of the submission's fingerprint.
- **Run API.** Worker routes under the Run's control capability to post an interaction request and to long-poll an inbox. The worker only pulls; nothing connects into the pod.
- **Store.** A new append-only migration with a per-Run interaction table. Every request, answer, message, pause and resume is recorded with its actor (R6, R13).
- **Operator API.** A consumer reads a Run's interactions and sends idempotent commands (`answer`, `message`, `pause`, `resume`) per Run. Events on `/operator/events` announce requests and resolutions.
- **Harness.** The ACP adapter gets an `ask` permission mode and an elicitation handler for attended Runs, queues messages for the next prompt turn (or interrupts the turn on request), and pauses by cancelling the turn and keeping the session.
- **Bounds.** Every wait has a deadline. An unanswered permission falls back to the Team's permission mode, and an unanswered question is refused as today. A pause past its bound ends the Run as `stuck` with a reason (R4), never as an automatic retry (R3). The ACP idle watchdog and prompt wall stop counting while a person is being waited on; the harness timeout and the Job deadline keep counting.
- The Teams read shows whether each Harness supports interaction.

## Capabilities

### New Capabilities

- `run-interaction`: the Run API and store side of the relay: requests, inbox, deadlines, and what happens on pod death, ploegd restart, retry and cancel.
- `acp-interaction`: what the ACP adapter does in an attended Run.

### Modified Capabilities

- `operator-api`: adds consumer reads and commands for an attended Run's interactions.

## Seams

Harness (`acp-interaction`), Run API and Store (`run-interaction`), and the operator read model. This crosses more than one seam on purpose: an interaction that one side can raise and no side can answer is a hung Run. Each seam has its own capability, and the tasks are split by seam so they can land in that order.

## Dependencies and relations

- Depends on `record-harness-per-run`: the Teams read and the per-Run Harness record say which Runs could be interactive.
- Independent of `select-harness-per-run`. Interaction works for any attended Run whose Harness supports it, including a Role's default. `select-harness-per-run` is how a person picks such a Harness per session.
- Depends on `submit-work-items` (ADR-0102, proposed) for Ploeg-executed operator sessions: the attended flag is a field of its `POST /api/v1/operator/work-items`, not of `AdmitOperatorExecution` (`POST /api/v1/operator/executions`), whose Run the consumer executes. The store, Run API and Harness parts can land first; nothing is attended until that route exists.
- Diverges from Unfold ADR-0023 (no pause in v1, interactive permission answers go away). The owner's decision of 2026-10-13 takes precedence; the Unfold side is changed separately.
- Narrows ADR-0068 (accepted): messages to attended ACP Runs reach the running Run at the next prompt turn, the option 0068 rejected. Context items and messages to every other Run keep 0068's rule. ADR-0104 (proposed) records the narrowing (design Decision 11, Open Question 8 resolved).

## Non-goals

- A user interface. The consumer renders interactions (`design.md` §2, ADR-0069).
- Speaking AHP or attaching a consumer to the agent's ACP session directly (ADR-0006, backlog #101).
- Implementing ACP `fs/*` or `terminal/*` (`client.go:23-34` reasons stand).
- Interaction for `openhands` (native), `claude-code`, `exec` or `claude-environment`. They degrade as specified.
- Interaction on unattended tracker Runs.
- Resuming a Harness session in a new pod after the pod died (backlog #67 tier 2, #70).
- Push delivery (SSE or webhooks) to consumers.

## Impact

- `pkg/harness/adapter.go` (`RunEnv` gains an optional interaction port), `pkg/harness/adapters/acp/{client.go,acp.go,permission.go}`, `pkg/worker`.
- `pkg/httpapi` (run and operator routes), `pkg/store` (migration, queries, sweeper), `cmd/ploegd/sweep.go`.
- `docs/contracts/run-api.v1.schema.json`, `docs/contracts/operator-api.v1.schema.json`, `docs/contracts/acp-profiles.md`, `docs/contracts/worker-control.md`.
- Backlog #101 (live run surface) is not implemented; its layer argument is kept, see design.
