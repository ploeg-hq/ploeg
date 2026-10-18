## Context

[ADR-0118](../../../docs/adrs/0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md) retires the unmaintained OpenHands CLI and keeps OpenHands through the Agent Server API, the interface OpenHands names for orchestrators. Today the Harness seam has spawn-and-wait adapters (`openhands`, `claude-code`, `exec`, lifted by `harness.RunCommand`), a session-protocol adapter (`acp`, `pkg/harness/adapters/acp`) and a remote-dispatch adapter (`claude-environment`, `pkg/harness/adapters/claudeenv`). None drives an HTTP agent server. The worker owns the clone, the branch, credential isolation through loopback proxies ([ADR-0034](../../../docs/adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)), the transcript ([ADR-0090](../../../docs/adrs/0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)), the prompt start and limits ([ADR-0114](../../../docs/adrs/0114-each-run-records-the-limits-that-stop-its-prompt.md)) and the outcome drop-box ([ADR-0018](../../../docs/adrs/0018-the-outcome-drop-box-is-every-harnesss-return-path.md)).

API surface observed in `openhands-agent-server` 1.55.1 (`openhands/agent_server`): `python -m openhands.agent_server`; session API key from `OH_SESSION_API_KEYS_0` (legacy `SESSION_API_KEY`); `POST /api/conversations` (`StartConversationRequest`, 429 at capacity, `X-OpenHands-Observability-*` headers); `POST /api/conversations/{id}/run|pause|interrupt|confirmation_policy`; `DELETE /api/conversations/{id}`; `GET /api/conversations/{id}/events/search`; WebSocket `/sockets/events/{id}`. The exact request and event shapes are pinned in the first task, against the version the image installs.

## Goals / Non-Goals

**Goals:**
- A Harness that runs OpenHands on its maintained SDK, inside the Run's workload, with the same guarantees every Harness has: placeholders only, transcript, limits, drop-box outcome, Lease-based crash safety (R2).
- An adapter Ploeg maintains; the agent stays OpenHands'.

**Non-Goals:**
- Anything in the proposal's Non-goals: removing the CLI Harnesses, a shared or remote server, the server's own runtimes and git/file routes, building the image, qualification.

## Decisions

### D1. Implement `harness.Adapter` directly, beside `acp`

The server is a session protocol, like ACP: a long-lived process with a request/event conversation. The adapter implements `Adapter.Run` in a new package `pkg/harness/adapters/agentserver`, reusing the process launcher pattern from `acp` (process group, SIGTERM then SIGKILL, stderr tail for the stuck reason). *Alternative:* `CommandAdapter` with a wrapper script that drives the API; rejected because the transcript, confirmations and limits need the event stream inside the worker.

### D2. One server per Run, loopback, fresh key

The worker picks a free loopback port, generates a random session API key, starts the server with only the allowlisted environment ([worker-control](../../../docs/contracts/worker-control.md)) plus `OH_SESSION_API_KEYS_0`, and polls its health endpoint with a start timeout. *Alternative:* a server per pod shared by Roles; rejected because a Run's workload is per Run ([ADR-0099](../../../docs/adrs/0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md)) and a shared server would carry another Run's state.

### D3. Credentials through the existing loopback proxies

The conversation's LLM base URL and key are `RunEnv.LLM`'s, which under `PLOEG_LLM_KEY_ISOLATION=proxy` are the loopback proxy and a placeholder, exactly as for `acp`. The server never sees the real key or forge token; git pushes stay the worker's. Nothing new is added to the credential path.

### D4. Events: WebSocket first, search endpoint to catch up

The adapter subscribes to `/sockets/events/{id}` and keeps the last event id it recorded. On a drop it reconnects and calls `events/search` after that id, deduplicating by id. Mapping to transcript kinds lives in one table (`message`, `thought`, `tool_call`, `tool_result`, `plan`) with unknown event kinds ignored, so a server upgrade adds no failure mode, only missing entries. Every event touches `RunEnv.Activity`.

### D5. Limits and stop

`harness.ReportPromptStart` is called when `POST /run` succeeds, with the Role's Harness timeout and idle timeout. The idle watchdog reads `RunEnv.Activity`. On idle, prompt limit or context cancellation the adapter calls `interrupt`, waits briefly, then stops the process; it never waits on the server to stop itself.

### D6. Confirmations from the permission mode

Before `run`, the adapter sets `confirmation_policy`: never-confirm under `allow_always`, otherwise confirm risky actions. A confirmation request event is answered from the permission mode for an unattended Run, or relayed through ploegd for an attended Run with the same fallback as ACP ([ADR-0100](../../../docs/adrs/0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)). The same request limits as ACP apply.

### D7. Outcome

The prompt tells the agent where the drop-box is, as for every Harness. After the conversation finishes, the adapter reads the drop-box; with none, a finished conversation is `stuck` ("the agent reported no outcome") and an errored one `failed` with the server's error (R4).

### D8. Telemetry through headers, not environment

`pkg/worker/telemetry.go`'s `TelemetrySupport` returns traces for `openhands-agent-server`, delivered as `X-OpenHands-Observability-Parent-Span-Context`, `-Tags` and `-Metadata` on the start request. The server process gets no `OTEL_` variables, which is how the CLI crashed on 2026-10-18.

### D9. Recorded-server tests

Tests run against a fake server (`net/http/httptest` with a WebSocket handler) that replays recorded event sequences captured from the pinned version: normal finish, error, quiet agent, stream drop, capacity refusal, confirmation request. A live check runs the real server through start, conversation creation and stop with no model call, in the style of the hostile-repository check, and is opt-in.

## Risks / Trade-offs

- [The server's API is young; the one-shot result contract is still being documented upstream] → pin the version in the image, record the exact shapes in `docs/contracts/agent-server-harness.md`, ignore unknown event kinds, and make a Renovate bump of the server fail the recorded-server tests until the fixtures are refreshed.
- [The server's dependency tree is large (the SDK, LiteLLM, tools)] → install it under a lockfile per the image's input rule; never resolve at build time.
- [A conversation can run tools the worker does not see beyond events] → the Run's sandbox and placeholder credentials bound what it can reach, as for every Harness.
- [Port collisions between Runs on one node] → each Run is its own pod; the port is chosen inside it.

## Migration Plan

Additive. A Team opts in by setting a Role's Harness to `openhands-agent-server`; nothing changes for other Teams. Rollback is the Role setting. The CLI Harnesses leave only per ADR-0118.

## Open Questions

- Does the server need a writable home or persistence directory per Run, as the CLI needed `agent_settings.json`? To be measured in the first task.
- Which start-request fields carry the base URL and key in 1.55.x, and does the server honour `litellm_proxy/` model prefixes as the CLI did?
- Does the server accept MCP server configuration for the Run's MCP servers in the start request, or only from files?
