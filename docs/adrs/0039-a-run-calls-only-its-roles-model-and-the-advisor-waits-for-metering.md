---
status: proposed
date: 2026-09-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-12-31
---

# A Run calls only its Role's model, and the advisor waits for metering that prices it

## Context and Problem Statement

Coding agents increasingly use more than one model within a task. Claude Code's advisor tool lets the executor consult a stronger model server-side, whenever the executor chooses. Subagents can run on a different model from their parent. Practitioners also report that a reviewer from a different model family catches what the builder cannot see.

Ploeg already covers the last pattern with Roles. Each Role names its model, and in production both Teams build on DeepSeek and review on GLM. The other two patterns put a second model inside one Run. Two things in Ploeg assume a Run has only one model: the Run's key is scoped to one model (`worker.ModelList`), and settlement reads the gateway's spend log.

The research note ([2026-09-30](../research/2026-09-30-advisor-and-multi-model-runs.md)) found:

- LiteLLM v1.102.1 forwards the advisor tool.
- It prices the advisor's tokens at the executor model's rate. With a Fable 5.1 advisor on an Opus 5.5 executor, the advisor's share is under-counted by about 60%.
- The key's model scope does not cover the advisor model.
- Until this change, a claude-code subagent that asked for `haiku` resolved to a model outside the key's scope.

May a Run call more than one model, and under what condition may the advisor be switched on?

## Decision Drivers

* [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md): spend is measured at a boundary the agent talks through, not parsed from what it says it did.
* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md): money is bounded before it is spent. A cap that under-counts the spend it caps is not a bound.
* R2: an agent reports and ploegd decides. An executor that decides when to consult a stronger model is a spend decision the agent makes, so a gateway-enforced bound has to hold it.
* The value that practitioners claim and that we can observe comes from a *different* model reviewing the work. Ploeg already provides that through Roles and Rounds.
* No Team runs `claude-code` today, so the advisor has no production user to weigh against these costs.

## Considered Options

* **Keep a second model in its own Role; keep the advisor off until the gateway prices it; pin native subagents to the Run's model**
* Enable the advisor now and accept the under-count
* Allow the advisor only when it is the same model as the executor
* Re-price advisor iterations from what the worker's loopback proxy observes
* A Ploeg-run consult tool that opens a reading Run on a stronger model mid-Run

## Decision Outcome

Chosen option: "**keep a second model in its own Role; keep the advisor off until the gateway prices it; pin native subagents to the Run's model**". It keeps every dollar a Run spends visible to the gateway at the right rate, and the multi-model pattern with evidence behind it already runs in production.

1. **A Run calls only its Role's model.** The claude-code adapter sets `ANTHROPIC_DEFAULT_HAIKU_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_OPUS_MODEL` and `ANTHROPIC_DEFAULT_FABLE_MODEL` to the Run's model. A subagent or background task that asks for an alias then stays on the model the key allows. The acp/opencode profile already registers one model, and the other adapters take one model.
2. **Put a second opinion from another model in a Role.** A reviewer from another family is a reading Role with its own `model` in the Team's plan. Its findings travel on the pull request ([ADR-0011](0011-the-pull-request-is-the-blackboard.md)), and its verdict drives the loop ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)).
3. **The advisor is off in every Run.** The claude-code adapter always sets `CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1`. With that set, Claude Code ignores `advisorModel` from any settings file, including the target repository's own.
4. **The advisor may be switched on only by a later record, and only when all of these hold:**
   * the gateway records advisor iterations at the advisor model's rate, or as separate spend entries, on both the streaming and the non-streaming path, as shown by the live spike in the research note;
   * the advisor model is in the Role's policy `models`, and the gateway rejects an advisor that is not;
   * `usage.byModel` shows the advisor's share separately, so its value can be measured against the Role's baseline.

### Consequences

* Good, because a Run's settled spend stays the gateway's number at the gateway's prices, and the per-Run cap still bounds everything a Run can call.
* Good, because a target repository cannot turn on a stronger, under-metered model through its `.claude/settings.json`.
* Good, because a claude-code subagent no longer fails against the key's model scope. It also no longer silently switches models.
* Bad, because a claude-code Run loses the option to use a cheaper model for background tasks and subagents. Accepted: the Role's model is the one its cap was sized for.
* Bad, because the advisor's claimed benefit, strong judgement at decision points without strong-model prices on every turn, is not available to any Team. Accepted: no Team runs claude-code, and a reviewing Role from another family already gives the second opinion at Round boundaries.

### Confirmation

* `TestPrepare_PinsEveryModelAliasToTheRunModelAndDisablesTheAdvisor` and `TestPrepare_NoKeyNoEnv` in `pkg/harness/adapters/claudecode/claudecode_test.go` check that every alias is pinned and that the advisor is off, with a key and without one.
* `TestControllerSettlementRecordsGatewayUsageOnTheRun` in `pkg/httpapi/llm_settlement_usage_test.go` checks that a Run that called two models settles with a share for each in `usage.byModel`.
* Gate: `go test ./...` in `.forgejo/workflows/on_pull_request.yml`, run locally through `mise run verify`.

## Pros and Cons of the Options

### Enable the advisor now and accept the under-count

* Good, because it is one flag, and Claude Code handles the rest.
* Bad, because the gateway would under-count the Run's spend by an amount set by how often the executor chooses to consult. That breaks ADR-0008 and ADR-0012 in the one place Ploeg touches money.

### Allow the advisor only when it is the same model as the executor

* Good, because LiteLLM prices it correctly.
* Bad, because it is the same model reviewing itself, the pattern the evidence says adds least. A reviewing Role from another family already does better.

### Re-price advisor iterations from the worker's loopback proxy

* Good, because the proxy ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)) sees every response, including `usage.iterations`.
* Bad, because a figure counted inside the Run's pod is an observation, not settlement. The glossary's Inference Account says so, and ADR-0008 puts the metering boundary at the gateway.

### A Ploeg-run consult tool

* Good, because it would work on every harness, and its spend would be a Run of its own, metered like any other.
* Bad, because it would give Runs a live channel to other Runs. That reopens [ADR-0011](0011-the-pull-request-is-the-blackboard.md) and [ADR-0007](0007-a2a-adopt-nothing-watchlist-a-facade.md), and it needs a tool in every harness image, which ADR-0011 ruled out.

## Re-evaluation triggers

* A LiteLLM release prices `usage.iterations` per model, or writes advisor iterations as their own spend-log entries. Watch [BerriAI/litellm#25516](https://github.com/BerriAI/litellm/issues/25516) and the release notes after v1.102.1.
* A Team's plan puts a Role on `claude-code` with an Anthropic model.
* A second harness (opencode, OpenHands) ships an advisor or consult feature of its own.
* A reviewing Role misses, in two or more Shifts, a defect that a mid-Run consultation would have caught, as recorded on the pull request.

## More Information

* Evidence: [2026-09-30 advisor and multi-model Runs](../research/2026-09-30-advisor-and-multi-model-runs.md).
* The per-model settlement split: commit `6b22d96`, [managed workers](../ops/managed-workers.md) and the per-model query in [KPIs](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/reference/kpis.md).
* Related: [ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md) for the strong-model lead that runs when a Work Item is stuck.
