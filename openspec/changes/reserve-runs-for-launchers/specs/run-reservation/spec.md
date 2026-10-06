# Sandbox launch

## ADDED Requirements

### Requirement: ploegd dispatches a specific Run under the Team's capacity cap

For a Team on the sandbox Executor, ploegd SHALL select the oldest eligible pending Run across the Team's Roles, ordered by Work Item age. In the same transaction that holds the Team's capacity lock, it SHALL move that Run to `launching` and record a launch with an attempt number.

A Run in `launching` SHALL count against the Team's `maxRunning`.

Dispatch SHALL NOT create a Lease, authorize spend or mint a credential.

#### Scenario: Concurrent dispatch respects the cap

* **WHEN** two dispatcher transactions run concurrently for a Team with `maxRunning: 1` and two pending Runs
* **THEN** exactly one Run moves to `launching`, and the other stays `pending`

#### Scenario: A launching Run holds a slot

* **WHEN** a Team with `maxRunning: 1` has one Run in `launching`
* **THEN** no further Run of that Team is dispatched until the launch is bound, released or requeued

### Requirement: Each launch creates one SandboxClaim idempotently

ploegd SHALL create one SandboxClaim per launch:
* named `ploeg-r<run>-a<attempt>`;
* labelled with the Run, the launch and `managed-by=ploegd`;
* referencing a chart-owned SandboxTemplate;
* optionally referencing a SandboxWarmPool.

A creation that returns `AlreadyExists` SHALL count as success.

A launch committed but not yet created SHALL be created after a ploegd restart.

#### Scenario: Crash between commit and creation

* **WHEN** ploegd stops after the dispatch transaction commits and before the SandboxClaim is created
* **THEN** on restart the outbox creates exactly one SandboxClaim for that launch

#### Scenario: Repeated creation

* **WHEN** the outbox retries a launch whose SandboxClaim already exists
* **THEN** no second claim is created and the launch is marked created

### Requirement: The worker binds the Run it was launched for

A worker started by a sandbox launch SHALL read its launch identifier from a downwardAPI volume and bind that Run through the run API. ploegd SHALL mint the Lease, inference capability and push right at bind.

A worker whose identifier is absent SHALL wait for it, up to the launch deadline, and SHALL NOT claim any other Run.

#### Scenario: A warm pod is adopted

* **WHEN** a SandboxClaim adopts a warm pod and the launch annotation appears on it
* **THEN** the worker binds the launched Run and starts its harness

#### Scenario: A bind for the wrong Run

* **WHEN** a worker presents a launch identifier whose launch is released or belongs to another attempt
* **THEN** the bind is refused, and no Lease or credential is minted

### Requirement: Missing capacity is a wait, not a failure

When the Sandbox of a launch reports `PodScheduled=False` with reason `Unschedulable`, ploegd SHALL record the launch as waiting for capacity:
* with the scheduler's message and the time the wait began;
* exposed on the Work Item through the operator API and in `/metrics`.

It SHALL NOT record a failed Run for that wait.

A launch that waits past the configured requeue limit SHALL have its SandboxClaim deleted and the Run returned to `pending` with backoff.

Template, warm pool, image-pull and pod failures SHALL fail that launch's Run with failure reason `infra_node`.

#### Scenario: Unschedulable sandbox

* **WHEN** a launched Sandbox stays unschedulable for 15 minutes
* **THEN** the Work Item shows "waiting for capacity" with the scheduler's message, no Run is failed, and `ploeg_launches_waiting` counts the launch

#### Scenario: Template missing

* **WHEN** a launch's SandboxClaim reports `TemplateNotFound`
* **THEN** that launch's Run fails with `infra_node`, and no other Run is affected

### Requirement: The reconciler removes orphans and requeues lost launches

About every 30 seconds ploegd SHALL list SandboxClaims labelled `managed-by=ploegd` in the sandbox namespace and compare them with launch rows:
* It SHALL delete a claim whose launch is released, or whose Run is terminal.
* It SHALL return a created launch whose claim no longer exists to `pending`, without failing its Run.
* It SHALL release a launch not bound within the launch deadline.

#### Scenario: Claim deleted out of band

* **WHEN** a created launch's SandboxClaim is deleted by someone other than ploegd before bind
* **THEN** the reconciler returns the Run to `pending` and records no failed Run

### Requirement: ploegd's Kubernetes authority is bounded

ploegd SHALL hold Kubernetes permissions only in the sandbox namespace:
* create, get, list, watch and delete on SandboxClaims;
* get, list and watch on Sandboxes;
* get and patch on SandboxWarmPool `/scale`.

The chart SHALL render no ScaledJob, TriggerAuthentication or launcher Job for sandbox Teams. It SHALL render a ResourceQuota for the sandbox namespace, and an admission policy that rejects SandboxClaims carrying pod metadata keys outside Ploeg's own label prefix.

#### Scenario: Golden render

* **WHEN** the chart is rendered with `executor.type: sandbox`
* **THEN** the output contains the Role, RoleBinding, ResourceQuota and admission policy, and contains no ScaledJob or TriggerAuthentication
