---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# A recurring attack drill runs every harness against a hostile repository

## Context and Problem Statement

A Harness runs on a repository Ploeg does not control, so the repository is an attacker's input. It can carry prompt-injection instruction files, configuration and plugins that start code when the agent loads them, symlinks that point out of the work directory, and text that tries to make the agent print a credential. Isolation against this is spread across the worker and the adapters — the credential proxies ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)), the forge proxy and push fence, the non-dumpable worker ([ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) and its research), the instruction-file scanner, and per-profile neutralisation of repository config (Goose plugins, Qwen Code folder trust). Each has its own test, but nothing exercises them together on a schedule, and a new harness or a harness upgrade can reopen a hole no single test covers. How does Ploeg keep proving, over time and across every supported harness, that a hostile repository cannot take over a Run or reach a real credential?

## Decision Drivers

* The repository is untrusted input on every Run; regressions here are security regressions.
* A harness is third-party and gets upgraded; an invariant that held at one version can break at the next with no code change in Ploeg.
* The drill must cost no model spend, or it will not run often — so it uses fake model servers and stops agents before any prompt turn.
* A failure must be loud and readable, and a scheduled failure must reach a person without one watching the run.
* Ploeg does not install agents ([ACP profiles](../contracts/acp-profiles.md)); the always-on part of the drill must run without any agent binary present.

## Considered Options

* A recurring attack drill in two tiers: a binary-free tier in `go test ./...`, and a real-binary tier on a schedule and on harness/worker changes
* Keep the existing per-component hostile tests and add cases to them ad hoc
* One scheduled job that runs the real agents end to end against a live gateway

## Decision Outcome

Chosen option: "**A recurring attack drill in two tiers**", because it catches both a Ploeg change and a harness upgrade, proves the real agents honour the configuration, and still runs on every pull request at no model cost.

* **Tier 1 — no binary, no model, every `go test ./...`.** For each ACP profile, a hostile repository fixture (prompt-injection `AGENTS.md`, `.agents/plugins/`, `.env`, `.qwen/settings.json`, `opencode.json`, `.opencode/` plugin) is handed to the profile's `Prepare`, and the generated launch configuration is asserted to neutralise every repository-supplied input: Goose disables each repository plugin, Qwen Code treats the repository as an untrusted workspace and keeps approvals on a read-only Run, and OpenCode's config is self-contained, outside the repository, and disables the repository's project config. At the worker, a full writer `execute` under both credential-isolation proxies, with a harness that dumps its own environment, asserts the real model key and forge token never enter the harness environment.
* **Tier 2 — real binaries, no model, scheduled and on change.** Each supported agent binary is started against the hostile repository and driven to `session/new` — the point at which a repository's plugins, MCP servers and hooks would start — and the drill fails if any repository-supplied code ran. It makes no model call: the gateway URL is an unroutable address and the run stops after the session is created. It is gated behind `PLOEG_HOSTILE_REPOSITORY=1`, which the drill workflow sets.
* **Cadence.** The drill workflow (`.github/workflows/attack-drill.yml`) runs weekly on a cron, on manual dispatch, and on every change to harness or worker code or to the drill itself. The binary-free tier also runs inside `mise run verify` on every pull request.
* **A scheduled failure opens an issue.** On the cron schedule only, a failure opens or updates one tracking issue through the default `GITHUB_TOKEN`, so a regression on the untended schedule is not lost in the run log.
* **What a failure blocks.** The binary-free tier is part of `verify`, so its failure blocks merge like any other gate. The real-binary tier gates no merge by itself — it needs agent binaries a pull request's image may not pin — but a red scheduled or on-change drill blocks qualifying or upgrading the harness it failed for until the hole is closed.

### Consequences

* Good, because a harness upgrade that reopens a repository-config hole fails the drill before any Team runs the new version.
* Good, because the binary-free tier runs on every pull request at no cost and pins the invariants [ADR-0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md) names for Qwen Code and Goose.
* Good, because the first run already found and closed a real hole: OpenCode started a repository's plugins and MCP servers at `session/new` (see Confirmation).
* Bad, because the real-binary tier only covers agents whose binaries the drill image installs; an agent it does not install is unproven until it is added.
* Bad, because a harness that changes how it reports `session/new` can make the real-binary tier skip rather than fail; the tier fails loudly when the session is never created, which bounds this.
* Neutral, because the drill proves repository-supplied code does not start and credentials do not leak; it does not prove a model-driven agent resists prompt injection, which needs the live canary conformance and its model spend.

### Confirmation

* `mise exec -- go test ./pkg/harness/adapters/acp/ -run TestHostileConfig` and `go test ./pkg/worker/ -run TestAttackDrill` run in `mise run verify` on every pull request and must pass.
* `.github/workflows/attack-drill.yml` runs the binary-free tier and the real-binary tier (`PLOEG_HOSTILE_REPOSITORY=1 go test -run TestHostileRepository ./pkg/harness/adapters/acp/`) with OpenCode and Goose installed, weekly, on dispatch, and on harness/worker changes; `actionlint` validates the workflow.
* The real-binary tier found that OpenCode (1.18.30) ran a repository's `.opencode/` plugin and its `opencode.json` MCP server at `session/new`; the OpenCode profile now sets `OPENCODE_DISABLE_PROJECT_CONFIG=true`, after which neither ran and `session/new` still succeeded. `TestHostileConfig_OpenCodeConfigIsSelfContainedAndOutsideTheRepository` pins the guard.
* That `OPENCODE_DISABLE_PROJECT_CONFIG=true` still loads the repository's root `AGENTS.md` is confirmed by the live canary conformance for `acp` in the Team's image (`mise run harness-conformance`), which needs the operator-approved model spend the isolation qualification already budgets.

## Pros and Cons of the Options

### Keep the existing per-component hostile tests and add cases ad hoc

* Good, because it adds no new machinery.
* Bad, because nothing runs on a schedule, so a harness upgrade between pull requests reopens a hole no test re-checks.
* Bad, because the invariants are scattered, so "is every harness still safe against a hostile repository?" has no single answer.

### One scheduled job that runs the real agents end to end against a live gateway

* Good, because it exercises the full prompt turn, including the agent's own resistance to injection.
* Bad, because it costs model spend on every run, so it runs rarely or not at all.
* Bad, because a live model makes the result nondeterministic, so a real regression is hard to tell from a flaky turn.

## Re-evaluation triggers

* A new harness or ACP profile is added, or a supported agent's major version changes how it loads repository configuration, plugins or instruction files.
* OpenCode releases a version where `OPENCODE_DISABLE_PROJECT_CONFIG` changes meaning, or stops loading the root `AGENTS.md` under it.
* The credential-isolation mechanism changes (the proxies, the push fence, or the non-dumpable worker), so the environment the drill asserts on changes shape.
* The drill opens more than a few tracking issues a quarter for the same harness, meaning the signal needs tuning.

## More Information

* Isolation design and evidence: [docs/research/2026-10-17-isolation-ladder-and-execution-cells.md](../research/2026-10-17-isolation-ladder-and-execution-cells.md), [docs/research/2026-10-04-isolation-qualification.md](../research/2026-10-04-isolation-qualification.md).
* The drill and its tiers: [docs/ops/attack-drill.md](../ops/attack-drill.md).
* Related: [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md), [ADR-0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md), [ACP profiles](../contracts/acp-profiles.md).
