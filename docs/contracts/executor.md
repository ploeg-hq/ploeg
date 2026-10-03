# Executor contract

This contract covers unattended worker launch against ploegd's run API. The
[chart](../../ops/helm/ploeg/) supports KEDA ScaledJobs and a CronJob executor
(`executor.type: cronjob`). The HTTP surface is the integration boundary.
Managed requests also require [worker control authentication](worker-control.md).
The [local demonstration](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/workflows/local-demo.md) runs Ploeg locally through the operator path, not this one.
Delegated workbench execution uses the separate [operator contract](README.md).

## The scale signal

Spawn workers for a team while its claimable count is above zero. Read it
by SQL: use the predicates in [the ScaledJob template](../../ops/helm/ploeg/templates/scaledjob.yaml),
including role eligibility and exclusion of operator-owned work. Do not
reconstruct them from the old queued-item example.

The HTTP scale signal, `GET /api/v1/queue/depth`, was removed on 2026-09-23.
Nothing consumed it: KEDA reads Postgres directly and the CronJob executor
polls by spawning. An executor without database credentials can poll by
spawning in the same way.

## Concurrency caps

A team can have a concurrency cap: the most Runs it may have running at
once. ploegd enforces it at claim time. A claim over the cap gets **204**,
the same as an empty queue, so the worker exits 0 and the pending Run
stays queued for a later claim. A Run stops counting when it finishes,
whether by outcome, expiry or sweep. The scale signal does not subtract
the cap, so an executor can spawn workers that find nothing. Keep the
executor's own ceiling at or below the cap; the chart clamps
`maxReplicaCount` to it.

Polling on a schedule is supported. An empty-handed spawn performs no model
inference, though it still consumes infrastructure resources.

## The run protocol

The spawned process (normally `ploeg-worker`, but anything speaking the run
API qualifies — schemas in [run-api.v1.schema.json](run-api.v1.schema.json)):

1. Send an authenticated claim with the worker's configured team and role.
   **204 means exit 0** immediately; no eligible work was claimed.
2. Renew the lease at TTL/3 via `POST /api/v1/runs/{token}/renew`; a 404
   means the lease is gone — kill the harness and stop.
3. Report progress via `.../checkpoint` (best-effort) and exactly one
   terminal `.../outcome` before exit (stuck requires a reason).

The Run token identifies the Run. Managed renewal, checkpoint and outcome
requests also require the returned signed control capability and worker identity.
Explicit legacy mode retains the old authentication behavior.

## Executor obligations

- **Never retry a failed run yourself** (`backoffLimit: 0` equivalent).
  Ploeg owns retries via lease expiry and outcome ingestion; a launcher
  retry would double-charge the attempt budget and split the audit trail.
- **Enforce a wall-clock backstop** (`activeDeadlineSeconds` equivalent,
  slightly above the lease TTL, backlog #52). The DB lease always expires
  first — the backstop only guarantees the process dies.
- **One spawn = one auditable run.** Don't reuse a process for a second
  claim.
- Give the worker the env contract (see `ploeg.workerPodTemplate` in the
  chart): `PLOEG_API_URL`, `PLOEG_TEAM`, repo/forge/LLM wiring, and the
  `PLOEG_HARNESS*` selection.

## Explicit non-obligations

- **Mutual exclusion** — the lease (`FOR UPDATE SKIP LOCKED` + single
  live lease per item) provides it; spawning too many workers is safe.
- **Crash reporting** — ploegd's sweeper is the crash detector; a worker
  that dies hard simply stops renewing.
- **Payload delivery** — workers claim at boot (KEDA cannot inject per-row
  payloads, kedacore/keda#5100; every executor inherits the convention).

## The agent-sandbox executor (experimental)

`executor.type: sandbox` runs each Run in a
[kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)
v1.0.x `Sandbox`. It is the second executor named by proposed
[ADR-0032](../adrs/0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md);
its requirements are the OpenSpec spec `sandbox-executor`.

A deployment can move one team at a time: with `executor.type` at `keda`, a
team's `executorType: sandbox` runs only that team's workloads this way, and
the other teams keep their worker ScaledJobs unchanged. The reverse, a team
set to `keda` under `executor.type: sandbox`, works too. `executorType` is
refused under the CronJob executor.

The ScaledJob and its scale signal are unchanged. Its pod becomes a launcher,
`ploeg-worker sandbox-launch`, which creates one cold `SandboxClaim` for its
Job and waits until the claim reports `Finished`, disappears, or the launcher's
deadline passes. On `Finished` it deletes the claim, so the agent-sandbox
controller cannot recreate a finished worker pod. It never creates a second
claim and never deletes a running one. The worker pod comes from a chart-owned
`SandboxTemplate` whose pod is the same worker pod template the ScaledJob uses,
so the run protocol above is unchanged.

The obligations hold through three backstops: the worker pod's
`activeDeadlineSeconds`; the claim's `shutdownTime` (the deadline plus
`executor.sandbox.shutdownMarginSeconds`) with `shutdownPolicy: Delete` and a
`ttlSecondsAfterFinished`; and the claim's owner reference to the launcher's
Job, which lets Job garbage collection remove the claim, its Sandbox and its
pod. The Lease still expires first. A claim that is not `Ready` within
`executor.sandbox.startTimeoutSeconds` is deleted by its launcher, so a claim
the controller cannot serve does not hold the Team's slot until the shutdown
deadline. The launcher then claims one of its Team and Role's Runs with the
worker bootstrap credential and reports it `failed` with `infra_node` and the
controller's reason, so the Work Item follows the infra retry budget
(ADR-0021) instead of staying queued. The launcher Job fails either way.

Only the launcher holds a Kubernetes API token. Its Role allows `create`,
`get` and `delete` on `sandboxclaims` in its namespace. The template sets
`envVarsInjectionPolicy: Disallowed`, so a claim cannot add environment to the
worker, and `networkPolicyManagement: Unmanaged` by default, so the cluster's
own policies, selected by the worker's labels, keep applying. Setting
`executor.sandbox.networkPolicy` to ingress and egress rules switches the
template to `Managed` with exactly those rules, an egress allowlist for
clusters that have none; include DNS, ploegd, the model gateway and the forge.
agent-sandbox's own secure default is never used, because it blocks all three.

Prerequisites, installed outside this chart: agent-sandbox v1.0.x with its
extensions, and the RuntimeClass named in `executor.sandbox.runtimeClassName`
when one is set. Qualify that RuntimeClass with the daemonless worker pod
shape — `ploeg.workerPodTemplate`, the same template the ScaledJob uses, run
under the RuntimeClass until one Run reaches a terminal outcome. The privileged
DinD sidecar is not part of that qualification; it belongs only to harnesses
that build inside a container. Warm pools are not supported yet: a warm pod
would start its worker and claim a Run before any `SandboxClaim` exists.
