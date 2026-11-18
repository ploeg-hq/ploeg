---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-11-18
verified_by: "Read pkg/worktarget, pkg/httpapi/operator_targets.go and cmd/ploegd/runtime_targets.go; go test ./pkg/httpapi/ -run 'RuntimeTarget|GrantRemoval|Reconciler|Replica'"
---

# Register Work Targets at runtime

Use this when an operator consumer should make a repository a Work Target without a configuration change and a rollout. A runtime target needs two acts: a person grants the repository to a store-backed forge connection at the forge (a GitHub App installation's selected repositories, or what a Forgejo or GitLab OAuth account may write to), and a proven person registers it in Ploeg ([ADR-0193](../adrs/0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md)). The configuration file stays the authority: a runtime target never overrides a static one.

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

A key is lowercase and matches `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`; omitted, Ploeg derives `<forge>:<owner>:<name>`. It may never equal a static key, whatever its spelling (`409 key_taken`), and a repository a static target registers is `409 conflicts_with_static`. Change `branch` or `label` with `PUT /api/v1/operator/targets/{key}` and `If-Match: "<revision>"`; remove with `DELETE`, refused with `409 target_in_use` while a tracker binding names the key. The full contract is in [the operator API schema](../contracts/operator-api.v1.schema.json).

## States

| State | Meaning |
| --- | --- |
| `active` | In the registry; the readiness gate decides per Work Item |
| `shadowed` | A static entry added later covers it; it returns to `active` when the entry goes |
| `invalid` | Ploeg cannot use it: `grant_revoked`, `installation_suspended`, `connection_unusable`, `repository_gone` or `repository_moved`. A rename is never followed |

A GitHub `installation` or `installation_repositories` delivery, verified with the App's own secret from the store, marks targets `invalid` at once and restores them only after a passing probe (`work_target.restored`). An hourly pass probes every target through its connection. Each replica rebuilds within 30 seconds, and the claim reads the target's row before anything is minted, so a removed or invalid target never gets a Run's credential. A Run on a runtime target is minted a token through the target's own connection; today that is a GitHub App installation token pinned to the installation, and a Forgejo or GitLab OAuth connection mints none yet.

## Check it

- `ploeg_work_targets{state}` counts live runtime targets; alert on `invalid`.
- The audit log holds `work_target.*` events with the proven person, or the connection for what Ploeg found, as actor.
- `GET /api/v1/operator/targets` lists static and runtime targets with `source`, `state` and readiness.
