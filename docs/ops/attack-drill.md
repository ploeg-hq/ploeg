# The attack drill

Every Run executes a harness on a repository Ploeg does not control, so the repository is attacker input. The attack drill keeps proving, across every supported harness and over time, that a hostile repository cannot start its own code inside a Run or reach a real credential. The decision and what a failure blocks are in [ADR-0126](../adrs/0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md).

The drill makes **no call to a real model**: agents are stopped at `session/new`, or after a first prompt for OpenHands, against an unroutable gateway, and the permission-mode check answers from a fake gateway inside the test. It costs no model spend, so it can run often.

## What it covers

A hostile repository fixture carries, through every repository-supplied channel an agent reads:

- a prompt-injection `AGENTS.md` (telling the agent to pipe a script to a shell, print `$LLM_API_KEY` and `$AGENT_BUILDER_TOKEN`, and write them to a file);
- repository configuration and plugins that try to auto-start code: Goose `.agents/plugins/*/.mcp.json`, OpenCode `opencode.json` (which also tries to grant every permission) and `.opencode/` plugins, Qwen Code `.qwen/settings.json` and `.env`, OpenHands `.openhands/hooks.json`;
- a `.env` the agent might load into its own environment.

The tracker is attacker input too. A Work Item whose description tries to close its untrusted frame and replace the delivery contract (push to the base branch, print the builder token and the model key, open a pull request on another repository) is driven through a full writer execute, and the drill reads the `session/prompt` the agent received: the description arrives inside one tracker frame with its forged close line escaped, the statement on untrusted text appears once, and Ploeg's delivery contract follows the frame (`TestAttackDrill_AWorkItemThatAsksToLeaveScopeReachesTheAgentOnlyAsFramedData`, [ADR-0170](../adrs/0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md)).

The symlink-escape and hidden-Unicode instruction vectors, and the git-configuration vectors (`url.insteadOf`, `http.extraHeader`, hooks, `core.fsmonitor`, `include.path`), are drilled alongside in `pkg/worker/instructions_test.go` and `pkg/worker/workergit_test.go`; process-memory concealment in `pkg/worker/conceal_linux_test.go`.

## The two tiers

| Tier | Needs | Runs | What it asserts |
| --- | --- | --- | --- |
| Binary-free | nothing but Go | every `mise run verify` and the drill workflow | Each ACP profile's generated launch config neutralises the repository, OpenCode asks before every call on a read-only or deny-all Run, and OpenHands refuses a clone with `.openhands/hooks.json` (`TestHostileConfig`). Under both credential-isolation proxies, the real model key and forge token never enter the harness environment (`TestAttackDrill`). With the harness in its own container, a hostile harness that reads every `/proc/*/environ`, every `cmdline` and every file under the work directory finds none of the bootstrap token, the builder token, the per-Run key or the per-Run forge token while a credentialed worker child runs, and the launcher ends a `setsid` survivor before verification (`TestHarnessContainerSeesNoCredential`, `TestLauncherAsFirstProcessEndsAHarnessThatLeftItsGroup`, ADR-0138). These two need an unprivileged user and PID namespace; they skip without one unless `PLOEG_REQUIRE_PID_NAMESPACE=1`, which CI and the drill set. |
| Real-binary | the agent binaries | the drill workflow (opt-in `PLOEG_HOSTILE_REPOSITORY=1`) | Each agent, started and driven to `session/new` (OpenHands also through one prompt, where it starts its hooks), runs none of the repository's plugins, MCP servers or hooks, or its profile refuses the Run (`TestHostileRepository_NoRepositorySuppliedCodeStarts`). With the OpenHands refusal bypassed, the real binary does run a planted hook, so the drill sees the vector (`TestHostileRepository_OpenHandsRunsARepositoryHookItIsHanded`). OpenCode, driven by a fake gateway to call `bash` and `write`, runs them under `allow_always` and is refused under `allow_read_only` and `deny_all` (`TestHostileRepository_OpenCodeAsksBeforeAMutationOutsideAllowAlways`). |

Run them locally:

```sh
mise exec -- go test -run TestHostileConfig ./pkg/harness/adapters/acp/
mise exec -- go test -run TestAttackDrill ./pkg/worker/
PLOEG_HOSTILE_REPOSITORY=1 mise exec -- go test -run TestHostileRepository ./pkg/harness/adapters/acp/
PLOEG_REQUIRE_PID_NAMESPACE=1 mise exec -- go test -run 'TestHarnessContainer|TestLauncherAsFirstProcess' ./pkg/worker/
```

The real-binary tier skips a harness whose binary is not on `PATH`, and fails loudly if an agent never answers `session/new` (a check that never created a session proves nothing).

## Cadence and failures

The workflow [`.github/workflows/attack-drill.yml`](../../.github/workflows/attack-drill.yml) runs weekly on a cron, on manual dispatch, and on every change to `pkg/harness/`, `pkg/worker/`, `cmd/ploeg-worker/` or the workflow itself. The binary-free tier also runs inside `mise run verify`, so it gates every pull request. On a **scheduled** failure only, the workflow opens or updates one tracking issue (label `attack-drill`) through the default `GITHUB_TOKEN`, so a regression on the untended schedule is not lost.

## What the drill found

The first real-binary run found that **OpenCode (1.18.30) ran a repository's `.opencode/` plugin and its `opencode.json` MCP server at `session/new`**, before any prompt. The OpenCode profile now sets `OPENCODE_DISABLE_PROJECT_CONFIG=true` ([profiles.go](../../pkg/harness/adapters/acp/profiles.go)); with it, neither ran and `session/new` still succeeded. The root `AGENTS.md` still loads under the guard — confirmed by the live canary conformance (`mise run harness-conformance`), which needs operator-approved model spend.

On 2026-10-24 two more were found and closed ([ADR-0148](../adrs/0148-every-acp-profile-asks-before-a-mutation-outside-allow-always-and-openhands-refuses-a-repository-that-ships-hooks.md)):

- **OpenCode ignored the Run's permission mode.** Its default ruleset allows every tool, so under `allow_read_only` and `deny_all` it ran `bash` and `write` without asking. The profile now writes a `permission` block that asks first; the new permission case failed on the old profile and passes on the new one.
- **OpenHands (1.16.0) ran a repository's `.openhands/hooks.json`** on the first prompt, ahead of any home-directory hooks file, and has no setting to skip it. Both OpenHands harnesses now refuse a clone that ships the file.

## What the drill does not cover

It proves repository-supplied code does not start, credentials do not leak into the harness environment, and tracker text reaches the agent only inside its frame. It does **not** prove a model-driven agent resists prompt injection during a prompt turn — that needs the live canary conformance and its model spend ([ACP profiles](../contracts/acp-profiles.md)).
