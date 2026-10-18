---
status: proposed
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The unmaintained OpenHands CLI is retired as a Harness

## Context and Problem Statement

`openhands` and `acp/openhands` are two of Ploeg's three qualified Harnesses ([ACP profiles](../contracts/acp-profiles.md), [isolation qualification](../research/2026-10-04-isolation-qualification.md)). Both run the OpenHands CLI (`openhands` on PyPI). Since 2026-08-11 the CLI's repository says "This project is no longer actively maintained"; its last release is 1.16.0 of 2026-05-08, which pins `openhands-sdk` 1.21.0. The SDK itself is maintained (1.55.1 on 2026-10-16) but ships no ACP agent: its `ACPAgent` drives other ACP servers. Upstream points CLI users to a desktop application, which cannot run an unattended Run.

On 2026-10-18 every OpenHands Run on a live cluster failed within 40 seconds once [ADR-0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md) telemetry reached it: the image installed the CLI without its lockfile, resolved `lmnr` 0.8.0, and SDK 1.21.0's tracing passes an argument `lmnr` 0.7.53 removed. The image now installs the CLI under the constraints of its own release lockfile, so it is reproducible again, but frozen: no upstream fix will reach it. Which Harness should Teams that default to OpenHands run instead?

## Decision Drivers

* A Harness runs unattended on someone else's repository, so it must get security fixes; an abandoned upstream does not.
* A replacement is qualified the way the current ones were: the live conformance suite in the Team's image, under `PLOEG_LLM_KEY_ISOLATION=proxy` and `PLOEG_FORGE_TOKEN_ISOLATION=proxy`, before any Team switches.
* ACP keeps the agent swappable behind one adapter, with structured tool calls and stop reasons ([ACP profiles](../contracts/acp-profiles.md)).
* Ploeg does not install or build agents ([ACP profiles](../contracts/acp-profiles.md), "What an image needs"), and adding a Ploeg-owned agent would make Ploeg maintain one.
* Qualification makes real model calls; it is a small, stated spend approved by the operator.

## Considered Options

* Qualify `acp/opencode` and `acp/qwen-code`, then move OpenHands Teams to the first that qualifies
* Keep the OpenHands CLI 1.16.0 under its release lockfile
* Run the CLI against the current SDK by overriding its pin
* A Ploeg-owned ACP agent built on the OpenHands SDK
* `acp/goose`

## Decision Outcome

Chosen option: "**Qualify `acp/opencode` and `acp/qwen-code`, then move OpenHands Teams to the first that qualifies**", because both are maintained, both already have profiles, and the adapter that runs them is the one OpenHands already runs through.

* **Order.** `acp/opencode` first: it is the default ACP profile, its gateway wiring is a generated config file with no repository-supplied settings above it, and it needs no permission mode of its own. `acp/qwen-code` second, after its two open risks are measured: a repository `.env` it loads, and a repository `.qwen/settings.json` merged below Ploeg's system settings.
* **Qualification.** `mise run harness-conformance` for the profile in the Team's image, then the isolation qualification Runs. The operator approves the model spend first.
* **Switch.** One Team at a time, by changing its Role defaults; `openhands` and `acp/openhands` stay in the allowlist until no Team defaults to them for a month, then leave `ploeg.qualifiedHarnesses`.
* **Until then.** The OpenHands CLI stays at 1.16.0 under its release lockfile. Telemetry may reach it again: with the locked `lmnr` its tracing starts.

### Consequences

* Good, because every default Harness has a maintained upstream again.
* Good, because the switch changes configuration, not code: the ACP adapter, transcript ([ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)), permission relay ([ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)) and limits ([ADR-0114](0114-each-run-records-the-limits-that-stop-its-prompt.md)) stay as they are.
* Bad, because qualification costs model calls and the Runs before and after are not directly comparable: a different agent may need different prompts or skills to deliver the same work.
* Bad, because until a replacement qualifies, the OpenHands Teams run an agent that gets no fixes.
* Neutral, because the native `openhands` adapter loses its last user once both OpenHands Harnesses leave the allowlist; removing it is its own change.

### Confirmation

* `mise run harness-conformance` passes for the `acp` Harness (the `opencode` profile) in the Team's image, and the isolation qualification record gains a dated row for it.
* A Team that switched shows the new Harness in the Teams read ([ADR-0097](0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md)), and its next ten Runs reach their normal outcome at least as often as its last ten OpenHands Runs.
* `ploeg.qualifiedHarnesses` lists no OpenHands Harness once no Team has defaulted to one for a month.

## Pros and Cons of the Options

### Qualify `acp/opencode` and `acp/qwen-code`

* Good, because both are maintained and already have profiles behind the qualified adapter.
* Bad, because neither has passed the live suite yet.

### Keep the OpenHands CLI 1.16.0 under its release lockfile

* Good, because it is stable and costs nothing now.
* Bad, because it gets no security or correctness fixes, and every future break is ours to work around.

### Run the CLI against the current SDK by overriding its pin

* Good, because it picks up the SDK's fixes.
* Bad, because the CLI was written against SDK 1.21.0 and nobody tests it against 1.55; an override trades one untested combination for another.

### A Ploeg-owned ACP agent built on the OpenHands SDK

* Good, because it keeps the OpenHands agent loop on a maintained SDK.
* Bad, because Ploeg would build and maintain an agent, which it does not do for any other Harness.

### `acp/goose`

* Good, because it is maintained and already in the reference image.
* Bad, because it enables MCP servers from a repository's `.agents/plugins/` the first time it sees them, so a target repository can start processes inside the Run ([ACP profiles](../contracts/acp-profiles.md)); it waits until that is closed.

## Re-evaluation triggers

* The OpenHands CLI is maintained again: a release after 1.16.0, or the "no longer actively maintained" notice is removed.
* The OpenHands SDK ships an ACP agent of its own.
* `acp/opencode` or `acp/qwen-code` fails qualification.

## More Information

* Upstream facts checked on 2026-10-18: [OpenHands-CLI](https://github.com/OpenHands/OpenHands-CLI) README and releases, PyPI `openhands`, `openhands-sdk` and `lmnr`.
