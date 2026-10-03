---
status: proposed
date: 2026-09-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# The OpenAI Agents API stays outside the Run until it can take an authorized budget; Unfold meets it over MCP and runs Codex itself

## Context and Problem Statement

On 2026-09-10 OpenAI released the Agents API in public beta. It is its Codex
harness, hosted by OpenAI, with durable sessions, subagents, context
compaction, hosted or self-hosted sandboxes and an MCP client. The harness and
the inference always run on OpenAI's service; only the sandbox that executes
commands can be self-hosted. It is the first large-vendor product that sells
the agent loop Ploeg's harness adapters run, and it is priced at zero beyond
tokens and container time.

The question is whether Ploeg should run Runs on it, support it some other
way, or ignore it.

## Decision Drivers

* **Admission before spend.** A Run's budget is authorized before it starts
  and enforced at a gateway the agent cannot bypass
  ([0012](0012-two-level-budgets-authorized-and-settled.md),
  [0008](0008-litellm-is-the-credential-and-metering-seam.md)).
* **The owner's stack stays self-hosted, EU-resident and model-neutral.**
* **Thin glue.** Consume the layers above and below Ploeg rather than rebuild
  them ([0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)).

## Considered Options

* Add an `openai-agents` harness adapter that drives hosted sessions now
* Reach OpenAI's harness through the open-source Codex under Ploeg's own
  gateway, and let Agents API sessions reach Unfold over MCP
* Ignore it

## Decision Outcome

Chosen option: **reach OpenAI's harness through the open-source Codex, and meet
the Agents API from above over MCP**, because the hosted API takes the
inference and credential seam with it.

The hosted API fails the drivers on three points:

* **Inference cannot be routed through LiteLLM**, so no per-Run key exists and
  the gateway cannot hard-stop a Run.
* **No budget can be set per session.** The only hard caps are monthly
  organization and project limits, which are "not instantaneous". Usage is
  "best-effort… not a final bill".
* **Data residency is the United States only, with no Zero Data Retention**,
  even with a self-hosted sandbox.

Concretely:

* **Codex.** Backlog #64's Codex profile, through `codex-acp` with inference
  through LiteLLM's `/v1/responses`, remains the path to OpenAI's harness. It
  keeps the per-Run key, the budget and the model choice.
* **MCP.** The north-facing MCP server being drafted on branch
  `docs/mcp-access` names the Agents API as a target client. The Agents API
  reaches HTTP MCP servers from OpenAI's side with Vault-held credentials, so
  an OpenAI-built assistant can read Unfold's state and propose work. Proposed
  work is held for approval
  ([0031](0031-runs-create-work-items-held-for-approval-within-limits.md)), and
  admission and budget stay in Ploeg.
* **No adapter yet.** No `openai-agents` harness adapter is built until a
  re-evaluation trigger fires.

### Consequences

* Good, because every Run keeps an authorized, gateway-enforced budget, and the
  review can point to a missing API field rather than to a judgement.
* Good, because Unfold gains an OpenAI-facing surface for the cost of an MCP
  server it was already designing.
* Bad, because Unfold gives up the managed harness's compaction, in-session
  recovery, subagents, hosted browser and web search until Codex, run by
  Ploeg, provides whichever of them the open-source harness carries.
* Bad, because once OpenAI documents a settable session budget, a GitHub-only
  competitor built on it can claim a per-run cap. Ploeg's claim must then rest
  on a Shift-wide, multi-provider, authorize-then-settle budget plus
  self-hosting, and a per-run cap alone stops being a differentiator.
  [0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)
  carries this as a trigger.

### Confirmation

* `go test ./internal/ledger/` validates this record and its Records row.
* No `github.com/openai/openai-go` import appears under `apps/ploeg/`, and no
  `case "openai-agents"` appears in `pkg/worker/adapters.go`. A reviewer checks
  any proposal that adds either against this record's triggers.

## Pros and Cons of the Options

### Add an `openai-agents` harness adapter now

* Good, because it fits the `Adapter` interface mechanically. `openai-go/v3`
  has the client and a streaming helper, `codex exec-server` can run in the Run
  pod against the cloned repository, and the drop box and forge poll already
  settle the outcome.
* Bad, because the only enforcement left is cancelling a turn when best-effort
  streamed usage crosses the hold. That is a soft stop, the kind
  [0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)
  disqualifies elsewhere.
* Bad, because worker egress must open `api.openai.com` and a `chatgpt.com`
  WebSocket, the environment key is readable by agent code, and client code
  leaves the EU.

### Ignore it

* Good, because it costs nothing.
* Bad, because it is now the largest vendor's reference shape for the layer
  under Ploeg, and its budget and residency gaps are the ones Ploeg's claims
  depend on. They need watching with named triggers.

## Re-evaluation triggers

Any one of these reopens this record:

* `CreateAgentSessionParams` in `openai/openai-openapi` gains a budget,
  max-cost or max-tokens field.
* `/v1/agents` appears in the EU data-residency tables or becomes ZDR-eligible.
* The Agents API accepts a custom inference base URL or gateway.
* Amazon Bedrock Managed Agents lists an EU region.
* The Agents API's GA contract is published, meaning the
  `OpenAI-Beta: agents=v1` header is no longer required.
* The quarterly market re-scan (`design.md` §10) on 2026-10-31.

## More Information

* Evidence trail:
  [research/2026-09-30-openai-agents-api-fit.md](../research/2026-09-30-openai-agents-api-fit.md).
* Re-confirms [0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md)
  ("`agent.session.idle` alone does not mean success") and
  [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)
  (OpenAI's Vaults use the same placeholder design and leave self-hosted
  sandboxes to "a trusted proxy… you provide").
* Backlog #64 (Codex via codex-acp), #82 (OTel).
