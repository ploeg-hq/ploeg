## Context

The sandbox Executor today goes in five hops:

1. KEDA polls Postgres every 30 s.
2. It starts a ScaledJob launcher pod.
3. The launcher creates a SandboxClaim.
4. agent-sandbox starts a Kata pod.
5. The worker in that pod claims any eligible Run.

The claim predicate exists in `pkg/store` and again in the ScaledJob queries. Nothing ties a sandbox to a Run until the worker claims. That is why a start timeout fails the next pending Run, and why warm pools are unusable.

agent-sandbox v1.0.5 provides what a push design needs:
* SandboxClaims that adopt warm pods and apply `additionalPodMetadata` in place;
* a scale subresource on SandboxWarmPool;
* a Sandbox `PodScheduled` condition that mirrors the Pod's, with reason `Unschedulable` and the scheduler's message.

Evidence: [research](../../../docs/research/2026-10-05-substrate-language-and-run-bottlenecks.md) §3.

## Goals / Non-Goals

**Goals:**

* One claim predicate, owned by `pkg/store`.
* A sandbox is tied to a specific Run from the moment it is requested.
* Missing capacity is reported as a wait on the right Work Item and never fails a Run.
* No spend, Lease or credential before the worker binds.
* No Executor pod holds a Kubernetes token, and KEDA holds no Ploeg database credential.
* Warm pools become usable.

**Non-Goals:**

* Changing the `keda` or `cronjob` executors.
* Informers or watches. List polling is enough below 200 Runs in flight (ADR-0072 trigger).
* Pod-bound service-account tokens for bind authentication. That is ADR-0025's re-evaluation trigger and a separate change.
* Placement policy: readers on gVisor or runc, writers on Kata. That is chart values per Role, which already exist.

## Decisions

### Launch state is a Run state plus a launch row

`agent_runs.state` gains `launching`. A new `run_launches` table holds:

* `run_id`, `attempt`, and a unique `claim_name`;
* `state`: `requested`, `created`, `waiting_capacity`, `bound` or `released`;
* `waiting_reason`, `waiting_since`;
* `next_attempt_at`, `deadline_at`;
* timestamps.

Keeping the launch apart from the Run keeps the Run's Outcome history clean. A requeued launch is a new attempt row, not a failed Run.

Alternative considered: put the launch columns on `agent_runs`. Rejected, because requeues would overwrite their own history.

### Outbox, then create

The dispatch transaction only writes rows. An outbox worker in ploegd picks `requested` launches with `FOR UPDATE SKIP LOCKED`, creates the claim, and marks the row `created`.

This is the same shape ADR-0060 chose for publications. If ADR-0060's outbox lands first, launches use it; otherwise `run_launches` is its own small outbox and is merged later.

Alternative considered: create the claim after commit, in the request goroutine. Rejected, because a crash in between strands the Run in `launching` until a deadline.

### Bind replaces claim for launched Runs

The run API gains `POST /api/v1/runs/bind` with the launch identifier. Bind:
* checks that the launch is `created` or `waiting_capacity` and that the attempt matches;
* moves the Run to `running`;
* creates the Lease and mints capabilities exactly as claim does today.

Worker authentication is unchanged: a bootstrap token plus the launch identifier. The identifier is unguessable (128 random bits) and single-use.

The worker reads the identifier from a downwardAPI volume, not an environment variable. Environment variables are fixed at pod start; a warm pod receives the annotation only when it is adopted.

### Capacity waits are read from the Sandbox, not the Pod

The reconciler reads the claim's `status.sandbox.name`, then that Sandbox's `PodScheduled` condition. ploegd needs no Pod permissions.

The launch is requeued after `launch.requeueAfter` (default 20 minutes): the claim is deleted and the Run returns to `pending` with backoff, so a slot is not held indefinitely.

### Client: raw HTTP with typed structs

`pkg/sandboxlaunch` keeps its 321-line raw client and adds list by label selector and a Sandbox status read. It authenticates with ploegd's mounted service-account token and the cluster CA. Tests use `httptest` fakes.

A contract test decodes fixtures captured from agent-sandbox v1.0.5 CRDs, so a schema change fails CI.

Alternative considered: `sigs.k8s.io/agent-sandbox/clients/k8s`. Rejected for now: it brings controller-runtime, client-go, OpenTelemetry and gRPC into the binary that mints credentials.

### Warm pool size is ploegd configuration

`teams[].roles[].warmPool.min`, with optional `windows`, sets the target, and the reconciler patches `/scale` to it. ploegd is the only writer.

The default is zero. On a single Kata node a warm pod holds capacity a Run may need.

## Risks / Trade-offs

* **ploegd gains Kubernetes write authority.**
  * Mitigation: namespace-scoped Role, no pod, Secret or exec verbs.
  * Claims can only reference chart-owned templates.
  * An admission policy restricts claim pod metadata.
  * A ResourceQuota caps claims and pods independently of ploegd.
* **The reconciler is a new loop that can misjudge.** For example, it could delete a claim during a slow bind.
  * Mitigation: it deletes only claims whose launch is `released` or whose Run is terminal, and requeues only after the claim is confirmed absent on two consecutive lists.
* **agent-sandbox API changes.**
  * Mitigation: the CRD fixture contract test, and a pinned supported version in the chart's `Chart.yaml` annotations.
* **Requeue loops on permanent capacity shortage.**
  * Mitigation: backoff on `next_attempt_at`, the waiting metric and the alert. The Work Item shows the wait, so a person sees it.
* **Migration of existing clusters.**
  * Mitigation: the chart change applies to `executor.type: sandbox` only.
  * Pending Runs are unaffected, because their state is still `pending`.
  * A `running` Run claimed through the old path completes as before.
