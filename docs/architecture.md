# Ploeg architecture

Ploeg authorizes and records agent work. It accepts tracker events and explicit operator admission, stores durable execution state in PostgreSQL, and supports unattended workers alongside delegated execution in De Vloer.

This page describes the source reviewed on 12 September 2026. It does not establish what is currently deployed. See the [published contracts](contracts/README.md), [managed worker guide](ops/managed-workers.md) and [qualification tests](../pkg/httpapi/operator_workbench_qualification_test.go) for the implementation boundaries.

## 1. System boundaries

| Component | Responsibility |
| --- | --- |
| Tracker | Requested work, priority and assignment; Ploeg retains source identity |
| [ploegd](../cmd/ploegd/main.go) | Admission, commands, claims, execution state and inference authorization |
| [PostgreSQL store](../pkg/store/) | Work Items, Shifts, Runs, Leases, outcomes, accounting and audit records |
| [Shift engine](../pkg/shiftengine/engine.go) | Advance configured rounds and roles; close or stop a Shift |
| [Unattended worker](../pkg/worker/worker.go) | Claim authorized work, prepare a repository, invoke a harness and report results |
| [De Vloer](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/vloer/docs/architecture.md) | Interactive sessions, delegated workspace execution, intervention and evidence |
| [LiteLLM integration](../pkg/httpapi/llm_control.go) | Scoped inference capability lifecycle and accounting observations |
| [Tracker and forge providers](../pkg/provider/) | Translate configured external systems at the integration boundary |

A manual-origin Operator Execution need not have an external ticket. It still needs authenticated admission and registered authority. In Vloer's shared mode, Start requests that admission. Creating a queued session alone does not start paid work. Vloer's standalone mode runs without Ploeg, and the product direction requires that independence for local work. Reusing a runner across both paths remains a [proposal to test](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/migration-proposal.md).

## 2. Execution paths

On the unattended path, a verified tracker event queues a Work Item. The Shift engine establishes its plan. An executor starts an eligible worker; that worker must obtain an authorized claim before running. A queue-depth signal alone grants no ownership. The worker renews its Lease and reports checkpoints and an outcome. The Shift engine evaluates completed rounds and configured review limits.

On the delegated path, an Operator Consumer admits one session linked to a Work Item, Shift and operator Run. De Vloer performs the crew steps and reports through the operator contract. These steps do not each become a new Ploeg Run. Changes between human and background supervision retain the execution identity.

[Canonical tracker binding](contracts/tracker-execution.md) allows registered selections to retain an existing Work Item and exclude an unattended claim atomically. It is qualified for the supported tracker paths in the [cross-service tracker tests](../pkg/httpapi/operator_tracker_qualification_test.go), not for arbitrary trackers or already-running harness sessions.

## 3. Executors and workspace placement

The [Helm chart](../ops/helm/ploeg/) supports KEDA ScaledJobs and a KEDA-free CronJob executor. Configured team plans can render separate workloads for roles with their own harness settings. KEDA polls the pending count in PostgreSQL directly; neither that polling nor idle controller activity requires model inference. ploegd has no HTTP queue-depth route.

A team can carry a concurrency cap, `maxRunning`: the most Runs it may have running at once. Set it as `executor.teams[].maxRunning` in the chart, which reaches ploegd as `PLOEG_TEAM_MAX_RUNNING`, or as `teams.<name>.maxRunning` in the `PLOEG_CONFIG` file, which wins for that team. Unset or `0` means unlimited. ploegd enforces the cap inside the claim transaction. A per-team advisory lock serialises capped claims, so concurrent workers cannot each see room under the cap. A claim over the cap answers `204` like an empty queue: the worker exits 0 and the pending Run stays queued. A Run releases its slot when it finishes, whether by outcome, Run expiry or Lease expiry. Operator executions do not count, because they are admitted through the operator API rather than claimed by a worker pod. The chart clamps every workload's KEDA `maxReplicaCount` to the cap. A planned team has one workload per role, so the sum of its ceilings can exceed the cap; the surplus pods find nothing and exit 0.

A delegated operator Run executes through Vloer's workspace backend, which can be local, Docker or Kubernetes. It need not correspond to a Ploeg Kubernetes Job. See the [executor contract](contracts/executor.md) for the unattended interface.

## 4. Claims, interruption and recovery

[Store transactions](../pkg/store/) and [worker authorization](../pkg/httpapi/worker_auth.go) govern claims and renewal. Lease expiry is detected by controller recovery, so safety cannot depend on a dying agent performing cleanup. Unattended retry behavior follows the configured execution policy. A live but wedged harness is bounded by the worker itself: `PLOEG_HARNESS_TIMEOUT` (chart default `100m`, below the Job's `activeDeadlineSeconds`) limits one harness run, and `PLOEG_HARNESS_IDLE_TIMEOUT` (chart default `15m`) ends a spawned harness that writes no output and makes no model call through the worker's loopback model proxy. Either kills the harness's process group, blocks its inference key through the usual revocation path and reports `failed`, with failure reason `timeout` for the time limit and `idle` for silence. Both count against the agent retry budget. The ACP adapter keeps its own prompt wall and idle watchdog and is subject only to the overall bound. When one of them stops a Run, the Run's summary names it ("acp idle watchdog stopped the agent: no protocol activity for 10m0s"), its stuck reason starts with what it saw ("no protocol activity for 10m0s after 29 events" or "prompt ran past 45m0s"), and the failure reason stays `agent_error` ([ADR-0043](adrs/0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md)). Set either variable to `0` to disable it.

Operator commands carry identity, revision and generation. Accepted command replays return the recorded result. Operator expiry interrupts work; cancellation intent and unresolved spending survive it. An admission that is never started, for example because its response was lost, is cancelled by the same sweep once it expires: its Run, Lease and Shift close, and its untouched Inference Account is blocked from minting. A restart must not silently repeat paid work. Pause, cancel, resume and a new execution are distinct operations. See [operator contracts](contracts/README.md) and [recovery procedures](ops/managed-workers.md).

## 5. Credentials and accounting

Managed mode is the current default in [controller startup](../cmd/ploegd/main.go) and [worker startup](../cmd/ploeg-worker/main.go). The controller holds management authority. Workers receive scoped control and inference capabilities; the harness receives its allowed environment and inference credential. A worker process and the harness it starts share a container, a filesystem and a network, so they are not separate security boundaries. The worker marks itself non-dumpable at startup ([conceal_linux.go](../pkg/worker/conceal_linux.go)), which keeps its environment and memory out of the harness's reach through `/proc`. The [environment policy](../pkg/worker/environment.go) and deployment isolation still matter.

An Inference Account records authorization independently from observed spend. Failed issuance, interruption, expiry or blocked access can leave that authorization unresolved. A provisional gateway observation is not final settlement. Trusted reconciliation releases the remaining hold; there is no general public settlement endpoint. The controller's settlement sweep in [the sweep loop](../cmd/ploegd/sweep.go) performs it for finished Runs whose accounts were never minted, or were blocked and stayed unchanged for a quiet period.

Legacy worker authorization and static credential compatibility are explicit migration modes. Do not use the old worker-mints-master-key description as the managed deployment model. The [worker control contract](contracts/worker-control.md) is the canonical reference.

## 6. Providers, harnesses and delivery

The source includes [Vikunja](../pkg/provider/vikunja/), [ClickUp](../pkg/provider/clickup/), [Forgejo](../pkg/provider/forgejo/) and [GitLab](../pkg/provider/gitlab/) integrations. Supported operations differ by provider and configuration. Tracker write-backs, forge operations, team plans and Shift orchestration exist; they are no longer future implementation gaps.

The [harness package](../pkg/harness/) defines Task Spec, Outcome Report and adapters. Published [JSON schemas](contracts/README.md) describe the wire formats. Harness-native conversation state is not a portable checkpoint.

The unattended publication path and delegated candidate-delivery path have different controls. [Operator delivery](contracts/operator-delivery.md) records immutable candidates, trusted verification, candidate-bound approval and a publication barrier. A reservation or successful verification is not a published change. The delegated path has no enabled live publisher executor.

## 7. HTTP trust boundary

Worker calls use the scoped authorization described in [worker control](contracts/worker-control.md). A Run token alone is not managed authentication. Operator calls use a scoped consumer identity and authenticated actor context; read access does not imply permission to execute. See [operator route authorization](../pkg/httpapi/operator.go) and [worker authorization tests](../pkg/httpapi/worker_auth_test.go). `/healthz`, `/readyz` and `/metrics` are unauthenticated and return only aggregate state; [metrics and alerts](ops/alerts.md) lists what `/metrics` exposes.

## 8. Configuration and development

Team plans, concurrency caps, target mapping, provider configuration and executor settings are defined by [controller startup](../cmd/ploegd/main.go), [chart values](../ops/helm/ploeg/values.yaml) and the [operations guides](index.md#operate). Desired deployed state belongs to the deployment repository. A copied team roster, IP address or image tag in this explanation would become stale independently.

Run the gates in the [pull-request workflow](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/.forgejo/workflows/on_pull_request.yml). Mock services and cross-service fixtures provide implementation evidence. Some tests need a PostgreSQL runtime; tool or database provisioning may need network access.

## 9. Where the code diverges from design.md

The current boundaries that matter to this audit are:

- Repository-free conversation is a Vloer product intention; its current session API requires a repository and crew.
- Local work must remain usable without Ploeg; a common runner has not been selected or extracted.
- Delegated candidate verification and approval do not provide live publication.
- Native harness state is opaque; cross-harness continuation is not guaranteed.
- Automated checks and a small paid fixture do not qualify arbitrary providers, production deployments or high concurrency.
- Forge webhooks record and deduplicate every event and settle a Work Item whose pull request was merged or closed. Only a Team's `forgeFollowUps` switches turn a failed check into a repair Follow-Up or a request for changes into rework. A forge without a webhook secret has every delivery rejected. The worker accepts a checkpoint field in its contract but does not automatically populate it from stored progress.
- The intended Work Target model forbids team-to-repository coupling; legacy executor target defaults remain compatibility behavior. Static forge credentials do not provide per-Run push revocation.
- Historical gap lists and backlog status require a fresh source check before implementing a ticket.

The [pre-audit architecture](https://forgejo.webgrip.dev/webgrip/ploeg/src/commit/94c7c8e2dc07037ee2fd38a69343426e40bd680d/docs/architecture.md) retains the detailed July observations and later additions. It is evidence of earlier understanding, not a second current specification. The [backlog](backlog.md) retains planning history; the tracker owns priority.

## Decisions and evidence

The [ADR index](adrs/README.md) is the decision ledger. Proposed records remain proposed even when related code exists. The [documentation audit](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/vloer/docs/research/2026-09-12-documentation-audit.md) records this correction pass and its verification limits.
