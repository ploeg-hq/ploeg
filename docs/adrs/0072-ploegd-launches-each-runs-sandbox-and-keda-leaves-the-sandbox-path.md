---
status: proposed
date: 2026-10-05
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# ploegd launches each Run's sandbox, and KEDA leaves the sandbox path

## Context and Problem Statement

Today Ploeg starts a worker first and lets it claim afterwards. A KEDA ScaledJob polls a copy of the claim predicate in Postgres (`ops/helm/ploeg/templates/scaledjob.yaml:64-78`) and starts a launcher Job. The launcher creates a SandboxClaim (`pkg/sandboxlaunch/launcher.go`). The worker inside then asks ploegd for any eligible Run.

This design has five consequences:

* **A third copy of the claim predicate.** It is kept in step only by a comment, which is the duplication [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) rejected.
* **Empty pods.** Pods that find nothing exit 0, and the cap has to be clamped into `maxReplicaCount`.
* **KEDA holds database credentials.** The homelab role behind them is in `pg_read_all_data`.
* **A launcher pod holds a Kubernetes token.**
* **The wrong Run fails.** `FailUnstartedRun` (`pkg/worker/unstarted.go:16`) claims and fails whichever Run is pending next. In [Work Item 138](../research/2026-09-29-incident-work-item-138.md) that turned 20 minutes of missing Kata capacity into failed Runs on the wrong Work Item.

Who decides which Run gets a sandbox, and when: the cluster autoscaler or ploegd?

## Decision Drivers

* **One claim predicate,** owned by `pkg/store` (ADR-0010).
* **Missing capacity is a wait, not a failure,** and a delay must never be charged to an unrelated Work Item.
* **Management authority stays in the control plane** ([ADR-0025](0025-management-authority-stays-in-the-control-plane.md)), and no workload holds credentials it does not need.
* **No spend before start.** A Run that never starts must never have authorized spend ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)).
* **Launches survive crashes,** with no new deployable ([ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)).

## Considered Options

* ploegd launches through a Postgres launch outbox, and KEDA leaves the sandbox path
* ploegd launches, and KEDA keeps sizing SandboxWarmPools
* Keep spawn-then-claim through KEDA, and fix `FailUnstartedRun`
* A separate scheduler service, in Go or Rust

## Decision Outcome

Chosen option: "**ploegd launches through a Postgres launch outbox, and KEDA leaves the sandbox path**". It removes the duplicated predicate, the empty pods, KEDA's database credentials and the launcher's Kubernetes token in one change, and lets ploegd tell a capacity wait from a failure.

### The launch path

* **Dispatch.** A dispatcher transaction:
  * takes the Team's capacity lock (`pkg/store/team_capacity.go`);
  * selects the oldest eligible pending Run across Roles with `FOR UPDATE SKIP LOCKED`;
  * moves it to `launching`, which counts against `maxRunning`;
  * inserts a `run_launches` row in the next migration.

  No Lease, budget authorization or credential is created at dispatch.
* **Create.** After commit, an outbox worker in ploegd creates a SandboxClaim named `ploeg-r<run>-a<attempt>`, labelled with the Run, the launch and `managed-by=ploegd`. `AlreadyExists` counts as success, so creation is idempotent.
* **Bind.** The worker in the sandbox reads its launch identifier from a downwardAPI volume. The claim becomes a *bind* of that specific Run, which mints the Lease and capabilities as today. A worker in a warm pod waits for the annotation before it binds.
* **Recover.**
  1. The outbox replays unsent launches.
  2. A reconciler (about every 30 s) lists claims labelled `managed-by=ploegd`, deletes orphans and requeues a launch whose claim vanished, without failing a Run.
  3. After bind, the Lease TTL and the sweep remain the crash detector.
* **Wait for capacity.** When the Sandbox's `PodScheduled` condition reads `Unschedulable` (agent-sandbox v1.0.5), the launch is shown as waiting for capacity, with the scheduler's message, and no failed Run is recorded. Template, warm pool, image-pull and pod failures fail that Run as `infra_node` ([ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)).
* **Size warm pools.** ploegd patches each SandboxWarmPool's `/scale`. The default is zero; minimum warm counts per time window are configuration.
* **Authority.** ploegd gets a Role in the sandbox namespace only:
  * `sandboxclaims`: create, get, list, watch, delete
  * `sandboxes`: get, list, watch
  * `sandboxwarmpools/scale`: get, patch
  * no pods, Secrets or exec.

  A ResourceQuota on that namespace is the cluster-side ceiling. An admission policy restricts claim pod metadata to Ploeg's own label keys.
* **Client.** The existing raw-HTTP client is extended with list-by-label and status reads. The upstream Go clientset is not imported: it lives inside the `sigs.k8s.io/agent-sandbox` module, which pulls in controller-runtime, client-go, OpenTelemetry and gRPC. This partly revises [ADR-0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)'s intent to use the generated clientset.

The `keda` and `cronjob` executors stay for clusters without agent-sandbox. They are not extended, and a later record sets their deprecation date.

The full design and tasks are in the OpenSpec change `openspec/changes/launch-sandboxes-from-ploegd/`.

### Consequences

* **Good:**
  * The claim predicate exists once.
  * Surplus pods and the `maxReplicaCount` clamp are gone.
  * KEDA no longer needs Ploeg database credentials or a NetworkPolicy path to the database.
  * No executor pod holds a Kubernetes token.
  * Missing capacity is reported as a wait on the right Work Item.
  * Warm pools become usable, because a warm worker binds a specific Run.
  * Dispatch can order by Work Item age across Roles, so one Role's queue no longer starves another's.
* **Bad:**
  * ploegd gains Kubernetes write authority over SandboxClaims, a new privilege for the control plane. It is bounded to one namespace and to chart-owned templates.
  * ploegd gains two loops (outbox and reconciler) and a migration, and must be tested against agent-sandbox's CRD schema version.
  * The sandbox executor now requires ploegd to reach the Kubernetes API; it is no longer only reached by pods.

### Confirmation

* **Store.**
  * A regression test in `pkg/store` proves that concurrent dispatch never exceeds `maxRunning`, and that `launching` counts against it.
  * A test that kills the process between dispatch commit and claim creation proves the outbox replays the launch exactly once.
* **Launcher.**
  * `pkg/sandboxlaunch` tests run against `httptest` fakes of the agent-sandbox API.
  * A contract test against the v1.0.5 CRD schemas covers creation, `AlreadyExists`, list-by-label and `PodScheduled=Unschedulable`.
* **Waiting.** A test proves an unschedulable launch records no failed Run and leaves the Run `launching` with a waiting reason.
* **Chart.**
  * The golden renders for the sandbox executor contain no ScaledJob and no TriggerAuthentication.
  * They contain the Role, RoleBinding and ResourceQuota.
  * `mise run verify` runs them.
* **Deployed.** After rollout, `kubectl get scaledjobs -n ploeg` lists no sandbox-Team workload, and the `ploeg_scaler` NetworkPolicy opening is removed in homelab-cluster.

## Pros and Cons of the Options

### ploegd launches, and KEDA keeps sizing SandboxWarmPools

* Good, because upstream ships a KEDA example for warm-pool scale-to-zero.
* Bad, because ploegd already knows the exact queue, so KEDA would need a metric ploegd exports: a circular signal.
* Bad, because two writers on a pool's `/scale` fight.
* Bad, because it keeps KEDA in Ploeg's install requirements for a job ploegd does with one PATCH.

### Keep spawn-then-claim through KEDA, and fix `FailUnstartedRun`

* Good, because it is the smallest change.
* Bad, because the copied predicate, empty pods, KEDA's database credentials and the launcher's token all remain.
* Bad, because a warm pool still cannot bind a specific Run.

### A separate scheduler service, in Go or Rust

* Good, because it would isolate Kubernetes authority from ploegd's process.
* Bad, because the scheduler must either copy the claim predicate or call ploegd over a new API, with a new deployable.
* Bad, because a Rust scheduler cannot share the claim transaction in `pkg/store` ([ADR-0073](0073-ploeg-stays-in-go-and-admits-rust-only-as-a-separately-deployed-component.md)).

## Re-evaluation triggers

* Runs in flight exceed 200, where list polling should give way to informers or watches.
* agent-sandbox publishes its Go client as a separate module with few dependencies.
* agent-sandbox removes or renames the Sandbox `PodScheduled` condition, or the claim's warm-adoption behaviour.
* A security review asks for SandboxClaim authority to leave ploegd's process. That reopens the separate-scheduler option.
* No cluster running Ploeg uses the `keda` or `cronjob` executor for 90 days. That sets their deprecation date.

## More Information

* Evidence: [Substrate, language and Run bottlenecks](../research/2026-10-05-substrate-language-and-run-bottlenecks.md), §3.
* Related:
  * [executor contract](../contracts/executor.md)
  * [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md), [ADR-0025](0025-management-authority-stays-in-the-control-plane.md), [ADR-0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md), [ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)
* Homelab follow-up, outside this repository: remove the KEDA-to-database NetworkPolicy opening. Either grant `ploeg_scaler` access to named tables only, or retire it.
* 2026-10-05: proposed after the owner agreed that ploegd should call agent-sandbox directly.
