# Credential gateway

The credential gateway of [ADR-0197](../adrs/0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md) (proposed) runs outside every Run pod and attaches the credentials ploegd minted for a Run to the requests that Run makes. This page states the contract the implementation in [pkg/credentialgateway](../../pkg/credentialgateway/) and [pkg/httpapi/credential_gateway.go](../../pkg/httpapi/credential_gateway.go) meets. The wire types are in [pkg/gatewaycontract](../../pkg/gatewaycontract/contract.go).

Gateway mode is opt-in. ploegd turns it on for the Teams that `PLOEG_CREDENTIAL_GATEWAYS` binds to a gateway. Nothing changes for a Team that no gateway serves.

## ploegd configuration

`PLOEG_CREDENTIAL_GATEWAYS` is a JSON list. Each entry names a gateway by the namespace and ServiceAccount that TokenReview reports for its projected token, and the Teams it serves:

```json
[{"namespace": "ploeg-runs", "serviceAccount": "ploeg-credential-gateway", "teams": ["builders"]}]
```

A Team belongs to one gateway at most. Gateway mode needs managed worker mode with the HMAC worker capability: the capability the gateway accepts is the Run's `controlToken`, and the key that seals grants at rest is derived from `PLOEG_WORKER_SIGNING_KEY` (HKDF-SHA256, AES-256-GCM, the Run token as additional data). ploegd refuses to start with `PLOEG_CREDENTIAL_GATEWAYS` and `PLOEG_WORKER_HMAC_CAPABILITY=off` or legacy worker mode. ploegd needs permission to create `tokenreviews.authentication.k8s.io` and its own ServiceAccount token.

## How a gateway proves itself

Every request a gateway sends ploegd carries `X-Ploeg-Gateway-Authorization: Bearer <projected token>`, a ServiceAccount token for the audience `ploegd`. ploegd verifies it with TokenReview, caches a verified token for one minute by its SHA-256, and maps it to the binding whose namespace and ServiceAccount match. No other secret identifies a gateway.

## The claim

The worker of a gateway Team sends `POST /api/v1/claim` to the gateway with no `Authorization` and its worker identity in `X-Ploeg-Worker-ID`. The gateway:

1. resolves the source address to exactly one pod of its namespace that matches the Run pod selector, is `Running`, is not being deleted and is controlled by a `Job` or a `Sandbox`;
2. checks that the worker identity is that pod's name or UID, and that the claim's `team` and `role` equal the pod's `ploeg.webgrip.dev/team` and `ploeg.webgrip.dev/role` labels;
3. attaches the bootstrap capability of that Team and Role, which only the gateway holds, its own token, and the pin headers `X-Ploeg-Gateway-Pod-Name`, `X-Ploeg-Gateway-Pod-Uid` and `X-Ploeg-Gateway-Pod-Ip`, and forwards the claim.

The gateway drops any of these headers a pod sends. Any other refusal answers `403` with `X-Ploeg-Refusal: claim_refused`.

ploegd refuses a claim for a gateway Team, before it claims anything, unless the gateway token verifies as the namespace and ServiceAccount of the Team's own binding and the pin names the claiming worker: `403` with `X-Ploeg-Refusal: claim_refused`. A worker of an older release, or one in another namespace, therefore never gets a Run of a gateway Team. For a claim that came through the gateway, ploegd mints the Run's model key, seals the grant with the forge credential it minted, the model key and the pin, stores it in `credential_gateway_grants`, and answers `"credentialGateway": true` with the Run's `controlToken` and without `forgeToken`, `forgeTokenPerRun`, `forgeTokenExpiresAt` and `transactionToken`. The `controlToken` is the only value of the Run that enters its pod. Such a claim meets the credential isolation every Role needs ([ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), amended 2026-11-18) without `forgeTokenIsolation` or `llmKeyIsolation`; its `harness` must still be a qualified one, or ploegd answers `403 credential_isolation_required`.

## A gateway Run's worker-control routes

ploegd serves `/api/v1/runs/{token}/...` for a Run with a grant, or a Run of a gateway Team, only when the request carries a gateway token that verifies for the pin's namespace and the Run's Team, pin headers equal to the stored pin, and a worker identity that is the pinned pod's name or UID. Anything else answers `403 pinned_to_another_pod`, whatever the Run's `controlToken` or transaction token says. A renewal of a gateway Run answers no `transactionToken`.

For such a Run ploegd also answers `403 credential_gateway_mode` to `POST /api/v1/runs/{token}/llm/credential`, `POST /api/v1/runs/{token}/forge-credential` and `POST /api/v1/runs/{token}/identity`: neither the pod nor its gateway fetches a credential or a Run identity token there.

## Grants

`POST /api/v1/credential-gateway/grants`, authenticated as a gateway, with `{"capability": "<controlToken>", "sourceIp": "<address the request came from>", "sourceUid": "<UID of the one running pod at that address>"}`.

| Answer | When |
| --- | --- |
| `200` and the grant, `Cache-Control: no-store` | the capability verifies, its Run is running and before its deadline, a gateway claimed it, the asking gateway's namespace is the pin's and is bound to the Run's Team, the capability's worker is the pinned pod, and the source address and pod UID are the pinned pod's |
| `401` | the gateway token does not verify |
| `403` | anything else about the capability, the Run, the namespace or the pin |
| `410` | the Run has settled or its deadline passed |

The grant carries the Run, Work Item, Team, Role, whether it writes, its branch, whether it prepares a repository, whether its harness loads repository hooks, the forge dialect and URL, owner, repository and base branch, the forge credential and its expiry, the model key and alias, the MCP access groups, the registered MCP servers the Run's Role was given with each server's credential (read from the secret store when the grant is asked for, never sealed or stored; ADR-0199), the pin and the Run's deadline. When the forge credential expires within five minutes, ploegd mints a new one before it answers (the refresh the worker used to do), revokes the old one and reseals the grant. The orphan sweep deletes the grants of every Run that no longer runs. When ploegd signs transaction tokens it adds a `transactionToken` signed for that answer, which the gateway attaches to the Run's worker-control calls. When the forge credential expires within five minutes, ploegd mints a new one before it answers (the refresh the worker used to do), revokes the old one and reseals the grant. The orphan sweep deletes the grants of every Run that no longer runs.

## Route families

The gateway serves each family on its own listener: ploegd on `:8080`, the forge on `:8081`, the model gateway on `:8082`, registered MCP servers on `:8083` at `/mcp/<server key>`, and `/healthz` on `:8090`.

- **ploegd** passes `POST /api/v1/claim` (above). A `/api/v1/runs/{token}/...` call must present the Run's `controlToken` as `Authorization: Bearer`, come from the pinned pod with its name or UID as the worker identity, and name the capability's own Run. The gateway then replaces the capability with the grant's transaction token (or keeps the `controlToken` when ploegd signs none), adds its own token and the stored pin headers, and audits the call under the `ploegd` family. After ploegd accepts the Run's outcome, the gateway forgets the Run's grant and refuses every further forge, model and MCP request of it with `run_settled`. Everything else answers `403 forbidden_route`.
- **forge** serves git smart-HTTP and the forge API of the Run's own repository behind the fences of the worker's forge proxy, one implementation in [pkg/worker](../../pkg/worker/gatewayfence.go): repository and Role allowlist, path normalisation, the push fence to `refs/heads/<run branch>` without deletion, the prepare-repository fence of ADR-0194, the OpenHands hooks-file fence of ADR-0148 (when the claim named an OpenHands harness) and leak inspection. The gateway checks a push's content from the pushed pack itself: it fetches the forge's base branch into a bare repository of its own, indexes the pack with `--fix-thin`, and refuses a prepare Run's push unless it only adds the root `AGENTS.md`, and an OpenHands Run's push that touches `.openhands/hooks.json`. A Run without a base branch cannot pass those checks.
- **mcp** ties the request to its Run and pin like every family, then enforces the Run's MCP grant with `pkg/mcpgateway`: only granted servers, methods and tools; no write tool to a Run that does not write; the server's credential attached; the tool list rewritten to Ploeg's annotations; tool text in an untrusted frame; server requests, redirects, oversized answers and credential echoes refused (ADR-0199 section 4). A worker reads its server keys and skill digests from `GET /api/v1/runs/{token}/tools`; a Run outside the gateway gets skills and no MCP server.
- **model** forwards inference only: `POST` to `chat/completions`, `completions`, `embeddings`, `responses`, `messages` and `messages/count_tokens`, `GET` of `models` and `model/info`, each with or without a `/v1` prefix, and LiteLLM's `/mcp` routes. Any other path, LiteLLM's `/key/*`, `/user/*`, `/team/*` and `/spend/*` among them, and any path that is not already clean, answers `403 forbidden_route`. It forwards to `PLOEG_GATEWAY_MODEL_URL` with the Run's key in the header the caller used (`Authorization: Bearer`, `X-Api-Key` or LiteLLM's key header), streaming unchanged, and removes every other header a capability can be presented in.

A request presents the capability where a credential goes: a Basic password, a `token` or `Bearer` authorization, `PRIVATE-TOKEN`, `X-Api-Key` or LiteLLM's key header. The gateway removes it before forwarding. A git request without any credential gets a `401` Basic challenge; any other request without one gets `403 missing_capability`.

Before any upstream is reached, the gateway resolves the source address to exactly one running pod that is not being deleted, fetches the grant for that pod's address and UID (cached in memory for at most 10 seconds, two minutes before the forge credential expires, or the Run's deadline, whichever is first, so a Run settled elsewhere loses its capability within 10 seconds), and checks that the pod's address, UID and name are the pin's. A new pod that reuses a settled pod's address has another UID and is refused. Refusals carry `X-Ploeg-Refusal`:

| Reason | Status |
| --- | --- |
| `capability_refused` | 403 |
| `run_settled` | 403 |
| `pinned_to_another_pod` | 403 |
| `rate_limited` | 429 |
| `credential_gateway_unavailable` | 503: ploegd cannot be reached or answers an error. Nothing is forwarded. |
| a forge fence's reason (`outside_repository`, `push_to_other_branch`, …) | 403 |

An upstream's own answer, `429` included, passes through unchanged.

## Pluggable upstreams

A route family is an `Upstream` (`Family`, `Credential`, `Serve`) that `Gateway.Register` adds. The gateway has tied the request to its Run, checked the pin and applied the Run's limits before `Serve` runs; `Serve` attaches the credential and must never forward the capability. The `anthropic` family of section 7 of ADR-0197 is not built.

## The Run pod the chart renders

With `executor.credentialGateway.enabled` the chart refuses to render a Run pod that could hold a usable secret; the per-Run capability is the only value in it. Besides a `secretKeyRef`, an `envFrom` Secret, a Secret or projected volume and a ServiceAccount token, it refuses DinD (`harness.dind: true`, whatever `acknowledgePrivilegedDind` says), any privileged container, `hostNetwork`, `hostPID` and `hostIPC`, `DOCKER_HOST`, `DOCKER_CERT_PATH` and `DOCKER_TLS_VERIFY`, a mount at `/certs` and a `docker-` volume: a Docker daemon's client certificates are root on the node. Volumes are an allowlist: a Run pod mounts only `emptyDir` and `image` (toolchain) volumes, so `hostPath`, `csi`, `persistentVolumeClaim` and every other type are refused. The Run pod's NetworkPolicy admits DNS and the gateway's four ports and nothing else: a harness's `telemetry.endpoint` and `networkPolicy.worker.extraEgress` are not added to it, so a harness's telemetry does not leave a gateway-mode Run pod, and `executor.sandbox.networkPolicy`, whose rules agent-sandbox would add, is refused. The check reads the rendered pod, and [check-credential-gateway.py](../../scripts/check-credential-gateway.py) checks the same properties of the CI render.

## Limits and audit

`PLOEG_GATEWAY_LIMITS` sets, per route family, a token bucket of requests per second with a burst and of request bytes per second, per Run (per source pod for ploegd), and `maxUpstreamConnections` for the namespace.

The gateway logs one record for every request it ties to a Run: Run, Work Item, Team, credential kind and identity (the model key alias, never a value), family, method, path with the capability redacted, status, the declared request bytes, duration and refusal reason. Every ten seconds it sends `POST /api/v1/credential-gateway/audit` the per-Run totals by family and credential and every refusal. ploegd adds the totals to `credential_gateway_use`, writes each refusal as a `credential_gateway.refused` audit row and each MCP call the mcp family answered (server, method, tool, access, `allowed` or the refusal reason, status, bytes and duration) as an `mcp.call` audit row under the Run's Work Item, only for Runs of the asking gateway's own Teams, read from the Run, so the audit of a Run whose grant the sweep already dropped is still kept. A batch ploegd does not accept stays for the next flush.
