# Credential isolation for Runs: state and cluster plan

> Record, 2026-09-28. A plan, not a decision: every step below is proposed and
> labelled "Not implemented yet" unless it cites code. Sources: the Glide tree
> at `24271e9`, and a read-only clone of `webgrip/homelab-cluster` at `0111413`.
> The owner calls this work "the sidecar thing".

**Verdict: keep the in-worker proxy ([ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)) for single-tenant Runs without DinD, and turn it on in the cluster after four small chart changes and a qualification Run. It does not hold for a DinD harness, and it cannot hold for multi-tenant SaaS.** A privileged DinD sidecar can reach the node, and a node reaches every worker's memory, so concealment inside the pod means nothing while `dind: true`. In a shared cluster, credentials must live outside the Run's pod, in an egress gateway that swaps a per-Run placeholder for the real credential. A sidecar in the Run pod is not enough, because every container in a pod shares its network namespace, its VM under Kata, and its NetworkPolicy.

The cluster repository cannot show that any of this is deployed. Its GitHub `main` (`0111413`, 2026-07-25, identical to `git ls-remote` on 2026-09-28) pins chart `0.1.0-rc.6`, which predates managed worker authentication and both proxies. It does not contain the homelab commits that ADR-0035 cites (`ab75cc35`, `92b15206`, `747890d0`) or `worker-egress-probe.job.yaml`. Either the mirror is stale or the canonical repository lives elsewhere. Settle that first (step H0).

## 1. Current state in Glide

### Implemented and tested

| What | Source | Test (`go test ./pkg/worker/` unless noted) |
| --- | --- | --- |
| The worker marks itself non-dumpable before anything else | [conceal_linux.go](../../pkg/worker/conceal_linux.go), called from [ploeg-worker main](../../cmd/ploeg-worker/main.go) | `TestHarnessCannotReadConcealedWorkerEnvironment` |
| The harness gets an allowlisted environment: no bootstrap token, no reader token | [environment.go](../../pkg/worker/environment.go), `scrubSecrets` in [git.go](../../pkg/worker/git.go) | worker suite |
| The model key stays in the worker behind a loopback reverse proxy that swaps a placeholder | [llmproxy.go](../../pkg/worker/llmproxy.go) | `TestIsolatedRunNeverHandsTheKeyToTheHarness`, `TestKeyProxySwaps*`, `TestKeyProxyStreamsWithoutBuffering` |
| A writer's forge token stays in the worker. Git (via `insteadOf`) and the API reach only the Run's repository | [forgeproxy.go](../../pkg/worker/forgeproxy.go) | `TestForgeProxyRefusesEverythingOutsideTheRunsRepository`, `TestGitPushesThroughTheForgeProxy`, `TestForgeProxyAuthenticates*` |
| Per-Run forge tokens scoped to one repository, revoked by the sweeper (ADR-0013 tier 2) | [forgebroker/forgejo.go](../../pkg/forgebroker/forgejo.go), [forgecreds.go](../../cmd/ploegd/forgecreds.go) | `pkg/forgebroker`, `cmd/ploegd` |
| Readers get a read-only token. The chart refuses to render a reader without one | [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) | `ci/reject-reader-without-read-token-values.yaml` |
| Managed worker auth: LiteLLM admin authority only in ploegd, workers get a scoped bootstrap token | [managed workers](../ops/managed-workers.md) | golden `executor.yaml` |
| Worker pods mount no service-account token. The chart has no knob for `shareProcessNamespace` or added capabilities | `automountServiceAccountToken: false` in `_helpers.tpl` | golden renders |

Both proxies first shipped in release `glide-v0.4.0-rc.1` (commits `10d98b1`, `042cf08`, `8f0f641`, 2026-09-26).

### Opt-in

- `executor.litellm.keyIsolation: proxy` and `executor.forgeTokenIsolation: proxy` (`PLOEG_LLM_KEY_ISOLATION`, `PLOEG_FORGE_TOKEN_ISOLATION`) default to `""`, which still hands credentials to the harness. No harness has been qualified with them on.
- Per-Run forge tokens need `executor.forgejo.botPasswordSecret`. Without it, ploegd logs "per-run forge credentials disabled" and every writer shares the long-lived `agent-builder` token.
- The agent-sandbox executor (`executor.type: sandbox`, [sandboxlaunch](../../pkg/sandboxlaunch/launcher.go)), with `runtimeClassName` and a managed per-template NetworkPolicy, is experimental. Goldens `executor-sandbox*.yaml` pin its egress: DNS, ploegd `:8080`, `ai:4000`, `forgejo:443,3000`.

### Gaps found

1. **The concealment test fails when the harness has `CAP_SYS_PTRACE`.** In this session, running as root with that capability in `CapEff`, `TestHarnessCannotReadConcealedWorkerEnvironment` failed with `readable`. All other isolation tests passed. This is the caveat ADR-0034 predicts, but neither the worker nor the test detects it. The test fails on a root CI runner, and the worker would run "isolated" without any protection.
2. **DinD defeats isolation.** The harness holds the DinD client certificates (`DOCKER_*` are allowlisted in `environment.go`). It can start a privileged container on a daemon that runs privileged, which reaches the node and every process on it. `dind: true` is the chart default and the OpenHands setting.
3. **Neither proxy checks the placeholder.** Any process that shares the pod's loopback can use them, including a DinD container started with `--network host`.
4. **The forge proxy scopes by repository only.** A harness can push to any branch, including the base branch, and call any repository API route: merge, delete a branch, change settings. Forgejo branch protection is the only backstop.
5. **The worker container has no `securityContext`.** It runs with the harness image's user and the runtime's default capabilities. The chart pins nothing (`allowPrivilegeEscalation`, `drop: [ALL]`, `runAsNonRoot`).
6. **`keyIsolation` has no enum in `values.schema.json`.** `forgeTokenIsolation` does. No golden renders either flag.
7. **`runtimeClassName` renders only for `executor.type: sandbox`.** KEDA and CronJob worker pods cannot use gVisor or Kata.

## 2. What the cluster has, checked against homelab-cluster `0111413`

| Need | Found | Verified how |
| --- | --- | --- |
| Ploeg chart deployed | Yes, `HelmRelease ploeg`, OCI chart `0.1.0-rc.6`, pinned back from rc.7 on 2026-07-25 | `kubernetes/apps/ploeg/ploeg/app/{helmrelease,ocirepository}.yaml` |
| Isolation flags | Not set. The pinned chart has no such values | helmrelease values |
| Managed worker auth secrets (`ploeg-worker-signing`, `ploeg-worker-bootstrap`) | Absent. The only ExternalSecrets are `agent-litellm-master`, `agent-builder-token`, `harbor-pull`, `webhook-secret` | `app/kustomization.yaml` |
| LiteLLM master key out of worker pods | **No.** `agent-litellm-master` is "injected only into worker pods". The agent-runner entrypoint mints per-Run keys with it. The same key also reaches `forgejo/forgejo-agent-runner` pods | ExternalSecret comments, `forgejo-agent-runner/app/` |
| Per-Run LiteLLM virtual keys | Yes: budget-capped and model-scoped. The exporter reports `litellm_key_max_budget` per `key_alias` | `ai/litellm/app/litellm-exporter-queries.configmap.yaml` |
| Per-Run forge tokens | No `botPasswordSecret`. Writers share `agent-builder` | helmrelease, ExternalSecrets |
| Default-deny egress in `ploeg` | Yes. The Kyverno-generated `default-deny` plus `allow-dns` (label `kyverno.io/default-network-policies`), reopened by `ploeg-allow-egress` | `namespace.yaml`, `app/networkpolicy.yaml`, `kyverno/policies/app/namespace-defaults-generate.yaml` |
| Egress limited to LiteLLM and the forge for Run pods | **No, in this snapshot.** `ploeg-allow-egress` selects every pod (`podSelector: {}`) and allows the whole namespace, `10.0.0.0/24` (the LAN, for CNPG WAL to Garage), `ai:4000` and `forgejo:3000`. ADR-0035 says later commits removed LAN access for workers. This clone does not contain them | `app/networkpolicy.yaml` |
| Cilium L7 or FQDN policy | None used. Cilium runs with `envoy.enabled: false` and Hubble drop and policy metrics. The only CiliumNetworkPolicy objects are for CNPG, forgejo-runner and backstage | `kube-system/cilium/app/helmrelease.yaml`, grep |
| RuntimeClass (gVisor or Kata) | **None.** Talos `v1.13.7` extensions are `binfmt-misc`, `iscsi-tools` and `util-linux-tools` only. No `RuntimeClass` object exists | `talos/talconfig.yaml`, `talos/talenv.yaml`, grep |
| agent-sandbox controller | Not installed | grep for `agents.x-k8s.io` |
| Image volumes (ADR-0035 toolchains) | Kubernetes `v1.36.1`, so the API is present. Runtime support is not verified | `talos/talenv.yaml` |
| No `CAP_SYS_PTRACE`, no `shareProcessNamespace` | Not enforced. `pod-security-baseline-enforce` covers host namespaces, host paths, host ports and privileged, but not capabilities. PSA `enforce: privileged` on the namespace | `kyverno/policies/app/pod-security-baseline-enforce.yaml` |
| DinD waiver narrowly scoped | **No.** `exception-ploeg-worker-privileged` matches every Pod labelled `app.kubernetes.io/name: ploeg-worker`, not the chart's hazard label `ploeg.webgrip.dev/privileged-dind`. It also names Jobs per team | `kyverno/policies/app/exception-ploeg-worker.yaml` |
| Secret delivery | ESO from the OpenBao `ClusterSecretStore`. The repository forbids new SOPS files | `CLAUDE.md`, ExternalSecrets |
| LiteLLM reachable only from agents | No. The `ai` namespace does not opt into default-deny (tracked follow-up, ADR-0044) | `ai/namespace.yaml` |
| Preview environments | Static SPA previews built by CI, served by `forgejo/preview-host`. Runs do not start them | `forgejo/preview-host/app/deployment.yaml` |

**Not verified:** the live cluster (no `kubectl` or MCP access from this session), the chart version actually running, whether the GitHub mirror matches the source Flux reconciles, the `agent-runner` image's user and capabilities (maintained in `webgrip/infrastructure`), the node hardware (bare metal or VM, which decides Kata versus gVisor), and whether chart rc.6 lets the harness read `LITELLM_MASTER_KEY`.

## 3. Steps

Each step is small and has its own check. Glide steps are chart and worker changes that ship in a release. Cluster steps are manifest edits in `webgrip/homelab-cluster` on `main`, validated with `./scripts/run-flux-local-test.sh`. All steps are Not implemented yet.

### Glide repository

- **G1. Refuse isolation the pod cannot give.** When either flag is `proxy`, `ploeg-worker` reads `/proc/self/status` and refuses to claim if `CapEff` or `CapBnd` contains `CAP_SYS_PTRACE`. It should drop the capability from the bounding set before it execs the harness where it can. Make the concealment test skip with a named reason under that capability instead of failing.
  *Confirmation:* a new `TestIsolatedWorkerRefusesPtraceCapableHarness` with a fake capability line, and `go test ./pkg/worker/` passing as root and as non-root.
- **G2. Pin the worker container's security context.** Set `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`, and `runAsNonRoot` when the harness image allows it. Qualify against `agent-runner` first.
  *Confirmation:* `scripts/helm-golden.sh check` with updated goldens, plus a grep check that no golden contains `SYS_PTRACE` or `shareProcessNamespace`.
- **G3. Schema and golden for the flags.** Add a `["", "proxy"]` enum for `executor.litellm.keyIsolation`. Add `ci/executor-isolated-values.yaml` (both flags `proxy`, `dind: false`) and its golden.
  *Confirmation:* `scripts/helm-golden.sh check`, and a `ci/reject-key-isolation-typo-values.yaml` that fails to render.
- **G4. Label the isolation level on the pod.** Render `ploeg.webgrip.dev/credential-isolation: proxy` only when both flags are `proxy` and `dind` is false. Otherwise render `none`. The cluster can then enforce on the label.
  *Confirmation:* golden diff across `executor.yaml` (`none`) and `executor-isolated.yaml` (`proxy`).
- **G5. Require the placeholder at both proxies.** Answer 403 when the request's credential header is not the Run's placeholder. For git, carry the placeholder as the Basic password in the `insteadOf` URL.
  *Confirmation:* `TestKeyProxyRefusesRequestsWithoutThePlaceholder` and `TestForgeProxyRefusesRequestsWithoutThePlaceholder`.
- **G6. Narrow the forge proxy to the Run's branch.** Allow `git-receive-pack` only for `refs/heads/<run branch>` (parse the pkt-line command list). Allow only the API routes the delivery contract uses: pull request create, update and comment, plus reads. Deny `DELETE`, merge and settings routes.
  *Confirmation:* `TestForgeProxyRefusesPushToTheBaseBranch` and `TestForgeProxyRefusesMergeAndDelete`.
- **G7. Offer `runtimeClassName` to KEDA and CronJob pods.** Use the same role, team and global resolution as `_sandbox.tpl`.
  *Confirmation:* a golden where `executor.yaml` renders `runtimeClassName: gvisor` for one Role.
- **G8. Offer an optional per-worker NetworkPolicy for KEDA pods.** Select `app.kubernetes.io/name: ploeg-worker`, with the same rules as the sandbox template's managed policy. Off by default.
  *Confirmation:* golden `executor-netpol.yaml`.
- **G9. Qualify each harness with both flags on** (`claude-code` and `acp`/opencode with `dind: false` first, OpenHands after ADR-0035 toolchains replace its DinD). Write a dated record, then accept ADR-0034. Flip the chart defaults to `proxy` in a `feat!` change only after that.
  *Confirmation:* the record links one real Run per harness with a pull request opened through both proxies, and `go test ./internal/ledger/` passes.

### homelab-cluster repository

- **H0. Establish what is live.** Before any edit, confirm the chart version running and whether the GitHub mirror is the source Flux reconciles.
  *Confirmation:* `flux -n ploeg get sources oci ploeg`, `kubectl -n ploeg get helmrelease ploeg -o jsonpath='{.status.history[0].chartVersion}'` and `flux -n flux-system get sources git flux-system`.
- **H1. Deliver managed-auth secrets and roll the chart forward** to a release that contains G1 to G4 (at least `0.4.0-rc.1` for the proxies alone). Add ExternalSecrets for `ploeg-worker-signing` and `ploeg-worker-bootstrap` from OpenBao (generate the signing key with `password-generator`). Keep `agent-litellm-master` for ploegd only.
  *Confirmation:* `kubectl -n ploeg get externalsecret` all `SecretSynced`, and `kubectl -n ploeg get pods -l app.kubernetes.io/name=ploeg-worker -o yaml | grep -c LITELLM_MASTER_KEY` returns `0`.
- **H2. Enable per-Run forge tokens.** Add an ExternalSecret with the `agent-builder` password, and set `executor.forgejo.botPasswordSecret`.
  *Confirmation:* `kubectl -n ploeg logs deploy/ploeg | grep "per-run forge credentials enabled"`, and during a Run the Forgejo token list for `agent-builder` shows one repository-scoped token that disappears after it.
- **H3. Turn isolation on for one team with `dind: false`.** Set `executor.litellm.keyIsolation: proxy` and `executor.forgeTokenIsolation: proxy`, on a reviewer Role first.
  *Confirmation:* `kubectl -n ploeg get scaledjob -o yaml | grep -A1 _ISOLATION`, plus a canary Work Item that tells the agent to write `env`, `/proc/1/environ` and `git config -l` into its pull request. The pull request shows only placeholders and loopback URLs.
- **H4. Split egress per workload.** Move `10.0.0.0/24` onto a policy that selects only the CNPG pods. Give worker pods their own policy (or G8's): DNS, ploegd `:8080`, `ai:4000`, `forgejo:3000`, nothing else.
  *Confirmation:* the ADR-0035 `worker-egress-probe` Job (add it if the mirror lacks it) shows LAN and internet refused and the four targets reachable, and `hubble observe -n ploeg --verdict DROPPED` shows no unexpected drops during a Run.
- **H5. Enforce the pod shape in Kyverno.** Add an `Enforce` policy for `ploeg`: no `shareProcessNamespace: true`, no `capabilities.add` containing `SYS_PTRACE`, and no `privileged` unless the pod carries `ploeg.webgrip.dev/privileged-dind: "true"`. Re-key `exception-ploeg-worker-privileged` on that hazard label instead of `app.kubernetes.io/name`.
  *Confirmation:* a chainsaw test under `kubernetes/apps/kyverno/tests/` with one rejected pod per rule and one admitted isolated pod, plus `kubectl -n ploeg apply --dry-run=server` of a `SYS_PTRACE` pod being refused.
- **H6. Retire the master key from agent pools.** Drop `agent-litellm-master` from `forgejo-agent-runner` once that pool mints through ploegd or a budget-capped minter key (tech-debt 282).
  *Confirmation:* `kubectl get secret -A | grep agent-litellm-master` lists only `ploeg`.
- **H7. Add RuntimeClasses (for multi-tenancy, not for the first rollout).** Add the `siderolabs/gvisor` extension (and `kata-containers` on bare-metal nodes) to `talconfig.yaml`, upgrade the nodes, and add `RuntimeClass` objects.
  *Confirmation:* `talosctl get extensions` on each node, `kubectl get runtimeclass`, and one Run with G7's `runtimeClassName` completing.

H3 depends on H1 and on G1 to G4 in the released chart. H5 depends on G4. H7 depends on G7 or on the sandbox executor with the agent-sandbox controller installed.

## 4. When a sidecar or egress gateway becomes necessary

The in-worker proxy relies on three things that hold today only for a `dind: false` Run in a single-tenant cluster: the harness runs in the worker's container, it lacks `CAP_SYS_PTRACE`, and nothing in the pod can reach the node. Each of the following breaks one of them.

| Trigger | Why the in-worker proxy fails | What is needed |
| --- | --- | --- |
| DinD harness (OpenHands today) | A privileged daemon reaches the node, and a container on the default bridge cannot reach the worker's loopback | Remove DinD (ADR-0035 toolchains), or run it rootless or under Kata. Credentials move outside the pod |
| Kata or gVisor RuntimeClass | Every container of the pod shares one sandbox, so an in-pod sidecar sits inside the same boundary as the harness | An egress point outside the pod |
| Multi-tenant (many customers' repositories and keys) | A credential inside any Run pod is one pod escape away. Per-tenant forge apps and model keys need per-tenant audit and revocation | A per-tenant egress gateway |
| Egress beyond LiteLLM and the forge (module proxies, package registries, preview deploys) | NetworkPolicy cannot express hostnames or HTTP routes | An allowlisting HTTP(S) egress proxy, or Cilium `toFQDNs` (not used in this cluster yet) |
| Run-started preview environments | The preview needs its own egress and ingress, and must never hold the Run's credentials | A separate pod or namespace per preview, reached only through the gateway (Not implemented yet. Previews are CI-built today) |

**Why a sidecar in the Run pod is the weaker shape.** NetworkPolicy and Cilium select pods, not containers, so no policy can force the harness's traffic through a sidecar. Forcing it takes an init container with `NET_ADMIN` and iptables redirection, as Istio does. A sidecar under a different UID does defeat the same-user `/proc` read, but G1 and G2 achieve that more cheaply. A sidecar helps a DinD container only if that container uses `--network host`.

**The proposed shape: a per-tenant egress gateway.** Not implemented yet.

1. Run pods get default-deny egress except DNS, ploegd and the tenant's gateway Service. They hold only per-Run placeholders.
2. When ploegd mints a Run's LiteLLM key and forge token (it already does, ADR-0008 and ADR-0013 tier 2), it registers `placeholder → credential, repository, branch, expiry` with the gateway instead of handing the credential to the worker.
3. The gateway swaps the placeholder for the credential, applies the G5 and G6 rules centrally, streams responses (as `TestKeyProxyStreamsWithoutBuffering` requires today), and forgets the mapping when the Lease ends. The sweeper revokes as it does now.
4. The gateway runs as a Deployment per tenant namespace. The only egress it needs is LiteLLM, the tenant's forge and the tenant's allowlisted hosts.

What it takes: a new `ploeg-egress` binary built from `llmproxy.go` and `forgeproxy.go`, an authenticated registration API on ploegd's side, a chart template (Deployment, Service, NetworkPolicy per tenant), a contract under `docs/contracts/`, and an ADR that supersedes ADR-0034. It also needs high availability, because the gateway becomes a critical dependency for every Run, and a latency measurement on streamed model calls. Upstream per-sandbox credential injection in agent-sandbox, OpenShell or Agent Substrate would replace most of this; that is ADR-0034's second re-evaluation trigger.

## 5. Open questions for the owner

1. Is `github.com/webgrip/homelab-cluster` the source Flux reconciles? Its `main` stops on 2026-07-25 at chart `0.1.0-rc.6`, and ADR-0035 cites later commits that it does not contain.
2. Which harness should run isolated first? Do writers give up DinD once ADR-0035 toolchains cover their checks?
3. What is the tenancy unit for the agency SaaS: a namespace per customer, a node pool per customer, or a cluster per customer? The gateway's placement and Kata's value depend on it.
4. Where do customer credentials live: OpenBao paths per tenant through ESO, or a GitHub or Forgejo App per tenant minted by ploegd?
5. Are the nodes bare metal? Kata needs hardware virtualization. gVisor systrap costs file I/O on clone-heavy Runs.
6. Should G6's branch fence ship before per-tenant forge Apps, or should Forgejo branch protection on each target repository stay the backstop?
7. ADR-0034 has been `proposed` since 2026-09-26. Accept it after G9, or supersede it straight to the gateway once multi-tenancy is committed?
8. Should `forgejo-agent-runner` (the older ADR-0048 pool, holding the LiteLLM master key) be retired, rather than hardened in H6?
