---
status: proposed
date: 2026-11-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-03-31
---

# MCP servers and skill sources are registered per Tenant, attached to a Team's Roles by id, and reached only through the credential gateway

## Context and Problem Statement

A Run reaches MCP tools in one way today: [ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md) mints the Run's model key with LiteLLM MCP access groups bound by a LiteLLM team, and the worker points `claude-code` or an `acp` agent at the model gateway's `/mcp` ([worker.go](../../pkg/worker/worker.go) `gatewayMCPURL`, [claudecode.go](../../pkg/harness/adapters/claudecode/claudecode.go) `writeMCPConfig`). That ties tools to one gateway product and one of its features: an installation on Envoy AI Gateway, or one that wants a vendor's hosted MCP server that no gateway fronts, gets none. The tool bound is LiteLLM's configuration, which Ploeg cannot see ("read-only is a property of the gateway's server configuration", ADR-0078 *Consequences*), and the static values `litellmTeamId` and `mcpAccessGroups` exist only in the chart, so [ADR-0198](0198-a-team-has-an-immutable-id-a-renameable-key-and-a-free-name-and-a-proven-person-creates-one-at-runtime.md) refuses `tools` and `skills` for a Team people create at runtime and left the question open in its section 14.

Skills have the same shape of problem. [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) installs Ploeg's embedded skills and [ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md) proposes an APM catalogue per static Team, fetched from git. A runtime Team has neither, and nothing lets an installation publish a skill bundle once (from an OCI registry, an HTTPS URL or a git commit) and attach it to the Roles of several Teams.

[ADR-0197](0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md) adds the constraint: no credential of any kind may be in a Run pod. An MCP server's token is a credential, so it can only be attached outside the pod.

The decision-maker set the requirement on 2026-11-18: MCP tools and skills work now for every Team, runtime Teams included, fully implemented and vendor-agnostic, with any MCP server and any skills source, and with an MCP server's credential held only outside the Run pod.

How does an installation register MCP servers and skill sources, how are they given to a Team's Roles, how does a Run reach a server and its credential stay out of the pod, how are skills delivered, who may change any of it, and what becomes of ADR-0078?

## Decision Drivers

* **Vendor-agnostic.** Any MCP server that speaks the Streamable HTTP transport, behind any gateway or none; any skills source that can be pinned by a digest. No gateway product is the mechanism.
* **No credential in the Run pod** (ADR-0197). A server's credential lives in the secret store of [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) and is attached by the credential gateway.
* **Ploeg is the bound.** Which tools a Role may call, and whether a tool may write, is Ploeg's record, enforced by Ploeg's own code on every call, not a property of a third-party configuration.
* **References are ids** (ADR-0198). A rename of a Team, a server or a source never breaks an attachment.
* **Tool output is untrusted** ([ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md)). A server's text reaches the agent framed as data it cannot close.
* **The operator's gates, as for targets and connections.** Opt-in per installation, a consumer permission, a proven person, `If-Match` and a dry run that probes, as [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md) and [ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) set them.
* **Nothing changes for an installation that does not opt in**, and ADR-0078's existing path keeps working ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)).

## Considered Options

Where the tool bound and the credential live:

* **A. A Ploeg registry of servers and sources, attached by id, with the credential gateway as the only path to a server**
* B. Keep ADR-0078: LiteLLM access groups are the registry, the bound and the credential holder
* C. A gateway contract like [ADR-0177](0177-ploeg-reaches-models-through-a-gateway-contract-with-litellm-and-envoy-ai-gateway-as-implementations.md)'s, in which Ploeg programs each gateway product's own MCP feature
* D. Pass each server's URL and credential to the Run and let the harness connect

How skills reach a Run:

* **S1. Content-addressed bundles from an OCI registry, an HTTPS URL or a git commit, pinned by digest, fetched by ploegd and served to the Run by digest, installed read-only**
* S2. ADR-0092's APM catalogue only
* S3. The Run fetches its skills itself

## Decision Outcome

Chosen options: "**A**" and "**S1**". Ploeg keeps a registry of MCP servers and skill sources per Tenant, attaches them to a Team's Roles by id, enforces each Role's tools itself in the credential gateway, and serves skills it fetched and verified by digest. It is the only combination in which any server and any source work, the credential never enters the pod, and the bound is Ploeg's own record checked by Ploeg's own code.

Proposed. The registry, the store, the operator API, the attachment, the grant ploegd computes for a Run and the skill fetcher are built with this record; the gateway's MCP route family and the harness wiring are built against the gateway's upstream seam (section 5, *More Information*).

### 1. The MCP server registry

An **MCP server** is one row per Tenant (ADR-0178):

| Field | Rule |
| --- | --- |
| `id` | `mcp_` and a lowercase ULID, generated by Ploeg, immutable. Attachments and audit rows store it. |
| `key` | A slug unique per Tenant (`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`), used in URLs and in the name a harness shows. A change is a rename; the id stays. |
| `name` | Free display text, 1 to 80 characters. |
| `url` | The server's Streamable HTTP endpoint. `https` only; `http` only for a host the installation lists in `toolRegistry.plainHTTPHosts` (a cluster-local adapter, section 7). No user info, no fragment. A host that resolves to a loopback, link-local, private or unspecified address is refused unless listed in `toolRegistry.privateHosts`. |
| `transport` | `streamable-http`. `stdio` is refused with `422 transport_needs_adapter` (section 7). |
| `auth` | `{kind}`: `none`; `bearer` (sent as `Authorization: Bearer`); `header` with `header` naming one request header (`X-Api-Key`, never `Host`, `Cookie`, a hop-by-hop or an `Mcp-*` header); `oauth-client-credentials` with `tokenUrl`, `clientId` and optional `scopes`, the client secret stored, the access token obtained by ploegd and cached in the grant until its expiry. |
| `credential` | Write-only. A request may carry `{"value": "..."}` (or `{"clientSecret": "..."}` for client credentials) once; Ploeg writes it to the ADR-0184 store under `mcp-servers/<id>` and keeps the reference. No response, log, event or row ever carries the value; reads show `credential: {"set": true, "ref": {store, path, version}}`. `auth.kind: none` refuses a credential. |
| `tools` | The **allowed tool list**: 1 to 200 entries `{name, access}`, `access` is `read` or `write`. A tool the server offers and the list does not name is never shown to a Run and never callable. |
| `adapter` | Optional, `gateway-access-groups` only: the server is a model gateway's MCP endpoint whose credential is the Run's per-Run model key (section 8). |

**Read-only versus write is Ploeg's annotation, and the server can only make it stricter.** The registrar sets `access` per tool. The dry-run probe (section 6) reads the server's `tools/list` and its annotations; a tool the server marks `destructiveHint: true` or `readOnlyHint: false` cannot be registered `read` (`422 tool_access_conflicts`), and a server's `readOnlyHint: true` never makes a tool `read` on its own. A `write` tool is accepted only when the installation sets `toolRegistry.allowWriteTools: true`. This replaces ADR-0078's "read-only only; a write-capable group needs a further ADR" with a rule Ploeg checks.

A static installation declares servers in the configuration file under `toolRegistry.mcpServers.<key>` with the same fields and a `credentialEnv`, the environment variable of ploegd that holds its credential (ploegd holds root credentials, ADR-0197 section 4; the value reaches only the gateway's grant, never a Run pod). A static entry's id is `mcp_static_<key>` (`skl_static_<key>` for a skill source), because the file is its identity. Static wins on a key conflict, as for targets and Teams: a runtime server whose key a static one takes turns `shadowed`.

### 2. The skill source registry

A **skill source** is one row per Tenant:

| Field | Rule |
| --- | --- |
| `id` | `skl_` and a lowercase ULID, immutable. |
| `key`, `name` | As for servers. |
| `kind` | `oci`, `https` or `git`. |
| `ref` | `oci`: `<registry>/<repository>@sha256:<64 hex>`, a digest reference only, never a tag. `https`: a URL to a `.tar.gz` bundle, with `digest: sha256:<64 hex>` of the archive. `git`: `url`, `commit` (40 hex) and `digest` of the tree's canonical archive. |
| `credential` | Optional and write-only, as for servers: a pull credential ploegd uses to fetch from a private registry or host. It never reaches a Run. |

**Bundles are content-addressed.** ploegd fetches a source once, verifies the archive against the pinned digest, unpacks it with the bundle rules below, and keeps it by digest. A Run never fetches a source and never sees its credential; the worker asks ploegd for the Run's skills by digest with its Run capability and installs them **read-only** (files `0444`, directories `0555`) in ADR-0035's directories and each harness's own.

Bundle rules, each a refusal with a reason, never a partial install: at most 10 MiB compressed and 50 MiB unpacked, at most 2,000 entries; regular files and directories only (no symlink, hard link, device or FIFO); no absolute path, no `..` segment, no path over 512 bytes; every top-level directory is one skill with a `SKILL.md` whose front matter names it; and **no credential in a bundle**: a file that matches Ploeg's secret patterns (the leak watch's private-key, token-prefix and high-entropy-assignment rules) refuses the bundle with `bundle_carries_credential`, because a skill is mounted into the Run pod.

A source that cannot be fetched leaves its skills out of the Run with the reason recorded, and the Run continues (ADR-0092's "fail open on availability, closed on content"). Content that does not match its digest is never installed.

ADR-0092's APM catalogue stays the format a static Team's file may name; its locked entries become `git` sources of this registry when that record is built.

### 3. Attachment to a Team's Roles

An **attachment** gives one Role of one Team (by ADR-0198 Team id) servers and skills:

```json
{
  "roles": {
    "builder": {
      "mcp": [{"server": "mcp_01jd…", "tools": ["search_logs", "get_metric"]}],
      "skills": ["skl_01jd…"]
    },
    "*": {"skills": ["skl_01je…"]}
  }
}
```

* `server` and each skill accept an id or a key and are stored as the id. `"*"` applies to every Role of the Team.
* `tools` is a subset of the server's allowed list; omitted means all of it. A `write` tool attaches only to a Role whose plan entry has `writes: true`; to a reader Role it is `422 write_tool_on_reader`.
* At most 20 servers and 50 skills per Role, and 100 tools per Role in total.
* One attachment per Team; it is replaced whole with `If-Match`. A static Team may also declare `teams.<key>.tools` in the file; then the file is the authority and the API answers `409 attachment_set_by_configuration` naming the key.
* This answers ADR-0198 section 14: a runtime Team gets tools and skills **through an attachment**, never through `tools` or `skills` fields in its manifest, which stay `422 field_not_allowed`. The attachment carries no URL and no credential, so it widens a Run only to what an operator registered.

**The Run's grant.** At claim ploegd resolves the Run's Tenant, Team id and Role to its attachment and builds the Run's tool grant (a Run whose Tenant is not the registry's gets no server, `tenant_mismatch`): for each server its id, key, URL, auth kind, credential reference and the Role's tools with their access; for each skill its id, key and digest. The grant is part of the credential gateway's grant (ADR-0197 section 3) and never part of a claim response. The claim response tells the worker only which server **keys** exist (so the harness can be configured with the gateway's URLs) and which skill digests to install.

A Team is resolved through ADR-0198's directory and the attachment row stores its `team_id` with the key it had when written. A Team the directory does not hold yet is stored by key with a null `team_id`, which ADR-0198's backfill fills like every other table it lists; every read resolves by `team_id` first. Runtime Teams resolve the same way once ADR-0198's create route is built.

### 4. A Run reaches an MCP server only through the credential gateway

The credential gateway of ADR-0197 gains a fifth route family, **MCP**, next to forge, model, ploegd and Anthropic: `https://<gateway>/mcp/<server key>`. The harness is configured with one MCP server per key in the claim, each pointing at the gateway, with the Run's capability as its bearer. The gateway, for every request:

1. checks the capability and its pod pin (ADR-0197 section 2) and that the Run's grant names the server;
2. accepts only JSON-RPC 2.0 over `POST` (and the `GET` stream and `DELETE` of the transport), at most 1 MiB per request, and only the methods `initialize`, `notifications/initialized`, `notifications/cancelled`, `ping`, `tools/list` and `tools/call`; `resources/*`, `prompts/*`, `completion/*`, `logging/*` and every other method are `-32601` from the gateway and never reach the server; a message whose JSON can be read two ways is refused before any check (`ambiguous_request`): an object anywhere in it that repeats a key, exactly or in another case under Unicode case folding, because Go's decoder matches keys case-insensitively and keeps the last duplicate while a server may read keys exactly; a batch, a member JSON-RPC does not define, invalid UTF-8 and nesting deeper than 64 are refused too, and the gateway forwards a canonical message re-encoded from the structure it checked, never the bytes it received;
3. on `tools/call`, refuses a tool the Role was not given, and a `write` tool under a read grant, with a JSON-RPC error that names the reason, before the server;
4. removes the capability and every `Authorization`, `Cookie` and `X-Api-Key`-like header the harness sent, attaches the server's credential (from the grant, never from the pod), forwards to the registered URL with redirects refused, and keeps the `Mcp-Session-Id` the server issues bound to the Run;
5. on the response, `tools/list` is rewritten to exactly the Role's tools, with the server's annotations replaced by Ploeg's (`readOnlyHint` true for `read`, `destructiveHint` true for `write`), so a server cannot advertise a tool it was not allowed to; a `tools/call` result's text content is wrapped in an ADR-0170 frame with source `mcp:<server key>/<tool>`, and non-text content (images, embedded resources, resource links) is replaced by a note that it was withheld;
6. refuses, as a server fault, any server-to-client request (`sampling/createMessage`, `elicitation/create`, `roots/list`), any response over 1 MiB or with more than 1,000 SSE events, any response that repeats a key as a request may not (`upstream_ambiguous`), and any response that carries the server's own credential or the Run's capability (the leak watch of ADR-0197 section 3) as is, JSON-escaped, in base64 or base64url at any byte offset, or as any 16 characters of it, in the bytes received, in each decoded message or in the answer the gateway would send;
7. writes one audit record per call: Run, Work Item, Tenant, server id, tool, access, outcome, bytes and duration, never arguments or results, and counts `tools/call` against a per-Run token bucket (default 60 calls a minute, 1,000 per Run).

Text after the frame and the gateway's own error texts are Ploeg's; everything inside the frame is the server's data. The delivery contract's one statement about framed text (ADR-0170) covers it.

**No gateway, no registry tools.** A Run in a namespace without the credential gateway gets no registry MCP server: ploegd leaves them out of the claim and records `mcp_requires_credential_gateway` on the Run, because the only other place to attach a credential is the pod. Registry skills carry no credential and are installed in every namespace. ADR-0078's path (section 8) is unchanged in a namespace without the gateway.

### 5. The gateway's seam

The enforcement of section 4 is one Go package, `pkg/mcpgateway`, an `http.Handler` constructed with a grant source and an audit sink. The credential gateway mounts it under `/mcp/`; until the gateway binary exists, its tests drive it against `httptest` MCP servers, including hostile ones. ploegd serves the MCP part of a Run's grant on the gateway's grant route of ADR-0197 section 3, reading credentials from the store at that moment and never caching values in its database.

### 6. The operator API

Versioned with the operator API (`docs/contracts/operator-api.v1.schema.json`):

| Method and path | Effect |
| --- | --- |
| `GET /api/v1/operator/mcp-servers` | Servers of the consumer's Tenant, static and runtime, with `id`, `key`, `name`, `source`, `state`, `revision`, `url`, `transport`, `auth.kind`, `credential.set`, `tools`. Never a credential value. |
| `GET /api/v1/operator/mcp-servers/{server}` | One server, by id or key. |
| `POST /api/v1/operator/mcp-servers` | Create. `201`, or `200 created: false` for the same key and body. |
| `POST /api/v1/operator/mcp-servers?dryRun=true` | Validate and **probe**: ploegd connects with the given credential (never stored), runs `initialize` and `tools/list`, and reports the server's tools and annotations, every listed tool the server does not offer (`tool_not_offered`), every access conflict, and the protocol version. Writes nothing. |
| `PUT /api/v1/operator/mcp-servers/{server}` | Replace, `If-Match` required, `?dryRun=true` accepted. A request without `credential` keeps the stored one. |
| `DELETE /api/v1/operator/mcp-servers/{server}` | Remove. `409 server_in_use` while an attachment names it; the stored credential is deleted with the row. |
| `GET`, `POST`, `PUT`, `DELETE /api/v1/operator/skill-sources[/{source}]` | The same for skill sources. The dry run fetches, verifies the digest and the bundle rules, and lists the skills it found. |
| `GET /api/v1/operator/teams/{team}/tools` | The Team's attachment and the grant each Role would get. |
| `PUT /api/v1/operator/teams/{team}/tools` | Replace the attachment. `If-Match` (or `If-None-Match: *` for the first), `?dryRun=true`. |
| `DELETE /api/v1/operator/teams/{team}/tools` | Remove the attachment. `204`. |

Gates, all required for every write, as ADR-0193 section 5 sets them:

1. **Opt-in.** The configuration file's `toolRegistry.enabled` (default `false`); without it every write is `422 tool_registry_not_accepted`. `toolRegistry` also carries `requireClaims` and `stepUp`, `allowWriteTools`, `plainHTTPHosts`, `privateHosts` and the limits.
2. **Permission.** `PLOEG_OPERATOR_CONSUMERS` gains `manageTools` (default `false`); `OperatorPrincipal` gains `CanManageTools`. A consumer without it reads only the servers and sources attached to the Teams it may read, and never a URL's query string.
3. **A proven person in every attribution mode** ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)), because a server reaches outside systems with a credential.
4. **Operator authority**: the claims and step-up `toolRegistry.requireClaims` names.

Errors use the operator envelope: `403 permission_required`, `403 person_token_required`, `404 unknown_server`, `404 unknown_source`, `404 unknown_team`, `409 key_taken`, `409 server_in_use`, `409 source_in_use`, `409 conflicts_with_static`, `409 attachment_set_by_configuration`, `412 revision_stale`, `428 if_match_required`, `422 tool_registry_not_accepted`, `422 invalid_url`, `422 host_not_allowed`, `422 transport_needs_adapter`, `422 credential_not_allowed`, `422 credential_required`, `422 write_tools_not_allowed`, `422 tool_access_conflicts`, `422 unknown_tool`, `422 write_tool_on_reader`, `422 unknown_role`, `422 digest_required`, `422 store_unavailable`, `429 limit_reached`, `429 probe_rate_limited`. Every change writes an audit row (`mcp_server.created`, `.updated`, `.removed`, `skill_source.*`, `team_tools.replaced`, `team_tools.removed`, `tool_registry.refused`) with the proven person as actor, never a credential value.

### 7. stdio servers run in a sandboxed adapter, outside the Run pod

A server that speaks only stdio is not run in the Run pod and not in ploegd. An installation that needs one renders it with the chart as an **MCP adapter**: its own Deployment in the gateway's namespace under the restricted Pod Security standard, with a stdio-to-Streamable-HTTP bridge as its entrypoint, no ServiceAccount token, egress only to the hosts it declares, and its credential from a Secret mounted only into the adapter. Its cluster URL is then registered as an ordinary `streamable-http` server listed in `toolRegistry.plainHTTPHosts`, so the gateway's enforcement applies unchanged. The chart renders it from `executor.credentialGateway.mcp.adapters`: the image pinned by digest, `ploeg-mcp-stdio-bridge` copied in from the worker image as the entrypoint, one server process per MCP session so two Runs never share one, ingress only from the gateway, and the API's `transport_needs_adapter` refusal points there.

### 8. ADR-0078 becomes one adapter

ADR-0078's path is re-read as one registry entry kind, `adapter: gateway-access-groups`: a server whose URL is a model gateway's MCP endpoint and whose credential is the Run's per-Run model key, bounded by the gateway's own team and access groups **and** by Ploeg's tool list. The existing `litellmTeamId` and `mcpAccessGroups` of a static Team keep working exactly as today in a namespace without the credential gateway, and are reported in `GET /api/v1/operator/mcp-servers` as a static server of that kind. In a gateway namespace the model gateway's MCP endpoint is reached through the MCP route family like any server, the model key attached by the gateway, so Ploeg's tool list and framing apply on top of LiteLLM's bound. Envoy AI Gateway, a vendor's hosted server, or any other product is a plain `streamable-http` entry; none is the mechanism. Deprecating the static `mcpAccessGroups` values is left to the release that makes the credential gateway the default (ADR-0197 section 6).

### Consequences

* Good, because any MCP server and any skills source work, behind any gateway or none, and the tool bound is Ploeg's record, checked on every call by Ploeg's code.
* Good, because an MCP credential is written once to the secret store, attached only by the gateway and never seen by a person after it is pasted, a consumer or a Run pod.
* Good, because runtime Teams get tools and skills (ADR-0198 section 14) without any field that names a URL or a credential.
* Good, because a server cannot widen what a Run sees: unlisted tools, resources, prompts and server-initiated requests never reach the agent, and its text arrives framed.
* Bad, because registry MCP needs the credential gateway: until a namespace enables it, its Runs get registry skills and no registry tools. Accepted: the requirement forbids the alternative.
* Bad, because the gateway now parses MCP JSON-RPC, which ties it to the protocol's evolution. The handler accepts the protocol versions it was tested with and refuses others at `initialize`.
* Bad, because ploegd gains an outbound HTTP client to arbitrary operator-named hosts for the probe and the skill fetch. Bounded by `https`, the private-address refusal, timeouts, size limits and the probe rate limit.
* Neutral, because framing lowers prompt-injection risk from tool output and does not remove it (ADR-0170); the Role's tool list, read-only enforcement, the budget and the human merge remain the controls when a model follows the text anyway.

### Confirmation

`mise run verify` runs these:

* `go test ./pkg/toolregistry/`: validation of every server and source field, including a `stdio` transport, a URL with user info, a private host, a `read` tool the server marks destructive, a tag instead of a digest, a credential with `auth.kind: none` and a write tool when `allowWriteTools` is off; the grant ploegd builds for a Role, `"*"` merged, write tools left out of a reader Role; and the bundle rules against hostile archives (a symlink, a `..` path, an absolute path, a device, an oversized archive, a digest mismatch, a bundle that carries a private key).
* `go test ./pkg/store/`: the migration, idempotent create by key, `If-Match` staleness, the one-live-key index, removal refused while attached, and audit rows that carry no credential value.
* `go test ./pkg/httpapi/`: every gate (no permission, opt-in off, asserted attribution, missing claim), create with a credential whose value then appears in no response body, audit row or log line, dry run that writes nothing and reports an offered tool missing, stale `If-Match`, attachment of a write tool to a reader Role, and every response validated against the operator schema.
* `go test ./pkg/mcpgateway/`: an `httptest` MCP server receives the stored credential and never the capability; a hostile server that lists extra tools, marks a write tool read-only, returns text that tries to close its frame, sends `sampling/createMessage`, redirects, echoes its credential, or streams past the limits is contained, each case asserted on what the harness side receives; a call to an unlisted tool, a write tool under a read grant, `resources/read` and a request without the capability never reach the server; and every call writes one audit record without arguments.
* `go test ./internal/ledger/` keeps this record well-formed and `go test ./internal/boundary/` keeps it free of consumer names.
* Not yet confirmed: a real `claude-code` Run in a gateway namespace listing a registry server's tools through the gateway. That needs the credential gateway binary of ADR-0197.

## Pros and Cons of the Options

### B. Keep ADR-0078

* Good, because it exists and is tested.
* Bad, because it works only with LiteLLM, its bound is LiteLLM's configuration that Ploeg cannot see, and runtime Teams cannot use it.

### C. A gateway contract that programs each product's MCP feature

* Good, because the gateway product would hold credentials and enforce tools.
* Bad, because every product's MCP feature differs (LiteLLM access groups, Envoy AI Gateway's MCP routes), Ploeg would depend on each one's tool filtering and annotation handling, and a server no gateway fronts would still need one.

### D. The harness connects to each server directly

* Good, because it needs no gateway route family.
* Bad, because the credential would be in the Run pod, which ADR-0197 forbids, and nothing but the server would bound the tools.

### S2. ADR-0092's APM catalogue only

* Good, because it pins by commit and hash and Renovate updates it.
* Bad, because it is per static Team, reads only git, and a runtime Team has no file.

### S3. The Run fetches its skills itself

* Good, because ploegd stores nothing.
* Bad, because a private source's credential would be in the pod, and every Run would need egress to every source.

## Re-evaluation triggers

* The MCP specification changes the Streamable HTTP transport, adds a method a harness needs from a server, or standardises a gateway or tool-filtering profile Ploeg could adopt instead.
* A harness Teams use cannot be pointed at an HTTP MCP server with a bearer header.
* An installation needs a stdio server and the adapter of section 7 is built, or one asks for a transport the gateway cannot proxy.
* A Run's transcript shows an instruction taken from framed tool output, or a server is found to have reached the agent outside its frame.
* The Agent Skills specification defines a bundle or lock format, or OCI defines a skill media type.
* The credential gateway of ADR-0197 is built, accepted or changed, or ADR-0198 is merged with a Team id shape other than section 3 assumes.

## More Information

* Requirement: owner decision 2026-11-18 (28): MCP tools and skills for Teams, runtime Teams included, work now, fully implemented and vendor-agnostic, not tied to LiteLLM's access groups or any one gateway product, with server credentials held only outside the Run pod.
* Answers section 14 of [ADR-0198](0198-a-team-has-an-immutable-id-a-renameable-key-and-a-free-name-and-a-proven-person-creates-one-at-runtime.md); when that record is merged its section 14 gains a line pointing here. Builds on [ADR-0197](0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md) (section 4 adds a route family to it), [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) (credential store), [ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md) (framing), [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) and [ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md) (skills). Re-reads [ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md) as one adapter (section 8); when this record is accepted, ADR-0078 gains a More Information line pointing here.
* 2026-11-18: proposed with the registry, store, operator API, attachment, grant and skill fetcher. The MCP route family is built as `pkg/mcpgateway` against the credential gateway's seam; mounting it in the gateway binary waits for that binary.
* 2026-11-18: built against the credential gateway (ADR-0197, #444 and #450): the gateway mounts the MCP route family on `:8083`, ploegd's grant carries each Run's servers with credentials read at that moment, `GET /api/v1/runs/{token}/tools` and `/skills/{digest}` serve a worker its server keys and bundles, claude-code and acp are configured with the gateway's URL per server and the Run's capability, the worker installs registry skills read-only after checking each digest, and the chart renders the sandboxed stdio adapter. Not yet confirmed on a cluster.
* 2026-11-18: the stdio adapter isolates its sessions: each session's process runs in its own directory, created 0700 under the adapter's `/sessions` emptyDir as its working directory, `HOME` and `TMPDIR` and removed when the session ends, with an environment built from PATH, locale, time zone, CA bundle and the credential `--pass-env` names; the adapter mounts no writable `/tmp`, the bridge makes itself non-dumpable so its own environment is unreadable to the sessions, and each adapter names the one Team that uses it (`team`). The sessions still run as one user: a uid per session needs `CAP_SETUID`, which the restricted Pod Security standard forbids, so a compromised server process can read a sibling session's directory and environment, and an adapter is never shared between Teams for that reason.
* 2026-11-18: the worker checks each registry skill bundle twice against the digest the Run's tool listing names: the canonical digest of the archive bytes it downloaded before it writes anything, and the canonical digest of the files it reads back from disk after it installs them, with every directory `0555`, every file `0444` or `0555` and no entry missing or added. It refuses a skill directory reached through a symlink and removes a bundle that fails in any directory from all of them. The harness runs as the worker's user (a child process in the shared layout; `executor.workerSecurityContext.runAsUser` for both containers in the separate one), and the worker cannot give the files another owner without `CAP_CHOWN`, so the modes stop an accidental write and the check catches a change before the harness starts, but a harness that runs `chmod` on its own files can still rewrite them during the Run.
