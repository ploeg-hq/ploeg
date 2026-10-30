---
status: proposed
date: 2026-10-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# Work Items, Shifts and Runs take their Tenant from configuration, and an operator consumer reads only its own

## Context and Problem Statement

The domain model already names a Tenant (the access boundary above Teams) and a Client (a customer inside a Tenant that owns some of its tracker sources and repositories), and [ADR-0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md) derives a Run's knowledge scope from them. Ploeg's store records neither: every Work Item is in one implicit Tenant without a Client (`KnowledgeScopeFor`, VIK-1876). An Operator Consumer is scoped by Team only ([ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)), so a deployment that serves several organisations from one ploegd can keep them apart only in each consumer's own code. A consumer that serves several Tenants filters Ploeg's answers itself today; one missed filter there shows one Tenant another's Work Items, Runs and spend. Ploeg admits and records the work, so it must enforce the boundary itself.

How does each operator record get a Tenant that nobody can choose by request, and how does Ploeg make sure an Operator Consumer reads only the records of its own Tenant?

## Decision Drivers

* A record's Tenant comes from Ploeg's own configuration of its source or target, never from a request field or model output (the rule ADR-0087 applies to knowledge scopes).
* A missing line of configuration narrows access, never widens it (ADR-0160).
* A read outside the consumer's Tenant reveals nothing: not the record, not that it exists.
* A deployment with one Tenant, which is every deployment today, changes no behaviour and no configuration.
* The boundary holds when a new operator route is added, not only on the routes that exist now.
* Every change is an addition to the operator contract ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)).

## Considered Options

* **A `tenant_id` and `client_id` on every Work Item, Shift and Run, stamped from configuration; each consumer bound to one Tenant; every operator query filtered by it**
* Model a Tenant as a set of Teams and keep scoping by Team
* PostgreSQL row-level security on the operator read path
* One ploegd and database per Tenant

## Decision Outcome

Chosen option: "**a `tenant_id` and `client_id` on every record, stamped from configuration, with each consumer bound to one Tenant**", because it puts the boundary where the work is admitted, keeps one ploegd for every Tenant, and costs a single-Tenant deployment nothing.

### 1. Configuration: Tenants, Clients and bindings

* `PLOEG_CONFIG` gains `tenants:`, a map from Tenant id to `{clients: [<id>, ...]}`. Ids match the operator name pattern `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`. The Tenant `default` always exists and need not be declared.
* A target (`targets.<key>`) and a tracker project (`trackers.<kind>.projects[]`) may carry `tenant:` and `client:`. `client:` needs `tenant:` and must be one of that Tenant's `clients`. Unbound, a source or target belongs to `default` without a Client.
* Each binding has a **key**: the instance plus the container id.

  | Binding | Instance | Container id |
  | --- | --- | --- |
  | Vikunja project | `vikunja` + `PLOEG_VIKUNJA_URL` | project id, after name resolution |
  | ClickUp project | `clickup` + `PLOEG_CLICKUP_URL` | the resolved container id |
  | GitHub tracker project | `github` + `PLOEG_GITHUB_URL` | `owner/repo` |
  | Forgejo target | `forgejo` + `PLOEG_FORGEJO_URL` | `owner/repo` |
  | GitHub target | `github` + `PLOEG_GITHUB_API_URL` | `owner/repo` |
  | GitLab target | `gitlab` + `PLOEG_GITLAB_URL` | project path |

  The instance URL is compared with its scheme and host lowercased and without a trailing slash. Two forges may both hold `acme/web`; the instance keeps them apart.
* **Start refusals.** ploegd refuses to start, naming the key and both bindings, when one key is bound to two Tenants or to two Clients; when a binding names an undeclared Tenant or Client; and when a legacy `PLOEG_TARGET_MAP` or `PLOEG_TEAM_MAP` rule, which can only mean `default`, routes to a key bound to another Tenant.
* **Consumers.** Each entry of `PLOEG_OPERATOR_CONSUMERS` gains `tenant` (one id) and an optional `clients` list. Unset, `tenant` is `default`. Once `tenants:` declares any Tenant other than `default`, a consumer without `tenant` refuses start: in a deployment with several Tenants no consumer is bound by omission. `clients` narrows the consumer to those Clients' records; without it the consumer reads its whole Tenant. Team scope (ADR-0160) still applies on top: a consumer reads a record only when both its Team and its Tenant are in scope.

### 2. Where a record's Tenant comes from

* **Tracker work.** From the source's binding; if the source is unbound, from the resolved target's binding; otherwise `default`. Client likewise. When both are bound and disagree on the Tenant, or both name a Client and the Clients differ, routing refuses the task with the new route-refusal code `tenant_mismatch` and creates no Work Item.
* **Admitted work** ([ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)). From the target's binding. A target outside the consumer's Tenant or Clients answers 422 `unknown_target`, the same answer as a key that does not exist.
* **Created and proposed Work Items** ([ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md)). From the Work Item that created them. A created Work Item whose target is bound to another Tenant is refused like any other created-work limit, and nothing is stored.
* **Forge follow-ups and pull request reads.** From the target's binding.
* **Shifts and Runs.** Copied from their Work Item when they are inserted. A chat Run, which belongs to no Work Item ([ADR-0128](0128-a-chat-message-is-a-read-only-run-about-no-work-item-paid-from-a-teams-chat-allowance.md)), carries the admitting consumer's Tenant and no Client.

No route accepts a Tenant or Client in its body. A record's Tenant changes only through the assignment in section 4.

### 3. Storage

* Migration `0082_tenant_on_operator_records.sql` adds `tenant_id TEXT NOT NULL DEFAULT 'default'` and `client_id TEXT NOT NULL DEFAULT ''` to `work_items`, `shifts`, `agent_runs` and `audit_log`. A constant default backfills every existing row to `default` without rewriting the table (PostgreSQL 11 and later).
* `work_items` gains `UNIQUE (id, tenant_id, client_id)`, and `shifts` and `agent_runs` reference it with a composite foreign key `(work_item_id, tenant_id, client_id)` with `ON UPDATE CASCADE`. PostgreSQL then refuses a Shift or Run in another Tenant than its Work Item, and an assignment of a Work Item carries its Shifts and Runs with it. A chat Run's `work_item_id` is null, so the key does not apply to it.
* Indexes `(tenant_id, team, id)` on `work_items` and `agent_runs` and `(tenant_id, id)` on `audit_log` serve the scoped lists.
* The migration extends `ploeg_grant_observer()` to grant the observer role the new columns: they are metadata, not people's text or credentials.
* An audit row written about a Work Item, Shift or Run carries that record's Tenant and Client. A route refusal carries its source's.

### 4. Assigning existing records

At start, after the bindings are checked, ploegd moves each record that is still in `default` and whose source or target key is now bound to another Tenant into that Tenant and Client, in one transaction per key, and writes one `tenant.assigned` audit row per key with the key, the Tenant, the Client and the number of Work Items moved. This is how a single-Tenant deployment adopts Tenants: records made before the binding follow it once. ploegd never moves a record out of a Tenant other than `default`. A key whose stored records sit in a Tenant other than `default` and other than the one it is now bound to refuses start, naming the key and both Tenants, so editing a binding never silently hands one Tenant's history to another.

### 5. The operator API

* **Headers.** Every operator request may carry, each at most once, `X-Ploeg-Acting-Tenant` and `X-Ploeg-Acting-Client`, matching the id pattern. A repeated or malformed one answers 400 `invalid_acting_tenant` or `invalid_acting_client`. `X-Ploeg-Acting-Tenant` must equal the consumer's Tenant, and `X-Ploeg-Acting-Client` must be one of its `clients`, or of its Tenant's Clients when it lists none. Otherwise ploegd answers **403 `tenant_forbidden`** or **403 `client_forbidden`** before any route runs, reads and changes nothing, and writes `operator.tenant_refused` with actor `operator:<consumer>` and `detail` holding `consumer`, `reason`, `method`, `path` and the refused value. Every value other than the consumer's own binding gets the same answer, whether that Tenant exists or not, so the refusal is no oracle for other Tenants. The check runs in the operator middleware beside attribution ([ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)), so a route added later is covered.
* **Scope.** The consumer's Tenant, its `clients` and the acting Client when sent form one scope with its Teams. Every store query the operator routes call takes that scope and filters by it. A list leaves out every record outside it; a single read, a change or a decision on a record outside it answers **404 `not_found`**, the answer ADR-0160 gives a Team outside scope. This covers Work Items (proposed ones included) and their detail, facts, context and notes; Runs and their accounting, transcript and briefing; events; route refusals; summary; unsettled accounts and settlement; connections, which leave out boards and repositories bound elsewhere; knowledge reviews; Asks and chats.
* **Response fields.** The Work Item, Shift and Run shapes, each item of the `runs` list (the Run's Work Item's values, or the chat Run's own) and each event gain `tenant` (string) and `client` (string or null). They are additions; a single-Tenant deployment answers `"tenant": "default", "client": null`.
* **Summary and unsettled accounts by target.** `GET /api/v1/operator/summary` and `GET /api/v1/operator/unsettled-accounts` take `target`, repeatable up to 20 times: `forge:owner/repo`, `forge:owner/*` for every repository of one owner, or `none` for Runs whose Work Item has no target and for chat Runs. The values are joined with OR and then intersected with the consumer's scope; an unknown forge or a malformed value answers 400 `invalid_request`. Both responses gain `targets`: the filters applied, in the request's order, or `[]` when none. A Ploeg older than this record answers a `target` parameter with 400 `invalid_request`, because both routes refuse unknown parameters; the `targets` echo lets a consumer prove from the answer itself that the filter was applied, also behind a proxy that drops query parameters.
* **Knowledge.** The graphs configured today (`PLOEG_NOTES_GRAPH`, `PLOEG_KNOWLEDGE_GRAPH`) belong to `default`. A Run of another Tenant is briefed with knowledge `off` until a later record maps graphs per Tenant; it never reads another Tenant's graph. Its Client now comes from `work_items.client_id`.

### 6. Support

* Every forge and tracker in the table above gets bindings in the same change; nothing about a binding depends on the forge or tracker. The legacy environment routing serves `default` only.
* The [support matrix](../reference/support-matrix.md) gains no cell from this record. A cell for several Tenants on one ploegd is added when a CI job or a dated live record exercises two bound Tenants, with the limit that every live record so far comes from a single-Tenant deployment.

### 7. Rollout

The change ships in one minor of the 0.2 line, with no flag. A deployment without `tenants:` stamps every record `default`, binds every consumer to `default`, accepts `X-Ploeg-Acting-Tenant: default` and refuses any other value; every answer it gave before it still gives, with two more fields. A deployment adopts Tenants by declaring them, binding sources and targets and giving each consumer a `tenant`; ploegd's start refusals catch a duplicate key and an unbound consumer, and section 4 moves the existing records. The chart passes `tenants:` through `config` and the consumer list unchanged, so it needs no new values.

### Consequences

* Good, because a consumer that forgets a filter, or a stolen consumer token, reads at most one Tenant: Ploeg enforces the boundary itself, and the consumer's own check stays as a second line.
* Good, because PostgreSQL refuses a Shift or Run in another Tenant than its Work Item, so a store bug cannot split a Work Item across Tenants.
* Good, because a single-Tenant deployment changes nothing, and adopting Tenants is configuration plus one audited assignment.
* Good, because ADR-0087's knowledge scope reads a recorded Client instead of an assumed one.
* Bad, because Teams stay deployment-wide. A Team shared by two Tenants shares its queue, its `maxRunning`, its spend cap and its Ask and chat Allowances: one Tenant's work can wait behind another's, and the allowance and cap figures a consumer reads count both Tenants. A deployment that needs these apart gives each Tenant its own Teams; a trigger below covers per-Tenant Teams.
* Bad, because the `teams` route still lists every Team in the consumer's Team scope, so a shared Team's name and plan are visible to every Tenant using it.
* Bad, because a key cannot move between two non-default Tenants without a decision that does not exist yet. Accepted: silently moving history between Tenants is the failure this record prevents.
* Bad, because every store query on the operator path gains a predicate, which a new query can forget. The route test in Confirmation fails such a route; row-level security, the stronger guard, is left to a trigger.

### Confirmation

The implementing change adds these tests, each run by `go test ./...` in `mise run verify`:

* `go test ./pkg/httpapi/ -run TestEveryOperatorReadAnswersOnlyTheConsumersTenant` enumerates every `GET` route the operator mux registers, seeds two Tenants with a Work Item, proposed Work Item, Shift, Run, chat Run, event, route refusal, unsettled account, note and knowledge review each, and calls each route as a consumer bound to the first Tenant: no list carries a record of the second, and every single read of one answers 404 `not_found`. A route the test does not know how to call fails it, so a new route is covered before it merges.
* `TestAChangeOnAnotherTenantsRecordAnswersNotFound` does the same for cancel, requeue, notes, approve, reject, question answers, claim, Asks and settlement, and checks that nothing changed.
* `TestAnActingTenantOutsideTheBindingIsRefused` sends an unknown Tenant, another existing Tenant, a Client outside `clients`, a repeated header and a malformed value, and checks 403 `tenant_forbidden`, 403 `client_forbidden` or 400, that the route never ran, and that `operator.tenant_refused` was stored, against PostgreSQL.
* `TestTheActingClientNarrowsReadsToThatClient` checks lists and single reads under `X-Ploeg-Acting-Client` and under a consumer `clients` list.
* `go test ./pkg/config/ -run TestOneKeyBoundToTwoTenantsRefusesStart` covers two targets and two tracker projects with the same instance and container id under different Tenants or Clients, an undeclared Tenant or Client, a legacy route to a bound key, and the same container id on two instances, which is allowed.
* `go test ./cmd/ploegd/ -run 'TestAConsumerWithoutTenantRefusesStartOnceTenantsAreDeclared|TestASingleTenantDeploymentStartsUnchanged'`.
* `go test ./pkg/store/ -run 'TestTenantMigrationBackfillsDefault|TestARunCannotLeaveItsWorkItemsTenant|TestAssignmentMovesDefaultRecordsOnceAndAudits|TestAKeyRecordedInAnotherTenantRefusesStart'` against PostgreSQL.
* `go test ./pkg/httpapi/ -run 'TestTenantMismatchRefusesRouting|TestAdmissionToAnotherTenantsTargetIsUnknown|TestSummaryAndUnsettledFilterByTargetAndEchoIt|TestRunListCarriesTheWorkItemsTenant|TestAnotherTenantsRunIsBriefedWithoutKnowledge'`.
* The operator contract test pins the new fields, headers, codes and the `target` parameter in `docs/contracts/operator-api.v1.schema.json` to their Go types, and `go test ./internal/compat/` finds no break, so `breaking-changes.yaml` gains no entry.

## Pros and Cons of the Options

### Model a Tenant as a set of Teams and keep scoping by Team

* Good, because it needs no new column: ADR-0160's Team scope already filters every read.
* Bad, because a Team is how work is run, not whose it is: two organisations sharing a Team would share every record, and a Client inside a Tenant has no Team to map to.
* Bad, because a Work Item's Team can come from a tracker label ([ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md)), which a person on the tracker sets. A Tenant must come from configuration only.

### PostgreSQL row-level security on the operator read path

* Good, because PostgreSQL would refuse a forgotten predicate instead of a test catching it.
* Bad, because the sweeps, dispatch and settlement read across Tenants on the same pool, so every operator read would need its own transaction with `SET LOCAL` and a second database role, and a missed `SET LOCAL` fails open on a pooled connection that kept the last value. Recorded as a trigger, not rejected.

### One ploegd and database per Tenant

* Good, because the isolation is total and needs no code.
* Bad, because each Tenant then needs its own workers, gateway keys, chart release and upgrades, which a shared ploegd exists to avoid, and a Client inside a Tenant still needs this record.

## Re-evaluation triggers

* A cross-Tenant read is found that `TestEveryOperatorReadAnswersOnlyTheConsumersTenant` did not catch: adopt row-level security on the operator read path.
* A deployment needs one consumer to serve several Tenants: decide whether `tenant` becomes a list and `X-Ploeg-Acting-Tenant` chooses among them.
* A deployment shares a Team between Tenants and needs separate queues, caps or allowances per Tenant: decide per-Tenant Teams or per-Tenant limits.
* A deployment needs to move a key from one non-default Tenant to another.
* A second Tenant needs its own knowledge graphs: decide the per-Tenant graph mapping ADR-0087 leaves open.
* A deployment runs two or more bound Tenants for a month: add the support matrix cell.

## More Information

* Fills the Tenant and Client the [domain model](../domain/model.yaml) marks "proposed; Ploeg's store does not record it yet", and the record ADR-0087's "Scopes from records" assumes (VIK-1876).
* Extends ADR-0160's Team scope with a Tenant scope, and keeps ADR-0102's admission, ADR-0171's attribution and [ADR-0069](0069-ploeg-names-none-of-its-consumers.md)'s rule that Ploeg names none of its consumers. The need came from an operator consumer that serves several Tenants and today enforces the boundary only on its own side.
* The bulk export per Tenant is a separate decision, [ADR-0179](0179-a-consumer-exports-its-tenant-as-one-consistent-jsonl-archive-with-a-checksummed-manifest.md), because it can be accepted or rejected without reopening this boundary.
* Compatibility ([policy](../ops/compatibility.md)): every surface change is an addition (response fields, two optional headers, error and route-refusal codes, the `target` parameter, the `tenants:` key and the consumer fields). The start refusals apply only to configuration that names Tenants, which no existing deployment does.
