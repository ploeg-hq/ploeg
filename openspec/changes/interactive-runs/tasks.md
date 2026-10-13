## 0. Prerequisites

- [ ] 0.1 Confirm `record-harness-per-run` is merged and the owner answered design Open Questions 1, 3 and 6 (4, 8 and 9 were resolved on 2026-10-13)
- [ ] 0.4 Ratify or reject ADR-0100, ADR-0101 and ADR-0104; on accepting 0104, append a dated "Narrowed by ADR-0104" note to ADR-0068's More Information
- [ ] 0.2 Add the missing domain terms (Interaction, Attended Run, Run pause) to `docs/domain/model.yaml` and regenerate with `mise run domain`
- [ ] 0.3 Confirm `submit-work-items` (`POST /api/v1/operator/work-items`, ADR-0102) is merged before task 5.3; sections 1 to 4 may land before it

## 1. Store

- [ ] 1.1 Add the next numbered migration: `run_interactions` and `attended` and `auto_approve` flags on the Work Item and `agent_runs`; copy the flag in `OpenRound` and `ReopenRound` (`pkg/store/shift.go:130,295`) and test a retry and a requeue
- [ ] 1.2 Store requests, inbox entries, answers (first wins), deadlines and states; tests for order, replay and conflicts
- [ ] 1.3 Sweeper in `cmd/ploegd/sweep.go`: expire past-deadline requests by policy; end a Run paused past its bound with `paused_too_long` (Outcome, inbox stop, Hold settled); on Run expiry expire open requests, carry unacknowledged messages over as operator notes, and end a paused Run with `paused_run_lost` without retry; tests for each, including a ploegd restart mid-pause

## 2. Run API

- [ ] 2.1 `POST /api/v1/runs/{token}/interactions` and `GET /api/v1/runs/{token}/inbox` (long-poll ≤ 25 s), acknowledgement and pause confirmation, under the run control capability; schema edits in `docs/contracts/run-api.v1.schema.json` and `worker-control.md`
- [ ] 2.2 Redact and bound payloads with the ADR-0090 redactor on ingestion; test a masked secret
- [ ] 2.3 `PLOEG_RUN_INTERACTION` switch, default off

## 3. Harness

- [ ] 3.1 Add the optional `Interaction` port to `RunEnv` (`pkg/harness/adapter.go`); nil keeps today's behaviour; conformance kernel case for a nil port
- [ ] 3.2 ACP `ask` mode in `permission.go` for attended Runs with `autoApprove` false: every request relayed except under `deny_all`, `RequestPermission` waiting on its request context only, storm caps unchanged; with `autoApprove` true the Team's mode decides and nothing is relayed
- [ ] 3.3 ACP elicitation handler for attended Runs only
- [ ] 3.4 Turn loop: queued messages as further prompt turns, interrupting messages, pause by `session/cancel`, resume in the same session, watchdogs stopped while waiting
- [ ] 3.5 Scripted-agent tests in `pkg/harness/adapters/acp/` for each, including a storm while relaying, `autoApprove` true (no relay, questions still relayed) and false (a read request relayed)

## 4. Worker

- [ ] 4.1 Worker goroutine doing the HTTP for the port; tests that a dead inbox resolves by policy and that losing the Run's renewal (ctx cancellation, `pkg/harness/adapter.go:26-27`) stops the Harness
- [ ] 4.2 Transcript `stage` entries for requests and resolutions

## 5. Operator API

- [ ] 5.1 Read a Run's interactions; commands `answer`, `message`, `pause`, `resume` with R13 idempotency and team scope; events on `/operator/events`; schema edits in `docs/contracts/operator-api.v1.schema.json`
- [ ] 5.2 `not_interactive` refusals and message-to-operator-note for Harnesses without the port
- [ ] 5.3 `attended` and `autoApprove` on `POST /api/v1/operator/work-items` (both part of its R13 fingerprint, refused on `POST /api/v1/operator/executions`, `autoApprove: true` without `attended` refused); test a replay that changes `autoApprove` (conflict) and a requeue that keeps it; `interactive` per Harness on the Teams read

## 6. Docs and gates

- [ ] 6.1 Update `docs/contracts/acp-profiles.md`, `docs/architecture.md` §2 and §4, and the `client.go` package documentation that says interactivity never belongs in the pod
- [ ] G.1 Run `mise run verify` (gofmt, vet, build, `go test ./...`, `openspec validate --all --strict`, Helm lint, template and golden checks); paste the output in the pull request
- [ ] G.2 Run `mise run docs-check` (ADR ledger, contracts, generated configuration, domain); paste the output in the pull request
