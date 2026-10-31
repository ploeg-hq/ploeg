---
status: proposed
date: 2026-10-31
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An operator consumer binds a tracker Scope to a Team at runtime, and static routing wins on conflict

## Context and Problem Statement

Today the only way to tell Ploeg "take work from this tracker container for Team X" is to edit ploegd's configuration file and redeploy. The pieces are all static:

* **Routing Rules** come from `trackers.<tracker>.projects[]` in the configuration file (`pkg/config/config.go`, `Project`): a name or id, a `team`, and a destination (`repo`, or `default` and `allow` keys into the `targets` registry). `config.File.RoutingTable` (`pkg/config/resolve.go`) resolves names to Scope ids once, at boot, and `cmd/ploegd/routing.go` builds one `target.MapResolver` from them. Nothing rebuilds it while ploegd runs.
* **`Validate`** (`pkg/config/config.go`) refuses at boot a Scope routed twice for the same Team, a route to an unknown Team or target, a ClickUp List without an id ("clickup name resolution is not implemented"), and one username on two Teams' `teams.<team>.assignees`. One Scope namespace spans every tracker, because the target map keys on the Scope id alone.
* **Status write-back** is one map per tracker kind, read from `PLOEG_CLICKUP_STATUS_MAP` by `provider.ParseStatusMap` in `cmd/ploegd/main.go`, not per Scope.
* **Webhook wiring** is checked and registered for Vikunja only, by a loop over the static projects (`cmd/ploegd/webhooks.go`, `PLOEG_VIKUNJA_WEBHOOK_REGISTER`). The tracker port already has the capabilities to make it generic: `WebhookChecker`, `WebhookRegistrar` and `WebhookSecretSink` (`pkg/provider/webhooks.go`), discovered with `provider.TrackerCapabilities` ([ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md)).
* **The operator API** (`/api/v1/operator/`) authenticates an Operator Consumer from `PLOEG_OPERATOR_CONSUMERS` with a Team scope and the permissions `execute`, `verify` and `readContent` (`OperatorPrincipal`, `pkg/httpapi/operator.go`). Every change needs a proven person ([ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)). `GET /api/v1/operator/connections` reports how each static board is wired ([ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md)); nothing in the API can change a route.

A client or an operator console that wants to offer "connect this List to Team X" must therefore ask a person to edit Helm values, open a pull request against the deployment, and wait for a rollout. Ploeg's connection secrets are moving to a store Ploeg writes itself ([ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md)), which removes the credential half of that redeploy. The routing half remains.

How can an authorised caller add, list and remove "this tracker Scope feeds this Team" while ploegd runs, without weakening what the static configuration guarantees: a closed, operator-declared set of Work Targets (rule R11), one route per Scope and Team, and no credential outside a secret store?

## Decision Drivers

* **The Work Target set stays closed and operator-declared** (R11, [ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md), [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)). A runtime caller must never be able to point a write-scoped forge credential at a repository the operator did not register.
* **The reviewed configuration is the operator's word.** A runtime change never overrides it, and a conflict is visible, not silent.
* **Accept only what works.** A binding Ploeg cannot read, write back to or receive events from should be refused at the call, not discovered when the first assignment goes nowhere.
* **No secret in a request, a response, a row, a log or an event** (ADR-0184).
* **Least authority.** The static configuration decides whether a Team accepts runtime bindings at all and through which connections; a consumer may only use what it was granted.
* **Core stays vendor-free** (ADR-0183): every vendor difference is a capability of the Tracker port.
* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). The contract is generic and any Operator Consumer can use it.

## Considered Options

* **Runtime Scope bindings in Ploeg's store, written through the operator API and ploeg-mcp, merged under the static Routing Rules**
* The caller edits the static configuration through GitOps (a pull request against the deployment) and waits for the rollout
* ploegd hot-reloads its configuration file, and the caller writes the ConfigMap
* A `TrackerBinding` Kubernetes custom resource that ploegd reconciles
* Runtime bindings that override static routes

## Decision Outcome

Chosen option: "**Runtime Scope bindings in Ploeg's store, written through the operator API and ploeg-mcp, merged under the static Routing Rules**", because it is the only option that validates a binding against the live tracker before accepting it, attributes it to a proven person under the Operator Consumer's Team scope, and keeps the static file the authority, while needing no write access to the deployment or the cluster.

### 1. What a Scope binding is

A **Scope binding** is one Routing Rule that an Operator Consumer adds at runtime. It names:

| Field | Meaning | Rule |
| --- | --- | --- |
| `team` | The Team its work routes to | From the path. Must exist in `teams` and accept bindings (section 5). |
| `connection` | The tracker connection that reads the Scope, by name | A configured tracker connection: today a tracker kind (`vikunja`, `clickup`, `github`); after ADR-0183 step 6 the id of a `trackers.<id>` entry; with ADR-0184 a store-backed connection. Never a credential. |
| `scope` | The tracker container: `{ "id": "…" }` or `{ "name": "…" }` | A name is resolved to an id through the connection's `ScopeNamer` capability. The stored key is always the id. |
| `default`, `allow` | The destination | Keys into the static `targets` registry, as `Project.Default` and `Project.Allow`. A raw `repo`, `branch` or `forge` is refused: a binding selects among registered Work Targets and never constructs one (R11). |
| `statuses` | Optional status names for `awaiting_review`, `needs_human` and `done` | Parsed by `provider.ParseStatusMap`; overrides the connection's map for this Scope only. Omitted means the connection's map applies. |
| `label` | Optional display name | Free text, at most 200 characters, never used for routing. |

A binding's natural key is `(connection, scope id, team)`, the same key `Validate` uses for static routes (`Project.routeKey`). It carries a monotonic `revision`, the attribution of who created and last changed it, and a `state` (section 4).

Out of the binding on purpose: assignees, tracker people and agents (`teams.<team>.assignees`, `trackers.<tracker>.people`, `trackers.clickup.agents`), delivery gates ([ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md)), and per-repository settings (`ci`, `fastCheck`). They stay static; see *Out of scope*.

### 2. API

New operator routes, versioned with the operator API (`docs/contracts/operator-api.v1.schema.json` and its Go types change together):

| Method and path | Effect |
| --- | --- |
| `GET /api/v1/operator/tracker-bindings` | Every binding and every static route of the Teams the consumer may read, with `source: "static"` or `"runtime"` and each one's `state`. One answer to "what feeds Team X", whichever way it was declared. |
| `GET /api/v1/operator/teams/{team}/tracker-bindings` | The same, for one Team. |
| `POST /api/v1/operator/teams/{team}/tracker-bindings` | Probe, then create. `201` with the binding, or `200` with the existing one when a live binding with the same key and the same body exists (`created: false`). |
| `POST /api/v1/operator/teams/{team}/tracker-bindings?dryRun=true` | Probe only. Returns what creation would store and every check's result; writes nothing and registers no webhook. |
| `PUT /api/v1/operator/teams/{team}/tracker-bindings/{id}` | Replace `default`, `allow`, `statuses` or `label`. Requires `If-Match: "<revision>"`; a stale revision is `412`. The key fields cannot change: remove and create instead. Re-probes. |
| `DELETE /api/v1/operator/teams/{team}/tracker-bindings/{id}` | Remove. `204`, also when it was already removed, so a retry is safe. |

**Idempotency is by natural key, not by a request key.** Creating the same key with the same body twice yields one binding. Creating the same key with a different body is `409 binding_exists` with the current binding and revision, so a caller that lost a response can tell "my create landed" from "someone else's binding is there". Removal is idempotent by id. No idempotency-key table is needed.

Errors use the operator API's existing envelope (`operatorError`): `403 team_not_allowed` and `403 permission_required`, `404 unknown_team`, `422 unknown_connection`, `422 bindings_not_accepted` (section 5), `422 capability_missing`, `422 probe_failed` with each failed check, `422 unknown_target` and `422 target_not_ready`, `409 conflicts_with_static`, `409 binding_exists`, `409 scope_bound_elsewhere`, `429 binding_limit`. Unknown request fields are refused, as `ParseOperatorConsumers` already refuses them, so a token pasted into a field is rejected rather than ignored.

ploeg-mcp gains `ploeg_list_tracker_bindings` (read-only) and two actions, `ploeg_bind_tracker_scope` and `ploeg_unbind_tracker_scope`, that need the person's confirmation like every other ploeg-mcp action ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)). Their confirmation text shows the dry-run result.

### 3. Validation through the Tracker port

Creation and replacement run the same checks as the static path, then probe the live tracker. Every check runs; the response lists each result, so a caller can show a person what to fix.

1. **Static rules.** The Team exists; the target keys exist in `targets`; `statuses` parses; the label fits. These are the rules `Validate` applies at boot, factored so both paths call one function.
2. **Conflicts** (section 4).
3. **Capabilities.** The connection's adapter must declare a new, proposed capability, **`ScopeProber`**: `ProbeScope(ctx, scopeID) (ScopeProbe, error)`, which returns the Scope's id and display name, whether the connection's credential can read items and write comments and statuses in it, the members who can see it, and its status vocabulary when the tracker has one. A name needs `ScopeNamer` (proposed in ADR-0183 step 4). An adapter without `ScopeProber` cannot be bound at runtime: `422 capability_missing`. Static routes for it keep working.
4. **Probe.** The Scope exists and is readable and writable with the connection's credential. When the connection names its agents (`trackers.clickup.agents`) or the Team names assignees, at least one of them can see the Scope, because work is handed over by assignment ([ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md)); otherwise the probe fails with `agent_cannot_see_scope`. Every status named in `statuses` exists in the Scope's vocabulary when the probe returns one.
5. **Destination readiness.** Each named target passes `target.Gate`'s readiness check through the forge's `RepositoryInspector`, as registered targets are checked at boot. A target that is not ready refuses the binding with `target_not_ready`, instead of routing work that would be refused at ingest.

The probe goes through the adapter's rate-limit policy (`apilimit`), and the create route is limited per consumer, so binding attempts cannot be used to hammer a tracker. ADR-0183's tracker conformance suite gains the `ScopeProber` cases: a readable Scope, an unreadable one, an unknown id, and a Scope the agent cannot see.

### 4. Storage, merging and conflicts

Bindings live in a new table, `tracker_bindings`, added by the next migration in `pkg/store/migrations/`. A row holds the fields of section 1, the resolved Scope id and the probe's display name, the Tenant ([ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md)), `revision`, `state`, the webhook it relies on (id and how it was obtained, never a secret), `created_by`, `updated_by`, `removed_at`. A partial unique index on `(tenant, connection, scope_id, team) WHERE removed_at IS NULL` enforces one live binding per key. Removal sets `removed_at`; rows are not deleted, so the history stays readable. The migration extends `ploeg_grant_observer()` with the table's non-attribution columns, since the table holds no credential and no person's text.

**The effective routing table is the static rules followed by the live bindings.** On boot and after every committed change, ploegd builds a new `target.Table` from both and swaps the resolver atomically; a Work Item already ingested keeps the Work Target pinned on it ([ADR-0014](0014-work-target-is-a-work-item-attribute.md)). Each ploegd also reads the highest binding revision every 30 seconds and rebuilds when it changed, so a second replica converges without a message bus.

**Static wins.** A binding conflicts with the static configuration when a static route has the same Scope id and the same Team, or the same Scope id with no Team (a bare static route already decides that Scope for every Team), or the same Scope id on another tracker (the one Scope namespace `Validate` enforces). A binding conflicts with another binding when the same Scope id is bound bare elsewhere or on another connection: `409 scope_bound_elsewhere`.

A conflict is reported in two ways, depending on when it arises:

* **At the call**, as `409 conflicts_with_static`, naming the static entry (`trackers.clickup.projects[3]`) and its Team and destination, so the caller can show what already routes the Scope.
* **After a redeploy**, when the static file gains a route that conflicts with a binding that already exists. ploegd never fails to boot over runtime state. It marks the binding `state: "shadowed"` with `shadowedBy` naming the static entry, leaves it out of the routing table, logs a warning at boot, writes one `tracker_binding.shadowed` audit event, and exports `ploeg_tracker_bindings{state}` so an alert can fire. The binding comes back into force by itself if the static route is removed later, with a `tracker_binding.unshadowed` event. A shadowed binding stays visible in both list routes and in `/connections`.

Other states are `active`, `awaiting_webhook` (section 7) and `invalid` (its target key or Team was removed from the static file: it is kept out of the table and reported, never deleted on the caller's behalf).

### 5. Who may call it

Three gates, all required:

1. **The installation lets the Team accept bindings.** A Team manifest gains `teams.<team>.trackerBindings`: `connections` (the connection names a binding for this Team may use) and `max` (live bindings, default 20). A Team without it accepts none: `422 bindings_not_accepted`. Nothing changes for an installation that does not opt in. The Team manifest is the right place because it already says what a Team may do ([ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md)).
2. **The consumer holds the permission.** `PLOEG_OPERATOR_CONSUMERS` gains `bindScopes` (default `false`), beside `execute`, `verify` and `readContent`, and `OperatorPrincipal` gains `CanBindScopes`. The consumer's Team scope must include the Team (`AllowsTeam`). Reading bindings needs only the Team scope ([ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)).
3. **A proven person makes the change** (ADR-0150, ADR-0171). Creation, replacement and removal are operator changes and refuse asserted attribution where ADR-0171's mode requires proof. The person is recorded on the row and the audit event.

### 6. Secrets

A binding names a connection; it never carries, returns or stores a credential. The tracker credential belongs to the connection: an environment value or Helm Secret for a static connection, a secret-store reference for a connection made under ADR-0184. A binding to a connection that has no usable credential fails the probe. A webhook signing secret that a vendor returns when Ploeg registers a webhook goes through the existing `WebhookSecretSink` into the connection's secret store; the binding row keeps only the webhook id. If that registration needs a store and the connection has none, the binding is still accepted as `awaiting_webhook` (section 7) and nothing is written to the database or a log. The redaction test that ADR-0184 requires covers these routes.

### 7. Webhooks

A binding is useful only if the tracker's events for its Scope reach Ploeg. After the probe and before the binding is committed as `active`:

* If the adapter declares `WebhookChecker` and a webhook already delivers dispatch events for the Scope to Ploeg's endpoint, the binding relies on it and records that it did not create it.
* Else, if the adapter declares `WebhookRegistrar` and the connection may register webhooks (store-backed under ADR-0184, or a static connection with registration switched on, as `PLOEG_VIKUNJA_WEBHOOK_REGISTER` is today), Ploeg registers one, stores its secret through the sink, and records the webhook id as created by this binding. `WebhookRegistrar` gains the Scope it registers for, as a proposed change to its signature, because a binding is per Scope.
* Else the binding is stored as `awaiting_webhook`. It is routed (so a delivery that does arrive is not lost), and `/connections` and the list routes report the webhook as missing, as ADR-0129 reports a static board.

On removal, Ploeg deletes a webhook only if this binding created it and no other live binding or static route on the same connection and Scope still relies on it. That needs a proposed `WebhookRemover` capability; without it the webhook is left and the removal event says so. The hourly coverage check in `cmd/ploegd/webhooks.go` becomes a loop over every tracker that declares `WebhookChecker`, over static routes and live bindings alike, which is ADR-0183 step 4's `WebhookChecker` item.

### 8. Audit and events

Every change writes one `audit_log` row with `work_item_id` NULL, the proven person as `actor`, and the consumer, the channel (HTTP or ploeg-mcp, per ADR-0108) and the binding in `detail`, never a secret:

* `tracker_binding.created`, `tracker_binding.updated` (with the old and new revision), `tracker_binding.removed`
* `tracker_binding.refused` (with the error code and failed checks, so repeated probing of Scopes a consumer cannot reach is visible)
* `tracker_binding.shadowed`, `tracker_binding.unshadowed`, `tracker_binding.invalid`
* `tracker_binding.webhook_registered`, `tracker_binding.webhook_removed`

`detail.team` carries the Team, and `GET /api/v1/operator/events` gains these rows for the Teams the consumer may read, which today it cannot return because it joins every event to a Work Item. An assignment from a removed binding's Scope is an ordinary unrouted event and is recorded as today's `work_item.route_refused`.

### 9. Out of scope

* Creating, reconnecting or removing a tracker or forge **connection** and its credential. That is ADR-0184's contract; a binding only names one.
* Adding **Work Targets** at runtime. A binding selects among registered targets. Letting a caller register a repository changes R11 and needs its own record.
* Creating or editing **Teams**, their Roles, budgets, caps, assignees, tracker people or agents.
* Per-Scope **delivery gates**, `ci` and `fastCheck`. They can follow as binding fields once the static path for them is settled.
* Pull request read routes ([ADR-0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md)) and anything about forges.
* More than one Tenant per ploegd. A binding takes the installation's Tenant as every Work Item does under ADR-0178.
* Migrating existing static routes into the store. Static routes stay where they are; an operator who wants a route to be runtime-managed removes it from the file and binds it.
* Any user interface. A client builds its own on this contract.

### Consequences

* Good, because "take work from this Scope for Team X" becomes one authorised call with a dry run, instead of a configuration pull request and a rollout.
* Good, because a binding is accepted only after the live tracker confirms Ploeg can read it, write back, and be told about it, which today is discovered by the first assignment that goes nowhere.
* Good, because R11 holds: a runtime caller can only choose among Work Targets the operator registered, and the forge credential can only reach those.
* Good, because the static file stays the authority, and the one case where it newly contradicts runtime state is reported, alerted on and recovers by itself.
* Good, because the work generalises what is Vikunja-only today (webhook check and registration) through capabilities ADR-0183 already lists.
* Bad, because routing now has two sources. Anyone reading the configuration file no longer sees every route; the list routes and `/connections` become the place to look, and the documentation must say so.
* Bad, because a store outage at boot now affects routing. Accepted: ploegd already needs its database to run, and the static rules alone still load if the bindings query fails, with a readiness warning.
* Bad, because adapters must implement `ScopeProber` (and `ScopeNamer` for names) before their Scopes can be bound at runtime, and the fixture's fake API must model member visibility.
* Neutral, because a consumer with `bindScopes` can route any Scope its connection can read to any Team it is scoped to. It is bounded by the Team's `trackerBindings.connections`, the cap, the closed Work Target set and the audit trail, but it is a real grant and operators should give it sparingly.

### Confirmation

Proposed, none of these exist yet:

* `pkg/httpapi` tests, with `httptest` fakes of the tracker and the forge, for create, repeat create (one row, `200`), conflicting create (`409 binding_exists`), stale `If-Match` (`412`), remove twice (`204` twice), a raw `repo` refused, an unknown field refused, a consumer without `bindScopes` or without the Team refused, a Team without `trackerBindings` refused, and asserted attribution refused under ADR-0171's proven mode.
* A routing test that a binding conflicting with a static route at the call is `409 conflicts_with_static` naming the static entry, and that a static route added afterwards marks the binding `shadowed`, removes it from the resolver, writes one `tracker_binding.shadowed` event, and restores it when the static route goes away.
* A test that an assignment in a bound Scope reaches the bound Team's queue within one rebuild, and that one in a removed binding's Scope is recorded as `work_item.route_refused`.
* `pkg/provider/trackertest` gains the `ScopeProber` cases, and every adapter that declares the capability passes them.
* ADR-0184's redaction test extends to these routes: no response, log line, event or row written by a binding call contains a credential or webhook secret.
* `go test ./internal/ledger/` keeps this record well-formed.

## Pros and Cons of the Options

### The caller edits the static configuration through GitOps

* Good, because the configuration file stays the one source, reviewed like code.
* Bad, because the caller needs write access to the deployment's repository, and every change waits for review and a rollout.
* Bad, because nothing probes the tracker before the change merges; a wrong id or an unreachable List is found after the rollout.

### ploegd hot-reloads its configuration file, and the caller writes the ConfigMap

* Good, because there is still one source of routes, and no new table.
* Bad, because the caller needs Kubernetes write access to Ploeg's namespace, which is a much wider grant than one Team's routing.
* Bad, because a ConfigMap write has no per-person attribution, no Team scope and no probe; an invalid edit is found when ploegd reloads, and a typo can take every route down at once.

### A `TrackerBinding` custom resource that ploegd reconciles

* Good, because it is declarative and GitOps tools can manage it, and Kubernetes keeps its history.
* Bad, because Ploeg's callers speak its HTTP and MCP APIs, not Kubernetes, and Kubernetes RBAC does not express Ploeg's Team scopes or proven persons.
* Bad, because it adds a CRD and a controller to the chart for one feature, and validation would happen after the resource was accepted, not before.
* Neutral, because it can be added later as another writer to the same table, with the same validation.

### Runtime bindings that override static routes

* Good, because a caller could change a route the operator declared without asking anyone.
* Bad, because that is the problem: the reviewed configuration would no longer mean what it says, and a consumer could silently take a Scope from the Team the operator gave it to.

## Re-evaluation triggers

* ADR-0183 step 6 lands, so tracker connections get ids of their own and `connection` stops being a tracker kind.
* ADR-0184 is accepted or rejected, which decides whether store-backed connections exist to bind to.
* A Tenant needs to bind a Scope to a Work Target the operator has not registered, which reopens R11.
* ploegd runs more than one replica and the 30-second revision poll lets one route an assignment the other has already unbound.
* A binding is shadowed in production and no one notices for more than a day.
* An installation holds more than 200 live bindings, or a probe hits a tracker's rate limit.

## More Information

* Related: [ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md) (opaque Scopes and Routing Rules), [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md) (registered targets), [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md) (hand-over by assignment), [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) (the Team manifest), [ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md) (wiring report; this record is the "runtime registration of boards" its triggers name), [ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md) (Team scope), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md) (proven person), [ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md) (ports and capabilities), [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) (connection secrets).
* Open questions: whether `statuses` belongs on the binding or on the connection once ADR-0183 step 6 gives connections ids; whether `dryRun` should also report how many open items in the Scope are already assigned to Ploeg's agent, so a caller can warn before work starts flowing.
