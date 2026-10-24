---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Team declares Claude Code plugins pinned to a commit, whose hooks run guarded with only their declared environment

## Context and Problem Statement

A person who works with Claude Code on their own machine installs plugins from a plugin marketplace: hooks that report what a session did, skills, slash commands and subagents. A `claude-code` Run carries none of them. Its Claude Code starts in a fresh home, with `--settings '{"disableAllHooks":true}'` so the target repository's hooks never run ([ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)), and that switch also turns off every plugin hook. So the Run behaves unlike the person's session, and whatever a hook records for a person is missing for the agent's work.

How does a Team give its `claude-code` Runs the same plugins a person uses, without letting a plugin read the Run's credentials, block or stop the Run, or reopen the path ADR-0030 closed?

## Decision Drivers

* The delivery contract and the credential boundary ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)) stay where they are: a plugin gets no Ploeg credential, no model key and no forge token.
* What a Run loads is pinned and reviewed, like a skill in the catalogue ([ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md)); a branch or tag that moves is not a pin.
* A plugin that is unreachable, malformed or failing costs the Run the plugin, never the Run.
* The target repository's hooks stay off.
* A Team that declares nothing gets byte-for-byte the invocation it has today.
* Measured, not assumed: on Claude Code 2.1.302 against a local fake model endpoint (no model spend), an unguarded plugin hook saw `ANTHROPIC_API_KEY`, and a `UserPromptSubmit` hook exiting 2 stopped the prompt before any model call. `disableAllHooks` in `--settings` turns plugin hooks off too, and `--setting-sources user` alone also stops the clone's `CLAUDE.md` from loading.

## Considered Options

* **A per-Team plugin list on the harness settings, fetched by the worker at a pinned commit, installed as a session-only `--plugin-dir` with hooks rewritten to run under a guard**
* Run `claude plugin marketplace add` and `claude plugin install` in each Run
* Bake plugins into the agent image
* Fetch plugins through ploegd's skill catalogue (ADR-0092)

## Decision Outcome

Chosen option: "**a per-Team plugin list, fetched by the worker at a pinned commit, installed as `--plugin-dir` with guarded hooks**", because it is the only option that both pins the plugin and controls what its hooks receive and return, and it needs nothing outside the worker.

1. **Declared on the harness settings.** `harness.claudeCode.plugins` at executor, Team or Role level, the most specific list winning whole, as [ADR-0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md)'s telemetry does. The chart renders it only into a `claude-code` pod as `PLOEG_CLAUDE_PLUGINS`. When the Team manifest of [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) lands, the list moves with the harness binding of its crew.
2. **Pinned to a commit.** Each entry names the plugin, a marketplace `url` (https, without credentials, or an absolute path such as a mounted volume) and a full `commit` id. The worker fetches the marketplace without any credential, checks out that commit, refuses a checkout whose HEAD differs, and reads `.claude-plugin/marketplace.json`. Only a plugin whose source is a path inside that marketplace and whose entry declares no parts of its own is installed; a plugin from another repository is pinned by declaring that repository as its marketplace.
3. **Only text and guarded hooks.** The worker copies the plugin into the Run's scratch directory, refuses symbolic links and anything over 2,000 files or 20 MiB, and writes a new `plugin.json` holding the name, the version, the descriptive fields and the `commands`, `agents` and `skills` paths. MCP servers, LSP servers, monitors, `bin/`, `settings.json` and user configuration are left out and logged. `--strict-mcp-config` stays. Hooks of type `http`, `prompt` and `agent` are dropped: they would call outside the worker or spend from the Run's budget. A plugin whose skill, command or agent declares hooks in its front matter is refused, because those hooks cannot be guarded.
4. **What a hook receives.** Every `command` hook is rewritten to `ploeg-worker hook-guard <spec>`, the worker binary already mounted in the pod. The guard runs the original command with `PATH`, `HOME`, `TMPDIR`, the locale, `CLAUDE_PROJECT_DIR`, `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_DATA`, `CLAUDE_ENV_FILE`, `CLAUDE_CODE_SESSION_ID`, and the plugin's declared `env`, and nothing else. A declared name that Ploeg, the harness or the gateway reads (`ANTHROPIC_*`, `CLAUDE*`, `PLOEG_*`, `LLM_*`, `LITELLM_*`, `OTEL_*`, `GIT_*`, `AGENT_BUILDER*`, `PATH`, loader and proxy variables) is refused. A Secret-backed value is rendered as a `PLOEG_CLAUDE_PLUGIN_ENV_*` variable, the only variables a declaration may read, and the chart refuses a Secret that holds the gateway master key, a worker bootstrap token, a static inference key or a forge token.
5. **A hook cannot fail the Run.** The guard always exits 0. It stops a hook after its timeout (10 s by default, 60 s at most, killing its process group), drops its output when it fails, and from JSON output keeps only `systemMessage`, `suppressOutput` and `hookSpecificOutput.additionalContext`. A `decision`, a permission answer, rewritten tool input and `"continue": false` never reach Claude Code. Claude Code's own hook timeout is set five seconds above the guard's as a backstop.
6. **Fail open on availability, closed on content.** A marketplace that cannot be fetched or a plugin Ploeg refuses is left out, with the reason in the worker log; the other plugins and the Run continue. Nothing is installed from content that does not match the pin.
7. **How it meets the target's hooks.** With no plugins, the adapter passes `--settings '{"disableAllHooks":true}'` exactly as before. With plugins it instead passes `--setting-sources user`, so the clone's `.claude/settings.json` and `settings.local.json`, and their hooks, are not read, plus `--add-dir <clone>` and `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1`, so its `CLAUDE.md` still is, and one `--plugin-dir` per installed plugin. The Run's home is fresh, so the user settings are empty. Ploeg adds no Claude Code hooks of its own; its controls (the drop box, the command-line settings, the strict MCP config, the worker's git `core.hooksPath=/dev/null` and the transcript) live in the worker and on the command line, which a plugin cannot override.

### Consequences

* Good, because a Run carries the hooks and skills a person uses, so what a hook records for a person's session it records for the agent's.
* Good, because a plugin update is a reviewed change of one commit id, and a Team that declares nothing sees no change.
* Good, because the failure modes measured above (a hook reading the model key, a hook blocking the prompt) are closed by the guard, not by trusting the plugin.
* Bad, because a Run with plugins no longer loads the clone's `.claude/skills`, `.claude/commands` and `.claude/agents`. Accepted: ADR-0083 already points the Run at the repository's `.agents/skills`, and ADR-0030 treats `.claude/` as repository configuration, not contract.
* Bad, because the declared env is readable by the agent: hooks run as the agent's user, and the guard's specification files sit in the Run's scratch directory. Declare only what a hook needs, scoped to what it does.
* Bad, because a skill's shell injection and a hook's own network calls run with the agent's reach. The guard narrows the environment, not the network; the worker's network policy ([ADR-0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md)) does that, and a marketplace or a hook endpoint off the forge needs `networkPolicy.worker.extraEgress`.
* Bad, because each Run fetches the marketplace. Bounded by one fetch per marketplace per Run and a 60 s timeout; moves to ploegd's content store when ADR-0092's fetcher exists.
* Bad, because plugins that need MCP servers, `bin/` executables, `userConfig` or hooks in front matter do not work under Ploeg.

### Confirmation

* `go test ./pkg/harness/claudeplugins/` proves the declaration's refusals (moving refs, credentials in URLs, reserved names, non-plugin `fromEnv`), that hooks are rewritten to the guard with 0600 specification files outside the plugin, that MCP, LSP, `bin/`, settings and monitors are left out, that refused plugins leave nothing behind while the rest install, that `GitFetch` checks out only the pinned commit, and that the guard withholds the model key and forge token, forwards stdin, exits 0 on failure, exit 2, a timeout and an unreadable specification, and strips blocking output.
* `go test ./pkg/harness/adapters/claudecode/` proves the invocation without plugins is unchanged and with plugins swaps `disableAllHooks` for `--setting-sources user`, `--add-dir` and `--plugin-dir`.
* `go test ./pkg/worker/` proves only a `claude-code` Run installs plugins and a Run continues without one it could not install.
* `scripts/helm-golden.sh check` pins the rendered `PLOEG_CLAUDE_PLUGINS` and its secretKeyRef, and refuses a branch as a commit and a Secret holding a Ploeg credential.
* `go test ./internal/ledger/` validates this record and its index row. CI runs all of them through `mise run verify`.

## Pros and Cons of the Options

### Run `claude plugin marketplace add` and `claude plugin install` in each Run

* Good, because it is exactly what a person runs.
* Bad, because the CLI accepts a ref, not a verified commit, writes the plugin into settings Ploeg does not otherwise control, and leaves its hooks running with the Run's whole environment.

### Bake plugins into the agent image

* Good, because a Run needs no fetch.
* Bad, because a plugin update becomes an image release, every Team of the image gets the same set, and the hooks are still unguarded.

### Fetch plugins through ploegd's skill catalogue (ADR-0092)

* Good, because a Run would need no egress and ploegd would record each plugin's digest.
* Bad, because the catalogue's fetcher and content store do not exist yet, and its APM subset has no plugin unit. This record adopts its pinning rule now and names the move as a trigger.

## Re-evaluation triggers

* ADR-0092's fetcher and content store land in ploegd; then plugins are served by digest and the worker stops fetching.
* Claude Code gains a setting that turns off project hooks alone, or a per-plugin environment allowlist; then `--setting-sources user` or the guard can go.
* Claude Code changes the hook output contract (new fields that block, stop or rewrite), which the guard must then strip.
* A Team needs a plugin's MCP server in a Run; that reopens ADR-0078's one-server rule, not this record.

## More Information

* Probe (2026-10-24, Claude Code 2.1.302, fake model endpoint, no spend): with `disableAllHooks` neither the project's nor a plugin's hooks ran; with `--setting-sources user` the plugin's hooks ran and the project's did not, but `CLAUDE.md` was not sent until `--add-dir` with `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` was added; an unguarded plugin's `SessionStart` hook saw `ANTHROPIC_API_KEY`, and its `UserPromptSubmit` hook exiting 2 stopped the prompt (0 model requests); under the guard the same plugin saw only its declared variable and the prompt reached the endpoint (1 request).
* Related: [ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md), [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md), [ADR-0083](0083-a-run-is-told-which-skills-bear-on-its-work-item.md), [ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md), [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md), [ADR-0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md), [ADR-0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md).
* Claude Code documentation: [plugins](https://code.claude.com/docs/en/plugins), [plugin marketplaces](https://code.claude.com/docs/en/plugin-marketplaces).
