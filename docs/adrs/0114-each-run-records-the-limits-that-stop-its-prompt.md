---
status: proposed
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Each Run records the limits that stop its prompt

## Context and Problem Statement

A Run stops when its prompt runs too long or its agent goes quiet. Those limits live only in the worker's configuration: the ACP profile's prompt timeout (default 45 minutes) and idle timeout (default 10 minutes), and the worker's harness timeout (default 100 minutes) and idle watchdog (default 15 minutes) for every Harness. Each can be set per Team and Role. The operator API serves `startedAt` and the heartbeat lease `expiresAt`, but neither is a limit, and `startedAt` is the claim, minutes before the agent gets its prompt. An operator consumer therefore cannot show how close a Run is to a stop, and a reading taken from today's configuration is wrong for any Run that started before a change. [ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md) records what a Run was configured with; what should a Run record about the limits that stop it, from which source, and when?

## Decision Drivers

* A recorded limit is the one that applied to that Run, never one derived later from today's configuration, as in ADR-0091. Unknown is served as unknown, never as zero.
* The value must be what actually stops the Run for its Harness, including the case where the harness timeout ends a Run before its ACP prompt timeout does.
* The moment the prompt starts is known only to the worker, after the clone, the context and the agent's session are ready.
* An older worker, and every Run recorded before this change, keeps working and reads as unknown.

## Considered Options

* The worker reports the limits in a `prompt_started` checkpoint when the prompt starts
* ploegd derives the limits from its own configuration at claim, as it does the model and plan
* The worker adds the limits to its claim request

## Decision Outcome

Chosen option: "**The worker reports the limits in a `prompt_started` checkpoint when the prompt starts**", because only the worker knows when the prompt starts and which limits its Harness enforces, and the checkpoint is an existing, authenticated report made while the Run is live.

What is decided:

* **Source.** The adapter calls `harness.ReportPromptStart` when the prompt starts: `acp` before its `session/prompt` request, `claude-environment` after the session is dispatched and recorded, and every spawned Harness (`openhands`, `claude-code`, `exec`) once its process has started. The worker sends a checkpoint with phase `prompt_started` and a `limits` object ([checkpoint](../contracts/checkpoint.v1.schema.json)). Recording is best effort: a failed report is logged and the Run continues.
* **Prompt limit.** `promptSeconds` is the most the prompt may run from `promptStartedAt`: the earlier of the adapter's own prompt limit and the deadline of the context the worker runs the Harness under, which is the harness timeout (`PLOEG_HARNESS_TIMEOUT`). For `acp` that is `min(PLOEG_ACP_PROMPT_TIMEOUT, harness timeout left)`; for every other Harness, the harness timeout left. A disabled harness timeout with no adapter limit records none.
* **Idle limit.** `idleSeconds` is how long the agent may show no activity: `PLOEG_ACP_IDLE_TIMEOUT` over protocol activity for `acp`, checked every half of it; `PLOEG_HARNESS_IDLE_TIMEOUT` over output and model traffic for the spawned Harnesses. `claude-environment` has no idle watchdog and records none.
* **Storage.** `agent_runs` gains `prompt_started_at`, `limit_prompt_seconds` and `limit_idle_seconds` (migration `0059_run_limits.sql`), all null by default and never backfilled. The first `prompt_started` report writes all three together and later ones change nothing. A limit outside 1 second to 7 days is stored as no limit. A `promptStartedAt` more than five minutes before the Run started or after ploegd's clock records nothing.
* **Reads.** The operator API's `run` (Run detail, Work Item detail runs, Work Item facts runs) and `runListItem` (Run list) carry `limits`: `{"promptSeconds": 5400, "idleSeconds": 600, "promptStartedAt": "2026-10-17T08:34:05Z"}` ([operator API](../contracts/operator-api.v1.schema.json) `runLimits`). An absent limit inside `limits` is no limit. `limits` itself is absent, never null or zero, while the prompt start is unknown: before the prompt starts, from an older worker, for an Ask or an operator execution, and for every Run recorded before migration 0059. ploeg-mcp's Run tools render no Run configuration, so they are unchanged.

### Consequences

* Good, because an operator consumer can show how close a Run is to a stop from facts recorded for that Run, and a configuration change does not rewrite them.
* Good, because the limit served is the one that fires first for that Harness, so a Run bounded by the harness timeout does not show the longer ACP prompt timeout.
* Good, because the clock starts when the agent gets its prompt, not at the claim, so the clone and session setup do not count against the prompt.
* Bad, because the limits and the start time are self-reported by the worker. They are authenticated by the Run's control token, bounded and checked against ploegd's clock, and a wrong value changes only this record.
* Bad, because the idle limit says when the watchdog may fire, not when the agent was last active; a consumer that wants the remaining quiet time needs activity of its own, such as the transcript ([ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)).
* Bad, because an ACP idle stop can come up to half the idle timeout later than `idleSeconds`, since the watchdog checks every half of it.

### Confirmation

* `go test ./pkg/harness/` checks that `ReportPromptStart` takes the earlier of the prompt limit and the context deadline, and that a spawned Harness reports once with its idle watchdog and never when its process does not start; a `prompt_started` checkpoint validates against `docs/contracts/checkpoint.v1.schema.json`.
* `go test ./pkg/harness/adapters/acp/` and `./pkg/harness/adapters/claudeenv/` check what each adapter reports.
* `go test ./pkg/worker/` runs a Run whose harness timeout is shorter than its prompt timeout and checks the `prompt_started` checkpoint it sends.
* `go test ./pkg/store/` checks that limits are absent before a report, recorded once on Run detail, the Run list and Work Item facts, kept out when the start time is skewed, and stored as no limit when out of range.
* `go test ./pkg/httpapi/` posts the checkpoint with a Run's control token and reads `limits` from Run detail, the Run list, Work Item detail and Work Item facts against `docs/contracts/operator-api.v1.schema.json`, and finds no `limits` before it.

## Pros and Cons of the Options

### ploegd derives the limits from its own configuration at claim

* Good, because it would not depend on a worker reporting.
* Bad, because ploegd does not hold the timeouts: the chart passes them only to the worker's environment, as it did the harness before [ADR-0097](0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md).
* Bad, because it cannot know when the prompt starts, so the limit would count from the claim.

### The worker adds the limits to its claim request

* Good, because it adds no report.
* Bad, because the prompt has not started at the claim; the start time would still need a second report.
* Bad, because the remaining harness timeout is known only once the Harness runs.

## Re-evaluation triggers

* ploegd learns each Role's timeouts from its own configuration, so the worker's report is no longer the only source.
* A Harness gains a limit of its own that stops a Run, such as a turn or token cap, or runs more than one prompt turn per Run.
* An operator consumer needs the agent's last activity time from Ploeg rather than from the transcript.

## More Information

* Extends [ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md); the same write-once, unknown-stays-unknown rules apply.
* Migration `pkg/store/migrations/0059_run_limits.sql`.
* 2026-10-18: proposed.
