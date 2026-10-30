---
status: proposed
date: 2026-10-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg reaches models through a gateway contract, with LiteLLM and Envoy AI Gateway as implementations

## Context and Problem Statement

[ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md) kept LiteLLM as the seam where Ploeg mints, caps, meters and revokes one credential per Run. It is due for review on 2026-10-31. It argued that the coupling was confined to `pkg/llmbroker`'s `Broker` interface. The code no longer bears that out. LiteLLM's management API and its wire details are hardwired in at least eight places:

| Where | What it takes from LiteLLM |
| --- | --- |
| [llmbroker/litellm.go](../../pkg/llmbroker/litellm.go) | `/key/generate` with `key_alias`, `max_budget`, `models`, `duration`, `team_id`, `object_permission.mcp_access_groups` and the metadata `disable_fallbacks`; `/key/block`; `/key/list` filtered by alias; `/key/info` |
| [llmbroker/broker.go](../../pkg/llmbroker/broker.go) | `Metered`, `Settler` and `KeyProber` are optional interfaces whose meaning comes from LiteLLM's spend logs (`/spend/logs`, `/spend/logs/ui`) and key identities (the SHA-256 LiteLLM stores as `api_key`) |
| [httpapi/llm_control.go](../../pkg/httpapi/llm_control.go) | `LLMPolicy.LiteLLMTeamID`, evidence strings `litellm:spend-logs` and `litellm:key-absent`, and `ManagedLLMBroker.SpendForRun`, which reads LiteLLM's running key counter |
| [premise/premise.go](../../pkg/premise/premise.go) | `/model/info` prices, a `litellm.MintRequest`, and the reply headers `x-litellm-response-cost` and `x-litellm-model-group` ([ADR-0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md)) |
| [litellm/errors.go](../../pkg/litellm/errors.go) | the error bodies the worker and the `acp` adapter classify, including whose budget is spent ([ADR-0111](0111-a-spent-litellm-budget-wider-than-the-runs-key-parks-the-work-item.md)) |
| [worker/llmproxy.go](../../pkg/worker/llmproxy.go) | the `X-Litellm-Api-Key` header the worker's key proxy swaps ([ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md)) |
| [cmd/ploegd/main.go](../../cmd/ploegd/main.go) | `LITELLM_ADMIN_URL` and `LITELLM_MASTER_KEY` decide whether revocation, settlement and the premise check exist at all |
| the chart | `executor.litellm.*`, `executor.teams[].litellmTeamId`, and the support matrix row "LiteLLM 1.102 and earlier, or another gateway: unsupported" |

Settlement ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)), the observation floor and operator settlement ([ADR-0155](0155-an-operator-settles-an-account-the-gateway-cannot-answer-for-from-evidence-and-never-below-what-ploeg-observed.md)), the Team spend cap's settled figures ([ADR-0162](0162-a-team-spends-at-most-its-cap-per-period-and-a-claim-over-it-starts-nothing.md)), live usage ([ADR-0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md)) and the per-Run accounting read (`GET /api/v1/operator/runs/{id}/accounting`) all rest on these calls.

On 2026-03-24 the LiteLLM packages 1.82.7 and 1.82.8 on PyPI were found to carry a credential stealer. Version 1.82.8 shipped a `litellm_init.pth` that ran at every Python start. It collected environment variables, SSH keys, cloud and Kubernetes credentials, and sent them to `models.litellm.cloud`. Version 1.82.7 carried the payload in `proxy/proxy_server.py`. The maintainers deleted both versions. They said the versions were published from a hijacked PyPI account, which they traced to a compromised Trivy dependency in their CI, and that users of the proxy Docker image were not affected because its dependencies are pinned ([BerriAI/litellm#24512](https://github.com/BerriAI/litellm/issues/24512), [#24518](https://github.com/BerriAI/litellm/issues/24518)). No Ploeg release or quickstart installed an affected version: the quickstart pins `litellm-database:v1.105.2` by digest. The incident still shows what is at stake. A gateway process holds every provider key and the master key (asset AS1 in the [threat model](../security/threat-model.md#assets)). An installer whose gateway is compromised today has no supported alternative, so moving off it would need a Ploeg release.

On 2026-10-30 the owner decided that the model gateway becomes a generic Ploeg contract, with LiteLLM as one implementation and Envoy AI Gateway as the second.

Which operations must any gateway provide, how do LiteLLM and Envoy AI Gateway each provide them, and how does Ploeg get there without changing what a LiteLLM deployment sees?

## Decision Drivers

* ADR-0008's drivers still hold. Each Run needs a credential with its own budget, expiry and identity, and spend must be metered at a boundary the agent talks through, not taken from what the agent reports.
* Ploeg errs high, never low: no implementation may settle below what was spent or observed (ADR-0012, ADR-0155).
* Ploeg knows work sources and change destinations, never vendors ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)). A gateway is a destination for model traffic.
* One gateway is one supply chain. An installer must be able to switch implementations without a Ploeg release.
* A LiteLLM deployment must see byte-identical admin traffic, evidence, chart output and metrics. The compatibility rules of [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) apply.
* Residency: a cell carries a residency tag ([ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md)), so Ploeg must be able to see which provider and region answered each request.
* ploegd keeps its state in PostgreSQL and is not a Kubernetes operator ([ADR-0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md)). An implementation must not need ploegd to write cluster objects on every mint.

## Considered Options

* **A gateway contract with six operations, LiteLLM and Envoy AI Gateway as implementations, and a contract suite every implementation passes**
* Keep LiteLLM as the only gateway (ADR-0008 unchanged)
* Define the contract as LiteLLM's management API, so a second gateway must emulate it
* Build a Ploeg-owned metering proxy and treat every gateway as a plain upstream
* Move to Envoy AI Gateway and retire LiteLLM

## Decision Outcome

Chosen option: "**a gateway contract with six operations, LiteLLM and Envoy AI Gateway as implementations, and a contract suite every implementation passes**". It keeps ADR-0008's properties: per-Run credentials, metering the agent cannot bypass, and errs-high settlement. It also gives an installer a second, independently built gateway behind the same guarantees. Today's LiteLLM behaviour stays the reference implementation, unchanged.

When this record is accepted, it supersedes ADR-0008. The accepting commit sets `supersedes: 0008` and changes ADR-0008's Records row. ADR-0078's rule ("the gateway's MCP endpoint, bounded by a gateway team") stays as it is, and its LiteLLM wording becomes the LiteLLM column below.

### 1. The contract

The contract is one Go interface in a new package `pkg/gateway`. It replaces `llmbroker.Broker`, `Sweeper`, `Metered`, `Settler`, `KeyProber`, `ManagedLLMBroker`, `premise`'s gateway interface and the parts of `litellm.ClassifyError` the worker uses. Every operation is required. An implementation that cannot do one natively does it through a documented fallback that passes the same contract tests; there are no optional capabilities. The sketch below is proposed, not built:

```go
type Gateway interface {
    Kind() string                                                      // "litellm", "envoy-ai-gateway", "static"
    Mint(ctx context.Context, req MintRequest) (Credential, error)     // §1.1
    Block(ctx context.Context, run RunRef) error                        // §1.2, idempotent
    SweepOrphans(ctx context.Context, alive []RunRef) (int, error)      // §1.2
    Usage(ctx context.Context, run RunRef, mode UsageMode) (Usage, error) // §1.3: Observed, Live, Settled
    CredentialsGone(ctx context.Context, run RunRef) (bool, error)      // §1.3
    Models(ctx context.Context) ([]Model, error)                        // §1.4
    Classify(status int, header http.Header, body []byte) Refusal       // §1.6
    Health(ctx context.Context) (Health, error)                         // §1.6
}
```

`RunRef` carries the Run token, the `ploeg-<12hex>` alias and the credential identities Ploeg recorded. The alias stays the join key for every implementation ([architecture §5](../architecture.md)).

#### 1.1 Mint a scoped, expiring credential per Run

`MintRequest` keeps today's fields: Run token, `BudgetUSD`, `Models`, `TTL`, `TeamID` (renamed from LiteLLM's `team_id` to the gateway's team) and `MCPAccessGroups`. It adds no new fields. The contract makes four of today's rules hold for every implementation:

* a budget that is zero, negative or not finite is refused, never minted uncapped;
* MCP groups without a team are refused (ADR-0078);
* a call for a model outside `Models` is refused, and no fallback model answers for the Run ([ADR-0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md), 2026-10-11 amendment);
* the gateway itself enforces the credential's expiry. Ploeg's sweeps are the second line, not the only one.

#### 1.2 Block or revoke it

`Block` stops every credential of a Run. It is idempotent, and its effect is visible to the contract test within the latency the implementation declares. `SweepOrphans` blocks every live `ploeg-` credential that belongs to no unfinished Run. A blocked credential's spend record must survive the block.

#### 1.3 Read spend and token usage per credential

`Usage` returns the same shape in all three modes: USD, input and output tokens, models, model groups, a split per model, and a list of request entries, each with a stable request id.

* `Observed` is the running figure that ADR-0155's observation floor records.
* `Live` is ADR-0049's usage so far. It records nothing.
* `Settled` is read from a durable source that survives revocation and deletion.

Settlement stays in Ploeg's state machine ([accounting invariants](../reference/accounting-invariants.md)). The contract makes it idempotent across implementations in three ways:

* reading `Settled` twice for an unchanged Run returns the same total;
* a later read is never lower than an earlier one;
* entries are counted by request id, so a re-delivered entry is never charged twice.

`CredentialsGone` answers true only on the gateway's own positive report that no credential of the Run exists. An error is never an absence.

#### 1.4 List models and their prices

`Models` lists each model group the gateway serves, with its deployments and each deployment's stated input and output price per token. A price the gateway does not state is absent, never zero. The premise check's "never a paid model" rule (ADR-0123) reads this, and so does the advisor condition in ADR-0039.

#### 1.5 Report provider and region per request

Each `Usage` entry names the provider and the deployment that served it. When the implementation knows the region, the entry names that too. Ploeg records them with the Run's usage. A cell with a residency tag can then refuse, or alert on, a Run that was served outside that tag. Which of those it does is a later decision. This record only requires the facts.

#### 1.6 Classify refusals, and check health and the premise

`Classify` maps a refusal to Ploeg's existing kinds: `budget_exceeded` with the budget's owner (credential, team or other), `rate_limited`, `provider_quota`, `key_rejected`, `model_unavailable`, `context_too_long` and the rest of [errors.go](../../pkg/litellm/errors.go). This keeps ADR-0111 working for every gateway.

`Health` reports three things: whether the management plane answers, whether the data plane accepts a probe credential, and whether the durable spend source is current. It also reports the gateway's version, so ploegd can refuse a version below the supported floor at boot. ploegd calls it at start and on `/readyz`. The premise check uses `Models` for the price check and `Usage(Settled)` of its own throwaway credential to confirm a charge of zero. That confirmation replaces the LiteLLM reply headers, which stay as an extra check in the LiteLLM implementation.

### 2. How each implementation does each operation

Envoy AI Gateway 1.0 went GA on 2026-06-23 and 1.1.0 followed on 2026-08-21. The project has since been renamed **Agent Router** and moved to the Agentic AI Foundation, with the same code and maintainers. Its API group is still `aigateway.envoyproxy.io`, and it runs on Envoy Gateway. It has no admin API for keys, budgets or usage: configuration is Kubernetes resources, and usage leaves the gateway as rate-limit charges, metrics, access logs and traces. The Envoy column therefore relies on Envoy Gateway's `SecurityPolicy` (JWT and external authorization) and `BackendTrafficPolicy` (global rate limit). It names a fallback wherever the gateway has no native operation.

| Operation | LiteLLM (today, unchanged) | Envoy AI Gateway | Gap and fallback |
| --- | --- | --- | --- |
| Mint: credential and TTL | `POST /key/generate`, `key_type: llm_api`, `key_alias: ploeg-<12hex>`, `duration: <seconds>s`; the master key authorizes it | ploegd signs a short-lived JWT for the Run (`sub` = alias, `exp` = TTL, claims `team`, `models`, `budget_tier`, `mcp_groups`). The gateway checks it with a `SecurityPolicy` JWT provider that reads ploegd's JWKS, so the gateway enforces the expiry. Minting writes nothing to the cluster. | The gateway has no mint API, so the credential is Ploeg-signed. The signing key replaces the master key as asset AS1. |
| Mint: model allowlist, no fallback | `models: [...]`, metadata `disable_fallbacks: true` | `claimToHeaders` projects `models` into a header with `recomputeRoute`. Each `AIGatewayRoute` rule matches `x-ai-eg-model` together with that header, so a model outside the claim matches no route. Each rule has one model's backends and no cross-model fallback. | None, if the spike confirms that a client-sent copy of the projected header is replaced, not merged. The contract test sends one. |
| Mint: budget | `max_budget`; the gateway refuses the call after the key's spend reaches it | A `llmRequestCosts` entry of type `CEL` prices each response from `model`, `input_tokens`, `cached_input_tokens` and `output_tokens`, in an integer money unit. A `Global` rate-limit rule with `clientSelectors` `Distinct` on the alias header charges that cost (`cost.response.from: Metadata`) against the Run's bucket. | A rule's limit is fixed, but Runs have different budgets. **Fallback:** a ladder of budget tiers, one rule each. Ploeg mints with the largest tier at or below the Run's authorization, so the bound is never above what ADR-0012 authorized. The spike also tries a per-descriptor limit taken from metadata, which would remove the ladder. Rate-limit windows are fixed and calendar-aligned, so the implementation shortens a credential's expiry to the end of its window, and a longer Run re-mints through credential refresh. An admitted stream is not cut off, so a Run can overshoot by one call, as it already can on LiteLLM (T-HN-6). |
| Mint: team and MCP groups | `team_id`; `object_permission.mcp_access_groups`; the LiteLLM team refuses groups it does not allow | An `MCPRoute` with `securityPolicy.oauth` takes the same JWT. Its `authorization.rules` are rendered per gateway team, and each rule allows a group's tools only when the claims `team` and `mcp_groups` both match. | The team boundary lives in rendered rules, not in a gateway object. The chart renders it from the same `executor.teams[]` values. Ploeg's refusal of groups without a team stays. |
| Block or revoke | `POST /key/block` per key; `RevokeForRun` lists by exact alias; `SweepOrphans` lists `key_alias=ploeg-&substring_matching=true&status=active` | A JWT cannot be revoked at the gateway. **Fallback:** a `SecurityPolicy` `extAuth` that calls a ploegd endpoint. The endpoint answers 403 for any Run whose account is `blocked`, `reconciled` or `unknown`, and fails closed. `SweepOrphans` blocks the alias in Ploeg's own table, and every credential also dies at `exp`. | ploegd joins the request path, so a ploegd outage stops model calls. Accepted: such an outage already stops Runs at lease renewal, and failing closed errs high. Block latency is the ext_authz cache time, which the implementation declares. |
| Observed and live spend | `GET /key/info` (`spend`, running counter); `/spend/logs` | The sum of the Run's ledger entries so far (row below) | None |
| Settled spend and tokens | `GET /spend/logs?api_key=<sha256>` per recorded key; `/spend/logs/ui` by alias when no key is known; each row's primary key is `request_id` | No usage store. **Fallback:** Envoy's access log. The `io.envoy.ai_gateway` metadata gives `llm_input_token`, `llm_output_token`, `backend_name` and `response_model`; the CEL cost key gives the cost and `%REQ(...)%` the alias. Envoy streams it over the access-log service to a ploegd receiver, which writes one row per request id into a new ledger table. | Access logs are best-effort. If the receiver saw a gap (a disconnected stream) during a Run's lifetime, the Run settles at its full authorization and records why, which errs high. The OTel GenAI metric `gen_ai.client.token.usage` (by `gen_ai.provider.name`, `gen_ai.backend`, `gen_ai.request.model`) is a Team-level cross-check and alert, never a settlement source. It has no per-credential attribute unless request headers are added as labels. |
| Credentials gone | `/key/list` by alias is empty and `/key/info` answers 404 per key id | True once the Run's last `exp` has passed: an expired JWT verifiably authorizes nothing | None |
| Models and prices | `GET /model/info`: `model_name`, `model_info.input_cost_per_token`, `output_cost_per_token` | `GET /v1/models` lists what the gateway serves, without prices. Prices come from one table in Ploeg's configuration, which is also what the chart renders into the CEL cost expression. | The gateway states no prices. **Fallback:** a single price table from which both the CEL and `Models` are rendered. A contract test confirms that a model priced at zero logs a cost of zero, and the premise check also confirms its own charge from the ledger. |
| Provider and region per request | Each spend log row has `custom_llm_provider`, `api_base`, `model_id` and `model_group`. The region is the serving deployment's `region_name` from `/model/info`, joined on `model_id`, where the deployment declares one. | `gen_ai.provider.name` in metrics and `backend_name` (the `AIServiceBackend`) in the access log. The region comes from a per-backend `region` in the gateway configuration. | Neither gateway reports a region by itself. Both implementations take it from configuration and leave it empty when none is declared. |
| Classify refusals | LiteLLM error bodies, including the owner of a spent budget | A 429 with `x-envoy-ratelimited` is the Run's own budget; provider errors pass through and are classified as today; ext_authz's 403 is `key_rejected` | A spent shared budget (ADR-0111) exists only if the installer adds Team-level rules. Their descriptor names the owner. |
| Health and version | `GET /health/readiness`, which reports the database and `litellm_version`; floor 1.103.0 | Accepted conditions on the rendered routes and policies, a probe call with a probe JWT, and the ledger receiver's last-entry age | ploegd reads route conditions only, with read-only access to its own resources |
| Static (no gateway) | `llmbroker.Static`: echoes a fixed key, revokes nothing, never settles | n/a | `kind: static` keeps legacy mode exactly as it is. It is not a contract implementation and stays outside P1. |

### 3. Configuration: `gateway.kind`

ploegd reads `PLOEG_GATEWAY_KIND`, and the chart renders it from `executor.gateway.kind`. Allowed values are `litellm`, `envoy-ai-gateway` and `static`.

* When the variable is unset and `LITELLM_ADMIN_URL` is set, the kind is `litellm`, exactly as today. When both are unset, the kind is `static`. An unknown kind stops ploegd at boot.
* `executor.litellm.*` stays where it is and is the `litellm` block. `executor.teams[].litellmTeamId` keeps working. A generic `executor.teams[].gatewayTeam` with the same meaning is added, and setting both to different values fails the render.
* `envoy-ai-gateway` reads a block of its own:

```yaml
executor:
  gateway:
    kind: envoy-ai-gateway          # default: litellm
    envoyAIGateway:
      baseUrl: http://envoy-ai.<ns>.svc:80/v1   # what workers call (LLM_BASE_URL)
      signingKeySecret: {name: ploeg-gateway-signer, key: key}
      budgetTiersUsd: [0.05, 0.10, 0.25, 0.50, 1, 2, 3, 5, 10, 20]
      prices:                       # the one price table; renders the CEL cost and answers Models
        - {model: deepseek-v4, inputPerMTokUsd: 0.27, outputPerMTokUsd: 1.10, backends: [{name: deepseek, region: eu}]}
      accessLogReceiver: {port: 9001}
      extAuthz: {cacheSeconds: 2}
```

The chart renders the routes, policies and rate-limit rules from these values. ploegd never writes them.

### 4. What the support matrix will say

The Model gateway section's lead becomes: "Ploeg mints, caps, watches and revokes one credential per Run through a gateway that passes the contract suite, and settles from the gateway's durable record."

* The two LiteLLM rows stay as they are.
* A new row, "Envoy AI Gateway (Agent Router) 1.x on Envoy Gateway, with the global rate-limit service, ploegd as external authorization and access-log receiver", starts `experimental`. It becomes `tested-in-ci` when the contract suite runs against it in CI. It becomes `qualified-live` only with a dated live record whose settlements match the gateway's access log within the tolerance of the [live gateway accounting](../research/2026-10-25-live-gateway-accounting.md) record.
* "LiteLLM 1.102 and earlier, or another gateway" is split. "LiteLLM 1.102 and earlier" stays unsupported for the sweep reason. "A gateway that has not passed the contract suite" is unsupported.
* Each gateway row's limits say that P1 needs the gateway's image pinned by digest. This is the gateway counterpart of the P1 Images row, which covers only the images a worker pod runs ([ADR-0154](0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md), 2026-10-27 amendment; [#299](https://github.com/ploeg-hq/ploeg/issues/299)). The chart cannot see an installer's gateway image, so this is a written requirement, not a render check.

### 5. LiteLLM stays byte-identical

The refactor is a move, not a rewrite. Before any code moves, a recording test drives a full lifecycle through `LiteLLM` against an `httptest` server: mint with and without a team and MCP groups, live and observed reads, block, settle by key and by alias, the gone-key probe, the orphan sweep, the premise check and `/model/info`. The test stores every request's method, path, query, headers and body under `testdata/`. After the move, the same test must reproduce those requests byte for byte. The following stay unchanged, and their existing tests are not edited:

* the alias format and its Grafana joins;
* the settlement evidence strings (`litellm:spend-logs …`, `litellm:spend-logs-by-alias …`, `litellm:key-absent …`);
* the environment variable names and the `X-Litellm-Api-Key` header the proxy swaps;
* the MCP server name `litellm`;
* the error kinds, the metrics and the operator API's accounting read.

`scripts/helm-golden.sh check` must render the default values identically, `executor.gateway.kind: litellm` included.

### 6. Migration

1. **Contract, no behaviour change.** Add `pkg/gateway` and move the LiteLLM code behind it. `pkg/litellm` stays the LiteLLM client. Add the recording test (§5). Callers in `httpapi`, `premise`, `worker`, `acp` and `cmd/ploegd` depend on `pkg/gateway` only.
2. **Provider and region.** Add `provider`, `deployment` and `region` per model to the settled usage, in an additive migration and an additive operator schema field.
3. **Contract suite** (Confirmation below). It runs against the LiteLLM implementation, with an `httptest` fake on every change and live LiteLLM in the quickstart job.
4. **`gateway.kind`.** Add the setting with the defaults in §3. Each `run_llm_accounts` row records the kind that minted it, defaulting to `litellm`. ploegd refuses to start with a kind that differs from that of an unsettled account, unless the old kind is still configured so it can settle what remains. Switching gateways therefore drains rather than strands Holds.
5. **Envoy AI Gateway implementation**, `experimental`. This adds the signer and JWKS endpoint, the ext_authz endpoint, the access-log receiver and its ledger table, and the chart templates. A spike comes first, on a kind cluster, and settles the open points marked in §2: replacement of the projected header, a per-descriptor limit, the integer money unit against the rate-limit limit's width, and window alignment.
6. **Support matrix and threat model** updated as in §4, with TB6 and AS1 reworded per kind.

### Consequences

* Good, because an installer can leave a compromised or abandoned gateway for another implementation without a Ploeg release, and both implementations carry the same bounds.
* Good, because every guarantee Ploeg makes about money is now a test that every gateway must pass, not a property of LiteLLM's API that Ploeg happens to rely on.
* Good, because the per-request provider and region give residency something to check.
* Good, because the Envoy path holds no gateway master key. The gateway trusts only Ploeg-signed, expiring credentials.
* Bad, because the Envoy implementation puts three things into ploegd: a signer, an external-authorization endpoint on the request path, and an access-log receiver that is now a settlement source. Accepted: each fails closed or errs high, and none needs ploegd to write cluster objects.
* Bad, because budget tiers give a Run at most its authorization, and often less, until a per-descriptor limit is proven. Accepted: ADR-0012 bounds from above, and a Run that hits its tier stops the way any Run that spends its budget stops.
* Bad, because the price table for Envoy is Ploeg's configuration, not the gateway's own statement, so a wrong price is a configuration error the gateway cannot catch. The contract test for zero-priced models, and the premise check's read of its own charge, catch the case that matters most.
* Bad, because the contract adds a layer to code that was one call deep. Accepted: the code was already layered in practice, only with LiteLLM names at every level.
* Neutral: Agent Router's rename may later change the API group. The kind name stays `envoy-ai-gateway` until it does, and a rename is a re-evaluation trigger.

### Confirmation

This record is proposed and none of this is built yet. Once it is implemented, compliance is checked by:

* **The contract suite** `pkg/gateway/gatewaytest`. Every implementation's package runs it, and `go test ./...` in CI's `verify` job runs it against `httptest` fakes. It is also run live, opt-in: `PLOEG_GATEWAY_CONTRACT_LIVE=litellm` in the quickstart job, and `=envoy-ai-gateway` in a new kind job. Each case exists once and runs for every kind:
  * `MintRefusesUncappedBudget`, `MintRefusesMCPGroupsWithoutTeam`;
  * `CredentialExpiresAtTheGateway`: after `exp` or `duration`, the data plane answers 401 or 403 without a Ploeg call;
  * `ModelOutsideScopeIsRefused`, `NoFallbackAnswersForTheRun`;
  * `BudgetStopsTheNextCall`: the call after the budget is reached is refused and classified `budget_exceeded` with owner credential, and overshoot is at most one call;
  * `ClientCannotChargeAnotherRun`: a request that sends another Run's alias or projected headers is charged to its own credential or refused;
  * `BlockStopsCallsWithinDeclaredLatency`, `BlockIsIdempotent`, `SweepOrphansBlocksOnlyDeadRuns`;
  * `SettledSurvivesRevocation`, `SettledIsStableWhenUnchanged`, `SettledNeverDecreases`, `RedeliveredEntryIsCountedOnce`;
  * `SettledMatchesUsageWithinTolerance`, using the 0.0001 USD tolerance per account;
  * `CredentialsGoneOnlyOnPositiveAbsence`: an unreachable gateway is an error;
  * `ModelsStatePricesOrOmitThem`, `ZeroPricedModelChargesZero`;
  * `EntriesNameProviderAndDeployment`, `RegionIsEmptyUnlessDeclared`;
  * `MCPGroupsOnlyWithinTheTeam`;
  * `HealthFailsWhenTheSpendSourceIsStale`.

  A mutation check accompanies the suite. Dropping the budget, scope, expiry, block or deduplication in either implementation must fail at least one case.
* **Byte-identical LiteLLM:** `TestLiteLLMAdminTrafficIsUnchanged` in `pkg/gateway/litellm`, against the recording made before the move, and an unchanged `scripts/helm-golden.sh check` for default values. Both run through `mise run verify`.
* **No consumer and no vendor in the contract:** `go test ./internal/boundary/` (ADR-0069) stays green. A review step refuses a vendor name in `pkg/gateway`'s exported API outside an implementation's package.
* **Support matrix:** `mise run docs-check` (`scripts/support-matrix.py --check`) refuses an Envoy cell above `experimental` until the CI job or the live record it names exists.

## Pros and Cons of the Options

### Keep LiteLLM as the only gateway

* Good, because it costs nothing, and LiteLLM settlement is qualified live within 0.0001 USD.
* Bad, because one supply chain holds every provider key and Ploeg's master key, and an installer has no supported exit.
* Bad, because the coupling ADR-0008 called confined is already spread across eight places, and it grows with each feature.

### Define the contract as LiteLLM's management API

* Good, because the LiteLLM implementation is free and other proxies sometimes copy `/key/generate`.
* Bad, because a contract that is one vendor's API shape is the coupling under a new name. Envoy AI Gateway has no such API, and building an emulation layer is the Envoy implementation's work plus a compatibility burden.

### Build a Ploeg-owned metering proxy

* Good, because Ploeg would own metering end to end, whatever is upstream.
* Bad, because Ploeg would then be a gateway: streaming, provider protocols, retries and MCP, a new supply chain of its own and a new hot path. ADR-0008 put metering in a gateway the agent talks through for good reason, and ADR-0039 says that what is counted inside the Run's pod is observation, not settlement.

### Move to Envoy AI Gateway and retire LiteLLM

* Good, because only one implementation would need maintaining.
* Bad, because every deployment would break, the qualified settlement path would be lost, and the budget, revocation and spend fallbacks are not yet proven.

## Re-evaluation triggers

* Envoy AI Gateway or Agent Router ships per-credential budgets in currency, revocable credentials, or an API that reads usage per credential. Each one replaces a fallback in §2.
* The project's API group moves away from `aigateway.envoyproxy.io`, or Envoy Gateway drops `claimToHeaders`, `extAuth` or response-cost rate limiting.
* The spike shows that a client can set a projected header, or that a per-descriptor limit works. The first blocks the Envoy implementation; the second removes the tier ladder.
* A LiteLLM release changes any request the recording test pins, or another LiteLLM package or image is found compromised.
* An installer asks for a third gateway, or a gateway passes the contract suite outside this repository.
* A settlement from the access-log ledger is found below the gateway's own record, or a ledger gap is found that the receiver did not see.

## More Information

* Evidence: [2026-10-30 model gateway contract](../research/2026-10-30-model-gateway-contract.md). It covers the code survey, Envoy AI Gateway's and Agent Router's documentation, and the LiteLLM incident as its maintainers described it.
* Related: ADR-0008 (this record answers its 2026-10-31 review), ADR-0012, ADR-0039, ADR-0049, ADR-0071, ADR-0077, ADR-0078, ADR-0111, ADR-0123, ADR-0134, ADR-0154, ADR-0155, ADR-0162; [accounting invariants](../reference/accounting-invariants.md); [managed workers](../ops/managed-workers.md#configure-the-controller); [support matrix](../reference/support-matrix.md#model-gateway).
* 2026-10-30: proposed, after the owner's decision that day.
