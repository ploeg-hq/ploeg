---
status: proposed
date: 2026-10-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# Worker workloads render into a namespace of their own, and profile P1 refuses a shared one from chart 0.3.0

## Context and Problem Statement

The threat model's trust boundary TB2 and its level 0 diagram describe a control-plane namespace and a separate worker namespace. The chart rendered no such split: every object, the KEDA ScaledJob, the CronJob, the TriggerAuthentication and the sandbox objects included, went into the release namespace. ploegd, the Secrets wired to it, ploeg-mcp, the sandbox launcher with its service-account token ([F-21](../security/threat-model.md#findings)) and the agent pods shared one namespace, one Pod Security level and one namespace for RBAC, so a RoleBinding, a mis-scoped policy or a Pod Security exemption granted for one applied to both. Finding [F-44](../security/threat-model.md#findings) records it ([#354](https://github.com/ploeg-hq/ploeg/issues/354)). Where do the worker workloads render, and what does profile P1 require of that place?

## Decision Drivers

* A worker pod runs model-directed code over an attacker-controlled repository; it must not be one namespace-scoped mistake away from ploegd's master key, signing key and forge credentials.
* The Pod Security level P1 requires on the worker namespace (`restricted`) must be enforceable without also binding the installer's other services in the release namespace, such as a database or a gateway.
* Existing 0.2 deployments must not break inside 0.2: a namespace move needs Secrets the installer creates, so it cannot happen on an upgrade by default.
* The chart cannot create Secrets it does not own, and Helm owning a Namespace object deletes everything in it on uninstall.

## Considered Options

* `executor.namespace`, empty by default; under P1 a shared namespace is warned about before chart 0.3.0 and refused from it unless `executor.acknowledgeSharedNamespace: true`
* `executor.namespace` with a non-empty default, such as `<release namespace>-workers`
* The chart creates and labels the worker Namespace itself
* Keep one namespace and document TB2 as a pod boundary

## Decision Outcome

Chosen option: "**`executor.namespace`, empty by default, warned and then refused under P1**", because it draws the boundary the threat model already assumes, keeps a 0.2 upgrade unchanged, and gives a deployment one minor's notice before P1 requires it.

1. `executor.namespace` names the namespace the worker workloads render into: every ScaledJob and CronJob, the TriggerAuthentication, every SandboxTemplate and SandboxWarmPool, the worker ServiceAccount, the sandbox launcher's ServiceAccount, Role and RoleBinding (whose subject names that namespace), the `<release>-worker`, `<release>-worker-<team>-anthropic` and `<release>-sandbox-launcher` NetworkPolicies, and the worker admission policy's namespace match. Empty, or the release namespace, renders them in the release namespace as before, byte for byte.
2. With a namespace of their own, the worker and launcher pods call ploegd at `http://<fullname>.<release namespace>.svc:<service.port>` unless `executor.apiUrl` is set; their NetworkPolicies allow ploegd's pods in the release namespace on 8080; and when `networkPolicy.ploegd.ingressFrom` names callers, ploegd's NetworkPolicy also admits the `ploeg-worker` and `ploeg-sandbox-launcher` pods of the worker namespace on 8080. ploegd, its Service, its Secrets, ploeg-mcp and the claude-runner stay in the release namespace.
3. The installer creates the worker namespace with Pod Security `enforce: restricted` and puts in it only what the worker pods and KEDA read there: `executor.workerAuth.bootstrapSecret` with the `<team>--<role>` keys and no `registryKey`, the forge token Secrets the workers mount, Secrets a plugin or login names, and, under the `keda` or `sandbox` executor, `executor.scaler.passwordSecret`. ploegd keeps its own copy of the bootstrap Secret with the registry in the release namespace. The chart does not create or label the Namespace.
4. Under `supportProfile: P1`, with the executor enabled, a worker namespace that is empty or equal to the release namespace is a departure from chart 0.3.0 (`semverCompare ">=0.3.0-0" .Chart.Version`), unless `executor.acknowledgeSharedNamespace: true`. Before 0.3.0 it renders, and the install notes carry a warning naming the value; the refusal switches on with the first 0.3.0 candidate and needs no later edit, the way ADR 0154 clause 8 switches on F-45.
5. The 0.3.0 refusal breaks P1 values that rendered before. It is recorded against `v0.2.0-rc.83` in [breaking-changes.yaml](../contracts/breaking-changes.yaml) and ships as a `BREAKING CHANGE`.
6. The quickstart page keeps one namespace while its pinned release predates `executor.namespace`; the Quickstart workflow's `checkout-chart` leg runs the page with `QUICKSTART_WORKER_NAMESPACE=ploeg-workers`, which creates the namespace with the restricted label and the worker's two Secrets before the install, installs with `executor.namespace`, reads the worker logs from it, and fails unless the Run's Jobs ran there and none in `ploeg`. The page itself moves to two namespaces when its pin is a release that carries the value.

### Consequences

* Good, because TB2 becomes the namespace boundary the threat model describes: ploegd's Secrets, a RoleBinding or a Pod Security exemption in the release namespace no longer reach a worker pod, and the worker namespace can enforce `restricted` on its own.
* Good, because the sandbox launcher's `sandboxclaims` Role, the only Kubernetes API authority in the executor, now covers the worker namespace instead of ploegd's ([F-21](../security/threat-model.md#findings)).
* Good, because nothing changes for a 0.2 deployment until it sets the value, and the install notes say what to set before 0.3.0 refuses it.
* Bad, because the installer keeps two copies of the bootstrap tokens and the forge token, one per namespace, and under KEDA a copy of the scaler password beside the agents. The scaler password is a read-only role on the queue count (ADR 0154's KEDA row); the worker pods never mount it.
* Bad, because the warning before 0.3.0 reaches only someone who reads the install notes, which GitOps controllers do not show.
* Neutral: a deployment can still keep one namespace under P1 with `executor.acknowledgeSharedNamespace: true`, which records the choice in the values where a reviewer sees it.

### Confirmation

* `scripts/helm-golden.sh check`, run by `mise run verify` and CI's `verify` job, keeps the goldens `executor-namespace` (KEDA, a sandbox team, a `claude-environment` team, the worker admission policy and named ploegd callers) and `executor-namespace-cronjob`, and runs [`check-executor-namespace.py`](../../scripts/check-executor-namespace.py) on a fresh render of each: every worker object must render into `ploeg-workers` and every other object into the release namespace, the RoleBinding's subject and the admission match must name the worker namespace, ploegd's NetworkPolicy must admit every worker and launcher pod template from it on 8080, a worker policy in the worker namespace must select them and let them reach ploegd, and their `PLOEG_API_URL` must name the release namespace. The same check runs on the `executor`, `executor-sandbox-team` and `executor-worker-admission` values with one namespace and must pass. Run against the chart before this record with `executor.namespace` set, the check reports 28 failures. Each of these mutations made the check fail on its own count before this record was written: `cronjob.yaml` without its namespace, the RoleBinding subject in the release namespace, ploegd's ingress without the worker-namespace rule, the admission match on the release namespace, the worker egress to ploegd without its namespace selector, and workers calling ploegd by its short Service name.
* The same script requires a P1 render with a shared namespace to warn in the notes before 0.3.0 and a render with the namespace set, acknowledged, `unsupported` or with the executor off not to; and, on copies of the chart at 0.3.0-rc.1 and 0.3.0, requires an empty, an explicit release and an unacknowledged shared namespace to fail under P1 with the F-44 message, the four passing values to render, and an `unsupported` render to list the departure without the warning. A guard that never fires, one that ignores a separate namespace, one that ignores the acknowledgement, a version gate always open, one never open, and notes without the warning each failed these cases before this record was written.
* `scripts/test-package-chart.sh` requires the packaged 9.9.9 chart to refuse the quickstart values with the workers beside ploegd and to render them with `executor.namespace` set.
* `scripts/test-quickstart.sh` requires `QUICKSTART_WORKER_NAMESPACE` to write the namespace, its label, the two Secrets before the install, the install value, the logs namespace and the final Job check, and refuses the release namespace, a name that is not a namespace and a page whose install line moved; dropping any one of those from `scripts/quickstart.sh` failed it. The Quickstart workflow's `checkout-chart` leg runs it on kind and requires the Run to end `done` with its Jobs in `ploeg-workers` under `enforce: restricted`; a local run of that leg against `v0.2.0-rc.83` on 2026-10-28 claimed and completed the Run that way.
* `go test ./internal/compat/` sees two added optional properties; the behavioural break is recorded in `docs/contracts/breaking-changes.yaml` under this record.

## Pros and Cons of the Options

### `executor.namespace` with a non-empty default

* Good, because every install would get the boundary without a value.
* Bad, because every existing deployment would break on its next 0.2 upgrade: the worker pods would start in a namespace without their Secrets, and with no notice inside the minor.

### The chart creates and labels the worker Namespace

* Good, because the Pod Security label would then be the chart's to set and check.
* Bad, because a Helm-owned Namespace is deleted with everything in it on `helm uninstall`, the installer's Secrets included, and GitOps tools that pre-create namespaces would fight the chart for the object.

### Keep one namespace and document TB2 as a pod boundary

* Good, because nothing changes.
* Bad, because the P1 Namespace row (`restricted` on the worker namespace) then binds the release namespace and every service the installer keeps there, and a namespace-scoped grant for ploegd still reaches the agents.

## Re-evaluation triggers

* Kubernetes or the chart gains a way to read a namespace's Pod Security label at render time, such as a `lookup` the chart adopts, so the restricted label can be checked instead of trusted.
* The F-21 fix (#338) removes the launcher's API token, which changes what the worker namespace's RBAC has to contain.
* An adopter needs worker workloads per team in separate namespaces.
* Two adopters report that the duplicated bootstrap or scaler Secret was rotated in one namespace and not the other.
* The quickstart pin reaches a release that carries `executor.namespace`, which moves the page to two namespaces.

## More Information

* Finding: [threat model F-44](../security/threat-model.md#findings), trust boundary TB2 and the level 0 diagram; issue [#354](https://github.com/ploeg-hq/ploeg/issues/354), refs [#15](https://github.com/ploeg-hq/ploeg/issues/15), [#34](https://github.com/ploeg-hq/ploeg/issues/34), [#338](https://github.com/ploeg-hq/ploeg/issues/338).
* Code: `ops/helm/ploeg/templates/_executor_namespace.tpl`, the worker templates, `networkpolicy.yaml`, `_networkpolicy.tpl` `ploeg.netpol.ploegdRule`, `agent-admission.yaml`, `NOTES.txt`; tests in `scripts/helm-golden.sh`, `scripts/check-executor-namespace.py`, `scripts/test-quickstart.sh`.
* Related: [ADR 0154](0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md) (the P1 guard), [ADR 0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md) (the NetworkPolicies).
* 2026-10-28: proposed.
