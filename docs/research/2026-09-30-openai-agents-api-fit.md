# OpenAI's Agents API × Glide — fit dossier

> Surveyed 2026-09-30, twenty days after the public beta, via four parallel
> research agents: a crawl of every Agents API guide and reference page on
> developers.openai.com (through their `.md` twins), a source dig of the
> OpenAI SDK, Agents SDK, Codex and OpenAPI repositories with `gh`, an
> ecosystem and absence sweep, and a read-only seam map of this repository.
> The load-bearing claims (US-only residency, no settable session budget, the
> harness is never self-hosted, the Go client and its missing budget field)
> were re-checked by hand against the downloaded pages and a clone of
> `openai-go` v3.68.0. The openai.com launch post returned 403 to every fetch,
> so the launch wording comes from the changelog and the official community
> announcement instead. The decision is recorded in
> [ADR-0041](../adrs/0041-the-openai-agents-api-stays-outside-the-run-until-it-takes-an-authorized-budget.md).

**Verdict: the Agents API is a hosted agent loop, one layer below Ploeg. Do not
run Glide's Runs on it yet. Meet it from above through MCP, and use OpenAI's
harness through the open-source Codex instead.** It competes with Ploeg's
harness adapters (OpenHands, opencode, Claude Code), not with Ploeg.

It would remove the control that justifies building Ploeg:

- Inference goes straight to OpenAI. There is no gateway hook, so the per-Run
  LiteLLM key that enforces an authorized budget
  ([ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md)) cannot
  exist.
- It sets no per-session budget. An error code for one exists, but no field sets
  it.
- Its data residency is the United States only, and it has no Zero Data
  Retention.

What it does well (compaction, recovery, subagents, steering, hosted sandboxes,
secret placeholders) sits inside the Run. That is the layer Ploeg deliberately
leaves to the harness.

It confirms the bet in
[ADR-0032](../adrs/0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md):
the agent loop is becoming a commodity, and admission, authorized spend and
delivery are what remain scarce. It also adds one trigger to that record. The
`session_budget_exceeded` error code suggests OpenAI will ship a per-session
budget, which is the nearest thing yet to Ploeg's primary differentiator.

Both verdict axes are stated separately:

- **Maturity** does not decide this: it is a public beta, single-vendor, and
  changes every few days.
- **Fit** does: it claims the harness seam but takes the inference and
  credential seam with it.

## 1. What it actually is

The page at
[/api/docs/guides/agents](https://developers.openai.com/api/docs/guides/agents)
chooses between four runtimes. The **Agents API** is new; the Agents SDK,
Responses API and ChatKit already existed.

- **Agents API:** "Run an agent with the Codex harness managed by OpenAI". Use
  it for "Long-running tasks where OpenAI manages the agent and saves its
  progress". Integration effort: Low.
- **Agents SDK:** "Control the agent loop in your application with reusable
  agents, tools, and handoffs". Integration effort: Medium.
- **Responses API:** "Work directly with model responses and control your
  integration". Integration effort: High.

**Self-description.** "The Agents API gives your application access to the
Codex harness through an OpenAI-managed API. OpenAI manages sessions,
orchestration, context compaction, and recovery while your application provides
tools and chooses its execution environment."
([overview](https://developers.openai.com/api/docs/guides/agents-api/overview))

**Status.**
- Public beta since 2026-09-10. Computer use was added 2026-09-29
  ([changelog](https://developers.openai.com/api/docs/changelog)).
- Every call needs `OpenAI-Beta: agents=v1`. The docs already refer to a "GA
  contract" that has not been published.
- The same harness has run on AWS as *Amazon Bedrock Managed Agents, powered by
  OpenAI* since a limited preview announced 2026-04-28
  ([AWS](https://aws.amazon.com/bedrock/managed-agents-openai/)).

**Abstractions**, as the docs spell them:
- **Agent:** model, instructions, tools and MCP servers. Saved (`agent_id`) or
  defined per session.
- **Environment:** `none`, `openai_hosted` or `self_hosted`.
- **Session** (`agent.session`): "a durable instance of an agent".
- **Turn:** one cycle of work. A message sent during an active turn *steers*
  that turn.
- **Events and items.**
- **Subagent:** at most `max_concurrent_subagents`, default 6. Subagents share
  the coordinator's filesystem.
- **Artifact:** files written to `/workspace/outputs` become immutable when the
  turn completes.
- **Vault / Credential.**
- **Environment template.**
- **Plugin** (`.codex-plugin/plugin.json`) and **Skill** (`SKILL.md` in
  `capability_directories`).

The Agents API has no Run, Handoff, Guardrail or Workflow. Those names belong
to the Agents SDK and to Agent Builder, which is deprecated and shuts down
2026-11-30 ([deprecations](https://developers.openai.com/api/docs/deprecations)).

**Lifecycle.**
- Session statuses: `idle`, `in_progress`, `requires_action`, `failed`.
- Turn statuses: `queued`, `in_progress`, `waiting`, `completed`, `failed`,
  `cancelled`.
- The only points where a person can step in are `required_actions`:
  `function_call`, `environment_connection` and
  `computer_use_approval_request`. There is no approval gate for shell commands
  or MCP calls.
- The docs are candid about outcomes: "`agent.session.idle` alone does not mean
  success" and "A completed turn does not guarantee every tool succeeded"
  ([quickstart](https://developers.openai.com/api/docs/guides/agents-api/quickstart)).
- Durability belongs to OpenAI and is partial: "The API does not guarantee
  recovery of pending input after a process crash", and the five-minute
  environment wait "does not provide a durable input queue"
  ([lifecycle](https://developers.openai.com/api/docs/guides/agents-api/environments/lifecycle)).

**Execution.**
- `openai_hosted` is a Linux workspace at `/workspace`.
  - Sizes: small 1 vCPU / 1 GB, medium 2 / 4 GB (default), large 4 / 16 GB.
  - Network egress is `enabled`, `disabled` or `restricted`, with up to 100
    allowed hosts.
  - An idle sandbox is deleted after an hour, and that timeout is not
    configurable.
- `self_hosted` means running `codex exec-server --remote <url> --environment-id
  <id>` from `@openai/codex@alpha` wherever you like.
  - The CLI labels it "[EXPERIMENTAL]".
  - It makes outbound connections only, to `api.openai.com` and
    `wss://codex-cloud-environments.chatgpt.com`, authenticated with a
    restricted environment key.
  - "Agent-generated code can read the environment key"
    ([self-hosted](https://developers.openai.com/api/docs/guides/agents-api/environments/self-hosted)).
  - Nine sandbox partners are documented: Blaxel, Cloudflare, Daytona,
    DigitalOcean, E2B, Modal, Oracle, Runloop and Vercel, plus AWS Lambda
    MicroVMs.
- **The harness itself never leaves OpenAI:** "Its managed harness and model
  inference still use the OpenAI service"
  ([Bedrock page](https://developers.openai.com/api/docs/guides/agents-api/bedrock-managed-agents)).

**Tools.**
- Types: `function`, `tool_search`, `programmatic_tool_calling` (on by default:
  a JavaScript `exec` tool), `mcp`, `web_search`, `computer_use`.
- Bash and apply-patch come with any environment.
- MCP is client-only: HTTP from OpenAI's side (`connection_origin: "service"`),
  HTTP from the sandbox, or stdio in the sandbox. Credentials go inline or in a
  Vault with OAuth refresh
  ([MCP](https://developers.openai.com/api/docs/guides/agents-api/tools/mcp)).
- Vault `environment_variable` credentials put a placeholder in the sandbox, and
  a proxy substitutes the real secret for allowed hosts. This works only in
  hosted sandboxes. For self-hosted ones, "configure a trusted proxy… This is
  infrastructure you provide"
  ([vaults](https://developers.openai.com/api/docs/guides/agents-api/tools/vaults)).

**Money.**
- "There are no additional fees for using the Agents API": you pay tokens,
  tools and container time
  ([announcement](https://community.openai.com/t/introducing-the-agents-api-and-hosted-sandboxes/1396481)).
- Container rates are $0.03 / $0.12 / $0.48 per 20 minutes for 1 / 4 / 16 GB,
  billed per minute with a five-minute minimum. Medium is about $0.36 an hour
  ([pricing](https://developers.openai.com/api/docs/pricing)).
- Every example uses `gpt-6-astra` at $10 input / $50 output per million tokens.
- **Spend controls**:
  - Organization and project hard limits, monthly, of which "Enforcement is not
    instantaneous"
    ([spend limits](https://developers.openai.com/api/docs/guides/spend-limits)).
  - `max_concurrent_subagents`.
  - The error `session_budget_exceeded` ("The session reached its usage
    budget") is listed in the
    [error table](https://developers.openai.com/api/docs/guides/agents-api/errors),
    but no field in `CreateAgentSessionParams` sets it. Confirmed in the
    OpenAPI spec and in `openai-go`'s `BetaAgentSessionNewParams`.
- Usage is "best-effort… can be `null`… not a final bill" and has no currency
  field
  ([observability](https://developers.openai.com/api/docs/guides/agents-api/observability)).

**Data.** "The Agents API currently supports data residency only in the United
States and does not support Zero Data Retention (ZDR). Choosing a self-hosted
sandbox does not make the Agents API ZDR-eligible"
([overview](https://developers.openai.com/api/docs/guides/agents-api/overview)).
- `/v1/agents` is absent from the `eu.api.openai.com` tables. Responses, Code
  Interpreter and remote MCP are present there.
- Abuse monitoring keeps data 30 days, and application state is kept "Until
  deleted" ([your data](https://developers.openai.com/api/docs/guides/your-data)).

**Observability.** Tracing is on by default. OTLP JSON export is pull-only:
`GET /v1/agents/sessions/{id}/traces`, which must be enabled per organization
([tracing](https://developers.openai.com/api/docs/guides/agents-api/tracing)).
Webhooks cover `created`, `action_required`, `in_progress`, `idle` and `failed`.

**SDKs and governance.**
- The spec entered
  [openai/openai-openapi](https://github.com/openai/openai-openapi/commit/df63773)
  on 2026-09-10, and four deltas followed in nineteen days.
- The generated clients shipped the same day:
  - **Go:** `github.com/openai/openai-go/v3` from v3.61.0. It exposes
    `client.Beta.Agents.Sessions.New` and a hand-written `Stream` helper that
    dispatches function tools to Go handlers.
  - **TypeScript:** `openai` v7.15.0.
  - **Python:** v3.13.0.
- `openai-go` is Stainless-generated and may break in minor releases.
- There is no Go Agents SDK, and a request for one was closed not_planned
  ([openai-agents-python#942](https://github.com/openai/openai-agents-python/issues/942)).
  Neither Agents SDK wraps the Agents API.
- All of it is OpenAI-controlled:
  - `openai/codex` states "We do not accept external code contributions or pull
    requests".
  - The Agents SDKs accept pull requests from collaborators only.
  - A2A was explicitly declined
    ([#472](https://github.com/openai/openai-agents-python/issues/472)).

**Adoption.** Shipped:
- the three SDKs;
- the cookbook apps (`github_issues`, `sev_bot`, `slack_bot`, `data_analyst`,
  `document_review`);
- partner code from Cloudflare, DigitalOcean and E2B.

Third-party application code calling `agents=v1` is a handful of small
repositories. Customer figures in the launch post (Ciridae, SafetyKit, Hypha)
are vendor-chosen quotes.

**Critiques.** The
[Hacker News thread](https://news.ycombinator.com/item?id=49649213) (2026-09-10)
leads with lock-in to a stateful API after the Assistants API was shut down on
2026-08-26. Other threads raise:
- the residency and ZDR gap
  ([StackOne](https://www.stackone.com/blog/openai-agents-api-zdr-residency/));
- whether sandboxes are billed while idle;
- whether `restricted` egress can be trusted;
- a 3-hour incident on 2026-09-14 in which turns could not start
  ([status](https://status.openai.com/incidents/01M2H3J1D6Y7RHAP49GRWGJAY0)).

## 2. Layer map

| Seam | Glide today | What Glide has bet on | What the Agents API claims |
| --- | --- | --- | --- |
| Intake: tracker to Work Item | Vikunja, ClickUp ([`pkg/provider`](../../pkg/provider/)) | tracker stays external ([ADR-0026](../adrs/0026-tracker-selections-bind-the-canonical-work-item.md)) | **nothing**: no tracker named anywhere |
| Admission and authorized spend | Shift pool; each Run authorized, then settled ([ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md)) | primary differentiator ([ADR-0032](../adrs/0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)) | **nothing settable**: monthly project limits; a `session_budget_exceeded` code with no parameter behind it |
| Dispatch, lease and recovery | Postgres lease with TTL, sweep ([`pkg/store`](../../pkg/store/)) | [ADR-0010](../adrs/0010-shift-owns-the-item-lease-owns-the-branch.md) | its own session durability, which "does not guarantee recovery of pending input" |
| Runtime: where commands run | KEDA Job per Run, agent-sandbox executor ([`pkg/sandboxlaunch`](../../pkg/sandboxlaunch/)) | `agents.x-k8s.io` v1beta1 (ADR-0032) | **competing**: hosted sandbox, or `codex exec-server` in yours |
| Harness: the agent loop | OpenHands, Claude Code, ACP profiles, exec ([`pkg/harness`](../../pkg/harness/)) | ACP ([ACP profiles](../contracts/acp-profiles.md)) | **competing**: the Codex harness, hosted by OpenAI only |
| Inference credential and metering | per-Run LiteLLM key ([ADR-0008](../adrs/0008-litellm-is-the-credential-and-metering-seam.md)) | LiteLLM | **takes it over**: inference is OpenAI's; no base URL, no gateway |
| Secrets inside the Run | worker proxy swaps placeholders ([ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)) | ADR-0034 (proposed) | the same design, hosted sandboxes only |
| Tools | none by default; Claude Code runs with `--strict-mcp-config` and ACP sessions get no MCP servers | [ADR-0030](../adrs/0030-target-repository-instructions-rank-below-the-delivery-contract.md) | MCP client, web search, computer use, programmatic tool calling |
| Delivery: branch, PR, forge truth | Forgejo, GitLab; the forge poll is ground truth ([ADR-0018](../adrs/0018-the-outcome-drop-box-is-every-harnesss-return-path.md)) | [ADR-0011](../adrs/0011-the-pull-request-is-the-blackboard.md) | **nothing**: artifacts only; the cookbook's GitHub app "never opens a pull request" |
| Review loop | verdict-driven, capped ([ADR-0017](../adrs/0017-the-review-loop-is-verdict-driven-and-capped.md)) | the PR is the blackboard | subagents inside one session, sharing one filesystem |
| Human steering | Vloer sessions: pause, resume, answer permissions | [ADR-0024](../adrs/0024-operator-work-uses-one-execution-authority.md) | steer a turn mid-flight; `required_actions` |
| Observability | `/metrics`, audit log; no OTel yet (backlog #82) | Prometheus | OTLP JSON pull per session |
| North-facing API | operator API; MCP server being drafted on branch `docs/mcp-access`; A2A watchlist ([ADR-0007](../adrs/0007-a2a-adopt-nothing-watchlist-a-facade.md)) | MCP first | **nothing**: MCP client only, no A2A, no ACP |

Four seams are claimed by nobody on OpenAI's side:
- intake;
- settable admission;
- delivery;
- a north-facing agent protocol.

Those four are the product Glide sells.

## 3. What it does that Glide does not

1. **Context compaction and in-session recovery.** A session survives across
   turns, and OpenAI compacts context. Ploeg treats harness-native state as
   opaque ([architecture §6](../architecture.md)), so a Run resumes only
   through the checkpoint contract.
2. **Steering a running turn.** A message sent mid-turn changes the agent's
   course. Ploeg's unattended Runs cannot be steered once started.
3. **Subagents inside one Run.** Up to six concurrent subagents share one
   workspace. Ploeg splits work into Roles and Rounds across Runs instead.
4. **Hosted tools.** Web search, a hosted browser for computer use, and
   JavaScript programmatic tool calling. Ploeg's workers reach only their model
   gateway and forge by design.
5. **A managed sandbox fleet with nine providers** and container sizes. Ploeg
   runs on the owner's own Kubernetes only.
6. **Secret placeholders with host-scoped substitution, as a product.** Ploeg
   has the same design (ADR-0034), but proposed and opt-in.
7. **Per-session OTLP trace export and a trace dashboard.** Ploeg has no OTel
   (backlog #82).
8. **SDKs in five languages**, including Go.

## 4. What Glide does that it does not

1. **Refuses unaffordable work before it starts.** Each Run is authorized at
   `min(roleCap, pool − spent − reserved)`. The gateway hard-stops the per-Run
   key, and the Run settles afterwards against the gateway's own logs
   ([`pkg/llmbroker`](../../pkg/llmbroker/)). The Agents API reports
   best-effort usage that "is not a final bill".
2. **Runs any model.** Through LiteLLM, the owner runs Fireworks-hosted open
   models today. The Agents API runs OpenAI models; Bedrock is the only other
   host, and it runs the same models.
3. **Keeps everything in the EU and on the owner's hardware.** The harness,
   inference gateway, control plane and state are self-hosted. The Agents API
   cannot be self-hosted, and it has neither EU residency nor ZDR.
4. **Starts from a tracker and ends at a pull request.** Vikunja and ClickUp
   intake, Forgejo and GitLab delivery, repo-scoped push rights minted per Run
   ([ADR-0013](../adrs/0013-push-rights-are-minted-per-run.md)), and the forge
   as ground truth for "PR opened". The Agents API names no tracker and no
   forge.
5. **Runs a review loop between separate Runs.** A reviewing Role's verdict
   reopens the writer, capped by the pool and `maxFixRounds`. Its findings
   travel through the PR.
6. **Recovers durably across crashes.** The lease and sweep close, block or
   re-queue work, and expiry revokes credentials without the agent's
   cooperation. An unstarted admission is cancelled, never silently repeated.
7. **Holds Work Items that agents create for approval**, within per-Team
   limits ([ADR-0031](../adrs/0031-runs-create-work-items-held-for-approval-within-limits.md)).
8. **Is harness-neutral.** Runs use OpenHands, Claude Code and any ACP agent
   today, and Codex once the gateway probe passes.

## 5. Where Glide's value is

The Agents API moves the agent loop and the sandbox into a vendor-run service
and prices the loop at zero. That lowers the cost of building an issue-to-PR
bot on GitHub + OpenAI to a few hundred lines. The cookbook's `github_issues`
app is one, and together with
[openai/symphony](https://github.com/openai/symphony) OpenAI now publishes both
halves of the pattern. So "we run coding agents" is no longer worth anything by
itself, even less than in the [2026-09-26
landscape](2026-09-26-agent-orchestration-landscape.md).

What stays scarce is exactly what the Agents API leaves out:

- **A price that holds.** A quoted, capped budget per Work Item that the system
  refuses to exceed, and a settled figure afterwards. The hosted agency
  offering (its ADRs are on branch `docs/agency-offering`) bills per ticket, so
  it depends on a hard cap. The Agents API cannot enforce one today: its only
  hard cap is a monthly project limit that is "not instantaneous".
- **Sovereignty.** EU residency, self-hosting, any model and no training. Dutch
  agencies working under a data processing agreement cannot put client code
  into a US-only, non-ZDR, "Until deleted" store without their clients' consent.
- **Delivery into the tools teams already use.** Self-hosted trackers and
  forges, per-Run push rights, and a reviewed pull request.
- **Governance evidence.** An authorization and settlement row per Run, the
  audit log, and an outcome the agent cannot assert on its own.

The risk is also clear. The day OpenAI documents a settable session budget, a
GitHub-only competitor built on Symphony and the Agents API can claim a per-run
cap. Ploeg's answer then has to be the rest of the list: a budget that spans a
Shift and several providers, is authorized before the Run and settled against
the gateway, plus EU residency and self-hosting. A per-run cap alone will not
be enough.

## 6. Can we use it or support it?

Cheapest first.

**a. North: be a tool that Agents API sessions can call. Yes, at no extra
cost.**
- The Agents API is an MCP client and can reach an HTTP MCP server from
  OpenAI's side, with a bearer token or OAuth held in a Vault.
- The MCP server being drafted on branch `docs/mcp-access` (a separate
  `ploeg-mcp` consumer of the operator API) would let any Agents API session:
  - read Glide's Work Items and spend;
  - propose work, which lands as `proposed`
    ([ADR-0031](../adrs/0031-runs-create-work-items-held-for-approval-within-limits.md));
  - steer, with approval.
- Admission and budget stay in Ploeg.
- Prerequisites: a public HTTPS endpoint, and a consumer identity per calling
  application.
- This is "support it" without depending on it: Glide becomes the governed
  back office that an OpenAI-built assistant hands work to.

**b. South: run OpenAI's harness under Ploeg's control. Yes, through the
open-source Codex, not the hosted API.**
- "The open-source Codex harness is what powers all these experiences… The
  open-source layer is the harness and integration surface; model access and
  managed services remain separate"
  ([Codex as a platform](https://developers.openai.com/blog/codex-as-a-platform)).
- Backlog #64 already plans Codex as an ACP profile through
  `agentclientprotocol/codex-acp`, with inference through LiteLLM's
  `/v1/responses`, so the per-Run key, the budget and the model choice survive.
- It waits on one probe: does LiteLLM bridge `apply_patch`, reasoning items and
  compaction ([ACP profiles](../contracts/acp-profiles.md))?
- This is the way to get Codex-quality behaviour into Glide without giving up
  the seam.

**c. South: an `openai-agents` harness adapter that drives the hosted API. Not
yet: watchlist.**

Mechanically it fits:
- The adapter's `Run` would create a session with a `self_hosted` environment
  whose workspace is the cloned repository.
- The worker would start `codex exec-server` in the Run pod and stream until
  the turn ends.
- It would map items and usage to an `OutcomeReport`.
- `openai-go` has the client and a streaming helper; the drop box and the forge
  poll already handle the outcome.

It fails on the invariants:
- Inference bypasses LiteLLM, so no per-Run key exists and the authorization in
  ADR-0012 cannot be enforced. The only enforcement left would be to cancel the
  turn when streamed usage times a price table crosses the hold. That is a soft
  stop on best-effort numbers: exactly the "downgrade gate, not a hard stop"
  that ADR-0032 disqualifies in Omnigent.
- The worker pod's egress, which today reaches only the gateway and the forge,
  would have to open `api.openai.com` and a `chatgpt.com` WebSocket.
- The environment key is readable by agent code, which is weaker than the
  placeholder design in ADR-0034.
- Every byte of the repository and the transcript goes to a US-only, non-ZDR
  store.

The shape that survives is an explicitly *unmetered-provider* harness class. It
is marked as such in the Team plan, allowed only where the owner accepts US
processing, and budgeted by a dedicated OpenAI project with a hard spend limit.
That is worth building only when a trigger in §9 fires.

**Never:**
- moving admission, budgets or the lease into the Agents API;
- letting a hosted session hold forge credentials, or open or merge a pull
  request on its own;
- a fallback from a managed Ploeg Run to the hosted API when a worker fails;
- presenting OpenAI's best-effort usage as settled spend;
- the Agents SDK in Vloer. Vloer does not execute
  ([ADR-0002](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/adr/adr-0002-ploeg-is-the-only-engine.md)), and
  the SDK has no Go version for Ploeg.

## 7. Design worth mining

Record these; do not adopt them wholesale.

- **"Idle is not success."** OpenAI's docs reach the same rule as ADR-0018: the
  harness's own end-of-turn is not an outcome. This is independent
  confirmation.
- **Secret placeholders with a host allowlist.** The Vault design matches
  ADR-0034, and OpenAI leaves self-hosted environments to "a trusted proxy…
  you provide". That is what `pkg/worker/llmproxy.go` and `forgeproxy.go`
  already are. This confirms ADR-0034 before its 2027-01-31 review.
- **Per-Run OTLP export by pull.** `GET …/sessions/{id}/traces` returning OTLP
  JSON is a cheap first shape for backlog #82: export one Run's trace on
  request, before building push delivery.
- **Egress as `enabled` / `disabled` / `restricted` plus an allowlist**, as one
  field. This is a vocabulary for the per-team registry egress proposal (ADR-0037
  on branch `docs/registries-network-profile`).
- **A mid-turn steering message.** This is a candidate for Vloer's intervention
  on an operator Run, carried through ACP's prompt-while-running when the
  harness supports it. It is a front-end concern, not a new Ploeg operation.

## 8. Recommendations

1. Record the verdict as
   [ADR-0041](../adrs/0041-the-openai-agents-api-stays-outside-the-run-until-it-takes-an-authorized-budget.md)
   (proposed), with a review on the 2026-10-31 market re-scan.
2. Add one re-evaluation trigger to ADR-0032: OpenAI documents a settable
   per-session budget.
3. Keep backlog #64's Codex profile as the route to OpenAI's harness, and run
   the `/v1/responses` probe next. This sweep raises its value: the Codex
   harness is now the one OpenAI itself hosts.
4. When the MCP server ADR on `docs/mcp-access` is written up, name the OpenAI
   Agents API as a target client and test it against service-origin HTTP MCP.
5. Build no `openai-agents` adapter until a trigger in §9 fires. The watchlist
   ticket VIK-1514 on the Glide board carries them.
6. Fold the mined items in §7 into backlog #82 (OTel) and the ADR-0034 review
   notes when those are next touched.

## 9. Re-evaluation triggers

Any one of these reopens the verdict:

- `CreateAgentSessionParams` (in
  [openai-openapi](https://github.com/openai/openai-openapi)) gains a budget,
  max-cost or max-tokens field. Grep `openapi.yaml` for `budget` under
  `CreateAgentSessionParams`.
- `/v1/agents` appears in the EU tables of
  [your data](https://developers.openai.com/api/docs/guides/your-data), or
  becomes ZDR-eligible.
- The Agents API accepts a custom inference base URL or gateway, so a per-Run
  LiteLLM key could meter it.
- Bedrock Managed Agents lists an EU region
  ([AWS page](https://aws.amazon.com/bedrock/managed-agents-openai/)).
- The Agents API publishes its GA contract, meaning the `OpenAI-Beta: agents=v1`
  header is no longer required.
- `codex exec-server` loses its "[EXPERIMENTAL]" label in `codex-rs/cli`.
- Symphony's SPEC names the Agents API as a backend, or OpenAI ships
  tracker-to-PR on the Agents API as a product.
- The quarterly market re-scan on 2026-10-31.
