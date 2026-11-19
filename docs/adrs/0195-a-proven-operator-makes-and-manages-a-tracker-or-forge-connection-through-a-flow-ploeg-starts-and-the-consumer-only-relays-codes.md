---
status: proposed
date: 2026-11-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-03-31
---

# A proven operator makes and manages a tracker or forge connection through a flow Ploeg starts, and the consumer only relays codes

## Context and Problem Statement

[ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) decides where a connection's secrets come from (an OAuth code exchange Ploeg performs, a GitHub App, or a pasted token where its rule 1 allows one) and where they live (a `SecretStore` Ploeg writes, with only references in its own records). Its *More Information* leaves the operator routes that make and manage a connection open, and asks for them in a record of their own. Two proposed records already depend on them: [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) binds a tracker Scope through a connection named by id, and [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md) registers a Work Target through a store-backed forge connection named by id and reads its grant through `GET /api/v1/operator/forge-connections/{connection}/repositories`.

Today a connection exists only as static configuration: a tracker kind with environment variables or Helm Secrets, and a forge registry entry ([ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md)). `GET /api/v1/operator/connections` ([ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md)) reports how boards and repositories are wired; nothing creates, tests, replaces or removes a connection.

What operator contract lets an operator consumer connect a tracker or forge on a person's behalf, see and test what it made, reconnect it and remove it, so that the consumer never holds a secret, a relayed code can only complete the flow the same proven person started, and the connection ids ADR-0186 and ADR-0193 depend on are stable?

## Decision Drivers

* **No secret reaches a consumer, a row, a log or an event** (ADR-0184). A consumer carries a one-time code or a pasted token in, once, and gets references back.
* **A relayed code completes only the flow its person started.** A stolen consumer token, a second person in the same consumer, or another consumer must not be able to finish someone else's consent.
* **Connecting is a grant of reach.** A connection decides what Ploeg's credentials can touch, so it needs the same proof as registering a Work Target: a proven person, in every attribution mode ([ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md), ADR-0193 section 5).
* **Ids that others depend on never move.** A binding, a runtime target, an audit event and a pinned Work Item name a connection by id; reconnecting must not change it, and removing must not let the id be reused for another account.
* **The installation opts in**, as it does for bindings and runtime targets; an installation that does not opt in sees no change.
* **Ploeg never calls an address a consumer chose.** Every vendor URL comes from static configuration, so a request cannot turn Ploeg's code exchange into a request to an arbitrary host.
* **Core stays vendor-free** ([ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md)), and **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **Ploeg starts every flow and issues its `state`, bound to the proven person and the consumer; the consumer hosts the vendor callback and relays the code to Ploeg in a request body**
* Ploeg hosts the vendor callback itself and redirects the browser back to the consumer
* The consumer issues and checks the `state`, and Ploeg trusts the code it relays
* Connections stay static configuration; the secret store is filled only by the chart or an operator

## Decision Outcome

Chosen option: "**Ploeg starts every flow and issues its `state`**", because it is the only option that binds a relayed code to the person who started the flow without giving ploegd a browser-facing surface, and it is the relay ADR-0184 rule 1 already chose. It follows the shape ADR-0094 uses for a Team's model login: the consumer starts a pending sign-in, carries only a one-time code, and Ploeg does the rest.

Proposed, not built. Each section states the contract the implementation must meet.

### 1. What a connection is

A **connection** is one credential Ploeg holds for one account at one tracker or forge instance, used through one port.

| Field | Meaning | Rule |
| --- | --- | --- |
| `id` | The name bindings, targets and events use | Matches the operator API's name pattern, `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$` (`operatorName` in [`pkg/httpapi/operator.go`](../../pkg/httpapi/operator.go)). Section 2. |
| `port` | `tracker` or `forge` | One connection serves one port. One GitHub App can back a tracker connection and a forge connection, each its own installation connection (section 3). |
| `kind` | The adapter: `clickup`, `vikunja`, `github`, `forgejo`, `gitlab`, … | An adapter in the one adapter table (ADR-0183 section 3). |
| `instance` | The tracker or forge instance | An id from static configuration: a forge registry id (ADR-0016, [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md)) or a configured tracker instance. Never a URL from the request. |
| `method` | `oauth`, `github_app_installation` or `token` | How the credential was obtained. |
| `account` | The vendor account the credential acts as | The vendor's immutable id (user id, installation id and its account id) and a display login. Read from the vendor at completion, never from the request. |
| `source` | `runtime` or `static` | Static connections are listed read-only (section 6). |
| `state`, `invalidReason` | Section 5 | |
| `secrets` | References (store, path, version) per ADR-0184 rule 2 | Never a value. |
| `handOverAgent` | Tracker connections only | Section 9. |
| `label` | Optional display name | Free text, at most 200 characters. |

A row also carries the Tenant ([ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md)), `revision`, `created_by`, `updated_by`, the time and person of the last reconnect, the last probe's result, and `removed_at`.

A **connection app** is a GitHub App Ploeg holds because it created it from a manifest (section 3): its App id, slug, client id, and references to its private key, webhook secret and client secret. It is not a connection: it reaches nothing until a person installs it and an installation becomes a connection.

### 2. Ids

* The start request may name the `id`; without one, Ploeg derives `<kind>-<account login>`, lowercased, with every character outside the pattern written as `-` and a numeric suffix on collision.
* An id is unique within the Tenant across tracker and forge connections, live **and removed**, and may never equal a static connection name (a static tracker kind, a static forge registry id): `409 id_taken`. A removed id is never reused, not even for the same account, so an old audit event, binding row or pinned Work Item can never come to mean another account. A person who wants to keep a name reconnects (section 4) instead of removing and connecting again.
* **An id is stable across reconnects.** Reconnecting replaces the credential under the same id; it never creates a second connection. A rename of the account at the vendor changes the display login, never the id.

### 3. Flows

Every connection is made by a **flow**: Ploeg records a pending flow, the consumer sends the person to the vendor, and the consumer relays what the vendor hands back.

**Start.** `POST /api/v1/operator/connection-flows` with `port`, `kind`, `instance`, `method` (`oauth`, `github_app_manifest`, `github_app_installation` or `token`), and as the method needs: `redirectUri` (where the vendor returns the person; one of the consumer's `connectionRedirectUris`, section 7), `app` (the connection app to install, for `github_app_installation`), `owner` (the GitHub organisation that will own a manifest App; omitted for the person's own account), `connection` (an existing id, to reconnect, section 4), `id`, `label` and `handOverAgent`. Ploeg answers `201` with:

* `flow`: a public flow id, the method, `expiresAt`, and the `state`. The `state` is 32 random bytes, returned once in this body, stored only as a hash, bound to the proven person (ADR-0150's `oidc-` id), the consumer, the Tenant and the request, single-use, and valid for 15 minutes.
* `next`, by method:
  * `oauth`: `authorizeUrl`, built by Ploeg from the instance's statically configured OAuth application (client id, scopes, `redirect_uri`, `state`, and a PKCE challenge where the vendor supports it; Ploeg keeps the verifier). The consumer redirects the person to it.
  * `github_app_manifest`: `manifest` (the JSON the consumer posts as the `manifest` form field) and `postUrl` (`<instance>/settings/apps/new?state=…`, or `<instance>/organizations/<owner>/settings/apps/new?state=…`).
  * `github_app_installation`: `installUrl` (`<instance>/apps/<slug>/installations/new?state=…`).
  * `token`: nothing; the consumer posts the token to the completion route.

**Complete.** `POST /api/v1/operator/connection-flows/complete` with `state` and exactly one of:

| Field | Method | What Ploeg does |
| --- | --- | --- |
| `code` | `oauth` | Exchanges the code with the client secret and PKCE verifier at the configured token endpoint, reads the account, writes the access token (and a refresh token, where the vendor issues one) to the store. Expiring tokens are in scope: see section 5. |
| `code` | `github_app_manifest` | Calls `POST /app-manifests/{code}/conversions` within the hour GitHub allows, stores the App id, slug and client id on a new connection app, and writes the private key, webhook secret and client secret to the store. When GitHub returns no webhook secret, Ploeg generates one and sets it with the App's own `PATCH /app/hook/config`. The response carries the connection app and a new `github_app_installation` flow for it, so the consumer can send the person straight on to install. |
| `installationId` and `setupAction` | `github_app_installation` | Reads the installation with the App's JWT (`GET /app/installations/{id}`), refuses one that belongs to another App or is already a live connection, and stores the installation id. No new secret: the connection references the connection app's private key and mints installation tokens from it. `setupAction: "request"` (an organisation owner must still approve) stores the connection as `awaiting_consent`. The `installationId` is not proved through a GitHub user authorization: every manifest App is created with `public: false`, so only the account that owns it can install it, and an id taken from elsewhere names an installation of another App, which is refused. A public App would need that proof (re-evaluation triggers). |
| `token` | `token` | Only where ADR-0184 rule 1 allows it: the kind offers no OAuth flow, or this instance has no OAuth application configured. Otherwise `422 oauth_required`. At most 4096 characters. |

Then, in this order: exchange; write every secret to the store under a new version; probe (section 8); and in one transaction write the connection row, consume the flow and write the audit event. If the store write fails, Ploeg revokes the token it just obtained where the vendor allows it and answers `503 secret_store_unavailable`; nothing is recorded. If the row write fails after the store write, Ploeg deletes what it wrote; an hourly sweep deletes store paths under Ploeg's prefix that no live row or connection app references and that are older than an hour. A failed probe stores nothing and answers `422 probe_failed` with each check, unless the only failure is an installation awaiting approval.

A `code`, `state`, `installationId` or `token` travels only in a request body, never in a path or query string, so neither Ploeg's ingress nor its access log ever records one. The response carries references and the account, never a value.

**Flow status.** `GET /api/v1/operator/connection-flows/{flowId}` answers the flow's method, state (`pending`, `completing`, `completed`, `failed`, `expired`) and, once completed, the connection or connection app; only to the consumer and person that started it.

**Callback URLs.** The vendor returns the person to the consumer, never to Ploeg:

* An OAuth application registered at a vendor lists the consumer's callback as its redirect URI; Ploeg's static OAuth configuration for that instance lists the same URIs, and a start request must name one of them.
* A manifest names URLs the vendor does not check against anything, so Ploeg fills them and checks each against the consumer's `connectionRedirectUris`: `redirect_url` (where GitHub sends the manifest `code`), `setup_url` (where GitHub sends `installation_id` and `setup_action` after an install), `setup_on_update: true`, and `public: false`. The webhook goes to Ploeg: `hook_attributes.url` is `<PLOEG_PUBLIC_WEBHOOK_BASE>/webhooks/forge/github` (or `/webhooks/tracker/github` for a tracker App), where `PLOEG_PUBLIC_WEBHOOK_BASE` is a new, proposed setting: the HTTPS base at which vendors reach ploegd's existing `/webhooks/...` routes ([`pkg/httpapi/server.go`](../../pkg/httpapi/server.go)). The manifest's permissions and events come from the adapter, through a proposed `AppManifest` capability of its port, so core names no GitHub permission. Without `PLOEG_PUBLIC_WEBHOOK_BASE`, a manifest flow answers `422 webhook_base_missing`. ADR-0193 section 7 already picks the App's secret per delivery from `X-GitHub-Hook-Installation-Target-ID`, so every App Ploeg holds can share one webhook URL.
* The consumer's callback receives the code and `state` in its query string. The consumer must keep that page out of its own access logs and send it with `Referrer-Policy: no-referrer`, and relay both to Ploeg at once. Ploeg's outbound request logging strips query strings, because some vendors take the code and client secret in the token request's query (ClickUp's `POST /api/v2/oauth/token` does).

### 4. Reconnect

A reconnect is a flow started with `connection: <id>`, of the same `port`, `kind` and `instance`; the method may differ (a pasted token replaced by OAuth once the installation registers an OAuth application). On completion Ploeg requires the **same vendor account** (the immutable id; for a GitHub App installation, the same account with the same or a new installation id), else `409 account_mismatch` and nothing changes: a binding or target that a person approved against one account must not silently reach another. It then writes the new credential as a new version under the same reference, swaps the row's reference, revokes the old token where the vendor allows it (ADR-0184 rule 3), and re-probes. The id, bindings and runtime targets stay as they were. Bindings and targets that were `invalid` with `connection_unusable` are re-probed and return to `active` with their own `restored` events when their probes pass, as ADR-0186 and ADR-0193 restore them.

### 5. States

* `active`: the credential is in the store and the last probe passed.
* `awaiting_consent`: a GitHub App installation awaits an organisation owner's approval; a later `installation` `created` delivery for that installation probes it and makes it `active`.
* `invalid`, with `invalidReason`: `credential_refused` (the vendor answered 401 to a probe, a mint or a write), `refresh_failed`, `installation_suspended`, `installation_deleted`, `scopes_insufficient` (the grant no longer carries what the adapter needs), or `account_changed` (the credential now acts as another account).
* `token_missing`: the store answers "not found" for the row's reference, so someone removed it outside Ploeg.

An unreachable store or vendor is not a state: the probe reports it as `unknown` and the state stays.

**Expiring OAuth tokens.** Vendors whose OAuth access tokens expire (GitLab, Forgejo) are in scope now. Ploeg stores the refresh token in the `SecretStore` as a second secret under the connection's reference, never in a row, and records the access token's expiry on the row. It refreshes before expiry, with a margin of a tenth of the token's lifetime and at least five minutes, and also when a vendor answers 401 to a token it believed valid. Each refresh writes the new access token, and the new refresh token where the vendor rotates it, as a new version under the same reference, in one store write. A refresh the vendor refuses makes the connection `invalid` with `refresh_failed`; the person reconnects. Refreshes of one connection are serialised, so a rotating refresh token is never spent twice. This amends ADR-0184 in the same change.

States change from four sources: a probe (on demand, and hourly for every store-backed connection, in the same reconciler ADR-0193 section 7 adds for grants); a vendor event (GitHub `installation` `deleted`, `suspend` and `unsuspend`, and `github_app_authorization` `revoked` for an OAuth grant); a vendor refusing a Run's mint or a write-back; and the store read. Each change writes `connection.invalid` or `connection.restored`. A connection that is not `active` cascades only as a state: ADR-0193 already marks its runtime targets `invalid` with `connection_unusable`, and this record proposes that ADR-0186 gains the same reason for bindings, to be added to ADR-0186 when either record is accepted. Nothing is deleted.

### 6. Read routes

| Method and path | Effect |
| --- | --- |
| `GET /api/v1/operator/tracker-connections` | Every tracker connection the consumer may read, static and runtime, with `id`, `kind`, `instance`, `method`, `account`, `source`, `state`, `invalidReason`, `handOverAgent`, the last probe, `createdBy`, `createdAt`, `updatedBy`, `updatedAt`, and the bindings that use it. Paged with `after` and `limit` (default 50, maximum 200). |
| `GET /api/v1/operator/forge-connections` | The same for forge connections, with the runtime targets that use each one and whether it can mint per-Run credentials (ADR-0192). |
| `GET /api/v1/operator/{tracker,forge}-connections/{id}` | One connection. |
| `GET /api/v1/operator/forge-connections/{id}/repositories` | As ADR-0193 section 3 defines it. |
| `GET /api/v1/operator/connection-apps` and `…/{id}` | Every connection app, with its App id, slug, owner and installations, never a secret. |

Tracker and forge connections are two separate collections, `/tracker-connections` and `/forge-connections`, as ADR-0193 already names them, not one `/connections` collection with a `port` filter: each port has its own fields (`handOverAgent`, bindings; per-Run mint, runtime targets) and its own readers. A static connection is listed with `source: "static"`, its environment or Helm reference name (never a value) and the same probe, so one route answers "what can Ploeg reach". It cannot be reconnected, edited or removed through the API: `409 static_connection`. ADR-0129's `/connections` stays the wiring view of boards and repositories and gains each item's connection id.

### 7. Who may call it

Four gates, all required, as ADR-0193 section 5 sets them for targets:

1. **The installation accepts runtime connections.** The configuration file gains `connections`: `enabled` (default `false`); `allow`, a list of `{ port, kind, instance, methods }` naming which connections may be made at runtime and how; `max` (live runtime connections per Tenant, default 20); and `requireClaims` with optional step-up (`acrValues`, `amr`, `maxAge`), matched as ADR-0150 matches claims. Each OAuth `instance` names its static OAuth application: client id, a reference to the client secret (ADR-0184's accepted cost), and its redirect URIs. Without `connections.enabled`, every changing route answers `422 connections_not_accepted`; with it but without a configured `SecretStore`, they answer `503 secret_store_unavailable` and write nothing (ADR-0184 rule 5).
2. **The consumer holds the permission.** `PLOEG_OPERATOR_CONSUMERS` gains `manageConnections` (default `false`) and `connectionRedirectUris` (exact HTTPS URLs, at most 10); `OperatorPrincipal` gains `CanManageConnections`. A connection belongs to no Team, so the permission is not narrowed by Team scope. Reading needs `manageConnections`, or returns only the connections a consumer's Teams may use: those its Teams' `trackerBindings.connections` (ADR-0186) or `targetRegistration.connections` (ADR-0193) name, for a consumer holding `bindScopes` or `registerTargets`.
3. **A proven person makes the change**, in **every attribution mode**, `asserted` included, because a connection widens what Ploeg's credentials reach (as ADR-0193 refuses asserted target registration). Starting and completing a flow, editing, probing, reconnecting and removing all need it; the flow is bound to that person and only that person can complete it.
4. **The person holds operator authority**: the claims `connections.requireClaims` names, and the step-up when configured.

### 8. Probe and change routes

| Method and path | Effect |
| --- | --- |
| `POST /api/v1/operator/{tracker,forge}-connections/{id}/probe` | Runs every check and returns each: the store returns a value at the reference; the vendor accepts the credential; it acts as the stored account; it holds the scopes or App permissions the adapter needs; for a forge, whether it can mint per-Run credentials; for a tracker, the Scopes it can see (count and the first 50). Updates `state`. At most one probe per connection per 30 seconds (`429 probe_rate_limited`), through the adapter's `apilimit` policy. |
| `PATCH /api/v1/operator/{tracker,forge}-connections/{id}` | Changes `label` and `handOverAgent` only. Requires `If-Match: "<revision>"`; a stale revision is `412`. Everything else changes by reconnecting. |
| `DELETE /api/v1/operator/{tracker,forge}-connections/{id}` | Remove (below). |
| `DELETE /api/v1/operator/connection-apps/{id}` | Remove a connection app: `409 app_in_use` while a live connection uses one of its installations; otherwise deletes its three secrets from the store. GitHub offers no API to delete an App, so the response names it in `leftAtVendor` with its settings URL. |

Probing needs a new, proposed capability on both ports, **`ConnectionProber`**: who the credential acts as, its scopes or permissions, and, for a tracker, the Scopes it can see. An adapter without it cannot be connected at runtime (`422 capability_missing`); its static connections keep working. The flows need **`ConnectionAuthorizer`** (build an authorize URL, exchange a code, refresh, revoke) for `oauth`, and **`AppManifest`** for the manifest and installation methods.

**Removal refuses while the connection is used** (`409 connection_in_use`), listing every live ADR-0186 binding and every ADR-0193 runtime target that names it, in any state. A removal never cascades: a binding or target is removed by its own route, with its own event, by a person who sees what stops. Once nothing uses it, Ploeg:

1. deletes every webhook it registered through this connection and still records (`WebhookRemover`, ADR-0186 section 7);
2. revokes the grant where the vendor offers it: an OAuth grant is revoked, and a GitHub App installation of a connection app Ploeg holds is uninstalled (`DELETE /app/installations/{id}` with the App's JWT), because a disconnect leaves nothing behind (ADR-0184 rule 3). An installation of an App Ploeg does not hold is not uninstalled and is reported in `leftAtVendor`;
3. deletes every secret the connection's references name from the store;
4. sets `removed_at` and writes `connection.removed`.

A vendor that answers 401 to steps 1 or 2 is skipped (the credential is already dead) and the step is reported in `leftAtVendor` (`[{ "kind": "webhook", "id": "…" }]`, or `{ "kind": "installation", "id": "…" }` for an uninstall that failed). A transient vendor or store failure answers `503` with the steps already done recorded on the row, and a retry continues from there. The answer is `204`, `200` with `leftAtVendor` when anything was left, and `204` again for an id already removed.

### 9. The connection's own account as hand-over agent

Work is handed to Ploeg by assigning one of its tracker users ([ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md)), and ADR-0186's probe needs an agent that can see the Scope; today the agents are static (`trackers.clickup.agents`). A store-backed tracker connection made from the dedicated member ADR-0184 rule 4 documents *is* such a user. So a tracker connection may set `handOverAgent: true` (at start or by `PATCH`): the connection's own account, by the vendor's immutable user id from the probe, is then a hand-over agent **for the Scopes bound through this connection only**, beside any static agents. A static route never gains it, so a runtime declaration cannot change what the reviewed file routes. Because the binding probe reads the Scope with the same credential, ADR-0186's `agent_cannot_see_scope` check passes for this agent by construction. The adapter must recognise an assignment to a per-connection agent, a proposed change to how its intake receives its agent set. An adapter that cannot answers `422 capability_missing` on `handOverAgent: true`.

### 10. Runtime connections in a Team's binding list

ADR-0186 lets a Team accept bindings only through the connections `teams.<team>.trackerBindings.connections` names, which would mean a file edit and a rollout for every runtime connection. This record proposes two wildcard entries: `"*"` (any store-backed tracker connection in state `active`) and `"*:<kind>"` (any such connection of that kind, for example `"*:clickup"`). `*` is outside the name pattern, so a wildcard can never collide with an id, and a wildcard never matches a static connection, which must still be named. ADR-0193's `targetRegistration.connections` already accepts `["*"]`; it gains `"*:<kind>"` the same way. Both records are amended when either is accepted.

### 11. Idempotency, retries and limits

* **Start is not idempotent.** Each call issues a new flow and state; an abandoned one expires. At most 5 open flows per person and 20 per consumer (`429 flow_limit`).
* **Complete is single-use and safe to retry.** Ploeg marks the flow `completing` in a transaction before it calls the vendor, so a vendor code is exchanged at most once; a concurrent second call is `409 flow_in_progress`. After completion, the same consumer and person repeating the call within an hour get the stored outcome with `replayed: true`, and never a second exchange. Any other caller gets `404 unknown_flow`, which says nothing about whether the state exists. An expired flow is `410 flow_expired`; a flow that failed after the exchange is `failed`, and the person starts again.
* **Remove is idempotent** by id, and a partial removal continues on retry (section 8).
* **Limits:** `connections.max` live runtime connections per Tenant (`429 connection_limit`); request bodies at most 64 KiB; one probe per connection per 30 seconds; labels 200 characters; tokens 4096.

Errors use the operator envelope: `403 permission_required`, `403 person_token_required`, `403 person_claim_required`, `401` with an RFC 9470 step-up challenge, `404 unknown_connection`, `404 unknown_flow`, `404 unknown_app`, `410 flow_expired`, `422 connections_not_accepted`, `422 kind_not_allowed`, `422 method_not_allowed`, `422 oauth_required`, `422 redirect_uri_not_allowed`, `422 webhook_base_missing`, `422 capability_missing`, `422 probe_failed`, `409 id_taken`, `409 account_mismatch`, `409 already_connected` (the installation or account is already a live connection; the response names it), `409 static_connection`, `409 connection_in_use`, `409 app_in_use`, `409 flow_in_progress`, `412` for a stale revision, `429 flow_limit`, `429 connection_limit`, `429 probe_rate_limited`, `503 secret_store_unavailable`, `503 vendor_unavailable`. Unknown fields are refused. A `token` field anywhere but a `token` flow's completion is refused, never ignored, as is any URL where an instance id is expected.

ploeg-mcp gains `ploeg_list_connections` (read-only) and no actions: a consent flow needs a browser, which an MCP client cannot be assumed to have, so connecting stays with a consumer that hosts the callback.

### 12. Audit and events

Every change writes one `audit_log` row with `work_item_id` NULL, the proven person as `actor`, and the consumer, channel, connection id, kind, instance, method, account and secret references in `detail`, never a code, state, token or secret: `connection.flow_started`, `connection.created`, `connection.reconnected` (old and new reference versions), `connection.updated`, `connection.refused` (the error code and failed checks, so repeated attempts are visible), `connection.invalid`, `connection.restored`, `connection.removed` (with `leftAtVendor`), `connection_app.created` and `connection_app.removed`. A flow that expires writes nothing. State changes a probe, reconciler or vendor event finds are attributed to the connection, not to a person. `GET /api/v1/operator/events` returns these rows to consumers that may read the connection, and `ploeg_connections{kind,state}` is exported so an alert can fire on an `invalid` or `token_missing` connection.

### 13. Storage

Two new tables in the next migration in `pkg/store/migrations/`: `connections` (the fields of section 1 and the removal steps done) and `connection_flows` (public id, state hash, person, consumer, Tenant, request, PKCE verifier reference, status, outcome, `expires_at`). The PKCE verifier is a secret of the flow and goes to the store like any other, under the flow's path, deleted when the flow ends. A partial unique index on `(tenant, port, kind, instance, account id) WHERE removed_at IS NULL` keeps one live connection per account and port, and a unique index on `(tenant, id)` without a filter keeps removed ids from being reused. `ploeg_grant_observer()` is extended with the non-attribution columns of `connections`; `connection_flows` is not granted.

### 14. Out of scope

* Where secrets live and how the store is authenticated: ADR-0184.
* Binding Scopes and registering targets: ADR-0186 and ADR-0193; this record only gives them ids, states and the wildcards of section 10.
* Moving a static connection into the store. An operator who wants a runtime-managed connection connects it at runtime and removes the static one.
* More than one Tenant per ploegd.
* Any user interface. A consumer builds its own on this contract.

The operator API schema (`docs/contracts/operator-api.v1.schema.json`) and its Go types gain these routes and shapes in the change that builds them.

### Consequences

* Good, because connecting a tracker or forge becomes one consent step in a consumer, with no secret on any clipboard, and the consumer gets back an id other routes can use at once.
* Good, because a stolen consumer token, a second person or a replayed callback cannot complete a flow: the state is Ploeg's, single-use and bound to one proven person and consumer.
* Good, because ids never move or get reused, so bindings, targets, events and pinned Work Items keep meaning the same account through every reconnect.
* Good, because removal never leaves routing pointing at nothing and never leaves a secret behind, and what it could not clean at the vendor is reported instead of forgotten.
* Good, because a runtime connection can feed a Team without a file edit (section 10), while a static route still never gains a runtime agent.
* Bad, because the operator API grows about a dozen routes, two tables and three adapter capabilities, and every adapter must implement `ConnectionProber` before its kind can be connected at runtime.
* Bad, because each consumer must host a callback page, register it with the vendor's OAuth application and keep its query strings out of logs. Accepted: the alternative is a browser-facing ploegd.
* Bad, because vendors whose OAuth tokens expire (GitLab, Forgejo) make Ploeg store a refresh token and refresh before expiry (section 5), which was ADR-0184's first re-evaluation trigger. Accepted: ADR-0184 is amended in the same change.
* Bad, because removing a connection uninstalls a GitHub App installation of an App Ploeg holds, so a person who removes by mistake must install the App again. Accepted: a disconnect leaves nothing behind.
* Neutral, because `manageConnections` with operator authority is a real grant: whoever holds both can give Ploeg reach into any account a person consents with. It is bounded by the opt-in, the allowed kinds and instances, the claims, the limit and the audit trail, and operators should give it sparingly.

### Confirmation

Proposed, none of these exist yet. Once built, `mise run verify` runs them through `go test ./...`:

* `pkg/httpapi` flow tests with an `httptest` fake of ClickUp's OAuth (authorize, token, `GET /user`, `GET /team`) and a fake `SecretStore`: start then complete (`201`, one row, the access token only in the fake store); the same completion repeated by the same person (`200`, `replayed: true`, the fake token endpoint called once); completion by another person or another consumer (`404 unknown_flow`); an expired flow (`410`); two concurrent completions (one `409 flow_in_progress`, one exchange); a `redirectUri` outside `connectionRedirectUris` (`422`); a `token` flow where the instance has an OAuth application (`422 oauth_required`); a `token` field on an `oauth` completion refused; a URL in `instance` refused; asserted attribution refused under both `asserted` and `proven` modes; no store configured (`503`, nothing written).
* GitHub tests with an `httptest` fake of the manifest conversion, `PATCH /app/hook/config`, `GET /app/installations/{id}` and installation tokens: a manifest flow whose manifest names `<PLOEG_PUBLIC_WEBHOOK_BASE>/webhooks/forge/github`, `public: false` and only allow-listed callback URLs; conversion storing the private key, webhook secret and client secret only in the fake store; a conversion that returns no webhook secret sets one; an installation of another App refused; `setupAction: "request"` stored as `awaiting_consent` and made `active` by a signed `installation` `created` delivery; `installation` `suspend` and `deleted` deliveries marking the connection `invalid`.
* Reconnect tests: same account (`200`, same id, new reference version, old token revoked at the fake, an `invalid` binding and target restored); another account (`409 account_mismatch`, nothing changed); a store entry deleted behind Ploeg's back reported as `token_missing` by the next probe.
* Removal tests: a connection with a live binding or runtime target (`409 connection_in_use` listing them); a clean removal deleting the fake vendor's webhook, revoking the grant and deleting every store path; removing a GitHub App installation connection of an App Ploeg holds calling `DELETE /app/installations/{id}` at the fake with the App's JWT, and a failed uninstall reported in `leftAtVendor` as `installation`; a vendor answering 401 (`200` with `leftAtVendor`, secrets deleted); a store failure midway (`503`, the retry finishing the remaining steps); remove twice (`204` twice); a removed id refused for reuse, also for the same account (`409 id_taken`), while a reconnect keeps it; a static connection refused (`409 static_connection`).
* Refresh tests with an `httptest` fake of GitLab's OAuth issuing expiring tokens: the refresh token only in the fake store; a refresh before expiry writing a new version under the same reference; a rotated refresh token stored and the old one never used again; two concurrent refreshes calling the fake token endpoint once; a refused refresh marking the connection `invalid` with `refresh_failed` and writing `connection.invalid`.
* A manifest test that every manifest Ploeg builds has `public: false`, whatever the start request carries, and that an `installationId` of an App Ploeg does not hold is refused.
* A route test that a tracker connection id is `404 unknown_connection` under `/forge-connections` and the reverse, and that `GET /api/v1/operator/connections` stays the wiring view and gains each item's connection id.
* A routing test that a tracker connection with `handOverAgent: true` routes an assignment to its account on a Scope bound through it, and not on a static route's Scope; and that `"*:clickup"` in `trackerBindings.connections` accepts a binding through a runtime ClickUp connection and refuses one through a static ClickUp connection.
* ADR-0184's redaction test extends to every route here: no response body, log line (including outbound request logs, with the fake ClickUp token endpoint taking the code in its query), event payload or database row contains a code, state, token, private key or secret; and an access-log capture of the HTTP server shows no `code` or `state` anywhere.
* `pkg/provider/trackertest` and `forgetest` gain the `ConnectionProber` and `ConnectionAuthorizer` cases, and every adapter that declares them passes.
* `go test ./internal/ledger/` keeps this record well-formed, and `go test ./internal/boundary/` keeps it free of consumer names.

## Pros and Cons of the Options

### Ploeg hosts the vendor callback itself

* Good, because the code goes from the vendor to Ploeg through the browser and never touches a consumer.
* Bad, because ploegd gains a browser-facing surface (the redirect target, an outcome page or a redirect back, CSRF handling) that it has never had; today it serves only APIs and webhooks.
* Bad, because the redirect carries no person token, so the person is still proved only through the `state` Ploeg issued, which is exactly what the chosen option uses, with no gain.
* Bad, because ADR-0184 rule 1 already chose the consumer relay.

### The consumer issues and checks the `state`

* Good, because it is the usual OAuth client shape and needs one route fewer.
* Bad, because Ploeg then trusts any code the consumer relays: a stolen consumer token, or a second person in the same consumer, can complete a connection nobody proved. ADR-0184 recommends against it for that reason.

### Connections stay static configuration

* Good, because nothing new has to be built or secured, and every connection stays in the reviewed file.
* Bad, because a person must still create the credential, catch a webhook secret the vendor returns once, and roll out a change, which ADR-0184 exists to end.
* Bad, because ADR-0186 and ADR-0193 then have no store-backed connection to name, and their runtime paths stay empty.

## Re-evaluation triggers

* ADR-0184 is accepted or rejected, which decides whether store-backed connections exist.
* ADR-0186 or ADR-0193 is accepted, which brings the wildcard and `connection_unusable` amendments due.
* A supported vendor drops the OAuth authorization-code flow or the GitHub App manifest flow, or GitHub adds an API to delete an App.
* A consumer needs to connect from a client without a browser, so an MCP action or a device-code flow is asked for.
* A connection stays `invalid` or `token_missing` for more than a day without anyone noticing.
* An installation passes 20 runtime connections, or the hourly probe hits a vendor's rate limit.
* A deployment needs one ploegd to hold connections for several Tenants.
* A public GitHub App is to be allowed (a manifest with `public: true`), which makes a setup redirect's `installationId` need proof through a GitHub user authorization.
* A vendor's refresh tokens expire or rotate in a way that refresh before expiry cannot keep a connection `active`.

## More Information

* Answers the open connection contract in [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md)'s *More Information*, including its recommendation that Ploeg issues the `state` and binds it to the proven person. Gives [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) and [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md) the connection ids, states and wildcards they rely on.
* Related: [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md), [ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md), [ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md), ADR-0094 (the one-time code pattern), [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md), [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md), [ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md), [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md).
* 2026-11-17 — the owner decided open questions 1–5 as recommended: removing a connection uninstalls a GitHub App installation of an App Ploeg holds (section 8); a setup redirect's `installationId` needs no GitHub user authorization while manifest Apps are created `public: false` (section 3, re-evaluation triggers); expiring OAuth tokens are in scope now, with ADR-0184 amended in the same change (section 5); a removed id is never reused (section 2); and tracker and forge connections are separate collections, with `/connections` staying the wiring view (section 6).
* 2026-11-18 — Amended after a security review of the built code: an `installation` delivery changes a connection only when the App's own webhook secret verified it, with the App id taken from the verified `X-GitHub-Hook-Installation-Target-ID`, and a delivery whose App secret cannot be read is refused instead of verified with the static secret. A `setupAction: "request"` completion stores no installation id the App cannot read; the `awaiting_consent` connection is bound to the first signed `created` delivery of that App on its owner's account. Runtime App deliveries reach `/webhooks/tracker/github` and `/webhooks/forge/github` whether or not a static GitHub tracker or forge is configured. A reconnect whose row write fails puts the previous credential back in the store.
* 2026-11-19 — Section 9 built for ClickUp, the first adapter that recognises an assignment to a per-connection agent. A kind declares it with `provider.Connector.HandOverAgent`; `handOverAgent: true` at start or by `PATCH` is still `422 capability_missing` for every kind that does not. The agent is the connection's `account_id`, the ClickUp user id `GET /user` returned, so no column was added. A `taskAssigneeUpdated` delivery whose added or removed member has that id hands the task over only when Ploeg, reading the task with the connection's own token, finds it in a List a routed binding binds through that same connection; a List any static rule names never qualifies, so a static route never gains the agent. The task then takes the static agent's path (dedupe, routing, gates, budget, withdraw on removal). Reads and write-backs of a task in a List bound through a store-backed connection use that connection's token, and a connection without a configured Workspace finds the List's Workspace through `GET /team` to check and register its List webhook. Deliveries arrive on `/webhooks/tracker/clickup`. When no static ClickUp tracker is configured and `connections.allow` names a ClickUp tracker kind, ploegd serves that route with a receiver that holds no static credential or secret; with or without a static tracker, a delivery that `PLOEG_CLICKUP_SECRET` does not verify is verified only with the signing secret, kept in the secret store, of a webhook a live binding registered, and any other webhook id is refused like a bad signature.
