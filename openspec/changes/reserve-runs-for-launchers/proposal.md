## Why

The sandbox Executor starts a worker first and lets it claim afterwards. A KEDA ScaledJob polls a copy of the claim predicate (`ops/helm/ploeg/templates/scaledjob.yaml:64-78`), using database credentials held by KEDA. It starts a launcher Job whose pod holds a Kubernetes token and creates a SandboxClaim (`pkg/sandboxlaunch/launcher.go`). The worker inside the sandbox then claims *any* eligible Run.

When the sandbox cannot be scheduled, `FailUnstartedRun` (`pkg/worker/unstarted.go:16`) claims and fails whichever Run is pending next. In Work Item 138 that turned missing Kata capacity into failed Runs charged to the wrong Work Item ([incident](../../../docs/research/2026-09-29-incident-work-item-138.md), factor 1). Warm pools cannot be used, because a warm pod starts before its Run exists and would claim outside the launcher.

[ADR-0072](../../../docs/adrs/0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md) decides that ploegd launches each Run's sandbox itself. This change implements that decision.

## What Changes

* **Dispatch.** A new dispatcher in ploegd selects the oldest eligible pending Run per Team under the existing capacity lock. It moves the Run to a new `launching` state, which counts against `maxRunning`, and records a launch in a new `run_launches` table. This is all one transaction.
* **Launch outbox.** An outbox worker creates one SandboxClaim per launch with a deterministic name, `ploeg-r<run>-a<attempt>`, and Ploeg-owned labels. `AlreadyExists` counts as success.
* **Bind instead of claim.** The worker in the sandbox reads its launch identifier from a downwardAPI volume and binds that Run. The Lease, inference capability and push right are minted at bind, not at dispatch. A worker in a warm pod waits until the identifier appears.
* **Reconciler.** About every 30 s ploegd lists its SandboxClaims by label and compares them with launch rows. It deletes orphans, requeues a launch whose claim vanished, enforces a launch deadline before bind, and reads each Sandbox's `PodScheduled` condition.
* **Waiting for capacity.** An unschedulable launch is recorded as waiting, with the scheduler's message. It is exposed through the operator API and `/metrics`, and never becomes a failed Run. Template, warm pool, image-pull and pod failures fail that specific Run as `infra_node`.
* **Warm pool sizing.** ploegd patches each SandboxWarmPool's `/scale` from configuration: default zero, optional minimum per time window.
* **Chart.** For sandbox Teams the chart drops the ScaledJob, the TriggerAuthentication and the launcher Job. It adds a Role and RoleBinding for ploegd in the sandbox namespace, a ResourceQuota, and an admission policy limiting claim pod metadata to Ploeg's label keys.
* **BREAKING (chart, sandbox Executor only).** `executor.type: sandbox` no longer renders KEDA objects. A cluster that relied on them for sandbox Teams must give ploegd network access to the Kubernetes API. The `keda` and `cronjob` executors are unchanged.

## Capabilities

### New Capabilities

* `sandbox-launch`: ploegd dispatches, launches, binds and reconciles each unattended Run's sandbox, and reports capacity waits.

### Modified Capabilities

* None in `openspec/specs/`. The executor contract (`docs/contracts/executor.md`) is updated in the same change: the scale-signal section stops applying to the sandbox Executor.

## Impact

* **Code:**
  * `pkg/store`: the `run_launches` table, the `launching` state, and the dispatch, bind and requeue methods.
  * `pkg/sandboxlaunch`: becomes a ploegd-side client with list and status reads.
  * `cmd/ploegd`: wires the outbox and reconciler loops.
  * `cmd/ploeg-worker` and `pkg/worker`: bind from the downwardAPI identifier; `FailUnstartedRun` is removed for sandbox Teams.
  * `pkg/httpapi`: the bind route and waiting-reason projection.
* **Migration:** the next file in `pkg/store/migrations/`.
* **Contracts:** `docs/contracts/run-api.v1.schema.json` gains the bind request (additive); `docs/contracts/operator-api.v1.schema.json` gains the waiting reason (additive optional field); `docs/contracts/executor.md` is updated.
* **Chart:** `ops/helm/ploeg/templates/{scaledjob,triggerauthentication,sandbox}.yaml`, `_sandbox.tpl`, the RBAC templates and the golden renders.
* **Metrics and alerts:**
  * `ploeg_launches_waiting{team}` and `ploeg_launch_wait_seconds`;
  * an alert when a launch waits longer than a configured limit ([alerts](../../../docs/ops/alerts.md)).
* **Outside this repository:** homelab-cluster removes the KEDA-to-database NetworkPolicy opening. It also either grants `ploeg_scaler` access to named tables only or retires it.
