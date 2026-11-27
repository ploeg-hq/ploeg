---
status: accepted
date: 2026-11-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-03-31
---

# An operator consumer reads a tracker connection's Scopes and items through Ploeg and holds no tracker credential

## Context and Problem Statement

On 2026-11-18 the owner decided that Ploeg owns every tracker and forge connection and that an operator consumer holds no tracker credential. [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) keeps the secrets in a store Ploeg writes, and [ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) lets a proven operator make a connection without seeing one.

A consumer still has to show a person what a connection reaches. Binding a Scope to a Team ([ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md)) starts with picking it: a ClickUp List under its Workspace, Space and Folder, a Vikunja project, a GitHub repository whose issues are work. Before and after the binding, a person wants to see the open items in that Scope and open one of them. A consumer used to read this from the tracker with a token of its own. After the decision it cannot, and the operator API has no route for it: [ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md)'s `/connections` reports only the boards the configuration already routes, and ADR-0195's probe returns at most a count and 50 Scope names.

What read-only contract lets a consumer browse a connection's Scopes, preview a Scope's open items and read one item, through Ploeg's own credential, without becoming a second copy of the tracker or a way to hammer it?

## Decision Drivers

* **No tracker credential outside Ploeg**: none in a request, a response, a log or a consumer's configuration (ADR-0184).
* **Tracker text is data, never instructions** ([ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md)). A consumer may pass it to a model.
* **Least authority.** Reading what a connection reaches is a grant of its own, separate from binding (`bindScopes`, ADR-0186) and from managing connections (`manageConnections`, ADR-0195).
* **Rate-limit friendly.** A person clicking through a hierarchy must not turn into one vendor call per click per consumer.
* **One id for a connection**, today and after ADR-0195: the routes must work against the configured trackers now and against store-backed connections later without a new path.
* **Core stays vendor-free** ([ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md)): each vendor difference is a Tracker port capability.
* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **Read-only operator routes per tracker connection, served live through two new Tracker port capabilities and a short shared cache**
* The consumer reads the tracker directly with its own token
* Ploeg mirrors every task of every reachable Scope into its store and serves the copy
* Widen ADR-0195's connection probe to return the whole hierarchy

## Decision Outcome

Chosen option: "**Read-only operator routes per tracker connection, served live through two new Tracker port capabilities and a short shared cache**", because it keeps the only credential in Ploeg, answers from the tracker itself so nothing drifts, and costs one vendor call per container per minute however many people look.

### 1. Routes

Three `GET` routes under the operator API, keyed by connection id:

| Method and path | Answers |
| --- | --- |
| `GET /api/v1/operator/tracker-connections/{connection}/scopes` | One level of the containers the connection reaches. `parent=<kind>:<id>` names the level, as a listed Scope's `ref` gives it; without it, the top level. Paged with `after` (the previous `nextCursor`) and `limit` (1 to 200, default 50). |
| `GET /api/v1/operator/tracker-connections/{connection}/scopes/{scope}/items` | One page of a bindable Scope's open items, in the tracker's own page size and order, at most 100. Paged with `after`. |
| `GET /api/v1/operator/tracker-connections/{connection}/items/{item}` | One item, read with the port's required `FetchItem`. |

A Scope carries `kind`, `id`, `ref`, `bindable` (a Routing Rule can name its `id`: a ClickUp List, a Vikunja project, a GitHub repository) and `browsable` (it can be a `parent`). An item preview carries `id`, `url` and `updatedAt`; one item adds `scope`, `closed` and `createdAt`. Every response carries `fetchedAt`, when Ploeg read the tracker.

**Connection id.** Today a tracker connection is a configured tracker, named by its kind (`vikunja`, `clickup`, `github`), as ADR-0186 names one before ADR-0183 step 6. When ADR-0195 adds store-backed connections, they answer on the same path under their own id, beside ADR-0195's `GET /api/v1/operator/tracker-connections/{id}`.

The shapes are `trackerScopesResponse`, `trackerScopeItemsResponse` and `trackerItemResponse` in `docs/contracts/operator-api.v1.schema.json`, versioned with the operator API.

### 2. Tracker text is untrusted

Every string that came from the tracker sits under an `untrusted` object: a Scope's `name`; an item's `title` and `status`; one item's `title`, `description` and `labels`. Each is escaped with `untrusted.Escape` (ADR-0170), so a name that writes `</untrusted …>` cannot close a frame the consumer or Ploeg builds around it later, has recognizable credentials redacted, and is bounded (4096 characters for names, titles and statuses, 16384 for a description), as the operator API bounds Work Item text. Ids are kept only when printable and at most 256 bytes; URLs keep only `http` and `https`, without user info, query or fragment.

### 3. Who may call it

`PLOEG_OPERATOR_CONSUMERS` gains `readTrackers` (chart `operator.consumers[].readTrackers`): the tracker connections whose Scopes and items the consumer may read, or `["*"]` for every one. It defaults to none and is independent of `teams`, `execute`, `readContent`, `bindScopes` and `manageConnections`, because a connection belongs to no Team (ADR-0195 section 7). `OperatorPrincipal` gains `ReadTrackers` and `CanReadTracker`.

Without `readTrackers` every route answers `403 permission_required`. A connection outside the grant, or one that does not exist, answers `404 unknown_connection`, so the grant does not reveal which connections exist. Reads need no proven person: they change nothing, as every other operator `GET`.

### 4. Capabilities

Two new, optional Tracker port capabilities (`pkg/provider/scopes.go`), reported by `provider.TrackerCapabilities`:

* **`ScopeLister`** (`scope_lister`): `ListScopes(ctx, parent Scope) ([]ScopeNode, error)`, one level, at most `provider.MaxScopeChildren` (1000).
* **`ScopeItemLister`** (`scope_item_lister`): `ListOpenItems(ctx, scopeID string, page int) (ScopeItemPage, error)`, one page of open items, at most 100.

A tracker without the capability answers `422 capability_missing`; one item needs only the port. An adapter marks "the tracker has no such thing, or the credential cannot see it" with `provider.NotFound`, which the routes answer as `404 not_found`; any other failure answers `502 tracker_unavailable` with no vendor text.

| Adapter | `ScopeLister` | `ScopeItemLister` |
| --- | --- | --- |
| ClickUp | Workspace → Space → Folder and folderless List → List (`/team`, `/team/{id}/space`, `/space/{id}/folder`, `/space/{id}/list`, `/folder/{id}/list`); only a List is bindable | `/list/{id}/task` without closed or archived tasks, subtasks included, ClickUp's page of 100 |
| Vikunja | projects nested by `parent_project_id`, archived projects left out, every project bindable | `/tasks/all` filtered on `done = false && project = <id>`, 50 to a page, and filtered again locally |
| GitHub Issues | the repositories configured as work sources, never another repository the App can reach | open issues of a configured repository, 50 to a page, pull requests left out |

ADR-0183's conformance suite gains a `scope_item_lister` case: a Scope's open items list every open item and no closed one, and an unconfigured adapter lists nothing.

### 5. Caching and rate limits

ploegd keeps every read in memory, shared by every consumer, keyed by connection and request: a level of Scopes for 60 seconds, a page of items for 30, one item for 15, and a failed read for 10, so a consumer that retries at once does not reach the tracker again. A level of Scopes is read once and paged from the cache, so `after` and `limit` cost no vendor calls. The cache holds at most 1024 reads and drops expired ones first. Every vendor call goes through the adapter's own `apilimit` client and has 20 seconds. The cache sits after the permission check, so a consumer without the grant never fills or reads it. A connection that is reconnected or removed (ADR-0195) is served from the cache for at most a minute.

### 6. Out of scope

* Writing anything to a tracker through these routes, and creating, binding or removing anything. Binding is ADR-0186; connections are ADR-0195.
* Search, filters beyond "open", sorting chosen by the caller, and closed items.
* A ploeg-mcp tool. It can follow on the same routes with the read-only tool rules of [ADR-0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md).
* Comments and attachments of an item.

### Consequences

* Good, because a consumer can offer "pick a List, see its tasks" with no tracker token, as the 2026-11-18 decision requires.
* Good, because the answer is the tracker's own, at most a minute old, and Ploeg stores no copy that can drift.
* Good, because the vendor sees one call per container per minute, not one per person.
* Good, because a binding (ADR-0186) can name exactly the `id` of a Scope these routes listed as `bindable`.
* Bad, because a read is live: a slow or failing tracker makes the picker slow or failing, answered as `502 tracker_unavailable`. Accepted: a stale copy is worse for choosing what to bind.
* Bad, because a consumer with `readTrackers` sees every container and item the connection's credential sees, not only those bound to its Teams. Operators should grant it per connection, and it is bounded to reading.
* Bad, because the cache lives in each ploegd replica, so two replicas may each call the tracker once per minute.
* Neutral, because Vikunja's `/tasks/all` filter syntax differs between versions; the local filter keeps the answer right on an instance that ignores it, at the cost of a shorter page.

### Confirmation

In the `go test ./...` CI step:

* `pkg/httpapi/operator_tracker_reads_test.go`: `TestTrackerReadsNeedTheReadTrackersGrantForThatConnection` (403 without the grant, 404 outside it, no tracker call either way), `TestReadTrackersRefusesAnInvalidOrMixedGrant`, `TestScopesBrowseTheHierarchyLevelByLevelWithinTheSchema` (each response validated against the published schema, a hostile name stays escaped, two pages cost one tracker call), `TestScopeReadsAreReusedUntilTheyExpireAndFailedReadsBriefly`, `TestScopeItemsPageThroughTheTrackersOwnPages`, `TestOneItemReadsWithItsTextUntrustedRedactedAndNoCredential` (the adapter's credential never reaches a response, a vendor error is never echoed) and `TestATrackerWithoutTheCapabilityAnswersCapabilityMissing`.
* `pkg/provider/clickup`, `pkg/provider/vikunja`, `pkg/provider/githubissues`: each adapter's `scopes_test.go` against an `httptest` fake of the vendor API; `TestClickUpDeclaresItsTrackerCapabilities` names both capabilities.
* `pkg/provider/trackertest`: the `scope_item_lister` case runs for every adapter that declares it.
* `go test ./internal/ledger/` keeps this record well-formed.

## Pros and Cons of the Options

### The consumer reads the tracker directly with its own token

* Good, because Ploeg changes nothing and the consumer can read anything the vendor API offers.
* Bad, because it is what the owner's decision of 2026-11-18 rules out: a second credential per tracker, held outside Ploeg's store, rotated and revoked separately, and able to write.
* Bad, because what the consumer sees can differ from what Ploeg's connection can reach, so a person can pick a Scope Ploeg cannot read.

### Ploeg mirrors every task of every reachable Scope into its store and serves the copy

* Good, because reads are fast and survive a tracker outage.
* Bad, because the copy drifts: it needs every webhook for every reachable Scope, including Scopes nobody bound, and a missed event leaves it wrong without anyone noticing.
* Bad, because it stores people's text for every task Ploeg can see, not only the work it was given, which widens what the store holds and what [read access](../how-to/give-dashboards-read-access.md) must keep out.

### Widen ADR-0195's connection probe to return the whole hierarchy

* Good, because no new routes.
* Bad, because the probe needs `manageConnections` and is limited to one call per 30 seconds, and a full ClickUp hierarchy is one call per Space and Folder, which a probe should not make.
* Bad, because it cannot preview items.

## Re-evaluation triggers

* ADR-0195 is built, so store-backed connections answer on these routes and the permission model is checked against `manageConnections`.
* A tracker answers the cached reads with a rate-limit refusal more than once a day.
* A consumer needs search or closed items in the picker.
* ploegd runs more than two replicas, so the per-replica cache multiplies tracker calls.
* A supported Vikunja version drops `/tasks/all` or its `filter` syntax.

## More Information

* Owner decision, 2026-11-18: Ploeg owns every tracker and forge connection; an operator consumer holds no tracker credential.
* Related: [ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md) (opaque Scopes), ADR-0129, ADR-0170, ADR-0183, ADR-0184, ADR-0186, ADR-0195.
* 2026-11-27: owner decision: accepted; the code on `development` matches the text: `pkg/httpapi/operator_tracker_reads_test.go`, `scopes.go` in `pkg/provider/clickup`, `pkg/provider/vikunja` and `pkg/provider/githubissues`, and `TestClickUpDeclaresItsTrackerCapabilities` in `pkg/provider/clickup/conformance_test.go`.
