---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run's Harness exports OpenTelemetry to a collector set per Team and Role

## Context and Problem Statement

A deployment cannot see what an agent does inside a Run. Claude Code exports metrics, logs and traces, and OpenHands exports traces, when the standard `OTEL_*` variables are set. But the worker hands the Harness an allowlisted environment (`harnessEnvironment`), so a variable on the worker pod never reaches the agent, and the chart has no per-Team or per-Role environment. The only side door is a toolchain's `env`, which needs a toolchain image.

How does an operator point a Run's Harness at a collector without widening what the Harness inherits?

## Decision Drivers

* The Harness environment stays an allowlist ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)): telemetry may add variables, never pass the pod's through.
* Every signal joins on the Run alias, never the Run token ([ADR-0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md)).
* Prompt text can carry ticket content and personal data, so it is off unless asked for.
* A Harness that exports nothing natively says so, instead of looking like a broken collector.

## Considered Options

* A typed `telemetry` block on the harness settings, translated per Harness by the worker
* A generic per-Team `extraEnv` passed to the Harness
* Leave it to toolchain `env`

## Decision Outcome

Chosen option: "**A typed `telemetry` block on the harness settings, translated per Harness by the worker**".

* `executor.harness.telemetry`, overridable on a Team and a Role (the most specific block wins whole), carries `endpoint`, `protocol` (default `http/protobuf`), `signals` (default all three), `resourceAttributes`, `logPrompts` (default false) and `logToolDetails`. The chart passes it as `PLOEG_TELEMETRY`.
* Only a Harness that exports natively and tolerates the variables gets them; today that is claude-code. It gets `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_RESOURCE_ATTRIBUTES`, one `OTEL_<SIGNAL>_EXPORTER` per signal, `CLAUDE_CODE_ENABLE_TELEMETRY`, `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS` and, with traces, `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA`. Every other Harness gets none of them.
* Resource attributes always carry `ploeg.team`, `ploeg.role` and `ploeg.run.alias`; a setting may not override them.
* At startup the worker logs the signals its Harness exports, or why telemetry is withheld: openhands and acp/openhands because an OTLP endpoint makes every prompt fail (below), acp/opencode and exec because they export nothing natively, claude-environment because its session runs on the self-hosted runner, whose own environment carries telemetry.

### Consequences

* Good, because an operator sees each Run's agent steps, tools and timings next to Ploeg's logs and the gateway's spend, joined on one key.
* Good, because nothing beyond the named variables enters the Harness.
* Bad, because OpenHands and opencode export nothing through this setting; their Runs stay visible only through Ploeg and the gateway.
* Bad, because a Harness that reads a variable the worker does not set (OpenHands' `LMNR_*`) needs a code change, not a setting.

### Confirmation

* `pkg/worker` tests: the chart's shape parses with defaults; malformed or spoofing settings are refused; each Harness gets its runtime's variables and none of another's; signals not asked for are `none`; the full Run token never appears; a worker with the setting hands it to the Harness it runs, and one without hands it nothing.
* Chart: `ci/executor-values.yaml` renders `PLOEG_TELEMETRY` into the golden; `ci/reject-telemetry-without-endpoint-values.yaml` is refused by the schema.

## Pros and Cons of the Options

### A generic per-Team `extraEnv` passed to the Harness

* Good, because any variable for any runtime is one line.
* Bad, because it is a hole in the allowlist: a credential or a proxy override reaches the agent with nothing to review.

### Leave it to toolchain `env`

* Good, because it exists.
* Bad, because it needs a toolchain image, and the run alias cannot be set from static values.

## Re-evaluation triggers

* A Harness Ploeg runs gains native OTLP export (opencode, goose, qwen-code), or an OpenHands CLI release allows an openhands-sdk without the `rollout_entrypoint` call.
* ploegd emits a trace per Run ([ADR-0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md) second step); then the worker also sets `TRACEPARENT`.
* A collector needs authentication headers from a Secret.

## More Information

* 2026-10-17 — The owner asked to see telemetry from every Harness at runtime, configured as settings. Research the same day: Claude Code honours `OTEL_*` from its process environment (not from a repository's `.claude/settings.json`); OpenHands' SDK exports traces when `OTEL_EXPORTER_OTLP_*` is set; opencode dropped `experimental.openTelemetry`.
* Related: [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md), [ADR-0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md), [ADR-0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md).
* 2026-10-18 — First live Run (bronze, VIK-2200) failed every prompt: with `OTEL_EXPORTER_OTLP_ENDPOINT` set, OpenHands initialises Laminar, and openhands-sdk 1.21.0 (pinned exactly by the OpenHands CLI 1.16.0) calls `lmnr.observe(rollout_entrypoint=…)`, which no lmnr release up to 0.8.0 accepts. openhands-sdk 1.55.1 no longer passes it. The setting now reaches only claude-code; the assumption that a Harness that does not read `OTEL_*` ignores it was wrong.
