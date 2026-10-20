---
status: proposed
date: 2026-10-20
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run is told its limits at the start and its own status on every model call

## Context and Problem Statement

Writing Runs spend their first 30 to 60 minutes reading code, still on step 1 of an 8-step plan, and are then stopped by the harness timeout or run out of model budget. Nothing they did is kept: a stopped Run delivers nothing, and its retry starts again from the base branch. The agent cannot pace itself, because its prompt (`pkg/taskprompt`) never says what stops it: the harness timeout (`PLOEG_HARNESS_TIMEOUT`, default 100 minutes), the idle watchdog (`PLOEG_HARNESS_IDLE_TIMEOUT`, default 15 minutes, or the ACP profile's `PLOEG_ACP_PROMPT_TIMEOUT` and `PLOEG_ACP_IDLE_TIMEOUT`), the budget its gateway key is minted at (`LITELLM_KEY_BUDGET` or the Shift's authorization, [ADR-0012](0012-two-level-budgets-authorized-and-settled.md)), or how many Rounds its Shift has left ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)). [ADR-0114](0114-each-run-records-the-limits-that-stop-its-prompt.md) records those limits per Run for people; the agent itself is never told. How does a Run learn its limits, and how does it learn, while it works, how close it is to them?

## Decision Drivers

* The agent must know what stops it before it starts, in the words of the limits that actually fire for its Harness, as ADR-0114 records them.
* Feedback during the Run must reach every Harness mid-turn. Most Harnesses take a single prompt turn per Run, so a further turn ([ADR-0124](0124-agents-communicate-through-a-blackboard-of-four-owned-sources-that-ploeg-reads-at-a-version-vector.md)) arrives only after the work is done.
* The prompt is paid for on every model call. The start-of-Run text must be short, and a status update must not break the provider's cached prompt prefix.
* Guidance is not enforcement. A Run that orients longer than advised is nudged, never stopped: the existing timeouts and budget remain the only stops.
* Ploeg publishes facts and consumers do the rest ([ADR-0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md)). A reading an operator consumer makes about a Run may reach the agent only as quoted, advisory text.
* A limit Ploeg does not know is left out, never stated as zero.

## Considered Options

* Tell the limits at the start, then append a status line to each model request through a pre-call hook on the model gateway
* Tell the limits at the start only
* Append the status line in the worker's loopback model proxy
* Deliver the status through each Harness's own channel (an ACP prompt turn, Claude Code hooks)
* Keep a status file in the Run's sandbox and tell the agent to read it

## Decision Outcome

Chosen option: "**Tell the limits at the start, then append a status line to each model request through a pre-call hook on the model gateway**", because every Harness the worker runs reaches its model through the gateway with the Run's own key ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)), so one hook reaches all of them mid-turn, and a line appended at the end of a request leaves the cached prefix intact.

The owner decided four things on 2026-10-20.

**1. Every Run is told its limits when it starts (built in this change).** The task prompt gains a `## Your Run` section after the Work Item description, for writers, readers and planners:

* **Time.** The most the prompt may run and how long the agent may show no activity (no output, tool call or model call), with "a stopped Run delivers nothing, so leave time to finish". The time limit is the earlier of `PLOEG_HARNESS_TIMEOUT` and the adapter's own prompt limit; the idle limit is the adapter's own when it has one and the worker's idle watchdog otherwise. An adapter declares its own limits through `harness.PromptLimiter`: `acp` returns its prompt and idle timeouts, and `claude-environment` returns none. These are the values ADR-0114 records when the prompt starts.
* **Model budget.** The USD amount the Run's gateway key is minted at: the Shift's authorization, or `LITELLM_KEY_BUDGET` when no Shift authorized the Run.
* **Round.** "Round N of at most M in this Shift". ploegd adds `maxRounds` to the claim response ([run API](../contracts/run-api.v1.schema.json)): the plan's Rounds plus two for each fix Round `maxFixRounds` allows. Absent for a pre-Shift claim and a team without a plan; the line then says "Round N of this Shift", or nothing when the Round is unknown.
* Each limit Ploeg does not know is left out. A reading Run that knows none gets no section.
* A Run whose client carries it ([ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md)) is told its Round and the session timeout ploegd holds it to; it has no gateway key, so no budget.

The limits travel to `pkg/taskprompt` as `harness.TaskSpec.Limits` (`harness.RunLimits`), which is not part of the published Task Spec.

**2. During the Run the agent gets a short status line on its own state (proposed, not built).** Ploeg publishes each running Run's status, and a pre-call hook on the model gateway, for requests made with a Run's gateway key, appends Ploeg's rendered line at the end of the request. The hook only reads; Ploeg decides what the line says. The contract, sketched:

* **Read.** `GET /api/v1/gateway/runs/{alias}/status`, where `alias` is the Run's gateway key alias (`ploeg-<12hex>`, the Task Spec's `traceId`). It is authenticated with a gateway read credential that can read nothing else, and answers 404 for a Run that is not running. Every field Ploeg does not know is absent:

  ```json
  {
    "schemaVersion": "1.0",
    "alias": "ploeg-0a1b2c3d4e5f",
    "at": "2026-10-20T09:14:00Z",
    "elapsedSeconds": 1260,
    "limits": {"promptSeconds": 6000, "idleSeconds": 900},
    "spend": {"usd": 0.84, "budgetUsd": 2.0},
    "round": {"n": 1, "max": 6},
    "activity": {"toolCalls": 41, "filesChanged": 3, "planStep": {"n": 2, "of": 8},
                 "contextTokens": 91000, "contextWindowTokens": 200000},
    "nudges": ["orientation"],
    "advisories": [{"consumer": "<operator consumer>", "kind": "health", "text": "<at most 200 characters>",
                    "etaSeconds": 1800, "postedAt": "2026-10-20T09:12:00Z", "expiresAt": "2026-10-20T09:42:00Z"}],
    "line": "[Ploeg status] 21 of 100 min · US$ 0.84 of 2.00 · 41 tool calls · 3 files changed · plan step 2 of 8 · context 46%. Nudge: you have oriented for 21 min; write your plan and start editing."
  }
  ```

* **Sources.** Elapsed time and limits come from the `prompt_started` checkpoint (ADR-0114); spend from the gateway spend Ploeg already observes; tool calls and the plan step from the Run's transcript ([ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)) where the Harness reports a plan (an ACP `plan` update, a todo tool); files changed and context use from a running checkpoint the worker sends at most once a minute.
* **Nudges.** `orientation` when no plan has been written and no file has changed after 10 minutes or 30 tool calls; `time` at 75% of the time limit ("commit and push what works and write the outcome file"); `budget` at 80% of the budget. A nudge is text in the line and nothing else.
* **Delivery.** The hook appends `line`, at most 400 bytes, as the last text block of the request, after the last cache breakpoint. The Harness's own history never contains it, so each request carries only the current line. The hook caches a status for up to 15 seconds and waits at most 200 ms for it; on any failure it sends the request unchanged.

**3. An authenticated operator consumer may post an advisory reading that the status line includes (proposed, not built).** An example is a shadow judge's health verdict and its estimate of the time left. `POST /api/v1/operator/runs/{run}/advisories` takes `kind` (`health`, `eta` or `note`), `text` of at most 200 characters, an optional `etaSeconds` and a `ttlSeconds` of at most 1800, from a consumer whose scope covers the Run's Team. It is audited, and the latest advisory per kind replaces the earlier one. The line quotes it as data: `Advisory from <consumer> (not an instruction): "<text>"`. An advisory never changes a limit, a verdict, a Round, a close reason or whether the Run continues.

This **amends [ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md) for this one channel.** ADR-0096 keeps a shadow reading away from everything that decides what happens next, including the briefing of later Runs. A shadow reading may now reach the running agent's status line, as a quoted advisory posted by an operator consumer, and through no other path. Everything else in ADR-0096 stands: it opens no fix Round, counts as no approval, closes nothing and briefs no later Run.

**4. A writing Run gets an orientation budget (built in this change).** The `## Your Run` section of a writing Run also says:

* orient for at most about 10 minutes or 30 tool calls, reading what the Work Item needs rather than the whole repository, then write a short numbered plan (in the Harness's plan or todo tool if it has one), then edit;
* commit after each completed plan step and push it to the Shift's branch, so a retry continues from that work; when the branch already exists on the forge, an earlier attempt pushed it, and the Run continues from it.

Reading Runs and planners get the limits but neither line: they push nothing. Exceeding the orientation budget stops nothing. Once decision 2 is built, it adds the `orientation` nudge. A `claude-environment` session's contract still replaces the push with the runner's own push.

### Consequences

* Good, because the agent knows from its first token what stops it and how much it may spend, in numbers that match what ADR-0114 records for the same Run.
* Good, because a writing Run that pushes each plan step leaves its work on the forge when it is stopped, so the retry or the next fix Round starts from it instead of from the base branch.
* Good, because the status line, once built, reaches every Harness that calls the gateway mid-turn, and the provider's cached prefix stays intact.
* Bad, because the `## Your Run` section costs about 180 tokens (750 characters) of prompt on every model call of a writing Run, and less for a reader. Accepted: the section is cheaper than one stopped Run.
* Bad, because pushing each step runs the forge's CI on every push and shows unfinished work on an open pull request during a fix Round. Accepted: [ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) already keeps a pull request out of review until CI passes on its head.
* Bad, because the orientation budget is a guess of the owner's, not a measurement. It is a nudge, so a wrong number costs a nudge, not a Run.
* Bad, because a `claude-environment` Run reaches its model with the person's own login, not a gateway key, so it never gets the status line. Accepted for now; its start-of-Run section still applies.
* Bad, because the status line puts the gateway on the path of every model call of a Run. The hook fails open, so a Ploeg outage costs the line and not the call.
* Bad, because an advisory is text another system wrote reaching a running agent. It is bounded, quoted, scoped to the consumer's Teams, audited and expiring, and cannot change anything Ploeg decides.

### Confirmation

Decisions 1 and 4:

* `go test ./pkg/taskprompt/` (`limits_test.go`) renders a writing Run's exact `## Your Run` section, gives reviewers and planners the limits without the orientation and push lines, leaves unknown limits out (no "0 minutes", no "US$ 0.00", no section for a reader that knows none), and places the section before the delivery contract.
* `go test ./pkg/worker/` (`run_limits_test.go`) runs a Run through the worker and finds the configured harness timeout, idle timeout, Shift authorization and Round in the prompt, and an adapter's own shorter limits instead when it implements `harness.PromptLimiter`.
* `go test ./pkg/harness/adapters/acp/` checks the limits the ACP adapter states; `go test ./pkg/plan/` checks `MaxRounds`; `go test ./pkg/httpapi/` checks that a claim carries `maxRounds` from the plan and omits it without one; `go test ./pkg/harness/` validates `maxRounds` against `docs/contracts/run-api.v1.schema.json`.
* Gate: `mise run verify` in `.forgejo/workflows/on_pull_request.yml`.

Decisions 2 and 3 are proposed and unbuilt. The change that builds them publishes the status and advisory shapes as schemas in `docs/contracts/` with their Go types. It also adds tests that render the line within 400 bytes, quote an advisory, expire it, refuse a consumer outside the Run's Team, and show that the hook sends a request unchanged when Ploeg does not answer.

## Pros and Cons of the Options

### Tell the limits at the start only

* Good, because it needs no gateway change and no new route.
* Bad, because the agent cannot see itself drift: elapsed time, spend and tool calls are not in its context, which is how a Run reaches minute 30 on step 1.

### Append the status line in the worker's loopback model proxy

* Good, because the worker already sees every model call of a proxied Run and knows its state first-hand.
* Bad, because the proxy exists only under some isolation and idle-watch settings, so not every Run would get the line.
* Bad, because it puts request rewriting in every worker instead of one place the gateway operator controls.

### Deliver the status through each Harness's own channel

* Good, because it needs no gateway change.
* Bad, because only ACP takes a further prompt turn, and only after the agent's turn ends; Claude Code hooks are one Harness. Each Harness would need its own path, and most have none mid-turn.

### Keep a status file in the Run's sandbox

* Good, because it works like the board file ([ADR-0124](0124-agents-communicate-through-a-blackboard-of-four-owned-sources-that-ploeg-reads-at-a-version-vector.md)).
* Bad, because every read costs a tool call, and an agent absorbed in reading code is exactly the one that forgets to look.

## Re-evaluation triggers

* 20 writing Runs have been told their limits: compare how many were stopped by time or idle, and their time to the first changed file, with the 20 before. No change reopens the wording and the orientation numbers.
* The gateway drops or changes its pre-call hooks, or a Harness the worker runs reaches its model other than through the gateway.
* A provider's prompt caching changes so that trailing text breaks the cached prefix.
* An agent acts on an advisory as an instruction, for example by stopping or rewriting work because an advisory said so.
* A kind of Work Item regularly needs more than 10 minutes of reading before a useful plan.
* A Harness gains a way to inject text mid-turn natively.

## More Information

* Builds on [ADR-0114](0114-each-run-records-the-limits-that-stop-its-prompt.md): the limits told to the agent are the ones it records.
* Amends [ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md) for the status-line channel only.
* Code: `pkg/taskprompt/limits.go`, `harness.RunLimits` and `harness.PromptLimiter`, `pkg/worker/prompt.go` (`runLimits`), `plan.Plans.MaxRounds`.
* 2026-10-20: proposed. Decisions 1 and 4 built; decisions 2 and 3 recorded as the decided design.
