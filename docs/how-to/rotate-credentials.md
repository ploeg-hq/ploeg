---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-09-23
verified_by: "Read apps/ploeg ops/helm/ploeg/{values.yaml,templates/deployment.yaml,templates/_helpers.tpl,templates/_worker_control.tpl}, pkg/httpapi/worker_auth.go (NewWorkerSecurity), cmd/ploegd/{main,sweep}.go, cmd/ploeg-worker/main.go and docs/ops/{managed-workers,ci-and-infra}.md"
---

# Rotate Ploeg's credentials

**Symptom:** a credential is due for rotation, may have leaked, or belongs to someone who left.

**Goal:** replace the LiteLLM master key, the forge bot and admin tokens, the worker bootstrap tokens or the worker signing key without leaving a Run holding a dead credential, and prove the new one works. This page names where each credential lives and who reads it. It never shows a value.

Read [Before you start](index.md#before-you-start) for names and the database session.

## Where each credential lives

Every original lives in OpenBao, the estate vault. An ExternalSecret copies it into a Kubernetes Secret in the `ploeg` namespace, and the chart references that Secret by name and key ([values.yaml](../../ops/helm/ploeg/values.yaml)). Git holds only the reference. The Forgejo Actions bridge, which copies vault values into Forgejo repository secrets, serves CI jobs. No runtime credential of Ploeg passes through it. The [estate secrets model](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0055-one-secrets-model-six-levels.md) defines these levels.

| Credential | Chart value (default Secret / key) | Read by | Old and new valid at once? |
| --- | --- | --- | --- |
| LiteLLM master key | `executor.litellm.masterKeySecret` (`agent-litellm-master` / `LITELLM_MASTER_KEY`) | `ploegd`, and the LiteLLM gateway's own copy | No: one key on both sides |
| Forge bot token | `executor.forgejo.tokenSecret` (`agent-builder-token` / `FORGEJO_TOKEN`) | `ploegd` (pull request comments) and every worker (`AGENT_BUILDER_TOKEN`) | Yes: a Forgejo user can hold several tokens |
| Forge read-only token | `executor.forgejo.readTokenSecret` (unset by default) | Reading workers | Yes |
| Forge bot password | `executor.forgejo.botPasswordSecret` (unset by default) | `ploegd` only; mints and revokes a push token per writing Run, limited to that Run's repository | No: one password. Until the Secret matches the new password, writing Runs fail to mint, end `failed` with `infra_node` and are retried |
| Worker bootstrap registry | `executor.workerAuth.bootstrapSecret` (`ploeg-worker-bootstrap` / `registry.json`) | `ploegd` | Yes: several entries may share a team and Role |
| Worker bootstrap token | Same Secret, key `<team>--<role>` or `<team>--default` | The worker pods of that team and Role | Yes, through the registry |
| Worker signing key | `executor.workerAuth.signingKeySecret` (`ploeg-worker-signing` / `key`) | `ploegd` only | No: one key, no overlapping generations |
| Deploy token | `deploys.tokenSecret` (unset by default) | `ploegd` only, to authenticate `POST /api/v1/deploys`; every pipeline that sends deploys holds a copy ([how-to](send-deploys-from-a-pipeline.md)) | No: one token. Deploys sent with the old one answer 401 until each pipeline has the new one; the next deploy marks what they missed |

A worker reads its credentials once, at pod start. `ploegd` reads its credentials once, at process start. A new value therefore reaches a process only when it restarts. The worker refuses to start when it sees administrative material such as the master key, the signing key, the registry or the admin token ([main.go](../../cmd/ploeg-worker/main.go)).

Release signing is separate. CI signs artifacts through an OpenBao signing role reached with short-lived OIDC access; it has no long-lived key for Ploeg to rotate. See [CI and infrastructure](../ops/ci-and-infra.md#credentials-and-signing).

## Steps shared by every rotation

1. Find the ExternalSecret that owns the Secret:

   ```sh
   kubectl -n ploeg get externalsecrets \
     -o custom-columns=NAME:.metadata.name,TARGET:.spec.target.name,READY:.status.conditions[0].status
   ```

2. Put the new value in OpenBao through the approved login procedure. A person does this; an agent does not enter secret values. Record the reference and the date, never the value.
3. Pull it into the cluster now instead of waiting for the refresh interval, and confirm the sync:

   ```sh
   kubectl -n ploeg annotate externalsecret <name> force-sync="$(date +%s)" --overwrite
   kubectl -n ploeg get externalsecret <name>
   ```

4. Restart the processes that read it. For `ploegd`:

   ```sh
   kubectl -n ploeg rollout restart deployment/ploeg
   kubectl -n ploeg rollout status deployment/ploeg --timeout=180s
   ```

   `ploegd` runs as one replica, so tracker and forge webhooks fail for the few seconds of the restart. Worker pods need no restart: each new pod reads the current Secret.

## Rotate the LiteLLM master key

The master key lets `ploegd` mint, block and read the spend of every per-Run key. Only one value works at a time: between the gateway switching and `ploegd` restarting, every gateway call from `ploegd` fails. Minting fails, so new Runs cannot get a model credential. Block and settlement retries wait until the key works again.

1. [Drain the workers](drain-workers.md) and wait until no account is `minting` or `issued`.
2. Change the key in OpenBao. Sync the ExternalSecret in `ploeg` and the one that feeds the gateway in `ai`.
3. Restart the gateway, then `ploegd`:

   ```sh
   kubectl -n ai get deployments
   kubectl -n ai rollout restart deployment/<litellm deployment>
   kubectl -n ploeg rollout restart deployment/ploeg
   ```

4. Resume the workers.

Verify: the `ploegd` log shows `litellm sweeper configured` and, from the orphan sweep that runs at boot, `orphan sweep: no stale keys found` or `revoked stale keys`. A log line `orphan sweep failed` means the gateway rejects the new key. The next Run's account reaches `issued`.

## Rotate the forge bot or read-only token

Both tokens support an overlap, so running Runs keep working.

1. In Forgejo, sign in as the bot user (`executor.forgejo.botUser`, `agent-builder` by default). Create a new access token with the same scopes as the old one. Do not delete the old token yet.
2. Store the new token in OpenBao, sync, and restart `ploegd`.
3. New worker pods now start with the new token. Wait until every Run that started before the sync has finished:

   ```sql
   SELECT count(*) FROM agent_runs WHERE state = 'running' AND started_at < '<sync time>';
   ```

4. Delete the old token in the bot user's Forgejo settings.

Verify: the next writing Run clones, pushes and opens its pull request, and `ploegd` posts review findings on it. A `401` from Forgejo in worker or `ploegd` logs means a process still holds the old token.

## Rotate the forge admin token

Only `ploegd` holds this token. It mints a push token per writing Run and revokes it when the Run ends ([ADR-0013](../adrs/0013-push-rights-are-minted-per-run.md)). The forge orphan sweep also uses it every 15 minutes to revoke push tokens that no Lease records.

1. Create a new admin token in Forgejo. Keep the old one.
2. Store the new token in OpenBao, sync, and restart `ploegd`.
3. Wait one orphan sweep (15 minutes), then delete the old token.

Verify: the `ploegd` log shows `forge orphan sweep: no stale push credentials found` or `revoked stale push credentials`, with no `forge orphan sweep failed` and no `forge credential revoke failed`. The next writing Run pushes.

## Rotate worker bootstrap tokens

A worker presents its bootstrap token to prove its team and Role. `ploegd` checks it against the registry, a JSON array of `{token, team, role}` entries. The registry rejects only a repeated token, so an old and a new entry for the same team and Role can coexist ([worker_auth.go](../../pkg/httpapi/worker_auth.go)). Each token must be at least 32 bytes.

1. Add a new entry for the team and Role to `registry.json` in OpenBao and keep the old entry. Sync and restart `ploegd`.
2. Replace the value of the worker key `<team>--<role>` with the new token. Sync. New pods start with it.
3. When every Run that started before step 2 has finished, remove the old entry from `registry.json`. Sync and restart `ploegd` again.

Verify: no rejected worker requests since the last restart, and the next Run for that team and Role reaches `running`:

```sql
SELECT count(*) FROM audit_log
WHERE action = 'worker.request_rejected' AND at > now() - interval '1 hour';
```

## Rotate the worker signing key

`ploegd` signs the short-lived capabilities it gives workers with this key. The implementation keeps one key and no overlapping generations, so every capability signed with the old key stops working at the restart ([managed workers](../ops/managed-workers.md#configure-the-controller)). A Run in flight then fails its next request and ends failed. Its inference account keeps its hold until the sweeper blocks and settles it.

1. [Drain the workers](drain-workers.md) so that no Run is `running`.
2. Put a new random value of at least 32 bytes in OpenBao. Sync and restart `ploegd`. A shorter key stops `ploegd` at start with `worker capability signing key must contain at least 32 bytes`.
3. Resume the workers.

Verify: `ploegd` becomes ready, the next Run reaches `running` and finishes, and the rejection query above returns 0.

**Not implemented yet:** a signing-key rotation that keeps in-flight Runs alive. It would need `ploegd` to accept an old and a new key for a while.

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| New Secret value present, behaviour unchanged | The process still runs with the value it read at start | Restart `ploegd`; wait for new worker pods |
| ExternalSecret not ready after `force-sync` | Vault path, policy or key name wrong | Check the ExternalSecret's events and the vault reference |
| `orphan sweep failed` or `managed key block retry unresolved` after a master-key change | Gateway and `ploegd` hold different master keys | Sync and restart both sides |
| New Runs never get a model credential | Master key mismatch, or the gateway was restarted before its Secret synced | Check the gateway's Secret, restart the gateway, then `ploegd` |
| `401` on clone or push | Old bot token deleted while a pod still used it | Let the Run fail and retry; next time wait for running Runs to end |
| `forge credential revoke failed` | Admin token invalid, or lacks rights over the bot user | Restore the previous token, fix the new token's scope |
| `worker.request_rejected` rows after a rotation | Worker token not in the registry, or signing key changed under a live Run | Add the token to the registry; drain before changing the signing key |
| `ploegd` does not start: `invalid worker bootstrap scope` | A registry entry lacks a team or has a token under 32 bytes | Fix the entry in OpenBao |
| `ploegd` does not start: `worker bootstrap credentials must be unique per scope` | The same token appears twice in the registry | Give each entry its own token |
