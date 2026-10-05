# Substrate, language and Run bottlenecks

Date: 2026-10-05. Source reviewed: ploeg `development` at `34e6d57`, unfold `development` at `e7d1588`, homelab-cluster `main`, and kubernetes-sigs/agent-sandbox `main` at `039b1a8` (v1.0.5 where the version matters). Five read-only research spikes ran in parallel; this record merges what they found and what the owner asked.

The owner asked six questions:

1. Should Ploeg become a Kubernetes operator?
2. Should Ploeg, or Unfold, be written in Rust?
3. Does WebAssembly have a role?
4. Can Unfold run on desktop and mobile at once?
5. Which bottlenecks could be removed outright?
6. If ploegd launches sandboxes itself, does that remove KEDA, and what would KEDA still offer?

The decisions are recorded in [ADR-0071](../adrs/0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md) to [ADR-0075](../adrs/0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md). The Unfold application decisions are in Unfold's ADR-0026 and ADR-0027 and its RFC-0004. This page holds the evidence.

## Summary

| Question | Verdict | Where recorded |
| --- | --- | --- |
| Kubernetes operator | No. Ploeg's core is a ledger with side effects that must not repeat; reconcile loops repeat. Kubernetes stays the executor | ADR-0071 |
| Push launch | Yes. ploegd creates each Run's SandboxClaim through a launch outbox. KEDA leaves the sandbox path | ADR-0072, OpenSpec `launch-sandboxes-from-ploegd` |
| KEDA for warm pools | No. ploegd knows the queue exactly and can patch the pool's `/scale` itself. A ResourceQuota is the cluster-side ceiling | ADR-0072 |
| Rust for ploegd | No. 30–65 person-weeks for no measurable gain on a low-QPS, Postgres-bound service | ADR-0073 |
| Rust for the launcher or worker | No. The claim must stay one Postgres transaction in Go; the worker's hardening is reachable in Go | ADR-0073 |
| Rust for Unfold | Only as a Tauri host, if a native shell is ever needed. Not for the server or a shared core | Unfold ADR-0026 |
| Wasm | Not as the agent sandbox, not for provider plugins now. The duplicated formulas leave Ploeg with the card, so there is nothing to share | ADR-0074 |
| Desktop and mobile | An installable PWA that notifies from the server. Off-LAN reachability is a homelab decision first | Unfold ADR-0026, RFC-0004 |
| Biggest bottlenecks | Discarded Runs, silence towards people, cold retries, and capacity treated as failure. Language and substrate are not among them | ADR-0072, ADR-0075; tickets on the board |

## 1. Where the time goes

[Work Item 138](2026-09-29-incident-work-item-138.md) is the best measured case. It cost US$ 0,87 in total and spent 3 h 07 min without reaching a reviewable pull request. Money is not the constraint; elapsed time is.

| Lost | Cause | Fix class |
| --- | --- | --- |
| 20 min | Two 600 s sandbox start timeouts (`values.yaml:258`) on unschedulable Kata pods. `FailUnstartedRun` (`pkg/worker/unstarted.go:16`) then claims and fails *whichever* Run is pending next | Capacity is a wait, not a failure (ADR-0072) |
| 56 min | Queued behind Work Item 130 on bronze's single builder slot, with FIFO per workload (`pkg/store/store.go:318`) | Concurrency and placement |
| 12 × ~45 min | Idle watchdog kills while the model was busy (193 calls in one "silent" Run) | Already fixed in code: an LLM observer counts model traffic as activity (`pkg/worker/worker.go:559-568`). Confirm it is in the deployed image; the incident ran `0.4.0-rc.11` |
| every retry | A fresh `emptyDir` clone with `briefing=0` re-reads the same code | Checkpoint and resume (ADR-0075) |
| throughout | Ploeg comments only at terminal states, as the owner's own account, so Vikunja probably notified nobody | Bot identity and a live status comment (board work) |

A Shift where all of these fixes held would, on WI-138's numbers, have reached a reviewable PR in roughly 60–75 minutes: one builder and one review.

Model spend inside a Run is already cheap. Run 196 sent 16,3 M input tokens over 193 calls for US$ 0,42, about US$ 0,026 per million, so most input was a cache hit. Caching across Runs would only share the system prompt and specification prefix, worth under US$ 0,05 per PR. The waste is Runs that produce nothing: on 2026-09-29, twelve idle kills cost US$ 4,07.

## 2. Is Ploeg an operator-shaped problem?

The operator pattern fits desired-state convergence: a reconcile loop that can run any number of times because each run is idempotent, with state in etcd. Ploeg's core is the opposite:

* **A ledger.** Two-level budgets ([ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md)), capped claims under an advisory lock inside the claim transaction (`pkg/store/team_capacity.go:23-36`), and audit rows. These need multi-row transactions; etcd offers none across objects, and a CRD `status` is not a ledger.
* **Side effects that must not repeat.** Minting a key, pushing a branch, posting a finding. [ADR-0060](../adrs/0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md) exists because these are already lost or doubled; a reconcile loop makes them repeat by construction.
* **Relational reads.** Run cards, card lists and grades query 43 migrations of relational state.
* **Non-Kubernetes paths.** The delegated operator path runs in local, Docker or Kubernetes workspaces, and [ADR-0069](../adrs/0069-ploeg-names-none-of-its-consumers.md) keeps Ploeg from assuming its consumers.

The one operator-shaped part is the executor: placing and isolating a pod. agent-sandbox is already that operator. Ploeg should be its client, which it partly is (`pkg/sandboxlaunch/launcher.go`).

Configuration CRDs (Team, Target, Forge registration) mirrored into Postgres would suit a Flux-managed estate. They remain an option, not a need: `PLOEG_CONFIG` and chart values already reach Git.

## 3. The executor today and under push launch

### What KEDA does now

* One ScaledJob per Team and Role (`ops/helm/ploeg/templates/scaledjob.yaml:4-6,17`), `pollingInterval: 30` (`values.yaml:376`), `minReplicaCount: 0`, `maxReplicaCount` clamped to `maxRunning` (`_helpers.tpl:113-117`). Under the sandbox executor its pod is the launcher (`scaledjob.yaml:44-47`).
* One trigger type, `postgresql`. Role workloads count pending `agent_runs` for the Team and Role (`scaledjob.yaml:74`); role-less ones count queued `work_items` (`:78`). These queries are a further copy of the claim predicate, kept in step by a comment (`:64-73`) — the duplication [ADR-0010](../adrs/0010-shift-owns-the-item-lease-owns-the-branch.md) rejected.
* **KEDA holds database credentials.** A TriggerAuthentication maps the `ploeg-scaler` Secret (`triggerauthentication.yaml:1-12`). In homelab-cluster the `ploeg_scaler` role is in `pg_read_all_data` (`kubernetes/apps/ploeg/ploeg/app/database/cluster.yaml:65-75`), so it can read every table, including `agent_runs.run_token` and the audit log. The Ploeg exporter reuses that role, and a NetworkPolicy opening lets the KEDA operator reach the database.
* KEDA stays installed in the cluster regardless; `forgejo-runner` uses its own ScaledJob.

### What push launch removes

ploegd chooses the Run and creates its SandboxClaim, so the following go away:

* the ScaledJob, TriggerAuthentication and launcher Job;
* the copied predicate;
* `ploeg_scaler` held by a cluster-wide operator;
* surplus pods that claim nothing and exit 0;
* the per-Role overshoot beyond the cap (`docs/architecture.md` §3);
* `FailUnstartedRun`, which fails the wrong Run.

### What KEDA could still offer, and why each is declined

| Function | Assessment |
| --- | --- |
| Scale a SandboxWarmPool | Possible: the CRD has a scale subresource (`extensions/api/v1beta1/sandboxwarmpool_types.go:119`, `Minimum=0`). Upstream ships `examples/keda-scale-to-zero/` on the claim-creation rate. But ploegd knows the exact queue and can patch `/scale` itself, and two writers on `/scale` fight |
| Time-of-day warm-up | A KEDA cron trigger needs no credentials, but a "minimum warm per window" in ploegd config does the same with one writer |
| Prometheus-driven scaling | Circular: the signal would be ploegd's own metric |
| Scale-to-zero | Already the default: pools render `replicas: 0` (`ops/helm/ploeg/templates/sandbox.yaml:38`) |
| Cluster-side ceiling | Better as a ResourceQuota on the sandbox namespace (`count/sandboxclaims.extensions.agents.x-k8s.io`, pods, CPU, memory), enforced by the apiserver independently of ploegd |
| Resilience when ploegd is down | None. A pull-model pod needs ploegd to claim anyway |

Verdict: KEDA leaves the sandbox path entirely. The `keda` and `cronjob` executors remain for clusters without agent-sandbox until a deprecation date is set.

### Push launch design

The design is detailed in the OpenSpec change `launch-sandboxes-from-ploegd`; its essentials are:

* **A launch outbox, not create-after-commit.** A dispatcher transaction takes the Team's capacity lock, picks the oldest eligible pending Run with `FOR UPDATE SKIP LOCKED`, moves it to `launching` (which counts against `maxRunning`) and inserts a `run_launches` row. No budget authorization, Lease or credential is minted at launch; that stays in the claim, which becomes a *bind* of that specific Run when the pod calls in. A Run that never starts never authorized spend.
* **Idempotent creation.** After commit, the outbox worker creates a SandboxClaim with a deterministic name `ploeg-r<run>-a<attempt>`, labelled with the Run and launch. `AlreadyExists` counts as success.
* **Three recovery layers.**
  1. The outbox replays `requested` rows.
  2. A reconciler about every 30 s lists claims labelled `managed-by=ploegd`, deletes orphans and requeues a launch whose claim vanished, without failing a Run.
  3. After bind, the existing Lease TTL and sweep stay the crash detector.
* **Waiting for capacity.** agent-sandbox v1.0.5 mirrors the Pod's `PodScheduled` condition onto the Sandbox, with reason `Unschedulable` and the scheduler's message (`api/v1beta1/sandbox_types.go:107-118`). An unschedulable launch is shown as "waiting for capacity since T" and is never a failed Run. Template, warm pool, image-pull and pod failures fail that specific Run as `infra_node` under [ADR-0021](../adrs/0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md).
* **Warm pools become usable.** A claim adopts a warm sandbox and applies `additionalPodMetadata` in place. A worker in a warm pod waits for its launch annotation through a downwardAPI volume, which updates live unlike environment variables, before it binds. That closes the blocker noted in `docs/contracts/executor.md`. On scarce Kata capacity a warm pod holds the node a Run needs, so pools default to zero.
* **RBAC.** A Role in the sandbox namespace only:
  * `sandboxclaims`: create, get, list, watch, delete
  * `sandboxes`: get, list, watch
  * `sandboxwarmpools/scale`: get, patch
  * no pods, Secrets or exec.

  No executor pod holds a Kubernetes token any more (today the launcher does, `_sandbox.tpl:50-67`). A claim can only reference a chart-owned template, so even a compromised ploegd launches only chart-shaped pods. An admission policy should restrict `additionalPodMetadata` to Ploeg's own label keys, so a label cannot select a laxer NetworkPolicy.
* **Client.** Keep the raw-HTTP client with small typed structs. The upstream `clients/k8s` lives inside the `sigs.k8s.io/agent-sandbox` module, whose `go.mod` pulls in controller-runtime 0.25, client-go 0.37, OpenTelemetry and gRPC. Ploeg has five direct dependencies, and this binary mints credentials. At tens of Runs a list every 5–10 s is enough; move to informers if Runs reach the hundreds.

Estimated effort: 2–2,5 engineer-weeks, including the chart and the homelab follow-up.

### Homelab follow-ups (not in this repository)

* Once KEDA leaves the sandbox path, remove the KEDA-to-database NetworkPolicy opening.
* Either grant `ploeg_scaler` access to named tables only, or retire it if only the exporter still uses it.
* Its `pg_read_all_data` membership is broader than its "read-only scaler" description, independent of this change.

## 4. Rust

### ploegd

Size: 45,5 k non-test and 46,9 k test lines of Go across the repository; the ploegd import closure alone is 34,7 k and 36,0 k. That includes:

* 1,315 tests and 157 `httptest.NewServer` uses;
* 43 migration files;
* about 480 store query sites, of which 42 build SQL dynamically.

There are five direct dependencies. No Prometheus client: `pkg/store/metrics.go` writes the exposition format by hand.

Every needed crate exists and is mature: axum 0.8, tokio 1.53, sqlx 0.9, kube 4.2, serde, reqwest, tracing, jsonschema 0.58. The gaps are minor:

* embedded Postgres for tests is a small crate;
* `serde_yaml` is archived;
* the agent-sandbox types would need kopium.

The Rust gains that would be real here:
* **Exhaustive matching on state enums.** Modest. Go's `exhaustive` linter gives most of it.
* **Compile-time checked SQL.** Moderate. The 42 dynamic builders would lose it, and the suite already runs against real embedded Postgres.
* **Typestate for Run, Shift and Lease.** Low. The authoritative transitions are guarded SQL updates and CHECK constraints across processes, and a typestate only protects the in-process path.
* **Memory and CPU.** Irrelevant. ploegd is sized at 100m CPU and 256Mi (`values.yaml:113-119`).

Costs:
* **The port itself.** About 60–65 person-weeks unassisted, or 30–40 with heavy agent help, plus 15–20 for the worker. This assumes roughly 1,000 faithfully ported lines a week for code carrying 70 ADRs of behaviour.
* **A moving target.** 70 ADRs since 2026-07-29.
* **Agent builds.** Agents generate working Go in fewer passes, and cold `cargo` builds take minutes.
* **The sandbox.** The worker sandbox cannot reach crates.io, so agents running inside Ploeg could not build a Rust Ploeg without vendored crates.

A strangler is possible: the operator API first, verified by Unfold's black-box qualification scripts. It would still mean two writers on one schema, with the advisory-lock key strings reproduced exactly. [ADR-0002](../adrs/0002-go-as-the-implementation-language.md) still holds.

The cheap Go equivalents are:
* the `exhaustive` linter on state types;
* `sqlc`, or a test that prepares every store query against the migrated schema;
* a transition-table test per state machine, matched to its CHECK constraint.

### Launcher and worker

**The launcher.** The launch must stay in the same Postgres transaction as the capacity lock. A Rust scheduler would either copy the claim predicate again or call ploegd over RPC as a new deployable.

**The worker.**
* Startup is milliseconds in either language, while scheduling and Kata boot take minutes.
* Go already handles process groups (`pkg/harness/procgroup_unix.go`), non-dumpability (`pkg/worker/conceal_linux.go`) and capability drops (`pkg/worker/capabilities_linux.go`).
* Rust's real advantage is a pre-exec hook for seccomp and Landlock. A Go re-exec shim reaches the same result: a `ploeg-worker harness-exec` subcommand sets no-new-privileges, a Landlock ruleset and a seccomp filter, then calls `execve` on the harness. That is worth more than a rewrite and is the recommended next hardening step.

### Unfold

* **Server.** About 11,9 k lines of TypeScript run directly by Node 24, an I/O proxy over Ploeg's operator API, with about 2,1 k lines of execution code already being removed. A Rust server gains nothing.
* **Frontend.** About 52 k lines of build-free ES modules and CSS (Unfold ADR 0002). Rust UI frameworks (Dioxus, Leptos) would mean rewriting all of it.
* **Tauri shell.** The only plausible place for Rust: a thin host with little Rust of one's own. It is justified only by a measured need for biometric-gated approval, deep links or a tray.

## 5. WebAssembly

* **As the agent sandbox:** no. Harnesses need git, compilers, package managers and Docker-in-pod; WASI cannot host them. Kata is qualified ([isolation qualification](2026-10-04-isolation-qualification.md)).
* **As a provider plugin system:** not now. Extism's Go SDK runs on wazero without cgo. But wazero has no Component Model (issue #2200, closed as not planned), and WASI 0.3 is implemented only by Wasmtime and jco. Tracker and forge adapters are HTTP clients, so every capability would be a host ABI to design and secure. The HTTP contracts (`tracker-execution.v1`, operator delivery) already let third parties integrate out of process, which satisfies [ADR-0005](../adrs/0005-build-a-dedicated-dispatch-plane.md)'s "no core-maintained connector matrix".
* **For a shared Rust core:** no. The duplicated logic is about 60 lines of arithmetic:
  * the rarity score: `pkg/rarity/rarity.go`, with copies in Unfold `src/rarity.ts` and `public/cards/card-model.js`;
  * the working-time calendar: `pkg/flow/calendar.go`, with a copy in `src/ploeg-demo-kpis.ts`.

  The copies already differ subtly: the TypeScript rarity percentile counts the card in its own cohort, while Go excludes it. Both formulas belong to the card, which leaves Ploeg under ADR-0074, so the consumer's copy becomes the only one and nothing needs to be shared. See the [boundary audit](2026-10-05-ploeg-boundary-audit.md).
* **For small, pure, operator-supplied functions:** a future option. A custom grader or a policy predicate could run in wazero with no WASI, plus fuel and memory limits. No such extension point exists.
* **Policy as code:** Cedar has a native Go implementation (cedar-go 1.8). Consider it only when admission rules outgrow Team, Role, model and budget caps. Budget arithmetic stays in `pkg/store`.

## 6. Contract drift between Ploeg and Unfold

* **Go is pinned to the schemas by sampled instances.** `pkg/harness/contract_test.go`, `pkg/httpapi/operator_test.go:169-190` and the qualification tests check this.
* **That only proves what Go emits fits.** It does not prove that every route and field is described.
* **Unfold never reads the schemas.** `src/ploeg.ts:135-540` re-derives shapes, enums and caps by hand.
* **Drift has already happened.** Unfold commit `33004aa` removed a client call to a route that no Ploeg release ever had; only a TypeScript fake had tested it.

The remedy is in Unfold's ADR-0027: Unfold's tests check its client and fakes against Ploeg's published schemas. Ploeg's ADR-0074 removes most of the surface that drifts, because 3,205 of the operator schema's 5,033 lines are card definitions.

## 7. Desktop and mobile

Facts:
* **`public/` is almost installable already.** It has `site.webmanifest` with `display: standalone` and a phone-adapted layout, but no service worker and no Web Push.
* **Notifications only reach an open tab.** They use the in-page Notification API (`public/core/attention.js:87-167`).
* **Unfold's route is LAN-only.** It sits on `envoy-internal` (`homelab-cluster/kubernetes/apps/ploeg/unfold/app/httproute.yaml:13`).
* **ntfy is already reachable off-LAN.** It is public on `envoy-external`, but has no upstream base URL configured, so iOS gets no instant delivery.

| Option | Reuse of `public/` | Push | Burden |
| --- | --- | --- | --- |
| Installable PWA + ntfy | All | Works off-LAN today through the existing ntfy | None |
| Installable PWA + Web Push | All | Desktop browsers and Android; iOS 16.4+ only after Add to Home Screen | Egress to the vendor push services |
| Tauri 2 | All, loading the remote URL | Remote push is not official (tauri#11651) | Rust, Xcode, Android SDK, Apple account |
| Capacitor | All, as `webDir` | Official FCM and APNs plugin | Store pipelines, no desktop target |
| Electron | All | Desktop only | Adds nothing over an installed PWA and the VS Code extension |
| Dioxus, Leptos, Flutter, React Native | None | Varies | A second UI |

How a phone reaches Unfold away from the LAN comes first. It is either a VPN or `envoy-external` behind Authentik, which Unfold already supports (`src/oidc.ts`). That decision belongs to homelab-cluster.

## 8. Ranked roadmap

Priorities live on the tracker; this is the evidence-ranked order for the product owner.

| Rank | Change | Gain | Effort | Recorded |
| --- | --- | --- | --- | --- |
| 1 | Stop discarding Runs: confirm the LLM-activity watchdog is deployed; headless prompt says nobody will answer; a closing question is `stuck`; no reviewer without a branch | 60–100 min on WI-138; most of the US$ 4/day idle waste | S–M | board (VIK-1365, VIK-1366, incident tickets 1–4) |
| 2 | Tell people: a Ploeg bot identity, one live status comment per Shift, push on needs-human and PR-ready | likely hours per PR | S–M | board (VIK-1331, incident tickets 8–9); Unfold ADR-0026 |
| 3 | Checkpoint a stopped writing Run and brief its retry from it | 20–30 min and US$ 0,20–0,40 per retry | M | ADR-0075 |
| 4 | Push launch, capacity as a wait, warm pools, readers off the Kata node | removes the 20 + 56 min class; 5–15 min per PR with warm pools | M | ADR-0072 |
| 5 | Per-repository seed image (mirror and dependency caches, cloned with `--reference`) and per-phase latency and cache-token capture | 1–5 min per Run; airgapped verification | M | board |

Deferred:
* runtime swaps (Firecracker, gVisor for writers) and snapshot restore;
* lazy-pull snapshotters;
* best-of-n builders, until pass@1 is measured;
* River or DBOS: they fit ADR-0060's Postgres-only rule, but none recovers lost agent work;
* a Rust rewrite;
* a Wasm plugin system.

## Sources

### agent-sandbox, KEDA and Kubernetes
* [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox): `extensions/api/v1beta1/`, `extensions/controllers/sandboxclaim_controller.go`, `controllers/sandbox_controller.go`, `docs/performance-tuning.md`, `examples/keda-scale-to-zero/`, `clients/k8s/README.md`
* [Agent Sandbox on GKE](https://cloud.google.com/blog/products/containers-kubernetes/bringing-you-agent-sandbox-on-gke-and-agent-substrate); warm-claim fallback in [agent-sandbox#1758](https://github.com/kubernetes-sigs/agent-sandbox/pull/1758)
* [kube-rs changelog](https://kube.rs/changelog/), [kopium](https://github.com/kube-rs/kopium)

### Rust crates
* [axum](https://crates.io/crates/axum), [sqlx](https://crates.io/crates/sqlx), [kube](https://crates.io/crates/kube), [jsonschema](https://crates.io/crates/jsonschema), [postgresql_embedded](https://crates.io/crates/postgresql_embedded)

### WebAssembly and policy
* [wazero](https://pkg.go.dev/github.com/tetratelabs/wazero), [wazero#2200](https://github.com/tetratelabs/wazero/issues/2200), [WASI 0.3](https://wasi.dev/releases/wasi-p3), [Extism Go SDK](https://github.com/extism/go-sdk)
* [cedar-go](https://pkg.go.dev/github.com/cedar-policy/cedar-go)

### Desktop, mobile and push
* [Tauri 2](https://v2.tauri.app/blog/tauri-20/), [tauri#11651](https://github.com/tauri-apps/tauri/issues/11651)
* [UnifiedPush](https://unifiedpush.org/), [ntfy as a UnifiedPush distributor](https://unifiedpush.org/users/distributors/ntfy/)

### Gateway and runtimes
* [LiteLLM prompt caching](https://docs.litellm.ai/docs/completion/prompt_caching), [LiteLLM Prometheus metrics](https://docs.litellm.ai/docs/proxy/prometheus)
* [River](https://github.com/riverqueue/river)
* [Kata Containers RuntimeClass](https://kubernetes.recipes/recipes/security/kata-containers-runtimeclass-kubernetes/)
