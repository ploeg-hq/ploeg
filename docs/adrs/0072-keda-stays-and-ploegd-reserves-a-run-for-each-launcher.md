---
status: proposed
date: 2026-10-06
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# KEDA stays, and ploegd reserves a Run for each launcher

## Context and Problem Statement

Today Ploeg starts a worker first and lets it claim afterwards. A KEDA ScaledJob polls a copy of the claim predicate in Postgres (`ops/helm/ploeg/templates/scaledjob.yaml:64-78`) and starts a launcher Job. The launcher creates a SandboxClaim (`pkg/sandboxlaunch/launcher.go`). The worker inside then asks ploegd for any eligible Run.

Four things are wrong with this:

* **A third copy of the claim predicate.** It is kept in step only by a comment, which is the duplication [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) rejected.
* **KEDA holds a database role in `pg_read_all_data`.** That role can read Run tokens and the audit log.
* **The wrong Run fails.** After its 600 s start timeout, the launcher calls `FailUnstartedRun` (`pkg/worker/unstarted.go:16-34`), which claims and fails whichever Run is pending next. In [Work Item 138](../research/2026-09-29-incident-work-item-138.md) that charged missing Kata capacity to an unrelated Work Item.
* **Warm pools are unusable.** A warm pod starts before any Run is bound to it.

An earlier version of this record, proposed on 2026-10-05, answered with push launch: ploegd would create each SandboxClaim itself and KEDA would leave the sandbox path. The owner asked for research into what KEDA provides before deciding.

Which component decides that a sandbox starts, and how does a full cluster stay a wait instead of a failure?

## Decision Drivers

* **ploegd holds no Kubernetes rights.** ploegd mints credentials and receives webhooks from the internet. It is the process whose blast radius matters most.
* **Ploeg knows as little as possible about the systems around it.** This is the owner's direction, and [ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md).
* **One claim predicate** (ADR-0010).
* **Missing capacity is a wait, attributed to the right Work Item, and never charged to another.**
* **No spend before a Run starts** ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)).

## Considered Options

* Keep KEDA. ploegd serves its demand signal over HTTP, and each launcher reserves a specific Run before creating its sandbox
* Push launch: ploegd creates SandboxClaims through an outbox and a reconciler, and KEDA leaves the sandbox path
* KEDA's gRPC external scaler served by ploegd
* Keep everything as it is

## Decision Outcome

Chosen option: "**Keep KEDA. ploegd serves its demand signal over HTTP, and each launcher reserves a specific Run before creating its sandbox**". It fixes the wrong-Run failure, the copied predicate and the over-broad database role. It keeps ploegd free of Kubernetes rights and knowledge, and it keeps KEDA's pause switch, gradual rollout, Job history and visibility.

### What changes

1. **Demand.**
   * ploegd serves `GET /api/v1/executor/demand?team=&role=`. The value is `min(pending, maxRunning − running − reserved)`, computed by the store's `PendingRuns`, which `TestClaimRoleAgreesWithPendingRuns` already ties to `ClaimRole`.
   * The ScaledJob trigger becomes KEDA's `metrics-api` scaler with bearer authentication, and the SQL predicate leaves the chart.
   * KEDA needs no database user. Its credential is a token that can only read demand.
2. **Reservation.**
   * A launcher calls `POST /api/v1/runs/reserve {team, role, launchRef}` before it creates a SandboxClaim. `launchRef` is an opaque string, the Job's UID, so ploegd learns nothing about Kubernetes.
   * The Run moves to `reserved`, and no Lease, budget authorization or credential is created. A reservation counts against `maxRunning`.
   * The launcher renews the reservation as a Lease is renewed. If the launcher dies, the reservation expires back to `pending` with no failure.
   * A 204 from reserve means no work: the launcher exits without creating a sandbox.
3. **Bind.**
   * The launcher passes the reservation id in the claim's `additionalPodMetadata`.
   * The worker reads it from a downwardAPI volume and binds that Run, which mints the Lease and capabilities as the claim does today.
   * A worker in a warm pod waits for the annotation. Warm pools become usable.
4. **Capacity is a wait.**
   * While the Sandbox's `PodScheduled` condition reads `Unschedulable`, the launcher keeps waiting. It reports `{reservation, reason, since}` to ploegd, which shows "waiting for capacity" on the reserved Work Item.
   * Template, image-pull and pod failures fail the **reserved** Run as `infra_node` ([ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)).
   * `FailUnstartedRun` is deleted.
5. **Alerts in Ploeg's chart:**
   * `PloegCapacityWaitLong`: a reservation waiting for capacity past a threshold;
   * `PloegReservationsExpiring`: launchers dying before bind;
   * `PloegScaledJobErrors`: `keda_scaled_job_errors_total` and `keda_scaler_detail_errors_total` for this release's ScaledJobs. KEDA reads a failing scaler as zero and starts nothing, so this alert is the only signal of that stall.

The `cronjob` executor keeps working. It reserves and binds through the same API. The `keda` executor without agent-sandbox uses the same demand endpoint and reserve call from its worker pod.

### Consequences

* **Good:**
  * ploegd gains no Kubernetes rights and no Kubernetes code. The launcher keeps its narrow, short-lived token.
  * The claim predicate exists once.
  * KEDA's credential shrinks from read-all to a demand-only token.
  * A full cluster is a wait on the right Work Item. No Run fails for it, and nothing is spent.
  * Warm pools become usable through bind, without push launch.
  * KEDA's operability stays: the pause annotation, gradual rollout, `kubectl get scaledjob` and admission webhooks.
* **Bad:**
  * Start latency keeps KEDA's poll interval (5 s in the homelab) plus a launcher pod start. This matters only once warm pools make Kata starts take seconds.
  * The store gains a `reserved` state with renewal and expiry. Push launch would have needed this too.
  * KEDA remains a dependency of the sandbox executor, though not of Ploeg core.

### Confirmation

* **Store.** A `pkg/store` regression test proves that a launcher timing out never fails an unrelated Run. Its old-code counterpart reproduces WI-138's Run 192. Other tests prove:
  * reserve, renew, expiry back to `pending`, and bind;
  * a reservation counts against `maxRunning` under concurrent reservers.
* **Demand.** A test proves `GET /api/v1/executor/demand` equals `min(pending, cap − running − reserved)`, and that it agrees with `ClaimRole`.
* **Launcher.** A `pkg/sandboxlaunch` test, against an `httptest` agent-sandbox fake, proves `Unschedulable` reports a wait and fails nothing, while a template fault fails the reserved Run.
* **Chart.**
  * The golden render shows the ScaledJob trigger as `metrics-api` with bearer authentication, and no SQL query.
  * `promtool` tests fire `PloegScaledJobErrors` on a rising error counter, and not on a flat one.
* **Deployed.** Once the release is deployed, KEDA's TriggerAuthentication references no database secret, and `ploeg_scaler` is no longer used by KEDA.

## Pros and Cons of the Options

### Push launch: ploegd creates SandboxClaims

* Good, because there is no launcher pod and no poll delay.
* Bad, because ploegd, the most exposed process, gains create and delete rights on SandboxClaims and must reach the Kubernetes API.
* Bad, because ploegd must own an outbox, a reconciler, agent-sandbox schema knowledge and a requeue state machine. That is Kubernetes knowledge in core.
* Bad, because KEDA's pause, rollout and visibility would have to be rebuilt.
* Bad, because the latency it saves is seconds, against minutes of Kata scheduling and boot.

### KEDA's gRPC external scaler served by ploegd

* Good, because it also removes the database credential.
* Bad, because `external-push` does not support ScaledJob, and plain `external` polls just like `metrics-api`.
* Bad, because it adds gRPC to the binary that mints credentials.

### Keep everything as it is

* Good, because nothing changes.
* Bad, because the wrong Run keeps failing on a full cluster, and KEDA keeps read-all database access.

## Re-evaluation triggers

* Ploeg must install on clusters where KEDA is not acceptable. A separate launcher component on the same reserve and bind API is built, still outside ploegd.
* Runs in flight exceed 200, so one launcher pod per Run costs real resources.
* Warm pools take Kata starts below 30 s, so KEDA's poll interval becomes the largest share of start latency.
* agent-sandbox drops `additionalPodMetadata` or warm-pod adoption.
* A KEDA defect in `accurate` scaling or `gradual` rollout affects this path.

## More Information

* Evidence: [KEDA, plug-and-play integrations, and card collection](../research/2026-10-06-keda-integrations-and-card-collection.md), §1. Also the [2026-10-05 research](../research/2026-10-05-substrate-language-and-run-bottlenecks.md), §3, which this decision overrules.
* Design and tasks: `openspec/changes/reserve-runs-for-launchers/`.
* Homelab follow-up, after the release: replace the KEDA-to-database NetworkPolicy opening with KEDA-to-ploegd, and narrow `ploeg_scaler` to the exporter's tables.
* 2026-10-05: proposed as "ploegd launches each Run's sandbox, and KEDA leaves the sandbox path".
* 2026-10-06: rewritten after research into KEDA. Push launch is withdrawn, and KEDA stays with a demand endpoint and Run reservations.
