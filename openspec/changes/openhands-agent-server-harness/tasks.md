## 1. Pin the upstream surface

- [ ] 1.1 Install `openhands-agent-server` at the version the image will ship under its lockfile in a scratch environment; record the exact start-request fields (model, base URL, key, workspace, initial message), event kinds, end states, health and version endpoints, confirmation-policy values and the session-key variable in `docs/contracts/agent-server-harness.md`
- [ ] 1.2 Answer the design's open questions (per-Run home or persistence directory, `litellm_proxy/` model prefix, MCP servers in the start request) and record the answers in the same document
- [ ] 1.3 Capture recorded event sequences from the real server for: normal finish, error, quiet agent, stream drop, capacity refusal, confirmation request; store them as test fixtures under `pkg/harness/adapters/agentserver/testdata/`

## 2. Adapter

- [ ] 2.1 `pkg/harness/adapters/agentserver`: start the server on a free loopback port with a fresh session key and the allowlisted environment, wait for health with a start timeout, stop it with SIGTERM then SIGKILL on every exit path; a start failure returns `stuck` with the stderr tail (R4)
- [ ] 2.2 Create the conversation from `RunEnv` (prompt, model, `RunEnv.LLM` base URL and placeholder key, repository directory) and start it; a capacity refusal returns `stuck`
- [ ] 2.3 Follow `/sockets/events/{id}`, map events to transcript entries through one table, ignore unknown kinds, touch `RunEnv.Activity`, reconnect and catch up through `events/search` after the last recorded id without duplicates
- [ ] 2.4 Report the prompt start and limits on `run` (ADR-0114); enforce the idle and prompt limits by interrupting, stopping the server and returning `stuck` naming the limit; on context cancellation interrupt, stop and return the cancellation error
- [ ] 2.5 Set the confirmation policy from the permission mode; answer confirmation requests for unattended Runs, relay them through ploegd for attended Runs with the Team-policy fallback (ADR-0100), under the same request limits as ACP
- [ ] 2.6 Read the outcome drop-box; with none, map a finished conversation to `stuck` and an errored one to `failed` with the server's error
- [ ] 2.7 Report the server's name and version through `RunEnv.AgentInfo`; send the Run's trace context and resource attributes in the observability headers and keep `OTEL_` variables out of the server's environment (`TelemetrySupport` in `pkg/worker/telemetry.go`)

## 3. Wiring

- [ ] 3.1 `pkg/worker/adapters.go`: accept the Harness name `openhands-agent-server`, check the server binary is on PATH before claiming, and build the adapter from the Harness configuration
- [ ] 3.2 Helm: accept the Harness name in Team and Role Harness settings and in the allowlist; leave `ploeg.qualifiedHarnesses` unchanged
- [ ] 3.3 `docs/contracts/acp-profiles.md` "What an image needs": add the server and its lockfile requirement; link `docs/contracts/agent-server-harness.md` from `docs/contracts/README.md`

## 4. Tests

- [ ] 4.1 Recorded-server tests against `httptest` with a WebSocket handler, one per fixture in 1.3, plus: lease loss mid-conversation stops the server; idle and prompt limits produce `stuck` with the right reason; no `OTEL_` variable in the server environment; the real key never appears in the server's environment or requests
- [ ] 4.2 Opt-in live check (`PLOEG_AGENT_SERVER_LIVE=1`, skipped without the binary): start the real server, create a conversation against an unreachable model, read its version and events, stop it; no model call
- [ ] 4.3 CI job that installs the pinned server and runs 4.2, beside the `hostile-repository` job

## 5. Gates

- [ ] 5.1 `mise run verify`, `go vet ./...`, `go test ./...`, `go test ./internal/ledger/`, `mise run docs-check`; paste the output in the pull request
