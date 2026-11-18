---
status: proposed
date: 2026-11-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-03-31
---

# A proven operator registers a Work Target at runtime only for a repository Ploeg's forge connection was granted, and static targets win on conflict

## Context and Problem Statement

A Work Target exists only when an operator writes it into the `targets` map of ploegd's configuration file and redeploys. `config.File` loads the map once at boot, `target.Gate` inspects each entry's readiness then ([`pkg/target/readiness.go`](../../pkg/target/readiness.go)), and every route, label (`repo/<key>`, [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)) and operator Admission ([ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)) selects among those keys. [ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md) recorded that "configuration stays a file read at boot" and named runtime registration of boards or targets as its re-evaluation trigger.

Three proposed records have since removed every other reason a repository needs a redeploy:

* [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) lets a person connect a forge through the vendor's consent flow (an OAuth code exchange Ploeg performs, or a GitHub App installation) and keeps the credential in a store Ploeg writes.
* [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) lets an operator consumer bind a tracker Scope to a Team at runtime. Its *Out of scope* says that adding Work Targets at runtime "changes R11 and needs its own record". This is that record.
* [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md) resolves a Run's forge from its Work Target and has the claim carry that forge and a credential minted for that Run. Once it is built, a target that names its forge is enough for one release to serve repositories on several forges.

Rule R11 says the set of reachable Work Targets is "closed and operator-declared", so that tracker content, which is untrusted, can never point a write-scoped forge credential at an arbitrary repository. An operator consumer that wants to offer "connect this repository" still has to ask a person to edit Helm values and wait for a rollout.

Under what conditions may a repository become a registered Work Target while ploegd runs, so that R11's guarantee still holds?

## Decision Drivers

* **R11's intent holds.** No runtime caller and no tracker text can point a write credential at a repository that a person did not grant at the forge.
* **Two deliberate acts.** The grant at the forge says what Ploeg may reach; registration says what Ploeg should work on. Neither alone makes a target.
* **The reviewed configuration is the operator's word.** A runtime target never overrides a static one, and a conflict is visible, not silent (as in ADR-0186).
* **Accept only what works.** A target that the connection cannot write to, or that the readiness gate refuses, is refused at the call.
* **A lost grant is a state, not a deletion.** When a person withdraws access at the forge, Ploeg stops using the target and says so; it never forgets what was registered.
* **No secret in a request, response, row, log or event** (ADR-0184).
* **Core stays vendor-free** ([ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md)), and **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **A proven operator registers a repository that a store-backed forge connection was granted, through the operator API, after a readiness probe; static targets win on conflict**
* Keep Work Targets static: a repository is added by a pull request against the deployment and a rollout (GitOps)
* Every repository a forge connection's credential can reach is a Work Target automatically

## Decision Outcome

Chosen option: "**A proven operator registers a repository that a store-backed forge connection was granted**", because it is the only option in which a runtime target needs both a person's grant at the forge and a proven person's request in Ploeg, so R11's closed set becomes "closed by two acts, one at the forge and one in Ploeg" instead of "closed by a file", while the file stays the authority.

Proposed, not built. Each section states the contract the implementation must meet.

### 1. What may be registered

A repository may be registered at runtime only when all three hold:

1. **(a) The forge connection was granted it.** The connection is store-backed under ADR-0184: a GitHub App installation, or an OAuth connection to a Forgejo or GitLab instance. The grant a person gave at the forge (the repositories selected for the installation, or the repositories the consenting account may write to) is the operator's grant. The connection's credential must be able to read the repository, push branches and open pull requests in it; a read-only grant is refused (`422 grant_insufficient`).
2. **(b) A proven person with operator authority asks** (section 5).
3. **(c) The repository passes the readiness inspection** that `target.Gate` applies to static targets: not archived, not a mirror, and an instruction file (`AGENTS.md`) on the base branch. The one exception is the one [ADR-0194](0194-a-repository-missing-only-its-instruction-file-may-receive-one-run-that-adds-it.md) allows: a repository whose only failure is the missing instruction file is registered, reported as not ready, and accepts only a prepare-repository Work Item until it is ready. If ADR-0194 is rejected, that case is refused like any other.

### 2. What a runtime target is

| Field | Meaning | Rule |
| --- | --- | --- |
| `connection` | The store-backed forge connection that holds the grant, by id | Must exist in the Tenant, be usable, and accept registration (section 5). Never a credential. |
| `forge` | The forge instance id in the registry ([ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md)) | Taken from the connection, not from the request. ADR-0192 resolves the Run's forge from it. |
| `repository` | `owner/name` on that forge; GitLab subgroups as `group/subgroup/project` | Compared as the forge compares names. The forge's own repository id is stored beside it. |
| `branch` | The base branch | Optional. When omitted, the repository's default branch at the probe is stored explicitly, so a later change of default branch does not move the target. |
| `key` | The registry key used by Admissions, bindings and `repo/<key>` labels | Optional. A runtime key must match the operator API's name pattern, `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$` (`operatorName` in [`pkg/httpapi/operator.go`](../../pkg/httpapi/operator.go)); static `targets` keys in the configuration file are not checked against it and keep whatever `Validate` accepts today. When omitted, Ploeg derives `<forge>:<owner>:<name>`, with every `/` of the path written as `:`, and refuses with `422 probe_failed` if the result does not match. A runtime key is unique among runtime keys in the Tenant and may never equal a static key, whatever its spelling under the pattern: `409 key_taken` at the call, `shadowed` if a later static key takes it (section 4). |
| `label` | Optional display name | Free text, at most 200 characters, never used for routing. |

The **identity** of a runtime target is instance-qualified: `(tenant, forge, repository)`. One repository is registered at most once at runtime per Tenant; a second base branch on the same repository is a static concern. A row also carries `revision`, `state` (section 4), the readiness verdict of its last inspection, the forge repository id, `created_by`, `updated_by` and `removed_at`. It never carries a credential.

A runtime key is a key like any other: an ADR-0186 binding's `default` and `allow` may name it, and so may an Admission's `target`. A static route in the configuration file may not, because the file must validate at boot without the database; `Validate` keeps refusing a key it does not know.

### 3. API

New operator routes, versioned with the operator API (`docs/contracts/operator-api.v1.schema.json` and its Go types change together):

| Method and path | Effect |
| --- | --- |
| `GET /api/v1/operator/forge-connections/{connection}/repositories` | The repositories the connection's grant reaches, each with whether it is registered (statically or at runtime) and under which key. Paged. A picker for a consumer; it registers nothing. |
| `GET /api/v1/operator/targets` | Every static and runtime target the consumer may read, with `source: "static"` or `"runtime"`, `state` and readiness. |
| `POST /api/v1/operator/targets` | Probe, then register. `201` with the target, or `200` with the existing one when a live target with the same identity and the same body exists (`created: false`). |
| `POST /api/v1/operator/targets?dryRun=true` | Probe only. Returns what registration would store and every check's result; writes nothing. |
| `PUT /api/v1/operator/targets/{key}` | Replace `branch` or `label`. Requires `If-Match: "<revision>"`; a stale revision is `412`. `connection`, `repository` and `key` cannot change: remove and register instead. Re-probes. |
| `DELETE /api/v1/operator/targets/{key}` | Remove (section 6). `204`, also when it was already removed. |

Idempotency is by identity, as ADR-0186's is by natural key: the same identity with the same body twice yields one target; the same identity with another body is `409 target_exists` with the current target and revision.

Errors use the operator envelope: `403 permission_required`, `403 person_token_required`, `403 person_claim_required`, `401` with an RFC 9470 step-up challenge, `404 unknown_connection`, `422 registration_not_accepted`, `422 connection_unusable`, `422 capability_missing` (the connection's adapter lacks `RepositoryGrantProber`, section 4), `422 not_granted` (the connection's grant does not reach the repository), `422 grant_insufficient`, `422 repository_not_found`, `422 probe_failed` with each failed check, `422 target_not_ready` with structured reasons, `409 conflicts_with_static`, `409 key_taken`, `409 target_exists`, `409 target_in_use`, `429 target_limit`. Unknown request fields are refused. A request carrying a raw URL, a clone address or anything shaped like a token is refused, never ignored.

ploeg-mcp gains `ploeg_list_targets` (read-only) and two actions, `ploeg_register_target` and `ploeg_remove_target`, that need the person's confirmation like every ploeg-mcp action ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)); the confirmation text shows the dry-run result.

### 4. Validation, storage, merging and states

**Probe.** Registration and replacement run every check and report each result: the static rules (key pattern, label), conflicts (below), then two forge port calls. A new, proposed forge capability, **`RepositoryGrantProber`**, answers for one connection and repository whether the grant reaches it, with which permissions, and the forge's repository id; a `ListGrantedRepositories` call on the same capability backs the picker and the reconciler. The existing `RepositoryInspector` gives readiness. An adapter without `RepositoryGrantProber` cannot register at runtime (`422 capability_missing`); its static targets keep working. ADR-0183's forge conformance suite gains the cases: granted with write, granted read-only, not granted, unknown repository, renamed repository. Probes go through the adapter's rate-limit policy, and the create route is limited per consumer.

**Storage.** Runtime targets live in a new table, `work_targets`, added by the next migration in `pkg/store/migrations/`, holding the fields of section 2 and the Tenant ([ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md)). Partial unique indexes on `(tenant, forge, repository)` and on `(tenant, key)` where `removed_at IS NULL` enforce one live target per identity and per key. Removal sets `removed_at`; rows are never deleted. The migration extends `ploeg_grant_observer()` with the non-attribution columns.

**Merging.** The effective registry is the static `targets` map followed by the live runtime targets in state `active`. On boot and after every committed change ploegd rebuilds `target.Gate` and the resolver from both and swaps them atomically. Each replica reads the highest target revision every 30 seconds and rebuilds when it changed, as ADR-0186 does for bindings. Because a replica may be up to 30 seconds stale, **the claim does not trust the cache**: before a Run starts on a runtime target, the claim reads the row in the same transaction and refuses the Run when it is not `active`. The mint (ADR-0192) is asked only after that read, and only through the target's own connection.

**Static wins.** A runtime target conflicts with the configuration when a static target has the same key, or names the same forge and repository on any branch. At the call this is `409 conflicts_with_static`, naming the static entry. When a redeploy adds a conflicting static entry later, ploegd never fails to boot over runtime state: it marks the runtime target `shadowed` with `shadowedBy`, leaves it out of the registry, writes one `work_target.shadowed` event and exports `ploeg_work_targets{state}`. It returns to `active` by itself, with `work_target.unshadowed`, if the static entry goes away. A binding that names a shadowed key becomes ADR-0186's `invalid` and is reported, because its key no longer resolves.

**States.**

* `active`: granted, registered, in the registry. Its readiness is reported beside the state and the gate decides per Work Item, as for static targets.
* `shadowed`: a static entry covers it.
* `invalid`: Ploeg can no longer use it, with `invalidReason`: `grant_revoked` (removed from the installation or the account's access withdrawn), `installation_suspended`, `connection_unusable` (the connection was disconnected or its credential refused), `repository_gone`, or `repository_moved` (the forge repository id no longer matches the stored name, after a rename or transfer). An invalid target is out of the registry, refused at ingest and at claim, and kept with its history. It is **never silently deleted**, and Ploeg never follows a rename on its own, because a key that someone registered for one repository must not start pointing at another.

### 5. Who may call it

Four gates, all required:

1. **The installation accepts runtime targets.** The configuration file gains `targetRegistration`: `enabled` (default `false`), `connections` (the store-backed connection ids, or `["*"]`, that may grant runtime targets), `max` (live runtime targets per Tenant, default 100), and `requireClaims` with optional step-up (`acrValues`, `amr`, `maxAge`), matched as ADR-0150 matches claims and as ADR-0180's administrator override checks step-up. Without it every route answers `422 registration_not_accepted` and nothing changes for an installation that does not opt in.
2. **The consumer holds the permission.** `PLOEG_OPERATOR_CONSUMERS` gains `registerTargets` (default `false`) and `OperatorPrincipal` gains `CanRegisterTargets`. A target belongs to no Team (R11), so this permission is not narrowed by Team scope; reading targets needs either `registerTargets` or, for consumers without it, returns only the targets their Teams' routes and bindings reach, as `/connections` does ([ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)).
3. **A proven person makes the change** ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)). Registration, replacement and removal refuse asserted attribution **in every attribution mode**, `asserted` included: widening what a write credential may reach is not a change a header can authorise.
4. **The person holds operator authority**: their token carries the claims `targetRegistration.requireClaims` names, and the step-up when configured.

### 6. Removal

`DELETE` sets `removed_at`, rebuilds the registry and writes `work_target.removed`. It answers `409 target_in_use` while a live ADR-0186 binding names the key, listing the bindings, so routing is never left pointing at nothing. Open Work Items pinned to the target ([ADR-0014](0014-work-target-is-a-work-item-attribute.md), R12) are not withdrawn: the response lists them, and each one's next claim is refused with `target_not_registered`, which ends the Run `stuck` and moves the Work Item to `needs_human`, as an unready target does today. A pull request a Run already opened is left alone. Ploeg changes nothing at the forge: the installation or consent stays as the person gave it.

### 7. When the grant goes away

* **GitHub.** The App's webhook delivers `installation` (`deleted`, `suspend`, `unsuspend`) and `installation_repositories` (`removed`, `added`) events. A removal or suspension marks every affected target `invalid` within one delivery, writes `work_target.invalid`, and refuses their next claims. A later `added` or `unsuspend` re-runs the probe and returns a target to `active` with `work_target.restored` only if the probe passes: the person who registered it already decided, and the forge grant is the authority on reach.

  The GitHub forge receiver cannot do this today, and the implementation must change it in three places ([`pkg/provider/github/webhook.go`](../../pkg/provider/github/webhook.go)):

  1. **Payloads without `repository`.** `ParseWebhook` returns nothing for a delivery whose `repository.full_name` is empty, before it looks at the event name. `installation` and `installation_repositories` carry `installation` and the `repositories_added` / `repositories_removed` lists instead, so the receiver must dispatch on `X-GitHub-Event` first and normalise these two events into grant events (installation id, action, affected repositories by forge id and name) that the core handles through the forge port, not through `ForgeEvent`'s pull-request shape.
  2. **The signing secret.** The receiver verifies every delivery with the one static `PLOEG_GITHUB_FORGE_SECRET`. A store-backed GitHub App connection (ADR-0184) keeps its webhook secret in the store, so the receiver must pick the secret per delivery: the App is identified by the `X-GitHub-Hook-Installation-Target-ID` header, that App's secret is read from the store (a GitHub App has one webhook secret for all its installations), and the signature is verified over the raw body before anything in the payload, `installation.id` included, is used to find the connection. The static secret keeps verifying deliveries for the static App.
  3. **Deduplication and the inbox.** Grant events go through the durable inbox ([ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)) and are deduplicated on `X-GitHub-Delivery`, as pull-request events are.
* **Every forge.** An hourly reconciler calls `ListGrantedRepositories` (or `RepositoryGrantProber` per target where listing is not offered) for every connection that holds runtime targets, so a Forgejo or GitLab grant withdrawn without an event, a revoked OAuth token, a deleted or moved repository, or a lost GitHub delivery is caught within an hour. A connection that ADR-0184 disconnects marks its targets `connection_unusable` at once.
* **At mint.** A per-Run mint that the forge refuses because the repository is outside the grant marks the target `invalid` with `grant_revoked`, and the Run ends as an infrastructure failure that is not retried while the target is invalid.

No case deletes a target, a Work Item or a pull request.

### 8. Audit and events

Every change writes one `audit_log` row with `work_item_id` NULL, the proven person as `actor`, and the consumer, the channel, the identity and the key in `detail`, never a secret: `work_target.registered`, `work_target.updated`, `work_target.removed`, `work_target.refused` (with the error code and failed checks, so repeated probing of repositories a consumer cannot reach is visible), `work_target.shadowed`, `work_target.unshadowed`, `work_target.invalid`, `work_target.restored`. Grant changes the reconciler or a webhook finds are attributed to the connection, not to a person. `GET /api/v1/operator/events` returns these rows to consumers that may read the target, and `/connections` lists runtime targets beside static ones with their `source`.

### 9. Out of scope

* Making, reconnecting or removing a forge **connection**: ADR-0184.
* Registering a target through a **static** forge connection (an environment-configured App, bot token or personal access token). See *More Information*.
* Registering **tracker** Scopes: ADR-0186.
* Per-target `ci` and `fastCheck` settings; they stay static until a record moves them.
* Any user interface. A consumer builds its own on this contract.

### Consequences

* Good, because "work on this repository" becomes one authorised call with a dry run, after a consent step at the forge, instead of a configuration pull request and a rollout.
* Good, because R11's guarantee survives in a stronger form: a write credential reaches only a repository that a person granted at the forge *and* a proven person registered in Ploeg, and the forge itself refuses a mint outside the grant.
* Good, because a withdrawn grant stops work within one webhook delivery or one hour and is reported, instead of surfacing as a failed push.
* Good, because with ADR-0192 a runtime target names its connection and forge, so one installation can register targets on several forges once 0192 is built.
* Bad, because the Work Target set now has two sources. The configuration file no longer lists every target; `GET /targets` and `/connections` become the place to look, and the documentation must say so.
* Bad, because the claim gains a database read of the target row, and every forge adapter must implement `RepositoryGrantProber` before its repositories can be registered at runtime.
* Bad, because a GitHub installation set to "all repositories" turns every repository of the account into a candidate. Registration is still a separate proven act, but the picker can be long; the documented installation selects repositories.
* Neutral, because `registerTargets` together with operator authority is a real grant: whoever holds both can make any granted repository a target for every Team that may route to it. It is bounded by the forge grant, the opt-in, the claims, the limit and the audit trail, and operators should give it sparingly.

### Confirmation

Proposed, none of these exist yet. Once built, `mise run verify` runs them through `go test ./...`:

* `pkg/httpapi` tests with `httptest` fakes of a GitHub App and a Forgejo OAuth connection: register (`201`), repeat (`200`, one row), same identity with another body (`409 target_exists`), stale `If-Match` (`412`), remove twice (`204` twice), a repository outside the grant (`422 not_granted`), a read-only grant (`422 grant_insufficient`), an archived repository and a mirror (`422 target_not_ready`), a raw URL or an unknown field refused, a consumer without `registerTargets` refused, an installation without `targetRegistration` refused, a connection whose adapter lacks `RepositoryGrantProber` refused with `422 capability_missing`, a key outside the name pattern refused, a key equal to a static key `409 key_taken`, and **asserted attribution refused under both `asserted` and `proven` modes**.
* A routing test: a registration that conflicts with a static key or repository is `409 conflicts_with_static`; a static entry added afterwards shadows the runtime target, removes it from the resolver, writes one `work_target.shadowed` event and restores it when the entry goes away; a binding naming the shadowed key turns `invalid`.
* A grant test: an `installation_repositories` `removed` delivery marks the target `invalid` with `grant_revoked`, and the next claim on a pinned Work Item ends the Run `stuck` without minting; an `added` delivery restores it only after a passing probe; the reconciler catches a Forgejo grant withdrawn with no event; a renamed repository is `repository_moved`, never followed.
* `pkg/provider/github` receiver tests: a signed `installation` (`deleted`, `suspend`, `unsuspend`) and `installation_repositories` (`added`, `removed`) delivery, which has no `repository` field, is normalised into a grant event instead of being dropped; a delivery for a store-backed App connection verifies with that connection's stored secret and is refused with the static secret, and the reverse for the static App; a delivery whose signature does not match the App its header names is refused before its payload is read further; a replayed delivery id is handled once.
* A two-replica test in `pkg/store`: a target removed through one replica is refused at claim by the other before its 30-second rebuild.
* A mint test: `forgebroker` is never asked to mint for a runtime target that is not `active`, and asks only the target's own connection.
* `pkg/provider/forgetest` gains the `RepositoryGrantProber` cases, and every adapter that declares the capability passes them.
* ADR-0184's redaction test extends to these routes.
* `go test ./internal/ledger/` keeps this record well-formed, and `go test ./internal/boundary/` keeps it free of consumer names.

## Pros and Cons of the Options

### Keep Work Targets static (GitOps)

* Good, because the configuration file stays the one reviewed list of targets, and R11 needs no new reading.
* Good, because nothing new has to be built, secured or reconciled.
* Bad, because every new repository needs write access to the deployment's repository, a review and a rollout, which a consumer offering self-serve onboarding cannot ask of its users.
* Bad, because nothing probes the grant or readiness before the change merges; a missing installation or instruction file is found after the rollout.

### Every repository a connection's credential can reach is a Work Target automatically

* Good, because it needs no registration step at all.
* Bad, because a grant at the forge is coarse: "all repositories" on an installation, or every repository a consenting account can write to, would make each one a target, including ones nobody meant Ploeg to work on and ones created later.
* Bad, because R11 would then rest on one act a person may have taken for another reason (a tracker integration, a read-only review), and tracker text could pick any of those repositories with a `repo/` label.
* Bad, because there is no point at which readiness is checked or a key is chosen, so labels and Admissions would name repositories by path, which ADR-0038 refused.

## Re-evaluation triggers

* ADR-0184 is accepted or rejected, which decides whether store-backed forge connections exist.
* ADR-0192 is built, or rejected, which decides whether runtime targets on several forges can share one release.
* An installation asks to register targets through a static forge connection.
* A target stays `invalid` for more than a day without anyone noticing, or the 30-second rebuild lets a stale replica route work to a removed target in production.
* An installation passes 100 runtime targets, or the hourly reconciler hits a forge's rate limit.
* GitHub, Forgejo or GitLab changes how an installation or OAuth grant reports or limits repository access.

## More Information

* Builds on [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) (store-backed connections), [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) (whose merge rules, states, revisions, dry run and audit this record mirrors for targets) and [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md) (the Run's forge and per-Run credential come from the target, so a runtime target's connection decides both).
* Reads rule R11 ([rules](../domain/rules.md#r11)) as: the set of reachable Work Targets is closed and declared by the operator, either in the configuration file or by a proven person with operator authority within what a person granted at the forge. If this record is accepted, R11's statement in `docs/domain/model.yaml` is amended in the same change that builds it.
* Related: [ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0014](0014-work-target-is-a-work-item-attribute.md), [ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md), [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md), [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md), [ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md) (GitHub App per-Run tokens), [ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md) (this record answers its runtime-registration trigger), [ADR-0172](0172-the-forgejo-broker-runs-only-on-forgejo-15-and-refuses-a-token-the-forge-did-not-restrict-to-the-runs-repository.md), [ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md), [ADR-0194](0194-a-repository-missing-only-its-instruction-file-may-receive-one-run-that-adds-it.md).
* Open questions for the owner:
  * Whether a static forge connection (an environment-configured GitHub App in particular, whose installations are the same kind of grant) may opt in to runtime registration. Recommendation: not in this record; allow it later for a GitHub App only, behind the same `targetRegistration.connections` list, because a static bot token or personal access token reaches whatever its account reaches and carries no per-repository grant a person chose.
  * Whether a target restored after a grant comes back (`added`, `unsuspend`) should return to `active` by itself or wait for a person. Recommendation: by itself after a passing probe, as a shadowed binding returns under ADR-0186, with the `work_target.restored` event so a consumer can tell the person.
* 2026-11-17 — Amended after an independent review, before anything was built: section 7 names the GitHub forge receiver changes that handling `installation` and `installation_repositories` needs (dispatch before the `repository` check, a per-connection secret from the ADR-0184 store, the inbox), with receiver tests; section 3 lists `422 capability_missing`, which section 4 already used; section 2 says a runtime key follows the operator API's name pattern, which static `targets` keys are not checked against, and may never equal a static key.
* 2026-11-18 — Implementation proposed in a pull request, still under review: `pkg/worktarget`, `pkg/httpapi/operator_targets.go`, migration `0088_work_targets.sql`. It differs from this record in four places, each to be settled in review: the claim reads the target row in its own statement after the claim transaction and before any mint, not inside that transaction; a runtime key must also be lowercase; the hourly reconciler probes each target through `RepositoryGrantProber` rather than listing grants; and only a GitHub App connection mints a per-Run credential so far. The ploeg-mcp tools of section 3 are not built.
