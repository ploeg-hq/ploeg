## Context

The executor today is the chart: a KEDA ScaledJob (or a CronJob) per (Team,
Role) whose pod is `ploeg.workerPodTemplate`. The worker claims at boot, renews
its Lease, reports one Outcome and exits (`docs/contracts/executor.md`).
Proposed [ADR-0032](../../../docs/adrs/0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)
names agent-sandbox v1.0.x (`v1beta1`) as the second executor's runtime; the
source research is in
[the 2026-09-26 landscape dossier](../../../docs/research/2026-09-26-agent-orchestration-landscape.md) §6.

Facts from agent-sandbox v1.0.4 source that shape the design:

- `SandboxClaim.spec.warmPoolRef` is required; there is no claim against a
  template alone. A pool with `replicas: 0` makes every claim cold.
- The controller copies `podTemplate.spec` verbatim, so `secretKeyRef`,
  native sidecars, `runtimeClassName` and `activeDeadlineSeconds` pass through.
- A pod that reaches Succeeded or Failed is never recreated, but a **deleted**
  pod is recreated while its Sandbox lives.
- `ttlSecondsAfterFinished` keys on the claim's mirrored `Finished` condition,
  which exists only for a terminal pod, so it needs `restartPolicy: Never`
  (already set by the worker pod template).
- Defaults to override: `shutdownPolicy` defaults to `Retain` (leaks claims);
  a Managed NetworkPolicy with no rules applies a secure default that blocks
  RFC1918 egress and forces public DNS, which would cut the worker off from
  ploegd, LiteLLM and the forge.
- There is no Job-like mode and KEDA cannot create claims; something Ploeg owns
  must.

## Goals / Non-Goals

**Goals:** Runs execute in agent-sandbox pods with optional RuntimeClass
isolation; every executor obligation holds (no launcher retries, a wall-clock
backstop, one spawn is one Run, the worker claims at boot); the claim predicate
is not copied a fourth time.

**Non-Goals:** warm pools, credential injection, installing agent-sandbox, a Go
Executor interface.

## Decisions

**Keep KEDA as the loop; swap the Job's pod for a launcher.** The ScaledJob and
its trigger stay byte-identical, so the claim predicate keeps its three copies
(store, `TestClaimRoleAgreesWithPendingRuns`, the scaler query) and no fourth.
The launcher blocks until its claim finishes, so KEDA's count of running Jobs
equals live Runs. *Alternative:* a claim-creating loop in ploegd. Rejected for
now: it gives ploegd Kubernetes RBAC and duplicates the scaler.

**Cold claims against a zero-replica pool.** Keeps worker-claims-at-boot
without touching the worker. *Alternative:* warm pools. Deferred: a warm pod
boots and claims a Run before it is claimed itself, and pool scale-down can
delete a Ready member mid-Run (backlog #50).

**Plain REST, no client-go.** The launcher needs three calls (create, get,
delete) on one resource. The in-cluster token and CA are read from the standard
service-account mount. *Alternative:* the generated clientset. Rejected: it is
one module with the whole repository's dependency graph, against the thin-glue
rationale of ADR-0002.

**Three backstops.** Pod `activeDeadlineSeconds` (the process dies), claim
`shutdownTime` = now + deadline + margin with `shutdownPolicy: Delete` (the
objects go even if the launcher is gone), and the claim's owner reference to
the launcher's Job (Job history limits garbage-collect the claim). The Lease
still expires first (R2).

**Delete the claim on `Finished`.** Waiting for `ttlSecondsAfterFinished` leaves
a window in which a deleted terminal pod would be recreated and claim a second
Run. Explicit deletion closes it; the TTL stays as the fallback.

**Never delete the claim on the launcher's own SIGTERM.** A drained launcher
node must not kill a healthy Run; the backstops clean up instead.

**Worker pod template reused unchanged.** The SandboxTemplate's `podTemplate`
is `ploeg.workerPodTemplate` parsed with `fromYaml`, plus
`activeDeadlineSeconds`, `dnsPolicy: ClusterFirst` and an optional
`runtimeClassName`. Labels, including `ploeg.webgrip.dev/privileged-dind`,
reach the pod, so the Kyverno PolicyException keeps matching. The ScaledJob
itself stops carrying the dind label in sandbox mode, because its launcher pod
is not privileged.

**`networkPolicyManagement: Unmanaged`.** The cluster's existing policies,
selected by the worker's labels, keep applying; the controller creates none.

## Risks / Trade-offs

- [A deleted worker pod is recreated and claims again] → the launcher deletes
  the claim on `Finished`; ploegd's concurrency cap and Leases bound any extra
  claim to at most one wasted spawn.
- [Two pods and two scheduling latencies per Run] → accepted for cold claims;
  warm pools are the later fix.
- [Kata plus a privileged DinD native sidecar is untested] → `runtimeClassName`
  defaults to empty; qualify per cluster before setting it.
- [agent-sandbox moves fast] → the chart targets the `v1beta1` API only, and
  the cluster pins a release.
- [RBAC] the launcher's token can create claims in the namespace. It cannot
  read Secrets or pods; the claim's pod comes from a chart-owned template, and
  `envVarsInjectionPolicy: Disallowed` stops a claim from adding environment.

## Migration Plan

Opt-in per deployment: `executor.type: sandbox` after the agent-sandbox
controller and extensions are installed. Rollback is switching back to `keda`;
in-flight Runs finish under their Leases, and remaining claims are removed by
their backstops.

## Open Questions

- Whether a Kata RuntimeClass admits the privileged DinD sidecar on the homelab
  nodes. To be qualified in `webgrip/homelab-cluster`, not here.
- ADR-0032 is proposed; this change builds against it. The executor is opt-in
  (`keda` stays the default) and documented as experimental until ADR-0032 is
  accepted and the executor is qualified on a cluster.
