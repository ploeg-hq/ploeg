---
status: accepted
date: 2026-11-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-03-31
---

# A Team has an immutable id, a renameable key and a free name, and a proven person creates one at runtime

## Context and Problem Statement

A Team is known by one string today: its name. That name is the key of `teams.<name>` in the configuration file (`config.Team`, [`pkg/config/config.go`](../../pkg/config/config.go)) and of `executor.teams[].name` in the chart. The same string is stored in every record that points at a Team: the `team` column of `work_items`, `shifts`, `agent_runs`, `leases`, `chats`, `card_cracks`, `work_item_premise_checks` and `tracker_bindings` ([migration 0088](../../pkg/store/migrations/0088_tracker_bindings.sql)), the `team` label of every Team metric ([`pkg/httpapi/metrics.go`](../../pkg/httpapi/metrics.go)), `detail.team` in the audit log, the worker's `PLOEG_TEAM` and its bootstrap key `<team>--<role>`, and the name of each worker workload, `<release>-worker-<team>[-<role>]`. Renaming a Team therefore orphans its history, its metrics and its routes, so nobody renames one, and the names an installation started with stay forever even when they no longer say what the Team does.

A Team also exists only as static configuration. [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) proposed one Team manifest (charter, crew, permissions, spend, intake, skills) and left "the operator API serving the whole manifest for a consumer's 'build a team' page" as its last step. [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) lets a person bind a tracker Scope to a Team at runtime, [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md) lets one register a Work Target and [ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) a connection, but ADR-0186 section 9 kept "creating or editing Teams" out. A person who wants "a Team that reviews documentation with model X on harness Y, capped at 50 dollars a month" still edits Helm values, opens a pull request against the deployment and waits for a rollout, and the hard part is not the file: the chart renders one KEDA `ScaledJob` per Team, Role and allowed Harness ([`scaledjob.yaml`](../../ops/helm/ploeg/templates/scaledjob.yaml), `ploeg.workerWorkloads` in [`_helpers.tpl`](../../ops/helm/ploeg/templates/_helpers.tpl)), with the Team name baked into the workload name, the scaler query and the bootstrap Secret key. A Team that exists only in Ploeg's store has no worker that can claim its Runs.

How does a Team get an identity that survives a rename, and how does a proven person create and change one while ploegd runs, without widening what a Run may reach, without a second unreviewed source of pod specifications, and without anything to migrate for an installation that only uses static Teams?

## Decision Drivers

* **References outlive names.** A Run, a binding, a budget hold, an audit row and a metric series must point at the same Team after it is renamed.
* **People read keys, records store ids.** Configuration, URLs and a command line need a short, readable handle; storage needs one that never changes.
* **The reviewed configuration is the operator's word** (ADR-0186, ADR-0193). A runtime Team never overrides a static one, and a conflict is visible, not silent.
* **No new way to widen a Run.** A runtime Team chooses among harnesses, models, targets and budgets the operator already allowed. It never chooses an image, a pod shape, a credential or a network path.
* **Only harnesses qualified for credential isolation** run a runtime Team's Roles: [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) today, the proposed credential gateway outside the Run pod later.
* **No Kubernetes write authority for ploegd** if it can be avoided. A process that can create pod specifications in the executor namespace is a second chart.
* **Nothing to migrate for operators.** Existing static Teams keep working with the names they have.
* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). The contract is generic.

## Considered Options

Identifiers:

* A name only, as today
* An immutable id and a display name
* **An immutable id, a renameable key with aliases, and a free display name**

Creation:

* Static configuration only, as today
* **Runtime creation through the operator API, under static configuration**

Worker provisioning for a runtime Team (section 7):

* **A generic worker pool per Tenant and qualified Harness that claims for any active runtime Team**
* ploegd creates a `ScaledJob` per runtime Team through the Kubernetes API with a narrow Role

## Decision Outcome

Chosen options: "**an immutable id, a renameable key with aliases, and a free display name**" and "**runtime creation through the operator API, under static configuration**", served by "**a generic worker pool per Tenant and qualified Harness**", because together they let a person rename a Team without breaking a reference, let a proven person create and change a Team within the operator's limits without a rollout, keep the static file the authority, and add no Kubernetes write authority to ploegd.

Proposed, not built. Each section states the contract the implementation must meet. The owner answered the open questions on 2026-11-18; sections 4, 5 and 3 carry those answers, and section 14 keeps the one still open.

### 1. Three identifiers

| Identifier | Example | Rule |
| --- | --- | --- |
| `id` | `team_01jd8x4k2v6r9t3m5n7p0q2s4w` | Generated by Ploeg: `team_` and a lowercase ULID. Immutable, never reused, never chosen by a caller. **Every reference stores it**: Work Items, Shifts, Runs, leases, chats, budget holds, ADR-0186 bindings, audit `detail.team_id`, the worker's control token, and metric series (section 10). |
| `key` | `bronze`, `docs-review` | A slug, unique per Tenant. Used by static configuration, the command line, URLs and tracker labels. A runtime key matches `^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`; static keys keep whatever `Validate` accepts today and are not checked against it, as ADR-0193 does for target keys. Changing it is a **rename** (section 4). |
| `name` | `builder · openhands` | Free display text, 1 to 80 characters, no control characters. Changeable at any time, never unique, never used to resolve anything. |

**Static Teams.** The key of a static Team is its name in the configuration file, so `bronze`, `silver`, `copper` and `pewter` stay what they are. On the first boot after this lands, ploegd gives each static Team without a row an id (section 6) and stores it; on every later boot it finds the row by key or alias and keeps the id. Nothing in the configuration file or the chart changes, and no operator migrates anything. `teams.<key>.name` is an optional new field; without it, the display name defaults to a hint built from the Team's plan, `<role> · <harness>` for a one-Role Team (`builder · openhands`) and the key for any other. The default is a convenience, never a rule: a runtime Team's creator may overwrite it in the request, and an operator overwrites a static Team's in the file.

**A static Team's name is set by configuration, and says so.** The configuration file is the only source of a static Team's `name`, whether `teams.<key>.name` sets it or the default hint fills it. Every read of a static Team carries `nameSource: {"source": "configuration", "key": "teams.<key>.name", "default": <true when the hint filled it>}`, and a runtime Team's carries `nameSource: {"source": "operator_api"}`, so a screen can say where a name comes from before anyone tries to change it. A `PATCH` of a static Team's name is refused with `409 name_set_by_configuration`, whose message names the file key to change (section 3). A person therefore never sees a rename that seems to work and then does not stick.

**Resolution.** Wherever a Team is named by a person or a file (a path segment, a request field, a tracker label, `teams.<key>.createdWork.refinementTeam`, an operator consumer's `teams` scope), Ploeg accepts an id, a current key or an alias, resolves it to the id once, and stores the id. Responses always carry all three, plus `aliases`, so a caller can show the name and link with the key.

### 2. Runtime Teams finish ADR-0110's manifest

A **runtime Team** is a Team a proven person created through the operator API. Its body is ADR-0110's manifest, restricted to what does not change a pod or a credential:

| Part | Fields a runtime Team may set | Rule |
| --- | --- | --- |
| Identity | `key`, `name` | Section 1. |
| Charter | `charter` | At most 4,000 characters; the default brief of its Runs (ADR-0110). |
| Crew | `plan`: Rounds of Roles, each with `name`, `writes`, `cap`, `planner`, `shadow`, `harness`, `model` | `plan.Validate` as for static Teams. A plan is required; a plan-less runtime Team is refused. `harness` must be a key the installation offers to runtime Teams (section 7); `model` must be in `teamCreation.models`. At most 8 Roles in 5 Rounds. |
| Spend | `spend.cap` (`{amount, currency, period}`, ADR-0162's spend cap), `spend.shift` (the Shift pool), Role `cap` | `spend.cap` is **required**. The sum of the caps of a Tenant's live runtime Teams stays within `teamCreation.tenants.<tenant>.spend`; a Role `cap` within `teamCreation.maxRoleCap`. `maxRunning` at most the pool's `maxReplicaCount`. |
| Permissions | `may` | ADR-0110's `{action, resource, effect}` list, restricted: `write` and `comment` as the operator allows in `teamCreation.may`; `createWork` only `proposed`, never `queued`; `egress` only allowlist names the operator declared; no `tools` (MCP groups need a gateway team, see *Out of scope*). |
| Intake | `intake.admissions` (whether an operator admission may name it), `intake.targets` (target keys its Work Items may use, static or ADR-0193 runtime) | Boards reach it through ADR-0186 bindings and the hand-over agent of section 11, never through `assignees`. |
| Sponsor | `sponsor` | [ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md)'s person. Defaults to the proven person who creates the Team. |

Refused on purpose, because each one changes a pod, a credential or a network path: `image`, `toolchains`, `dind`, `sandbox`, `runtimeClassName`, `claudePlugins`, `harness.claudeEnvironment` (paid by a Team's Claude plan, ADR-0084), `litellmTeamId` or `gatewayTeam`, MCP groups, `assignees`, and a raw repository. A request that carries any of them, or any unknown field, is refused, never ignored.

Static Teams keep every field they have today. The manifest a runtime Team stores is the same shape the operator API serves for a static Team, so a person reads both alike.

### 3. API

New and changed operator routes, versioned with the operator API (`docs/contracts/operator-api.v1.schema.json` and its Go types change together). `{team}` accepts an id, a key or an alias.

| Method and path | Effect |
| --- | --- |
| `GET /api/v1/operator/teams` | Exists. Gains `id`, `key`, `name`, `aliases`, `source` (`static` or `runtime`), `state`, `revision` and the manifest of every Team in scope. |
| `GET /api/v1/operator/teams/{team}` | One Team. A request by alias answers `200` with the canonical `key` in the body and a `Content-Location` naming it, so a link made before a rename keeps working. |
| `POST /api/v1/operator/teams` | Validate, then create. `201` with the Team, or `200` with the existing one when a live runtime Team with the same key and the same body exists (`created: false`). |
| `POST /api/v1/operator/teams?dryRun=true` | Validate only. Returns what creation would store, the generated default `name` when none is given, every check's result and the spend headroom left in the Tenant; writes nothing. |
| `PUT /api/v1/operator/teams/{team}` | Replace the manifest of a runtime Team, `key` included. Requires `If-Match: "<revision>"`; a stale revision is `412`. Accepts `?dryRun=true`. A static Team answers `409 team_is_static`; it is changed in the file. |
| `PATCH /api/v1/operator/teams/{team}` | Change only `name` of a runtime Team. Requires `If-Match`. A static Team answers `409 name_set_by_configuration`, with `detail.configKey` (`teams.<key>.name`) and a message that says to change that key in the configuration file; nothing is stored, and `team.refused` is written. |
| `DELETE /api/v1/operator/teams/{team}` | Remove a runtime Team (section 5). `204`, also when it was already removed. |

Idempotency is by key, as ADR-0186's is by natural key: the same key with the same body twice yields one Team; the same key with another body is `409 team_exists` with the current Team and revision.

Errors use the operator envelope: `403 permission_required`, `403 person_token_required`, `403 team_not_allowed`, `404 unknown_team`, `409 conflicts_with_static`, `409 key_taken` (the key is a live key or alias of another Team in the Tenant), `409 key_reserved` (a released alias or a removed Team's key still in its 90-day reservation, with `reservedUntil`), `409 name_set_by_configuration`, `409 team_draining`, `409 team_exists`, `409 team_is_static`, `409 team_in_use`, `412`, `422 creation_not_accepted`, `422 field_not_allowed` naming the field, `422 harness_not_offered`, `422 harness_not_qualified`, `422 model_not_allowed`, `422 unknown_target`, `422 spend_exceeds_tenant` with the headroom, `422 plan_invalid` with the plan validator's message, `429 team_limit`.

ploeg-mcp gains `ploeg_list_teams` (read-only) and the actions `ploeg_create_team`, `ploeg_change_team` and `ploeg_remove_team`, which need the person's confirmation like every ploeg-mcp action ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)); the confirmation text shows the dry-run result, spend headroom included.

### 4. Rename and aliases

A `PUT` that changes `key` is a rename. The old key becomes an **alias** of the same id: every route, label, link and configuration reference that named it keeps resolving to the Team, and an alias is never handed to another Team in the Tenant while it is held. A Team holds at most 10 aliases; renaming past that is `422 too_many_aliases` until a person releases one (`DELETE /api/v1/operator/teams/{team}/aliases/{alias}`, which answers `409 alias_in_use` while a static reference such as another Team's `refinementTeam` still names it). A rename back to an alias makes it the key again.

**A released alias stays reserved for 90 days.** Releasing an alias, and removing a runtime Team (section 5), sets `released_at` on its key rows; for 90 days after that no other runtime Team in the Tenant may take the key, and an attempt answers `409 key_reserved` with `reservedUntil`. While reserved, the key no longer resolves: a request by it answers `404 unknown_team` with `detail.reservedUntil`, so a link or label made before the release fails visibly instead of reaching a Team its author never meant. The Team that held it may take it back within the window, as a rename back to an alias. A static key in the file is the operator's word and is never refused over a reservation; it takes the key at boot and writes `team.alias_released` for the reservation it ends.

A **static** Team is renamed in the file. To keep its id, the operator lists the old key under `teams.<new-key>.aliases`; at boot ploegd finds the row by that alias, makes the new key current and writes `team.renamed`. A static Team whose key changed without the alias is a new Team with a new id, and the old row is removed (section 6), which is what an operator who deletes one Team and adds another means.

Nothing that stores a Team stores its key, so a rename touches one row and one alias row, never a Work Item, a Run, a binding, a metric series or a tracker.

### 5. States, conflicts and removal

States, as ADR-0186 and ADR-0193 name them:

* `active`: in force. Its Runs are claimable and bindings may name it.
* `shadowed`: a runtime Team whose key a static Team in the file now takes, with `shadowedBy` naming it. **Static wins.** At the call this is `409 conflicts_with_static`; after a redeploy that adds the static key, ploegd never fails to boot over runtime state. It marks the runtime Team `shadowed`, writes one `team.shadowed` event and exports `ploeg_teams{source,state}`. Its id is unchanged, so its history and bindings stay attached; its key no longer resolves to it, its pending Runs are not claimed, and its bindings turn ADR-0186's `invalid` with the new reason `team_unavailable`. A person recovers it by renaming it to a free key, or it returns to `active` by itself, with `team.unshadowed`, when the static key goes away. A static key that equals a runtime Team's **alias** releases that alias, with `team.alias_released`; the runtime Team stays `active`.
* `invalid`: Ploeg can no longer run it, with `invalidReason`: `harness_not_offered` (the pool for its harness was removed from the chart), `harness_not_qualified` (its harness lost credential isolation), `model_not_allowed`, `target_unavailable` (a key in `intake.targets` no longer resolves), `spend_exceeds_tenant` (the Tenant's allowance was lowered below the Team's cap) or `creation_disabled` (the installation switched runtime Teams off). Its Runs are refused at claim and its Work Items are not dispatched; it is never deleted on a person's behalf, and it returns to `active` by itself, with `team.restored`, when the cause goes away.

Removal of a static Team happens in the file. ploegd sets `removed_at` on its row at boot, writes `team.removed`, and ADR-0186's bindings of that id turn `invalid` with `team_removed`, as they do today. If the same key comes back, the row is found and keeps its id.

**Deleting a runtime Team** answers `409 team_in_use`, listing what refers to it, while any of these holds:

* a live ADR-0186 binding names its id;
* a Work Item that is not in a terminal state carries its id;
* a static or runtime Team names it as `refinementTeam`.

Work Targets belong to no Team (R11, ADR-0193), so a target never blocks a removal; a Team's `intake.targets` refers to targets, not the reverse.

**Drain.** `DELETE …?drain=true` is accepted only when no live binding names the Team. It sets `draining_since`, refuses new admissions for the Team with `409 team_draining`, lets open Work Items finish, and removes the Team with `team.removed` when its last open Work Item reaches a terminal state. A draining Team stays `active` until then, so its Runs are still claimed. A `PUT` on a draining Team answers `409 team_draining`; `POST /api/v1/operator/teams/{team}/undrain` cancels the drain.

**A drain lasts at most 14 days.** The response and every read of a draining Team carry `drainDeadline`, `draining_since` plus 14 days. When the deadline passes, the sweep moves every open Work Item of the Team to `needs_human` with the reason `team_drain_expired`, claims no further Run of the Team, writes `team.drain_expired` listing the Work Items it moved, and removes the Team with `team.removed`. A person then decides each Work Item; Ploeg never re-routes one to another Team on its own. A drain that ends early because the last Work Item finished is unchanged.

Removal sets `removed_at`; rows are never deleted. A removed runtime Team's key and aliases are released with it and stay reserved for 90 days (section 4); after that a new Team may take one and gets a new id.

### 6. Storage and migration

Migration `0090_teams.sql` (after `0088_tracker_bindings.sql` on `development`; `0089` is reserved by the runtime Work Targets change) adds:

* **`teams`**: `id` (text, primary key), `tenant` (null for a static Team, which stays deployment-wide under [ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md); the Tenant id for a runtime Team), `source`, `name`, `manifest` (JSONB, runtime Teams only; a static Team's manifest stays in the file), `revision`, `state`, `shadowed_by`, `invalid_reason`, `draining_since`, `created_by`, `updated_by`, `removed_by`, `created_at`, `updated_at`, `removed_at`.
* **`team_keys`**: `team_id`, `tenant`, `key`, `kind` (`current` or `alias`), `since`, `released_at`. A partial unique index on `(coalesce(tenant, ''), lower(key)) WHERE released_at IS NULL` and a second on `lower(key) WHERE tenant IS NULL AND released_at IS NULL` enforce one live holder per key per Tenant; a static key is checked against every Tenant in code, because a static Team is visible in all of them. The 90-day reservation of section 4 is checked in code, in the transaction that takes the key, against rows whose `released_at` is less than 90 days old.
* A nullable **`team_id`** column on every table that has a `team` column today (`work_items`, `shifts`, `agent_runs`, `leases`, `chats`, `card_cracks`, `work_item_premise_checks`, `tracker_bindings`), with indexes matching today's `team` indexes, `agent_runs_claimable_harness` included.

ULIDs are generated in Go, not in SQL. The **boot reconciler** runs after migrations: it upserts a row for each static Team by key or alias (a new id only for a key it has never seen), sets `removed_at` on static rows whose key and aliases left the file, recomputes runtime Team states, then backfills `team_id` where it is null, by key, in batches of 10,000 per transaction, so a large `agent_runs` table is not locked. New writes set `team_id` from the moment the reconciler finished. Every read path resolves by `team_id` and falls back to the `team` text only while a row's `team_id` is still null.

The `team` text columns stay and keep the key **as it was when the row was written**, which an auditor may want; nothing resolves through them after the backfill. A later migration, once one release has run with a complete backfill, makes `team_id` `NOT NULL` and moves the chart's scaler queries to it. The migration extends `ploeg_grant_observer()` with `teams` and `team_keys` columns other than the attribution columns, since neither table holds a credential.

### 7. Worker pools: how a runtime Team's Runs are executed

Today the chart renders, for each `executor.teams[]` entry, each Role and each allowed Harness, one `ScaledJob` whose name, pod template, `PLOEG_TEAM`, `PLOEG_ROLE`, bootstrap Secret key (`<team>--<role>`) and scaler query (`agent_runs WHERE team = '<team>' AND role = '<role>' AND harness_requested …`) are fixed at render time. A runtime Team cannot use any of them. Two designs were weighed.

**Chosen: a generic worker pool.** The chart gains `executor.runtimeTeams`:

```yaml
executor:
  runtimeTeams:
    enabled: false
    pools:
      - tenant: default
        harness: openhands
        maxReplicaCount: 4
      - tenant: default
        harness: acp/openhands
        maxReplicaCount: 2
```

For each pool the chart renders **one** `ScaledJob`, `<release>-pool-<tenant>-<harness>`, from the same `ploeg.workerPodTemplate` with the harness's default image and the chart's default toolchains, no DinD, no plugins and no Team-specific environment. Its labels carry `ploeg.webgrip.dev/pool` instead of a Team and Role. Its scaler query counts every claimable Run of an active runtime Team in that Tenant for that Harness:

```sql
SELECT COUNT(*) FROM agent_runs r JOIN teams t ON t.id = r.team_id
WHERE t.source = 'runtime' AND t.state = 'active' AND t.tenant = '<tenant>'
  AND r.state = 'pending' AND r.harness_requested = '<harness>' AND r.carrier IS NULL
```

A runtime Team's Role always stores its Harness on the Run, so the query never needs the `harness_requested IS NULL` branch. The pool's worker holds a **pool bootstrap** (`pool--<tenant>--<harness>` in the bootstrap Secret) that ploegd's `WorkerBootstrap` accepts with a new `Pool` field instead of `Team` and `Role`. A pool claim selects the oldest pending Run of any active runtime Team in that Tenant with that Harness, respecting each Team's `maxRunning` and spend cap in the same transaction as today's claim; it never selects a static Team's Run, and a static Team's worker never selects a runtime Team's Run. The claim response names the Run's Team id and Role, and the control token is bound to them as today, so everything after the claim is unchanged. A static installation renders no pool and sees no difference.

The chart renders a pool only for a Harness in `ploeg.isolationQualifiedHarnesses`, and fails the render when credential isolation would be off for it (`executor.litellm.keyIsolation` or `executor.forgeTokenIsolation` unset). ploegd learns the offered pools from `PLOEG_RUNTIME_POOLS`, rendered from the same values as the `ScaledJob`s, the way `PLOEG_TEAM_HARNESSES` is today, so the API refuses a Harness no pool runs (`422 harness_not_offered`) or one that is not qualified (`422 harness_not_qualified`). When the proposed credential gateway outside the Run pod ([ADR-0197, proposed in #436](https://github.com/ploeg-hq/ploeg/pull/436)) is built, a pool behind it is qualified by construction, and the pool's pod labels are what the gateway reads to forward the claim with the pool's capability; the qualified set then widens to every Harness the gateway serves.

One pool serves the runtime Teams of **one** Tenant, so a pod escape in one Tenant's Run is never one step from another Tenant's Run. Several pools for one Tenant and Harness are refused at render.

Why not the alternative, ploegd creating a `ScaledJob` per runtime Team through the Kubernetes API with a Role narrowed to `scaledjobs.keda.sh` in the executor namespace:

* A `ScaledJob` carries a full pod template. Whoever may create one may run any image under any ServiceAccount in that namespace, with only the admission policy ([ADR-0168](0168-profile-p1-turns-on-the-worker-admission-policy-and-a-sandbox-team-names-its-runtime-class-there.md)) between a compromised ploegd and a privileged pod. "Narrow" does not narrow the pod.
* ploegd would also need to write the bootstrap Secret, a second write grant, and a Secret in a namespace ploegd writes is a credential a person never reviews.
* The pod template lives in the chart's helpers. ploegd would have to reproduce it in Go and keep it byte-identical, the drift ADR-0097's single source exists to prevent, or render the chart at runtime.
* A GitOps controller that owns the namespace flags or prunes resources it did not render.
* What it buys, a pod shape per Team, is exactly what a runtime Team must not choose (section 2).

Accepted costs of the pool: runtime Teams cannot have their own image, toolchains or sandbox, and a Team that needs one is a static Team; the scaler counts Runs of Teams that are at their `maxRunning`, so KEDA may start a pod that exits empty, bounded by the pool's `maxReplicaCount` (an empty pod is the overshoot ADR-0099 already accepts; undershoot, which stalls work silently, cannot happen); and the pool's `maxReplicaCount` is shared, so a busy runtime Team can delay another up to its own `maxRunning`.

### 8. Who may call it

Four gates, all required, as ADR-0193 sets them:

1. **The installation accepts runtime Teams.** The configuration file gains `teamCreation`: `enabled` (default `false`); `models` (the gateway models a Role may name); `may` (the `write`, `comment` and `egress` grants a runtime Team may hold); `maxRoleCap`; `max` (live runtime Teams per Tenant, default 10); `tenants.<tenant>.spend` (the allowance the caps of that Tenant's runtime Teams may add up to; a Tenant without one may create none); and `requireClaims` with optional step-up, matched as ADR-0150 and ADR-0193 match them. Without it every write route answers `422 creation_not_accepted`, and nothing changes for an installation that does not opt in. The chart's `executor.runtimeTeams.enabled` and `teamCreation.enabled` must agree; the chart refuses to render one without the other.
2. **The consumer holds the permission.** `PLOEG_OPERATOR_CONSUMERS` gains `manageTeams` (default `false`) and `OperatorPrincipal` gains `CanManageTeams`. A consumer with it reads and manages every runtime Team of its own Tenant; its `teams` scope ([ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)) still decides which static Teams it reads, and may name a Team by key or alias, resolved to the id at boot and after each change.
3. **A proven person makes the change** ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)). Create, replace, rename, remove and drain refuse asserted attribution **in every attribution mode**, because a Team spends money. Changing only a runtime Team's `name` follows the installation's attribution mode.
4. **The person holds operator authority**: their token carries the claims `teamCreation.requireClaims` names, and the step-up when configured.

A runtime Team belongs to the Tenant of the consumer that created it; no request names a Tenant (ADR-0178). This makes runtime Teams the first per-Tenant Teams, which ADR-0178's *Consequences* anticipated.

### 9. Audit and events

Every change writes one `audit_log` row with `work_item_id` NULL, the proven person as `actor`, and the consumer, the channel and the Team's id, key and revision in `detail`: `team.created`, `team.updated` (with the old and new revision and the changed parts), `team.renamed` (old and new key), `team.name_changed`, `team.removed`, `team.drain_started`, `team.drain_cancelled`, `team.drain_expired`, `team.refused` (with the error code and failed checks, so repeated probing for keys or headroom is visible), `team.shadowed`, `team.unshadowed`, `team.invalid`, `team.restored`, `team.alias_released`. Changes the reconciler finds at boot are attributed to the configuration, not a person. `GET /api/v1/operator/events` returns these rows to consumers that may read the Team. Every existing event about a Work Item, Shift or Run gains `detail.team_id` beside `detail.team`.

### 10. Metrics and label cardinality

Every series that carries `team` today gains `team_id`; `team` stays, carrying the current key, for one minor release so dashboards and the chart's `PrometheusRule` keep working, and is then removed. A new info series, `ploeg_team_info{team_id, key, source, state} 1`, lets a dashboard join a readable key onto `team_id`; a rename changes only that one series. The free `name` is never a label: it is unbounded text that people change, which belongs in the API, not in a time-series index.

Cardinality stays bounded: runtime Teams are capped per Tenant (`teamCreation.max`, default 10), a removed Team's series disappear on the next scrape because every Team metric is computed from the store at scrape time, and the existing `team × role × outcome × reason` product of `ploeg_runs_finished_total` grows only by the Roles a runtime Team declares, at most 8. `ploeg_teams{source,state}` counts Teams for alerting on `shadowed` and `invalid`.

### 11. Trackers: assignees and people

A runtime Team has no `assignees` and no tracker people of its own. Work reaches it through two runtime records that already exist: an ADR-0186 binding routes a Scope to the Team's **id**, and the connection's own account is the hand-over agent for the Scopes bound through it ([ADR-0195 section 9](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md)). A person assigns that agent; the binding decides the Team. A tracker therefore never stores a Team's name or id, and a rename touches nothing in any tracker. Where a tracker label picks among several Teams ([ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md)), the label carries a key, and an old key keeps resolving through its alias; a display name is never matched.

Static Teams keep `teams.<key>.assignees` and `trackers.<tracker>.people` exactly as today.

### 12. Limits

* `teamCreation.max` live runtime Teams per Tenant (default 10), `429 team_limit`.
* 8 Roles in 5 Rounds per runtime Team; `charter` 4,000 characters; `name` 80; 10 aliases per Team.
* The sum of runtime Team spend caps per Tenant within `teamCreation.tenants.<tenant>.spend`, checked at create and update in the same transaction as the write, so two concurrent creates cannot both fit.
* The create and replace routes are rate-limited per consumer, as ADR-0186's create route is.

### 13. ADR-0186 is amended

ADR-0186 section 9 excluded creating and editing Teams. This record amends it in place: a binding's `team` is resolved to and stored as the Team's id, a binding may name a runtime Team, and a binding whose Team is shadowed or invalid turns `invalid` with `team_unavailable`. Its other rules are unchanged.

### 14. Out of scope

* MCP tool groups and skills catalogues for runtime Teams. MCP groups need a gateway team ([ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md), [ADR-0177](0177-ploeg-reaches-models-through-a-gateway-contract-with-litellm-and-envoy-ai-gateway-as-implementations.md)) that a pool would have to share, and a skills catalogue ([ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md)) is fetched per Team. This is the one question still open on 2026-11-18: the owner is deciding it, and a follow-up record will carry the decision. Until then a runtime Team's request that carries `tools` or `skills` answers `422 field_not_allowed`.
* The sandbox and CronJob executors for pools; this record renders pools for the `keda` executor only.
* Converting a static Team into a runtime Team or back. An operator who wants it removes the static Team and a person creates a runtime one, which gets a new id.
* Any user interface. A consumer builds its own on this contract.

### Consequences

* Good, because a Team can be renamed, by a person or in the file, without orphaning a Run, a binding, a budget, an audit row, a metric or a tracker label.
* Good, because a proven person can build a Team (charter, Roles, models, harnesses, caps) in one validated call with a dry run that shows the spend headroom, instead of a configuration pull request and a rollout.
* Good, because ploegd gains no Kubernetes write authority: the chart still renders every pod template, and a runtime Team chooses only among harnesses, models, grants and allowances the operator rendered or declared.
* Good, because static installations change nothing: their Teams keep their names as keys and receive an id silently.
* Bad, because Teams now have two sources, as routes and targets already do; the operator API, not the file, is where a person sees every Team.
* Bad, because the backfill touches every table that names a Team, and read paths carry a `team_id`-or-`team` fallback until the follow-up migration lands.
* Bad, because runtime Teams cannot have their own image, toolchains or sandbox, and one pool's replicas are shared by every runtime Team of a Tenant and Harness.
* Neutral, because `manageTeams` with operator authority is a real grant of spend: whoever holds both can spend a Tenant's whole runtime allowance. It is bounded by the allowance, the limits, the qualified harnesses and the audit trail, and operators should give it sparingly.

### Confirmation

Proposed, none of these exist yet:

* `pkg/store` migration test: after `0090`, a store holding Work Items, Runs and leases for static Teams `bronze` and `silver` boots, each gets one `team_` id, every row's `team_id` is backfilled, a second boot changes no id, and a static Team renamed in the file with `aliases: [bronze]` keeps `bronze`'s id.
* `pkg/httpapi` tests with `httptest` for create, repeat create (one row, `200`), conflicting create (`409 team_exists`), dry run (writes nothing, reports headroom), stale `If-Match` (`412`), a refused field (`image`, `dind`, `assignees`, `createWork: queued`) answering `422 field_not_allowed`, an unknown field refused, a harness without a pool (`422 harness_not_offered`), a model outside `teamCreation.models`, a spend cap over the Tenant's headroom, two concurrent creates of which only one fits, a consumer without `manageTeams` or another Tenant's Team (`404`), and asserted attribution refused in every attribution mode.
* A rename test: after `PUT` changes `bronze-docs` to `docs-review`, `GET …/teams/bronze-docs` answers `200` with `key: docs-review`, a binding and a pending Run made before the rename still resolve to the same id, the `team` label of `ploeg_runs_finished_total` changes while `team_id` does not, and creating another Team with key `bronze-docs` is `409 key_taken`.
* A shadowing test: a runtime Team `docs-review` exists; the file gains a static `docs-review`; boot marks the runtime Team `shadowed`, writes one `team.shadowed`, its bindings turn `invalid` with `team_unavailable`, its pending Runs are not claimed by the pool, and removing the static Team restores all of it.
* A removal test: `DELETE` answers `409 team_in_use` listing a live binding and an open Work Item; with `?drain=true` and no binding, admissions are refused, the open Work Item finishes, and the Team is removed with one `team.removed`.
* Chart goldens: `executor.runtimeTeams` renders one `ScaledJob` per pool with the pool bootstrap key and no Team environment; the render fails for a pool on an unqualified Harness, with credential isolation off, with DinD, for two pools on one Tenant and Harness, and when `executor.runtimeTeams.enabled` and `teamCreation.enabled` disagree.
* A scaler agreement test, as `TestTheChartsScalerQueryCountsWhatItsWorkerClaims` does for Roles: the rendered pool query counts exactly the Runs `store.ClaimForPool` would claim, and never a static Team's Run or another Tenant's.
* A worker auth test: a pool bootstrap claims a runtime Team's Run and gets a control token bound to that Team id and Role, and cannot claim a static Team's Run; a Team bootstrap cannot claim a runtime Team's Run.
* A metrics test: `ploeg_team_info` has one series per live Team, no series carries `name`, and a removed Team's series are gone on the next scrape.
* An alias reservation test: after a rename from `bronze-docs` to `docs-review` and the release of `bronze-docs`, `GET …/teams/bronze-docs` answers `404 unknown_team` with `reservedUntil`, creating another Team with key `bronze-docs` answers `409 key_reserved` for 90 days and succeeds with a new id on day 91 (a fake clock), the Team that held it takes it back within the window, and a static Team with key `bronze-docs` in the file takes it at boot.
* A removal reservation test: a runtime Team removed with `DELETE` releases its key and aliases, and each answers `409 key_reserved` for another Team until 90 days have passed.
* A drain deadline test: a draining Team with one open Work Item reports `drainDeadline` 14 days after `draining_since`; with a fake clock past the deadline the sweep moves the Work Item to `needs_human` with `team_drain_expired`, the pool claims none of the Team's pending Runs, one `team.drain_expired` and one `team.removed` are written, and a drain whose last Work Item finished on day 3 is removed on day 3.
* A static name test: `GET …/teams/bronze` carries `nameSource.source: configuration` and `nameSource.key: teams.bronze.name` (with `default: true` when the file sets no name), `PATCH …/teams/bronze` with a new `name` answers `409 name_set_by_configuration` naming `teams.bronze.name`, stores nothing and writes one `team.refused`, and a `PATCH` of a runtime Team's name answers `200` with `nameSource.source: operator_api`.
* `go test ./internal/ledger/` keeps this record well-formed, and `go test ./internal/boundary/` keeps it free of consumer names.

## Pros and Cons of the Options

### A name only, as today

* Good, because there is nothing to build and one string is easy to read in every table.
* Bad, because a rename orphans every record and metric, so Teams keep names that stopped being true.
* Bad, because a runtime Team's name would be both a person's label and every table's foreign key; people change labels.

### An immutable id and a display name

* Good, because references survive any rename and the name is free text.
* Bad, because configuration, URLs, command lines and tracker labels would have to carry an opaque id, or match a free-text name that is neither unique nor stable. Static configuration in particular needs a readable handle an operator writes by hand.

### An immutable id, a renameable key with aliases, and a free display name (chosen)

* Good, because each consumer of the identity gets the property it needs: storage an id that never changes, people and files a readable key that can change without breaking links, and screens a name nobody depends on.
* Bad, because there are three identifiers to explain and an alias table to keep, and resolving a key goes through one more lookup.

### Static configuration only, as today

* Good, because every Team is reviewed like code and the chart renders exactly its workloads.
* Bad, because a person who wants a Team for a task waits for a pull request against the deployment and a rollout, and nothing validates a model or a budget before it merges.

### Runtime creation through the operator API, under static configuration (chosen)

* Good, because it validates a Team before accepting it, attributes it to a proven person within a Tenant allowance, and keeps the file the authority, as ADR-0186 and ADR-0193 do for routes and targets.
* Bad, because it needs the pool of section 7 and a second source of Teams.

### A generic worker pool per Tenant and Harness (chosen for provisioning)

* Good, because the chart stays the only author of pod templates, ploegd needs no Kubernetes write grant, and the claim, the control token and everything after it are unchanged.
* Bad, because pod shape is fixed per pool and replicas are shared within a Tenant.

### ploegd creates a `ScaledJob` per runtime Team

* Good, because each Team gets its own replicas and could get its own pod shape.
* Bad, because a grant to create `ScaledJob`s is a grant to run any pod in the namespace, ploegd would also write bootstrap Secrets, the pod template would be duplicated in Go, and a GitOps controller would fight the resources (section 7).

## Re-evaluation triggers

* The credential gateway outside the Run pod (proposed ADR-0197) is accepted or built, which widens the qualified harnesses and changes how a pool's worker claims.
* A person needs a runtime Team with its own image, toolchains, sandbox, MCP groups or skills catalogue.
* A pool's empty pods (claims that found nothing) exceed 20 percent of its pods in a week, or one runtime Team waits more than an hour behind others in its pool.
* An installation holds more than 50 runtime Teams, or the backfill of section 6 takes more than ten minutes at boot.
* A tracker label or a link fails to resolve after a rename.
* A runtime Team is shadowed or invalid in production and no one notices for a day.

## More Information

* Owner decisions of 2026-11-18: three identifiers (`id`, `key`, `name`); static Teams keep their current names as keys and get an id on first boot; display names default to a `<role> · <harness>` hint; people create and change Teams at runtime through the operator API within the Tenant's budget, with a consumer permission, a proven person and an installation opt-in; only isolation-qualified harnesses; static wins on a key conflict.
* Amends [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) section 9 (section 13 above). Completes the last step of [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) for runtime Teams.
* Builds on [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) (credential isolation), [ADR-0099](0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md) (one workload per Harness, claim selects on it), [ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md) (sponsor), [ADR-0162](0162-a-team-spends-at-most-its-cap-per-period-and-a-claim-over-it-starts-nothing.md) (spend cap), [ADR-0169](0169-worker-workloads-render-into-a-namespace-of-their-own-and-profile-p1-refuses-a-shared-one-from-chart-0-3-0.md) (executor namespace), [ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md) (Tenants), [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md) and [ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) (whose gates, states, dry run and audit this record mirrors). Related: the proposed credential gateway outside the Run pod ([#436](https://github.com/ploeg-hq/ploeg/pull/436)).
* 2026-11-18 — the owner answered the open questions. A released alias, and a removed runtime Team's key, stays reserved for 90 days (section 4). A drain lasts at most 14 days, after which open Work Items move to `needs_human` with `team_drain_expired` and the Team is removed (section 5). The configuration file is the only source of a static Team's name: every read says so with `nameSource` and the file key, and a `PATCH` of a static Team's name is refused with `409 name_set_by_configuration` (sections 1 and 3). MCP groups and skills catalogues for runtime Teams stay open while the owner decides, as a follow-up record (section 14).
* 2026-11-18 — the owner accepted two choices of the first implementation (#440). In operator API schema version 1.0 a Team's existing `id` stays its key and the generated id is served as `teamId`; `id` moves to the generated id only in a next schema version. The `team` label of Team metrics keeps the key a row was written with, beside `team_id`, instead of the current key, so a rename never yields two series with the same labels (section 10).
* 2026-11-18 — Section 14's open question is answered by the proposed [ADR-0199](0199-mcp-servers-and-skill-sources-are-registered-per-tenant-attached-to-a-teams-roles-by-id-and-reached-only-through-the-credential-gateway.md): a runtime Team gets MCP servers and skills through an attachment by registry id, never through `tools` or `skills` fields in its manifest, which stay `422 field_not_allowed`.
* 2026-11-26 — Accepted by the owner, built: the three identifiers, the boot reconciler and the `team_id` backfill (#440, #449); runtime Teams with section 8's gates, the dry run, states, rename and aliases with their reservation, removal and the bounded drain, on a generic worker pool per Tenant and Harness (#448, #456); a runtime Team's work run end to end through Shifts and its pool (#452); and section 3's ploeg-mcp tools `ploeg_list_teams`, `ploeg_create_team`, `ploeg_change_team` and `ploeg_remove_team` (#482). Their confirmation shows the dry run with the spend headroom. The operator API's dry run of a replace does not compare `If-Match`, so `ploeg_change_team` refuses a stale revision itself before it asks, and ploegd checks it again when it acts.
