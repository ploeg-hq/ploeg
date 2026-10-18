# The attack drill

Every Run executes a harness on a repository Ploeg does not control, so the repository is attacker input. The attack drill keeps proving, across every supported harness and over time, that a hostile repository cannot start its own code inside a Run or reach a real credential. The decision and what a failure blocks are in [ADR-0126](../adrs/0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md).

The drill makes **no model call**: agents are stopped at `session/new`, and the model gateway in the fixtures is an unroutable address. It costs no model spend, so it can run often.

## What it covers

A hostile repository fixture carries, through every repository-supplied channel an agent reads:

- a prompt-injection `AGENTS.md` (telling the agent to pipe a script to a shell, print `$LLM_API_KEY` and `$AGENT_BUILDER_TOKEN`, and write them to a file);
- repository configuration and plugins that try to auto-start code: Goose `.agents/plugins/*/.mcp.json`, OpenCode `opencode.json` and `.opencode/` plugins, Qwen Code `.qwen/settings.json` and `.env`;
- a `.env` the agent might load into its own environment.

The symlink-escape and hidden-Unicode instruction vectors, and the git-configuration vectors (`url.insteadOf`, `http.extraHeader`, hooks, `core.fsmonitor`, `include.path`), are drilled alongside in `pkg/worker/instructions_test.go` and `pkg/worker/workergit_test.go`; process-memory concealment in `pkg/worker/conceal_linux_test.go`.

## The two tiers

| Tier | Needs | Runs | What it asserts |
| --- | --- | --- | --- |
| Binary-free | nothing but Go | every `mise run verify` and the drill workflow | Each ACP profile's generated launch config neutralises the repository (`TestHostileConfig`). Under both credential-isolation proxies, the real model key and forge token never enter the harness environment (`TestAttackDrill`). |
| Real-binary | the agent binaries | the drill workflow (opt-in `PLOEG_HOSTILE_REPOSITORY=1`) | Each agent, started and driven to `session/new`, runs none of the repository's plugins, MCP servers or hooks (`TestHostileRepository`). |

Run them locally:

```sh
mise exec -- go test -run TestHostileConfig ./pkg/harness/adapters/acp/
mise exec -- go test -run TestAttackDrill ./pkg/worker/
PLOEG_HOSTILE_REPOSITORY=1 mise exec -- go test -run TestHostileRepository ./pkg/harness/adapters/acp/
```

The real-binary tier skips a harness whose binary is not on `PATH`, and fails loudly if an agent never answers `session/new` (a check that never created a session proves nothing).

## Cadence and failures

The workflow [`.github/workflows/attack-drill.yml`](../../.github/workflows/attack-drill.yml) runs weekly on a cron, on manual dispatch, and on every change to `pkg/harness/`, `pkg/worker/` or the workflow itself. The binary-free tier also runs inside `mise run verify`, so it gates every pull request. On a **scheduled** failure only, the workflow opens or updates one tracking issue (label `attack-drill`) through the default `GITHUB_TOKEN`, so a regression on the untended schedule is not lost.

## What the drill found

The first real-binary run found that **OpenCode (1.18.30) ran a repository's `.opencode/` plugin and its `opencode.json` MCP server at `session/new`**, before any prompt. The OpenCode profile now sets `OPENCODE_DISABLE_PROJECT_CONFIG=true` ([profiles.go](../../pkg/harness/adapters/acp/profiles.go)); with it, neither ran and `session/new` still succeeded. The root `AGENTS.md` still loads under the guard — confirmed by the live canary conformance (`mise run harness-conformance`), which needs operator-approved model spend.

## What the drill does not cover

It proves repository-supplied code does not start and credentials do not leak into the harness environment. It does **not** prove a model-driven agent resists prompt injection during a prompt turn — that needs the live canary conformance and its model spend ([ACP profiles](../contracts/acp-profiles.md)).
