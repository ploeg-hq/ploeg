# The advisor tool and Runs that call more than one model

Checked on 2026-09-30. Decision: [ADR-0039](../adrs/0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md).

## The question

A Reddit thread (r/ClaudeCode, "Opus 5.5 + Fable 5.1 as automatic Advisor", September 2026) describes three patterns:

1. An executor model consults a stronger advisor model mid-task, when the executor decides to (Claude Code's `/advisor`).
2. A reviewer from another model family checks the builder's work. The commenters' claim: "the reviewer needs to be a different model. Same model reviewing itself is mostly vibes."
3. A supervisor agent assigns lanes, and each lane gets the model its stakes justify.

Which of these fit Ploeg, and what would the advisor need?

## What Ploeg already has

| Pattern | In Ploeg | Where |
| --- | --- | --- |
| Reviewer from another family | Yes, in production | homelab-cluster `kubernetes/apps/ploeg/ploeg/app/helmrelease.yaml` on 2026-09-30: bronze builds on `fireworks-deepseek-v4p1-flash` and silver on `deepseek-chat`, both review on `fireworks-glm-5p3-flash` |
| Model per lane | Yes | A Role's `model` in the chart becomes the worker's `LLM_MODEL` (`ops/helm/ploeg/templates/_helpers.tpl`) |
| Supervisor agent | Deliberately no | ploegd advances Shifts from Run state (R2, [ADR-0017](../adrs/0017-the-review-loop-is-verdict-driven-and-capped.md)) |
| Architect consulted when stuck | Proposed | [ADR-0036](../adrs/0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md) T0 premise check and T2 lead brief |
| Advisor mid-Run | No | This note |

Runs pass findings forward through the pull request ([ADR-0011](../adrs/0011-the-pull-request-is-the-blackboard.md)). No Run can talk to another Run while it is running.

## The advisor tool

Sources: [Claude Code advisor](https://code.claude.com/docs/en/advisor), [Claude API advisor tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool), [LLM gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol).

- The request carries a tool `{"type":"advisor_20260301","name":"advisor","model":"<advisor id>"}` and the beta header `advisor-tool-2026-03-01`. Anthropic runs the advisor server-side.
- The advisor reads the whole transcript on every call, and that read is not cached. The executor decides when to call it. Claude Code has no setting that caps or forces calls.
- Top-level `usage` counts executor tokens only. Advisor tokens arrive as `usage.iterations[]` entries with `type: "advisor_message"` and their own `model`. Anthropic bills them at the advisor's rates.
- Claude Code turns the advisor on through `advisorModel`, `--advisor` or `/advisor`. `CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1` turns it off and makes Claude Code ignore `advisorModel`.
- It needs the Anthropic API. Bedrock, Vertex and Foundry do not have it.

## LiteLLM v1.102.1 (the deployed gateway)

Verified in the source at tag `v1.102.1` (commit `d09bbae1`). Nothing was tested live.

- **Forwarding works.** PR [#25525](https://github.com/BerriAI/litellm/pull/25525) added the tool type. On an `anthropic/*` deployment the tool goes through unchanged, and LiteLLM adds the beta header (`llms/anthropic/experimental_pass_through/messages/transformation.py:729-734`).
- **Advisor tokens are priced at the executor's rate.** `calculate_usage` (`llms/anthropic/chat/transformation.py:2352-2357`) sums `input_tokens` and `output_tokens` over every entry in `usage.iterations`. It does not read an entry's `type` or `model`. `completion_cost` then prices the sum at the request model's rate. No code in LiteLLM prices tokens per iteration or per model. The rollout issue [#25516](https://github.com/BerriAI/litellm/issues/25516), which listed per-model advisor pricing, was closed as not planned.
- **The size of the error.** At the cost map's prices, an Opus 5.5 executor with a Fable 5.1 advisor (US$10/US$50 per million against US$4/US$20) under-counts the advisor's share by about 60%. With Opus 5 as the advisor it is about 20%. An advisor on the same model as the executor is priced correctly.
- **Streaming is less certain.** The main streaming path probably sums the same way. The fallback path that rebuilds usage from raw SSE (`anthropic_passthrough_logging_handler.py`) ignores `iterations`, so on that path advisor tokens are not counted at all.
- **The key's model scope does not reach the advisor.** Nothing under `proxy/auth` reads the tool definition, so a key scoped to one model can name any advisor. This is inferred from the missing code.
- **The deployed `model_list` has no 5.5-generation model.** It lists `claude-opus-5` and `claude-fable-5`, not `claude-opus-5-5` or `claude-fable-5-1`. Its fallbacks (`claude-fable-5 → claude-opus-4-8`) could produce an executor and advisor pair that Anthropic rejects.

Ploeg's per-model settlement split (`usage.byModel`, commit `6b22d96`) cannot see advisor spend either. Advisor tokens land inside the executor's spend-log entry, under the executor's model name.

## Harness-native subagents

- **claude-code.** Until this change the adapter set only `ANTHROPIC_MODEL`. A subagent or background task that asked for `haiku`, `sonnet`, `opus` or `fable` resolved to Claude Code's built-in model IDs, and the Run's key, scoped to one model, would reject those calls. Nothing in the logs shows whether that happened, because no Team currently runs `claude-code`. The adapter now pins all four aliases to the Run's model.
- **acp/opencode.** The generated config registers exactly one model (`adapters/acp/profiles.go`), so a `task` subagent can only use that model.
- **openhands, qwen-code, goose.** Each is configured with one model.

## What a live spike has to show before the advisor is enabled

1. Add the executor and advisor models to LiteLLM's `model_list` in homelab-cluster, and confirm the running pod's cost map prices both.
2. With a key scoped to the executor model, send one non-streaming and one streaming `/v1/messages` request with the advisor tool and a different advisor model. Use a prompt that forces a consultation.
3. Compare Anthropic's `usage.iterations` with the `LiteLLM_SpendLogs` row (`prompt_tokens`, `completion_tokens`, `spend`), and measure the gap for each path.
4. Confirm pings pass during the advisor pause and nothing trips `stream_timeout: 120`.
5. Name an advisor model outside the key's scope, and record what gets through.
6. Run Claude Code v2.1.280 or later through the proxy with `--advisor`, and confirm it does not silently retry without the tool.
