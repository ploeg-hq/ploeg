# Managed-worker deployment and reconciliation

The [worker-control contract](../contracts/worker-control.md) defines the implemented authority and accounting behavior. [ADR 0025](../adrs/0025-management-authority-stays-in-the-control-plane.md) records the decision and remains proposed. Configure desired state in Git; this guide does not authorize live deployment or secret-value entry by an agent.

## Upgrade existing deployments

**This is a breaking configuration change.** Managed authentication is now the default. Existing unauthenticated worker requests and worker-side LiteLLM administration do not continue automatically. An image-only upgrade with the previous configuration can fail controller or worker startup; managed mode fails closed when its identity, broker or policy inputs are missing.

Before reconciling the new application and chart revision, provision the signing and bootstrap Secret references described below, register each worker's team/role scope, and configure positive budgets and explicit model scopes in the controller. Move LiteLLM management credentials to the controller and remove them from every worker container. Update controller and worker workloads together for managed operation. Complete or explicitly stop existing executors before changing their authentication generation; a rejected old credential does not authorize a replacement paid attempt.

An existing deployment that needs temporary compatibility must opt in through `executor.workerAuth.mode: legacy` and provide the optional `executor.workerAuth.staticInferenceSecret` when its harness requires a key. The chart sets both the controller's `PLOEG_WORKER_AUTH_MODE=legacy` and the worker's `PLOEG_LLM_CREDENTIAL_MODE=static-compatibility`. Standalone deployments must set both explicitly. This compatibility path accepts the historical worker control model and lacks managed accounting guarantees; it does not restore worker-side LiteLLM administration. Do not configure managed signing or bootstrap material alongside legacy mode.

## Configure the controller

Keep LiteLLM management authority in the controller workload. Worker bootstrap credentials, controller signing material and the bootstrap registry originate in the vault and reach Kubernetes through the estate's ExternalSecret process. Commit Secret references and configuration structure, never their values.

| Setting | Requirement |
| --- | --- |
| `executor.workerAuth.mode` | `managed` by default; `legacy` is explicit compatibility |
| `executor.workerAuth.signingKeySecret` | Secret name and key for the controller-only signing material; at least 32 bytes |
| `executor.workerAuth.bootstrapSecret` | Secret name and `registryKey` for the controller registry |
| Bootstrap registry structure | JSON array whose entries have `token`, `team` and `role`; tokens are at least 32 bytes and unique per scope |
| Bootstrap Secret worker entries | Separate keys named `<team>--<role>`, or `<team>--default` for an empty role; each worker receives only its entry |
| `executor.workerAuth.additionalLLMPolicies` | Additional trusted team/role policies, including the `operator` role for a workbench consumer |
| `executor.litellm.masterKeySecret` | Controller-only LiteLLM management Secret reference |
| `executor.litellm.keyDuration` | Positive Go duration between one second and 24 hours |

The chart builds policies from each effective team/role model and budget. An additional policy contains `team`, `role`, `budgetUsd`, `models` and `ttl`, and optionally `litellmTeamId` and `mcpAccessGroups`. Team/role combinations must be unique; budgets must be finite and positive; model scopes must be nonempty; MCP access groups need a LiteLLM team id ([ADR-0078](../adrs/0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md)). A team sets both with `executor.teams[].litellmTeamId` and `executor.teams[].mcpAccessGroups`: ploegd mints the Run's key with `team_id` and `object_permission.mcp_access_groups`, and a `claude-code` Run loads the gateway's `/mcp` as its only MCP server, through the worker's key proxy. The effective account authorization is the smaller of the trusted policy cap and the positive authorization already recorded on the Run. Repeated reservation must match the existing immutable account policy.

Outside Helm, the controller reads `PLOEG_WORKER_AUTH_MODE`, `PLOEG_WORKER_SIGNING_KEY`, `PLOEG_WORKER_BOOTSTRAPS` and `PLOEG_WORKER_LLM_POLICIES`. The worker reads `PLOEG_WORKER_BOOTSTRAP_TOKEN` and `PLOEG_WORKER_ID`, falling back to downward-API `POD_UID` for identity. These are deployment inputs, not values to place in repository examples.

Rotate bootstrap or signing material through the same GitOps and vault process. Replacing the single signing key invalidates existing signed capabilities; this implementation does not provide overlapping signer generations. Plan rotation around active Runs or accept their explicit interruption and retained spend holds.

## Validate before rollout

Run the repository gates through the pinned toolchain:

```sh
mise exec -- go build ./...
mise exec -- go vet ./...
mise exec -- go test ./...
mise exec -- helm lint ops/helm/ploeg
mise exec -- ./scripts/helm-golden.sh check
```

Use the Helm version pinned in `mise.toml`. [The render script](../../scripts/helm-golden.sh) checks default, executor, CronJob, GitLab and monitoring cases. Inspect rendered worker containers for controller signing material, the full bootstrap registry, LiteLLM management credentials and operator credentials; none belongs there. Confirm workers still disable service-account token mounting. The rendered controller must receive the configured management Secret references.

The automated tests use synthetic credentials and fake external services. The [accounting tests](../../pkg/store/llm_accounts_test.go) use PostgreSQL and exercise crash boundaries, concurrent mint intent, trusted reconciliation and late charge deltas. The [worker tests](../../pkg/worker/environment_test.go) execute a child process to inspect its effective environment; [Git tests](../../pkg/worker/git_test.go) inspect a real clone's configuration. They do not qualify a live gateway's block/cache propagation or final billing latency.

Before you change a harness version in the agent image, run `mise run harness-conformance` from the repository root inside that image. The suite is opt-in and makes model calls. It reports which instruction files each harness loads by itself and checks that `claude-code` does not run a target repository's hooks or `.mcp.json` servers ([live_test.go](../../pkg/harness/harnesstest/live_test.go)). The offline [argv test](../../pkg/harness/adapters/claudecode/claudecode_test.go) runs a fake `claude` in every `go test` run.

## Reconcile uncertainty

1. Establish the Work Item, Shift and Run through the authorized operator view. Inspect the account state and retained budget hold. Do not extract or share raw worker credentials.
2. Stop the runtime and confirm gateway blocking through the controller. Retain the alias and hashed accounting identity. Missing key history is uncertainty, not proof of no spend.
3. Obtain trustworthy final provider/gateway evidence, including any delayed usage. Keep that evidence with the operational record. An observed gateway snapshot alone is provisional.
4. The controller's settlement sweep is the trusted caller of `Store.ReconcileLLMAccount`. On every sweep interval it pages finished Runs whose account is still `reserved`, or `blocked` and unchanged for `PLOEG_LLM_SETTLE_AFTER` (default `15m`). An account with no durable sign of a mint (no gateway key identity, no positive observation and no `llm.minting`, `llm.issued` or `llm.unknown` audit record) settles at zero with `ploegd:mint-never-began` evidence. Any other blocked account settles at the total of LiteLLM's spend logs (`GET /spend/logs?api_key=`) for its recorded hashed key and any key still carrying its alias, with the alias, key and entry counts, amount, quiet-since time and read time as evidence. The same entries fill the Run's `agent_runs.usage`: summed `prompt_tokens` and `completion_tokens` become `inputTokens` and `outputTokens`, the distinct `model` values become `models`, the same sums per model become `byModel` (`model`, `inputTokens`, `outputTokens` and `costUsd`, each cost rounded to four decimals on its own), and the settled amount becomes `costUsd`. Other keys the harness reported, such as `sessionId`, are kept. An entry without token counts adds zero tokens; it never blocks settlement. The key's own running total is never the settlement source: LiteLLM writes it asynchronously and loses it when a key is deleted. Settlement therefore requires spend logs to stay enabled on the gateway; a gateway with spend logs disabled would report no entries, and only a recorded positive observation stops that from settling at zero. A minted account settled from no entries at all records its cost as unknown (`cost_known` false): the operator API reports its `settledUsd` as null and its Run `costStatus` as `unknown`, not as a zero cost, until an entry arrives. The response shape was qualified against fakes, not a live gateway. The sweep never settles `minting`, `issued` or `unknown` accounts, never settles below a recorded observation and never treats a missing gateway key as zero. The block sweep moves such an account of a finished Run to `blocked` when the gateway reports that it holds no key for the account (no key with its alias in `/key/list` and a `404` from `/key/info` for its recorded key identity), and records that report as `litellm:key-absent` evidence on the `llm.blocked` audit event. Any other answer, including an error or an unreachable gateway, leaves the account as it was. A blocked account with a gone key then settles from its spend logs like any other, because spend logs outlive the key. An account with no recorded key identity and no key under its alias has no spend logs Ploeg can read, so its settlement fails and it keeps its hold for this manual procedure. This store operation is not exposed as a worker or unauthenticated HTTP settlement endpoint. Lengthen the quiet period if the gateway's spend writes lag further behind a block. A spend-log settlement stays provisional for `PLOEG_LLM_CORRECTION_WINDOW` (default `24h`; `0` makes it final at once). Every 15 minutes inside that window the correction sweep reads the account's spend logs again; a higher total charges only the difference to the Shift and is recorded as a `run_llm_adjustments` row and an `llm.reconciled` audit event marked `adjustment`, while `settled_at`, `settled_spend` and the first evidence stay as they were. The operator API's `settledAt`, `costFinalAt` and `costFinal` tell a provisional cost from a final one. The window is fixed at the first settlement, so changing the setting affects later settlements only.
5. Verify the Shift's charged spend and remaining hold. If a later charge appears, retain the positive delta and reconcile a correction; never discard history or reset authorization to hide it.

A failed or interrupted execution does not authorize another paid attempt. An expired worker may no longer authenticate its own cleanup requests; controller sweeping handles that boundary. Unresolved accounts remain visible until external effects are established. Blocking a key retains accounting history and does not itself settle cost.

Legacy compatibility is configured deliberately with `PLOEG_WORKER_AUTH_MODE=legacy` and worker `PLOEG_LLM_CREDENTIAL_MODE=static-compatibility`. Its optional static inference Secret is configured through `executor.workerAuth.staticInferenceSecret`. This path keeps historical behavior and does not provide managed authority or conservative account guarantees for unmanaged Runs.

## Operating limits

Gateway budgets and TTLs bound exposure but cannot prove an exact monetary ceiling for requests already in flight. A confirmed pause in the operator workbench can retain the same capped inference key for an explicit resume; the key remains usable until blocked or expired. A strict per-request pause or generation fence would require an additional gateway enforcement mechanism.

The worker's parent process still carries a scope-limited bootstrap credential and selected forge access. Same-container process inspection and existing privileged DinD choices are separate isolation concerns. Read-only role intent also does not establish repository write fencing when a shared write credential is configured. See [the architecture](../architecture.md) and [ADR 0013](../adrs/0013-push-rights-are-minted-per-run.md) for the existing forge boundary.
