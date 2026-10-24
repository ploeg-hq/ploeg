# The ACP permission mode under OpenCode, and repository hooks under OpenHands

Date: 2026-10-24 · Decision record: [ADR-0148](../adrs/0148-every-acp-profile-asks-before-a-mutation-outside-allow-always-and-openhands-refuses-a-repository-that-ships-hooks.md)

Two gaps in how a Run's harness is held to what Ploeg intends. Both were read from upstream source at the versions Ploeg runs and then reproduced with the real binaries on 2026-10-24.

## 1. OpenCode ignored the Run's permission mode

**Source, `opencode-ai` 1.18.30** (tag `v1.18.30` of `anomalyco/opencode`):

- `packages/opencode/src/agent/agent.ts` builds every agent's ruleset as `Permission.merge(defaults, <agent rules>, user)`, where `defaults` starts with `"*": "allow"` and asks only for `doom_loop`, `external_directory` (paths outside the working directory and a few allowed scratch paths) and `.env` reads; `user` is the config's `permission` block. An agent's own `agent.<name>.permission` from config is merged after `user`.
- `packages/opencode/src/permission/index.ts`: `fromConfig` turns the block into rules in object order, and `evaluate` takes the last matching rule (`findLast`), defaulting to `ask`. So a user block whose first rule is `"*": "ask"` turns every tool to `ask`, and later rules in the same block grant exceptions.
- `packages/opencode/src/acp/permission.ts`: an `ask` becomes an ACP `session/request_permission` with options `once` (`allow_once`), `always` (`allow_always`) and `reject` (`reject_once`); `packages/opencode/src/acp/tool.ts` maps `bash` to kind `execute`, `edit`/`write`/`apply_patch` to `edit`, `webfetch` to `fetch`, `read` to `read`, `grep`/`glob` to `search`, `task` to `think`, and anything else to `other`.
- `packages/opencode/src/session/processor.ts`: a refused permission ends the turn unless `experimental.continue_loop_on_deny` is `true` (schema in `packages/core/src/v1/config/config.ts`).
- `packages/opencode/src/config/config.ts`: load order is the global config, `OPENCODE_CONFIG`, the project config (skipped under `OPENCODE_DISABLE_PROJECT_CONFIG`), `.opencode` directories (the project's also skipped under that flag), `OPENCODE_CONFIG_CONTENT`, managed config, then `OPENCODE_PERMISSION`.

The profile wrote no `permission` block for any mode, so OpenCode's `"*": "allow"` stood under `allow_read_only` and `deny_all`.

**Reproduced.** `TestHostileRepository_OpenCodeAsksBeforeAMutationOutsideAllowAlways` runs the real `opencode` 1.18.30 through the ACP adapter against an in-test gateway that answers the first turn with one tool call. Before the fix, `bash` (`touch ran-a-command`) and `write` (a file in the clone) both ran under `allow_always`, `allow_read_only` and `deny_all` (4 of 6 cases failed). After it, both ran under `allow_always` and neither ran under the other two (6 of 6 pass); under `allow_read_only` the Run went on to a second model turn after the refusal, which is `continue_loop_on_deny` at work.

## 2. OpenHands runs a repository's hooks file

**Source, `openhands` 1.16.0 with `openhands-sdk` 1.21.0** (the installed packages):

- `openhands/sdk/hooks/config.py`, `HookConfig.load(path=None, working_dir=...)`: with no explicit path it tries `<working_dir>/.openhands/hooks.json`, then `~/.openhands/hooks.json`, and uses the first that `Path.exists()`.
- `openhands_cli/acp_impl/agent/local_agent.py` (`_setup_conversation`, reached from `new_session`) calls `HookConfig.load(working_dir=<session working directory>)`; `openhands_cli/setup.py` (headless) calls `HookConfig.load(working_dir=get_work_dir())`, which is `OPENHANDS_WORK_DIR` or the process working directory.
- `openhands/sdk/conversation/impl/local_conversation.py`, `_ensure_plugins_loaded`: on the first `send_message` or `run`, the hook processor is created and `run_session_start()` runs the `SessionStart` hooks. Hook commands also run on `UserPromptSubmit`, `PreToolUse`, `PostToolUse` and `Stop`, outside the confirmation request ACP carries.
- Neither package has a setting that skips the working directory's file.

**Reproduced.** `TestHostileRepository_OpenHandsRunsARepositoryHookItIsHanded` prepares the `openhands` ACP profile on a clean clone, plants a `.openhands/hooks.json` whose `SessionStart` hook touches a marker, and drives the real binary through `initialize`, `session/new` and one `session/prompt` with an unroutable gateway. The marker appeared: the hook ran before any model call. With the refusal in place the hostile-repository case for `openhands` stops at `Prepare`; with the refusal bypassed the same case fails with "the repository's .openhands/hooks.json started code inside the openhands Run".

**Also noted, not changed.** OpenHands 1.16.0 registers project subagent definitions from `.agents/agents/*.md` and `.openhands/agents/*.md` (`openhands/sdk/subagent/load.py`), whose front matter may declare `hooks` and `mcp_servers`. These start only when the model delegates to that subagent, so they need the model's cooperation rather than starting on their own; they are a prompt-injection vector, not an auto-start one.

## 3. The other profiles

- **Qwen Code**: `tools.approvalMode` is `yolo` under `allow_always` and `default` otherwise, and folder trust forces `default` in the untrusted clone, so edits and shell calls ask. Whether `default` also asks before a read was not checked here; if it does not, a `deny_all` Qwen Code Run still reads the clone.
- **Goose**: `GOOSE_MODE` is `auto` under `allow_always` and `approve` otherwise.
- **OpenHands (ACP)**: the ACP server starts in `always-ask`, and every request carries tool kind `other` and title "Confirm Agent Actions", so `allow_read_only` refuses reads as well as writes.
- **`custom`**: the profile has no `Configure`, so nothing tells the agent the Run's mode; whether it asks is up to the agent and its `argv`.
