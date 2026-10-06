## Why

The sandbox Executor starts a launcher Job through a KEDA ScaledJob. KEDA polls a copy of the claim predicate in Postgres (`ops/helm/ploeg/templates/scaledjob.yaml:64-78`) through a role in `pg_read_all_data`.

When the sandbox cannot be scheduled, the launcher gives up after 600 s, and `FailUnstartedRun` (`pkg/worker/unstarted.go:16-34`) claims and fails whichever Run is pending next. In Work Item 138 that charged missing Kata capacity to an unrelated Work Item ([incident](../../../docs/research/2026-09-29-incident-work-item-138.md), factor 1). Warm pools cannot be used, because a warm pod starts before any Run is bound to it.

[ADR-0072](../../../docs/adrs/0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md) keeps KEDA. It fixes these problems with a demand endpoint and Run reservations, and gives ploegd no Kubernetes rights.

## What Changes

* **Demand endpoint.** `GET /api/v1/executor/demand?team=&role=` returns `min(pending, maxRunning − running − reserved)`, computed by `store.PendingRuns`.
* **Chart.** The ScaledJob trigger becomes KEDA's `metrics-api` scaler with bearer authentication. The SQL query and the TriggerAuthentication's database secret leave the chart.
* **Reservation.** `POST /api/v1/runs/reserve {team, role, launchRef}` moves the oldest eligible pending Run to `reserved` and returns its reservation id, or returns 204. It creates no Lease, budget or credential. Renewal and expiry back to `pending` follow the Lease pattern.
* **Wait report.** `POST /api/v1/runs/reservations/{id}/wait {reason, since}` records a capacity wait on the reserved Work Item.
* **Bind.** `POST /api/v1/runs/bind {reservation}` is called by the worker with the id read from a downwardAPI volume. It mints the Lease and capabilities as the claim does today.
* **Launcher.**
  * It reserves before creating its SandboxClaim, and passes the id in `additionalPodMetadata`.
  * It waits while the Sandbox reports `PodScheduled=Unschedulable`.
  * It fails only the reserved Run on a real fault.
* **Worker.** It binds the Run it was launched for. `FailUnstartedRun` is deleted.
* **Alerts in the chart:** `PloegCapacityWaitLong`, `PloegReservationsExpiring` and `PloegScaledJobErrors`.
* **BREAKING (chart):** the scaler no longer reads Postgres. A deployment must give KEDA's operator network access to ploegd's demand endpoint, and a bearer token Secret.

## Capabilities

### New Capabilities

* `run-reservation`: an executor reserves a specific Run before starting its sandbox, reports capacity waits, and binds that Run from inside the sandbox.

### Modified Capabilities

* None in `openspec/specs/`. `docs/contracts/executor.md` is updated in the same change: the scale signal becomes the demand endpoint.

## Impact

* **Code:**
  * `pkg/store`: the `reserved` state; reserve, renew, wait, bind and release; expiry in the sweep.
  * `pkg/httpapi`: the routes.
  * `pkg/sandboxlaunch`.
  * `cmd/ploeg-worker` and `pkg/worker`.
* **Migration:** the next file in `pkg/store/migrations/`.
* **Contracts:** additive changes to `run-api.v1.schema.json`.
* **Chart:** `scaledjob.yaml`, `triggerauthentication.yaml`, `_sandbox.tpl`, `prometheusrule.yaml` and the golden renders.
* **Outside this repository:** homelab-cluster replaces the KEDA-to-database NetworkPolicy opening with KEDA-to-ploegd, and narrows `ploeg_scaler` to the exporter's tables.
