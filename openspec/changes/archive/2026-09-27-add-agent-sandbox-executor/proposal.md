## Why

Every Run today is a plain Kubernetes Job pod whose only isolation is the
container runtime the node happens to use. [ADR-0032](../../../docs/adrs/0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)
(proposed) chooses kubernetes-sigs/agent-sandbox v1.0.x as the runtime for the
second Executor, because ADR-0005's trigger "`agents.x-k8s.io` graduates past
alpha" fired on 2026-08-28. The same CRDs already back Vloer's
`SandboxWorkspaces`, Omnigent, NVIDIA OpenShell and kagent v0.10, so an
Executor on them gives Runs RuntimeClass isolation (Kata or gVisor) and a path
to warm pools without Ploeg owning a runtime. Backlog #58 still pins the
removed `v1alpha1`; this change replaces that plan.

## What Changes

- A third executor type, `executor.type: sandbox`, beside `keda` and
  `cronjob`. It keeps the KEDA ScaledJob and its claim predicate unchanged, but
  the Job's pod becomes a **launcher** that creates one cold `SandboxClaim`
  and waits for it; the Run's worker pod is created by the agent-sandbox
  controller from a chart-rendered `SandboxTemplate`.
- A new `ploeg-worker sandbox-launch` subcommand and a `pkg/sandboxlaunch`
  package that speak to the Kubernetes API over plain REST with the in-cluster
  service account. No `client-go` dependency.
- Chart: per (Team, Role) a `SandboxTemplate` (the existing worker pod
  template plus `activeDeadlineSeconds`, optional `runtimeClassName`,
  explicit cluster DNS, unmanaged NetworkPolicy, claim env injection
  disallowed) and a `SandboxWarmPool` with zero replicas; a launcher
  ServiceAccount with a Role limited to `sandboxclaims` create, get and delete.
- A golden render for the sandbox path and an executor-contract section.

## Capabilities

### New Capabilities
- `sandbox-executor`: launching one Run per claimable count through an
  agent-sandbox `SandboxClaim`, with the executor obligations of
  `docs/contracts/executor.md` preserved.

### Modified Capabilities
- none. The run API, the claim predicate and the worker are unchanged.

## Non-goals

- **Warm pools.** A warm pod boots its worker before any claim exists and would
  claim a Run at boot; supporting warm pools needs the worker to detect its own
  claim first. Deferred.
- **Credential injection** (a placeholder key in the sandbox, the real key
  swapped at an egress proxy). A separate change.
- **Installing agent-sandbox** or changing production desired state. The
  controller and CRDs are installed through `webgrip/homelab-cluster`.
- **A Go Executor interface** (backlog #46 stays "done at the Helm layer").

Checked against `design.md` §2: this adds no board UI, persistent agent, model
serving, grooming semantics or connector matrix.

## Impact

- Seam: **executor** only. The run API, store predicates, harness adapters and
  worker behaviour are untouched; rules R2, R3 and R6 hold because the worker
  still claims, renews and reports exactly as under the ScaledJob.
- Code: `pkg/sandboxlaunch` (new), `cmd/ploeg-worker` (subcommand wiring).
- Chart: `templates/scaledjob.yaml`, a new `templates/sandbox.yaml`,
  `values.yaml`, `values.schema.json`, a CI values file and golden.
- Docs: `docs/contracts/executor.md`; backlog #58 is superseded by this change.
- Cluster prerequisite, out of this repository: agent-sandbox v1.0.x with its
  extensions installed, and a RuntimeClass if one is named.
