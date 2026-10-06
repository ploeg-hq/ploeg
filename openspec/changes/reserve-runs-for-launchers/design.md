## Context

KEDA's `accurate` ScaledJob strategy computes `maxScale − pendingJobCount`, capped at `maxReplicaCount − runningJobCount`. A launcher waiting on an unschedulable sandbox counts as running. So KEDA itself does not flood a full cluster; the launcher's 600 s timeout and `FailUnstartedRun` cause the damage.

KEDA also gives Ploeg, without writing code:
* a pause switch;
* gradual rollout;
* Job history;
* `kubectl` visibility;
* admission webhooks;
* metrics.

Evidence: [research](../../../docs/research/2026-10-06-keda-integrations-and-card-collection.md) §1.

## Goals / Non-Goals

**Goals:**

* A full cluster is a wait on the right Work Item, and never a failed Run.
* One claim predicate, served by ploegd.
* KEDA holds no database credential.
* Warm pools become usable through bind.
* ploegd gains no Kubernetes rights or Kubernetes knowledge.

**Non-Goals:**

* Replacing KEDA. ADR-0072 keeps it.
* Warm pool sizing: a later change, possibly KEDA on the pool's `/scale`.
* Pod-bound service-account tokens for bind authentication (ADR-0025's trigger).

## Decisions

### Reservation is a Run state

`agent_runs.state` gains `reserved`, alongside these columns:
* `reservation_id` (128 random bits);
* `launch_ref` (an opaque string);
* `reserved_until`;
* `wait_reason` and `wait_since`.

A reservation renews like a Lease. The sweep returns an expired reservation to `pending`. A requeue keeps the Run's history clean, because no failed Run is ever recorded for it.

### Demand is computed, not queried by KEDA

The demand endpoint is the scaler's only input. It answers in KEDA's `metrics-api` JSON format, e.g. `{"demand": 3}` with `valueLocation: demand`.

Its token is a separate, demand-only bearer, issued like the worker bootstrap token. The endpoint holds no Run data.

### The launcher stays the Kubernetes client

`pkg/sandboxlaunch` keeps creating the SandboxClaim, now with the reservation id in `additionalPodMetadata`. It reads the Sandbox's `PodScheduled` condition (agent-sandbox v1.0.5) to tell a capacity wait from a fault.

The launcher's token stays narrow and lives only as long as one Run.

### Bind replaces claim inside reserved sandboxes

The worker reads `/etc/ploeg/reservation` from a downwardAPI volume, because environment variables are fixed at pod start. A warm pod gets its annotation on adoption.

Bind validates the reservation and its expiry, then performs today's claim transition for that specific Run.

## Risks / Trade-offs

* **Network path.** KEDA's operator must reach ploegd. The NetworkPolicy opening moves from KEDA-to-database to KEDA-to-ploegd. That is a smaller grant, but a new one.
* **Starvation.** A long capacity wait holds a slot. That is correct: the cluster is full. Only the alert and the person decide.
* **Multi-Run launchers.** None today, since there is one launcher per Run. The API allows only one active reservation per `launchRef`.
