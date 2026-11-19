---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-11-19
verified_by: "Read pkg/worktarget, pkg/httpapi/operator_targets.go, cmd/ploegd/runtime_targets.go and cmd/ploegd/runtime_forges.go; go test ./pkg/httpapi/ -run 'RuntimeTarget|GrantRemoval|Reconciler|Replica|GitLab'; go test ./cmd/ploegd/ -run 'RuntimeOnly|OnlyARuntimeGitLab'; scripts/helm-golden.sh check"
---

# Register Work Targets at runtime

Use this when an operator consumer should make a repository a Work Target without a configuration change and a rollout. A runtime target needs two acts: a person grants the repository to a store-backed forge connection at the forge (a GitHub App installation's selected repositories, the projects of the group a GitLab service account is a Maintainer of, or what a Forgejo or GitLab OAuth account may write to), and a proven person registers it in Ploeg ([ADR-0193](../adrs/0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md)). The configuration file stays the authority: a runtime target never overrides a static one.

## Before you start

- [Let operators connect trackers and forges](let-operators-connect-trackers-and-forges.md): only a store-backed forge connection can grant a runtime target. A static forge connection from the environment cannot.
- Person tokens must be configured: every registration, change and removal needs a proven person in every attribution mode, `asserted` included.

## 1. Opt in

Add a `targetRegistration` section to the configuration file:

```yaml
targetRegistration:
  enabled: true
  connections: ["gh-acme"]   # store-backed forge connection ids, or ["*"]
  max: 100                   # live runtime targets per Tenant
  requireClaims:
    groups: [ploeg-operators]
  stepUp:
    acrValues: [mfa]
    maxAge: 900
```

Without it every change answers `422 registration_not_accepted`.

## 2. Grant the consumer

In `PLOEG_OPERATOR_CONSUMERS` (chart `operator.consumers[].registerTargets`), give the consumer `"registerTargets": true`. It is not narrowed by `teams`: a target belongs to no Team. Give it sparingly; with operator authority it can make any granted repository a target.

## 3. What the consumer does

1. `GET /api/v1/operator/forge-connections/{id}/repositories` lists what the grant reaches, with the key each repository is registered under, statically or at runtime.
2. `POST /api/v1/operator/targets?dryRun=true` with `connection`, `repository` (`owner/name`, or `group/subgroup/project` on GitLab) and optionally `branch`, `key` and `label` runs every check and writes nothing: the grant, write access, the static targets, the key and readiness, with `prepareAvailable` when only the instruction file is missing ([ADR-0194](../adrs/0194-a-repository-missing-only-its-instruction-file-may-receive-one-run-that-adds-it.md)).
3. `POST /api/v1/operator/targets` registers it: `201`, or `200` with `created: false` for the same target again.

A key is lowercase and matches `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`; omitted, Ploeg derives `<forge>:<owner>:<name>`. It may never equal a static key, whatever its spelling (`409 key_taken`), and a repository a static target registers is `409 conflicts_with_static`. Change `branch` or `label` of an active target with `PUT /api/v1/operator/targets/{key}` and `If-Match: "<revision>"`, which probes the grant and the repository id again (`409 target_not_active`, `422 repository_moved`); remove with `DELETE`, refused with `409 target_in_use` while a tracker binding names the key. The full contract is in [the operator API schema](../contracts/operator-api.v1.schema.json).

## States

| State | Meaning |
| --- | --- |
| `active` | In the registry; the readiness gate decides per Work Item |
| `shadowed` | A static entry added later covers it. It keeps any invalid reason it had, and when the entry goes it returns to `invalid` with that reason, or to `active` when it had none |
| `invalid` | Ploeg cannot use it: `grant_revoked`, `installation_suspended`, `connection_unusable`, `repository_gone` or `repository_moved`. A rename is never followed |

A GitHub `installation` or `installation_repositories` delivery, verified with the App's own secret from the store, marks targets `invalid` at once and restores them only after a passing probe (`work_target.restored`). An hourly pass probes every target through its connection. Each replica rebuilds within 30 seconds, and the claim reads the target's row before anything is minted, so a removed or invalid target never gets a Run's credential. A Run on a runtime target is minted a token through the target's own connection, by the repository id stored at registration, so it can never land on a same-named repository of another account. A GitHub App connection mints an installation token. A GitLab `gitlab_service_account` connection, or a GitLab `token` connection whose probe found `api` scope and Maintainer reach (`canMint: true`), mints a project access token on the target's project by its id ([ADR-0203](../adrs/0203-a-gitlab-forge-connection-is-a-group-service-account-that-mints-a-project-token-per-run.md)): Developer with `api` and `write_repository` for a writing Run, Reporter with `read_api` and `read_repository` for a reading one, named `ploeg-run-<first 12 characters of the Run token>`, expiring at 00:00 UTC two days after the mint and revoked when the Run settles. The connection must be a Maintainer of the project, the least role that may create a project access token, so a Developer grant answers `422 grant_insufficient`. A GitLab OAuth connection acts as a person and a Forgejo OAuth connection mints nothing yet; both are refused with `422 capability_missing`.

Registering a GitLab target also registers a project webhook at `PLOEG_PUBLIC_WEBHOOK_BASE/webhooks/forge/gitlab?connection=<id>` for merge request, note, pipeline and push events, before the target is stored; removing the target deletes it. On GitLab 19 or later the hook carries the connection's own signing token and every delivery an HMAC in `webhook-signature`, checked within five minutes of its `webhook-timestamp`; on an older GitLab the same secret is the hook's `X-Gitlab-Token`. Without `PLOEG_PUBLIC_WEBHOOK_BASE` a GitLab registration answers `422 webhook_base_missing`, and a hook GitLab refuses answers `422 webhook_failed`. A hook that could not be deleted is named in the removal's audit row as `webhookLeft`. The deliveries' events are acted on by the static GitLab forge when the release configures one. Without one, the runtime GitLab instance is the forge itself (next section); with more than one runtime GitLab instance and no static GitLab, deliveries are verified and acknowledged only, as runtime GitHub App deliveries without a static GitHub are.

## A release without a static forge

A release whose forges are all runtime connections needs no static forge block: no `executor.<forge>.url`, no `tokenSecret` and no forge Secret seeded by hand. The chart lets the executor run when `config.targetRegistration.enabled` is true and `config.connections.allow` has an entry with `port: forge`, and fails the render when neither a forge `url` nor such an entry is set. Workers then get no `FORGE_URL`, `AGENT_BUILDER_TOKEN` or reader token, and ploegd no `PLOEG_GITLAB_*`, `PLOEG_FORGEJO_*` or `PLOEG_GITHUB_URL`. A team may not pin `targetSource: env`, because its workers have no forge of their own.

For a GitLab-only release, leave every forge block's `url` empty (the chart default) and drop what a static GitLab needed: `executor.gitlab.url`, `publicUrl`, `readTokenSecret`, `webhookSecret` and `mintPerRun`, and the Secrets `tokenSecret` and `readTokenSecret` name (`agent-builder-token`, `agent-reader-token`), which nothing renders without a `url`. Then set:

```yaml
executor:
  enabled: true
  forge: gitlab
networkPolicy:
  runtimeForgeUrls: [https://gitlab.example.com]
env:
  PLOEG_PUBLIC_WEBHOOK_BASE: https://ploeg.example.com
secretStore:
  type: kubernetes        # or openbao
  kubernetes:
    namespace: ploeg-connections
config:
  connections:
    enabled: true
    allow:
      - {port: forge, kind: gitlab, instance: gitlab-main, methods: [gitlab_service_account]}
    instances:
      gitlab-main:
        kind: gitlab
        url: https://gitlab.example.com
  targetRegistration:
    enabled: true
    connections: ["*"]
```

Each claim then carries the target's forge, resolved from its connection's instance (dialect `gitlab`, the instance's `url`), and a project access token minted through the connection, so `executor.forge` only names the dialect of a claim that carries none. ploegd acts on each project outside a Run, posting the Run's card, reading the merge request, its CI and notes, and taking the project webhook's deliveries on `/webhooks/forge/gitlab?connection=<id>`, with the credential of the connection the project's active runtime target was granted through. A project no active runtime target names, or one on a GitLab OAuth connection, is refused before any request. A release with a static forge of the same dialect keeps serving that dialect's runtime targets through the static forge, as before.

The network policy keeps a runtime forge closed by default: list each instance URL in `networkPolicy.runtimeForgeUrls`, and ploegd, every worker pod, the credential gateway and claude-runner get egress to it, derived as a forge block's `url` is ([network](../ops/network.md)). GitHub and Forgejo runtime-only releases resolve the claim the same way but have no runtime forge provider yet, so their cards, merge reads and webhooks still need a static forge.

## Check it

- `ploeg_work_targets{state}` counts live runtime targets; alert on `invalid`.
- The audit log holds `work_target.*` events with the proven person, or the connection for what Ploeg found, as actor.
- `GET /api/v1/operator/targets` lists static and runtime targets with `source`, `state` and readiness.
