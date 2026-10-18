---
status: proposed
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The unmaintained OpenHands CLI is retired as a Harness, for OpenCode, Qwen Code and Goose

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

* Support three maintained ACP agents, OpenCode, Qwen Code and Goose, each qualified before a Team uses it, and keep the OpenHands CLI frozen until they are
* Replace OpenHands with one agent only
* Keep the OpenHands CLI 1.16.0 under its release lockfile
* Run the CLI against the current SDK by overriding its pin
* Fork the OpenHands CLI, or build a Ploeg-owned ACP agent on the OpenHands SDK

## Decision Outcome

Chosen option: "**Support three maintained ACP agents, OpenCode, Qwen Code and Goose, each qualified before a Team uses it, and keep the OpenHands CLI frozen until they are**", because all three are maintained, all three already have profiles behind the qualified ACP adapter, and offering more than one keeps a Team from depending on a single upstream again.

* **OpenCode first** (`acp`, profile `opencode`). It is the default ACP profile, its gateway wiring is a generated config file with nothing repository-supplied above it, and it needs no permission mode of its own. The reference image must ship it; `opencode-ai` 1.x is the package (OpenCode 2.x is a different package and a later move).
* **Qwen Code** (`acp-qwen-code`) qualifies once its profile closes two repository-supplied inputs: a repository `.env`, which Qwen Code reads for variables the environment does not set, and a repository `.qwen/settings.json`, merged below Ploeg's system settings. The profile sets every variable Qwen Code would read from `.env`, and the system settings pin every key a repository file could change; a conformance case with a hostile `.env` and settings file proves both.
* **Goose** (`acp-goose`) qualifies once a repository's `.agents/plugins/` can no longer start processes inside the Run: the profile disables plugin loading or runs Goose from a directory outside the repository, and a conformance case with a hostile plugin proves it.
* **Qualification.** `mise run harness-conformance` for the profile in the Team's image, then the isolation qualification Runs under `PLOEG_LLM_KEY_ISOLATION=proxy` and `PLOEG_FORGE_TOKEN_ISOLATION=proxy`. The operator approves the model spend first.
* **Switch.** One Team at a time, by changing its Role defaults; a Team may list several qualified agents and choose per Role ([ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md)). `openhands` and `acp/openhands` stay in the allowlist until no Team defaults to them for a month, then leave `ploeg.qualifiedHarnesses`.
* **Until then.** The OpenHands CLI stays at 1.16.0 under its release lockfile. Telemetry may reach it again: with the locked `lmnr` its tracing starts.
* **Fallback.** If none qualifies, a fork is allowed: the CLI and the SDK are MIT-licensed, so a fork keeps their copyright and license notices and does not present itself under the OpenHands name. It is a last resort, because Ploeg would then maintain an agent.

### Consequences

* Good, because every default Harness has a maintained upstream again, and no Team is tied to one.
* Good, because the switch changes configuration, not code: the ACP adapter, transcript ([ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)), permission relay ([ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)) and limits ([ADR-0114](0114-each-run-records-the-limits-that-stop-its-prompt.md)) stay as they are.
* Bad, because qualification costs model calls, three times, and Runs before and after are not directly comparable: a different agent may need different prompts or skills to deliver the same work.
* Bad, because the Qwen Code and Goose profiles need changes and hostile-repository tests before they qualify.
* Bad, because until a replacement qualifies, the OpenHands Teams run an agent that gets no fixes.
* Neutral, because the native `openhands` adapter loses its last user once both OpenHands Harnesses leave the allowlist; removing it is its own change.

### Confirmation

* `mise run harness-conformance` passes for `acp` (OpenCode), `acp-qwen-code` and `acp-goose` in the Team's image, each with a dated row in the isolation qualification record.
* The Qwen Code and Goose conformance runs include a hostile repository (`.env`, `.qwen/settings.json`, `.agents/plugins/`) and show none of it took effect.
* A Team that switched shows the new Harness in the Teams read ([ADR-0097](0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md)), and its next ten Runs reach their normal outcome at least as often as its last ten OpenHands Runs.
* `ploeg.qualifiedHarnesses` lists no OpenHands Harness once no Team has defaulted to one for a month.

## Pros and Cons of the Options

### Support OpenCode, Qwen Code and Goose

* Good, because all three are maintained and already have profiles behind the qualified adapter.
* Good, because a Team can fall back to another qualified agent when one breaks.
* Bad, because none has passed the live suite yet, and two need profile changes first.

### Replace OpenHands with one agent only

* Good, because it qualifies and maintains one profile, not three.
* Bad, because it repeats the situation this ADR fixes: one upstream every Team depends on.

### Keep the OpenHands CLI 1.16.0 under its release lockfile

* Good, because it is stable and costs nothing now.
* Bad, because it gets no security or correctness fixes, and every future break is ours to work around.

### Run the CLI against the current SDK by overriding its pin

* Good, because it picks up the SDK's fixes.
* Bad, because the CLI was written against SDK 1.21.0 and nobody tests it against 1.55; an override trades one untested combination for another.

### Fork the OpenHands CLI, or build a Ploeg-owned ACP agent on the OpenHands SDK

* Good, because it keeps the OpenHands agent loop, and the MIT licence allows it.
* Bad, because Ploeg would build and maintain an agent, which it does not do for any other Harness; the CLI is also months behind the SDK it would need to track.

## Re-evaluation triggers

* The OpenHands CLI is maintained again: a release after 1.16.0, or the "no longer actively maintained" notice is removed.
* The OpenHands SDK ships an ACP agent of its own.
* OpenCode, Qwen Code or Goose fails qualification, or one of them stops being maintained.

## More Information

* Upstream facts checked on 2026-10-18: [OpenHands-CLI](https://github.com/OpenHands/OpenHands-CLI) README, releases and MIT licence; [software-agent-sdk](https://github.com/OpenHands/software-agent-sdk) MIT licence; PyPI `openhands`, `openhands-sdk` and `lmnr`; npm `opencode-ai` 1.19.3.
* Licence facts are not legal advice.
