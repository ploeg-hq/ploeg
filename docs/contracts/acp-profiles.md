# ACP profiles

The `acp` harness runs any agent that speaks the Agent Client Protocol (wire version 1) over stdio. A profile tells it three things: how to start the agent, how to point it at the Run's model gateway, and which config file (if any) to write. Profiles live in [profiles.go](../../pkg/harness/adapters/acp/profiles.go). A team selects one with `executor.harness.acp.profile` (`PLOEG_ACP_PROFILE`). An unknown name, or a config document that a profile cannot read, stops the worker at startup, before it claims anything.

Every profile gets the same inputs from the worker:

- The model gateway's base URL and a model key. With `PLOEG_LLM_KEY_ISOLATION=proxy` these are a loopback URL and a placeholder, and the worker adds the real key on the way out ([ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)). Profiles pass the key only through environment variables and never write it into a config file.
- `PLOEG_OUTCOME_FILE`, the outcome drop box ([ADR-0018](../adrs/0018-the-outcome-drop-box-is-every-harnesss-return-path.md)). ACP returns a stop reason, not a review, so a reading Run's findings and verdict, and a writing Run's problem and solution ([ADR-0042](../adrs/0042-a-writing-run-reports-the-problem-and-solution-a-reviewer-reads.md)), still come back through this file.
- The Run's permission mode (`allow_always`, `allow_read_only` or `deny_all`). A profile sets the agent's own approval setting to match it. Under `allow_always` the agent runs without asking. Under the other two modes the agent asks for every tool call, and the adapter answers from the mode.

Config files and agent home directories are written to the scratch directory with the trace id in their names, because concurrent Runs share that directory.

## Profiles

The upstream facts below come from reading each project's source at the named tag on 27 September 2026. No live Run has measured them yet. `mise run harness-conformance` does that for the versions an image ships ([live_test.go](../../pkg/harness/harnesstest/live_test.go), harness names `acp`, `acp-qwen-code` and `acp-goose`).

| | `opencode` (default) | `qwen-code` | `goose` |
| --- | --- | --- | --- |
| Upstream checked | `opencode-ai` 1.18.30, as pinned when checked | [QwenLM/qwen-code](https://github.com/QwenLM/qwen-code) v0.24.6 (2026-09-26), npm `@qwen-code/qwen-code`, Apache-2.0 | [aaif-goose/goose](https://github.com/aaif-goose/goose) (formerly `block/goose`) v1.52.0 (2026-09-23), Apache-2.0 |
| Command | `opencode acp` | `qwen --acp --auth-type=openai` (`--acp` exists since v0.6.1; the older `--experimental-acp` is deprecated) | `goose acp` |
| Gateway wiring | Generated `OPENCODE_CONFIG` file with an `@ai-sdk/openai-compatible` provider; the key is the reference `{env:LLM_API_KEY}` | `OPENAI_API_KEY`, `OPENAI_BASE_URL` and `OPENAI_MODEL` | `GOOSE_PROVIDER=litellm`, `GOOSE_MODEL`, and `LITELLM_HOST`/`LITELLM_BASE_PATH`/`LITELLM_API_KEY`, with the base URL split into host and `<path>/chat/completions` |
| Config file | `opencode-<trace>.json`; `configJson` replaces it | `qwen-code-<trace>.json`, passed as `QWEN_CODE_SYSTEM_SETTINGS_PATH` (the settings scope that ranks above the repository's `.qwen/settings.json`); `configJson` replaces it. `QWEN_HOME` is a per-Run directory | None. Configured by environment only, and `configJson` is refused. `GOOSE_PATH_ROOT` is a per-Run directory, and `GOOSE_DISABLE_KEYRING=1` keeps it off the system keyring |
| Instructions | `AGENTS.md`, walking up from the working directory | `context.fileName: ["AGENTS.md"]` (upstream default is `QWEN.md` then `AGENTS.md`) | `CONTEXT_FILE_NAMES=["AGENTS.md"]` (upstream default is `.goosehints` and `AGENTS.md`), from the git root down to the working directory |
| Approval under `allow_always` / otherwise | Not set by the profile | `tools.approvalMode` `yolo` / `default` | `GOOSE_MODE` `auto` / `approve` |
| Calls outside the gateway, switched off | — | Usage statistics to Alibaba RUM (`QWEN_USAGE_STATISTICS_ENABLED=false`, `privacy.usageStatisticsEnabled: false`); auto-update (`general.enableAutoUpdate: false`); web search (`tools.webSearch.enabled: false`). `QWEN_CODE_NO_RELAUNCH=true` keeps the signalled process the agent itself | Telemetry (`GOOSE_TELEMETRY_OFF=1`); the extra session-naming call (`GOOSE_DISABLE_SESSION_NAMING=true`; `1` does not work in v1.52.0 because goose parses it as a number). Goose also calls `GET /model/info` on the gateway |
| Stop reasons | — | `end_turn`, `cancelled`, `max_tokens`; gateway errors arrive as JSON-RPC errors | `end_turn`, `cancelled`, `max_tokens`; a gateway authentication failure is a JSON-RPC `auth_required` error |
| ACP `authenticate` | Not needed | Not needed once the auth type resolves from the flag. Calling it would write to the user settings file | Not needed (the handler accepts and does nothing) |
| Shutdown | — | Exits 0 on stdin EOF and handles SIGTERM | Exits when stdin closes; has no SIGTERM handler, so SIGTERM kills it at once |

The adapter never calls `authenticate`, and no profile needs it. OpenHands advertises only an OpenHands Cloud login; its profile writes an agent settings file instead (see below).

## Before a team uses `qwen-code` or `goose`

Neither profile is qualified yet. Before a team switches, run the live conformance suite in the team's agent image and check these points:

- **Repository-supplied configuration.** Goose v1.52.0 enables MCP servers from `<working directory>/.agents/plugins/` the first time it sees them. `GOOSE_PATH_ROOT` does not cover that path. That means a target repository can start processes inside the Run. Qwen Code loads the first `.env` it finds walking up from the working directory, for variables the environment does not already set, and it merges the repository's `.qwen/settings.json` below Ploeg's system settings.
- **Permission storms.** Under `allow_read_only` and `deny_all`, both agents ask once per tool call. The adapter stops a Run after 200 requests, or after 60 in one minute ([permission.go](../../pkg/harness/adapters/acp/permission.go)).
- **The loopback proxy.** Both agents make their model calls from inside the worker container, so `PLOEG_LLM_KEY_ISOLATION=proxy` should hold. That is not yet measured.

## `openhands`

The OpenHands CLI (`openhands` on PyPI, MIT) serves ACP with `openhands acp`. The profile runs `openhands acp --override-with-envs`, checked against the CLI's help for 1.16.0, the version `agent-runner` ships for the native `openhands` adapter. That same image needs nothing added.

- **Agent settings.** OpenHands 1.16.0 answers `session/new` with `auth_required` ("Authentication required to create a session") until `$OPENHANDS_PERSISTENCE_DIR/agent_settings.json` exists. Its ACP entrypoint does not pass `--override-with-envs` on, so the `LLM_*` variables alone do not count, and the only method `initialize` advertises is `oauth`, a device-flow login to OpenHands Cloud. The profile therefore writes `openhands-<trace>/agent_settings.json` in the scratch directory and points `OPENHANDS_PERSISTENCE_DIR` at it. The file holds `llm.model` (`litellm_proxy/<model>`), `llm.base_url` and `llm.usage_id`, and no key. OpenHands keeps its conversation history in the same directory. `configJson` is refused at startup.
- **Gateway wiring.** The `litellm_proxy/` model prefix makes LiteLLM inside OpenHands read the key from `LITELLM_PROXY_API_KEY`, which the profile sets alongside `LLM_API_KEY`, `LLM_BASE_URL` and `LLM_MODEL`. The profile keeps `--override-with-envs`: in 1.16.0 it only suppresses the CLI's warning that the `LLM_*` variables are ignored, and a release that honours it gets the same values.
- **Approval.** The profile passes no approval flag, so OpenHands asks for each action over ACP and the adapter answers from the Run's permission mode.
- **Checked on 29 September 2026** with the 1.16.0 CLI (SDK 1.21.0, LiteLLM 1.92.0) against a stub gateway, through the adapter itself: `session/new` succeeds, the model request carries `Authorization: Bearer <the Run's key or placeholder>`, a tool call raises `session/request_permission` (options `accept`, `reject`, `always_proceed` and more), and a finished turn stops with `end_turn`. OpenHands exits at once on SIGTERM but not on stdin EOF (still running after 30 seconds); the adapter sends both. Besides the model request it makes an unauthenticated `GET <base URL>/v1/model/info` to the gateway, and two calls outside it: LiteLLM's model cost map from `raw.githubusercontent.com` and a `git clone` of `github.com/OpenHands/extensions` into `$HOME/.openhands/cache`. Both fail harmlessly when the connection is refused; a pod whose egress drops packets instead may wait for their timeouts. Per its source, it reads `AGENTS.md` and other third-party instruction files such as `CLAUDE.md` and `.cursorrules` from the working directory.
- **Not yet checked:** other stop reasons, and the loopback proxy under `PLOEG_LLM_KEY_ISOLATION=proxy`. Run the live conformance suite (`acp-openhands`) in the team's image before a team switches.

Why this profile exists alongside the native adapter: ACP gives structured tool calls and stop reasons instead of log tailing, and it keeps OpenHands swappable behind the same seam as the other agents.

## What an image needs

Ploeg does not install agents. Every profile's binary must be on `PATH` in the image that the team's `executor.harness.image` names (by default `executor.runnerImage`, `agent-runner`, which is built outside this repository). The worker checks this before it claims ([adapters.go](../../pkg/worker/adapters.go)). `executor.harness.entrypoint` replaces the binary path and keeps the profile's arguments.

- `qwen-code`: Node.js 22 or later and `npm install -g @qwen-code/qwen-code@0.24.6`, which provides `qwen`. Upstream releases every few days, so pin the version.
- `goose`: the `goose-x86_64-unknown-linux-musl` (or `aarch64`) release archive for v1.52.0, which is a static `goose` binary.

## Codex is not a profile yet

Codex reaches ACP through an adapter process. The Zed repository `zed-industries/codex-acp` was archived at v0.16.0. Its successor is [agentclientprotocol/codex-acp](https://github.com/agentclientprotocol/codex-acp) v1.13.1 (2026-09-23): TypeScript, npm only, Apache-2.0. It drives `codex app-server` from the npm dependency `@openai/codex` (^0.157.0) and takes its configuration from `CODEX_HOME` and a `CODEX_CONFIG` JSON variable, not from command-line flags.

Codex speaks only the Responses API. Its `wire_api = "chat"` option was removed in rust-v0.95.0 (2026-02-04). LiteLLM v1.102.1, the gateway version in production, serves `POST /v1/responses` and, according to its source, bridges it to chat completions for Anthropic and DeepSeek. What has not been shown is that the bridge carries Codex's own item types end to end: the freeform `apply_patch` tool, reasoning items, and compaction. Upstream issue [openai/codex#45393](https://github.com/openai/codex/issues/45393) reports that compaction fails against third-party Responses providers. codex-acp also has no mode that combines "never ask" with a workspace-write sandbox, so its permission mapping needs its own design. Adding the profile is tracked on the Ploeg board; it starts with a live probe of the gateway.
