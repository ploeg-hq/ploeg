# Model gateway contract: Ploeg's LiteLLM coupling, Envoy AI Gateway's primitives and the LiteLLM PyPI incident, 2026-10-30

Status: research record, 2026-10-30. This is the evidence behind [ADR-0177](../adrs/0177-ploeg-reaches-models-through-a-gateway-contract-with-litellm-and-envoy-ai-gateway-as-implementations.md).

**Question.** Which operations does Ploeg need from a model gateway? Where does today's code take them from LiteLLM? Which of them can Envoy AI Gateway provide?

**Answer.** Ploeg needs six things from a gateway:

* a scoped, expiring credential per Run;
* a way to block it;
* durable usage per credential;
* models and prices;
* the provider and region of each request;
* refusal classification and health.

LiteLLM provides all six through its management API. Envoy AI Gateway provides the data-plane parts natively: JWT-checked expiry, model routing, token-cost rate limits, MCP authorization on claims, and per-request token and backend metadata. It has no API for minting, revoking, budgets in currency, usage reads or prices. Each of those needs a fallback that Ploeg owns.

## Ploeg's code at `origin/development` 10911fd

These are the calls that reach LiteLLM, by file:

* `pkg/llmbroker/litellm.go` mints with `POST /key/generate`. The body carries `key_type: llm_api`, `key_alias: ploeg-<12hex>`, `max_budget`, `models`, `duration` in seconds, `team_id`, `object_permission.mcp_access_groups` and metadata `disable_fallbacks: true`. The file refuses a budget that is not positive and finite, and MCP groups without a team. It revokes with `POST /key/block`. It lists keys with `GET /key/list` by exact alias, or by alias prefix with only active keys for the orphan sweep. It reads `GET /key/info` for the running spend and for the absence proof. It settles by summing `GET /spend/logs?api_key=<sha256>` per recorded key, or `GET /spend/logs/ui` by alias when no key identity is known.
* `pkg/httpapi/llm_control.go` covers block, observe, live, settle and correct, all through the broker. Its evidence strings name `litellm:`. `LLMPolicy.LiteLLMTeamID` carries the team.
* `pkg/premise/premise.go` reads prices from `GET /model/info`, mints a `ploeg-premise-` key with a 0.01 USD budget and a five-minute expiry, and checks the `x-litellm-response-cost` and `x-litellm-model-group` reply headers.
* `pkg/litellm/errors.go` classifies refusal bodies, including the owner of a spent budget. `pkg/worker/budgetspent.go` and `pkg/harness/adapters/acp/outcome.go` use it.
* `pkg/worker/llmproxy.go` swaps the placeholder in `Authorization`, `X-Api-Key` and `X-Litellm-Api-Key`.
* `cmd/ploegd/main.go` builds the sweeper and the premise checker only when `LITELLM_ADMIN_URL` and `LITELLM_MASTER_KEY` are both set.
* `pkg/llmbroker/static.go` is the no-gateway broker for legacy mode. It mints nothing and settles nothing.

The LiteLLM schema (`schema.prisma` on `main`, read 2026-10-30) gives `LiteLLM_SpendLogs` a `request_id` primary key, plus `model`, `model_id`, `model_group`, `custom_llm_provider` and `api_base` on each row. No spend-log field names a region. Only `LiteLLM_EndUserTable.allowed_model_region` mentions one.

## Envoy AI Gateway, read 2026-10-30

* **Project state.** 1.0.0 went GA on 2026-06-23 and 1.1.0 followed on 2026-08-21. The repository README says "Envoy AI Gateway is now Agent Router, an Agentic AI Foundation project". The repository moved to `theagentrouter/agent-router` and the site to theagentrouter.ai. The old site redirects there. The CRD group is still `aigateway.envoyproxy.io`. Sources: [release notes 1.0](https://theagentrouter.ai/release-notes/v1.0/), [release notes 1.1](https://theagentrouter.ai/release-notes/v1.1), [envoyproxy/ai-gateway](https://github.com/envoyproxy/ai-gateway).
* **Token cost and budgets.** `AIGatewayRoute.spec.llmRequestCosts[]` sets `metadataKey` and `type`. The types are `InputToken`, `CachedInputToken`, `CacheCreationInputToken`, `OutputToken`, `TotalToken`, `ReasoningToken` and `CEL`. A CEL expression returns a non-negative integer and can read `model`, `backend`, `input_tokens`, `cached_input_tokens`, `cache_creation_input_tokens`, `output_tokens`, `total_tokens` and `reasoning_tokens`. Enforcement is Envoy Gateway's `BackendTrafficPolicy` with `rateLimit.type: Global`. Its rules have `clientSelectors[].headers` (`Distinct` or `Exact`) and `limit.requests` per `limit.unit`. `cost.request.number: 0` together with `cost.response.from: Metadata` (namespace `io.envoy.ai_gateway`) charges the computed cost after the response. Over the limit the answer is 429. An admitted stream is not cut off, so a bucket can overshoot. Budgets are counts of tokens or of the CEL unit, never currency. No API reads a bucket's balance. `QuotaPolicy` (1.0) bounds traffic toward an upstream provider, not per client. Sources: [usage-based rate limiting](https://theagentrouter.ai/docs/capabilities/traffic/usage-based-ratelimiting), [API reference](https://theagentrouter.ai/docs/api/).
* **Client credentials.** The gateway has no client key API. Client authentication is Envoy Gateway's `SecurityPolicy`. One option is `apiKeyAuth`, with keys in a Kubernetes Secret per client id and `forwardClientIDHeader`. The other is JWT providers that check `exp` and can project claims into headers. Upstream credentials are `BackendSecurityPolicy`, and 1.1 adds a `credentialOverride` per request. Sources: [Envoy Gateway API key authentication](https://gateway.envoyproxy.io/docs/tasks/security/apikey-auth), release notes 1.0 and 1.1.
* **MCP.** `MCPRoute` multiplexes MCP servers. `spec.securityPolicy.oauth` follows the MCP authorization flow. `spec.securityPolicy.authorization.rules` match on JWT `scopes` and `claims` against `target.tools` (backend and tool), with optional CEL over `request.auth.jwt`, `request.headers` and `request.mcp.*`. The first match decides. `tools/list` is filtered by the same rules. There is no "tool group" object. Source: [MCP](https://theagentrouter.ai/docs/capabilities/mcp/).
* **Usage leaving the gateway.** Metrics follow the OpenTelemetry GenAI conventions: `gen_ai.client.token.usage` with `gen_ai.token.type`, `gen_ai.server.request.duration`, and time to first token and per output token. Their attributes are `gen_ai.operation.name`, `gen_ai.original.model`, `gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.provider.name` and `gen_ai.backend`. `controller.metricsRequestHeaderAttributes` adds request headers as labels. Access logs can carry `%DYNAMIC_METADATA(io.envoy.ai_gateway:llm_input_token)%`, `llm_output_token`, `llm_total_token`, `response_model`, `backend_name` and the MCP fields, plus any `%REQ(header)%`. No cost field is documented beyond the metadata keys an installer defines. Sources: [metrics](https://theagentrouter.ai/docs/capabilities/observability/metrics), [access logs](https://theagentrouter.ai/docs/capabilities/observability/accesslogs).
* **Not documented, left to the ADR's spike:**
  * whether a JWT-projected header always replaces a client-sent one;
  * whether a rate-limit descriptor's limit can come from request metadata;
  * the width of the limit field against an integer money unit;
  * how the fixed rate-limit windows align to the calendar.

## The LiteLLM PyPI incident, 2026-03-24

* [BerriAI/litellm#24512](https://github.com/BerriAI/litellm/issues/24512), opened 2026-03-24, reports "Malicious litellm_init.pth in litellm 1.82.8 — credential stealer". The `.pth` file runs at every Python start. It collects environment variables, SSH keys, cloud, Kubernetes, git and Docker credentials and shell history, encrypts them, and posts them to `https://models.litellm.cloud/`. A commenter found the payload in 1.82.7's `proxy/proxy_server.py`, and a maintainer later wrote that only 1.82.7 and 1.82.8 were compromised.
* [BerriAI/litellm#24518](https://github.com/BerriAI/litellm/issues/24518), the maintainers' update, says the compromised packages "have been deleted (v1.82.7, v1.82.8)". It says the compromise "came from trivvy security scan dependency" and that the maintainer's PyPI account was hijacked, so the versions never went through the official CI. It also says "Proxy Docker image users were not impacted, all dependencies are pinned on requirements.txt". Neither thread gives an exact exposure window, and the image claim is the maintainers' own.
* Ploeg's exposure: no Ploeg release installs LiteLLM. The quickstart pins `ghcr.io/berriai/litellm-database:v1.105.2@sha256:38cd7e70…`, a later line than the affected versions, by digest. P1's digest rule covers only the images a worker pod runs ([threat model, P1 Images row](../security/threat-model.md#profile-p1-unattended-workers-without-docker-credential-proxies-on), [#299](https://github.com/ploeg-hq/ploeg/issues/299)). The installer's gateway image is outside it.
