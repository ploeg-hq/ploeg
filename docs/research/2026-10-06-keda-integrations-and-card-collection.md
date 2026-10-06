# KEDA, plug-and-play integrations, and card collection

Date: 2026-10-06. Source: ploeg `claude/ploeg-definition-fvz3oz` at `a3a30e4`, unfold `development`, homelab-cluster `main`, KEDA 2.21 documentation and source.

The owner raised four points:
1. They doubted push launch and asked for research on KEDA before a real decision.
2. They said Ploeg should have zero knowledge of forges and trackers, with plug-and-play integrations.
3. They said Ploeg should expose only the minimum, plus its own metrics, and that card data should be gathered by a separate Unfold service.
4. They said alerting belongs in Ploeg or Unfold, never in homelab-cluster without explicit reasoning.

Three read-only spikes answered these. The decisions:

| Decision | Record |
| --- | --- |
| KEDA stays; ploegd serves its demand signal and reserves a Run for each launcher | [ADR-0072](../adrs/0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md) (rewritten) |
| Ploeg exposes its execution facts, events and metrics; consumers collect everything else | [ADR-0074](../adrs/0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md) (rewritten) |
| Ploeg knows Work Sources and Change Destinations, never vendors | [ADR-0077](../adrs/0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md) |

They supersede the 2026-10-05 record's push-launch verdict (§3) and its "Ploeg collects every card fact" revision.

## 1. KEDA

### What KEDA gives this system

* **`accurate` scaling.** Ploeg uses `scaledjob.yaml:39`. Its source formula is `maxScale − pendingJobCount`, capped at `maxReplicaCount − runningJobCount` ([scale_jobs.go](https://github.com/kedacore/keda/blob/main/pkg/scaling/executor/scale_jobs.go)).
  * A launcher waiting on an unschedulable sandbox counts as running, so KEDA does not start more Jobs into a full cluster.
  * The "next Job into the same full cluster" described in the first ADR-0072 came from the launcher's 600 s start timeout (`values.yaml:289`, `pkg/sandboxlaunch/launcher.go:214-217`), not from KEDA.
* **Settings already used:**
  * `maxReplicaCount` as a cluster-side ceiling;
  * `rollout.strategy: gradual`, so an upgrade never kills a Run in flight (`scaledjob.yaml:37`);
  * Job history for forensics;
  * scale to zero.
* **Operability Ploeg gets free:**
  * the `autoscaling.keda.sh/paused` annotation as a per-Team, per-Role drain switch;
  * `kubectl get scaledjob` with READY, ACTIVE and PAUSED;
  * KEDA's admission webhooks;
  * CloudEvents for ScaledJob lifecycle;
  * Prometheus metrics: `keda_scaled_job_errors_total`, `keda_scaler_detail_errors_total`, `keda_scaler_active`.

  See [ScaledJob spec](https://keda.sh/docs/2.21/reference/scaledjob-spec/) and [metrics](https://keda.sh/docs/2.21/integrations/prometheus/).
* **Least privilege.** ploegd needs no Kubernetes API access. ploegd mints credentials and receives webhooks from the internet, so this is the strongest argument.
* **Maturity.** KEDA is a CNCF graduated project, already installed at 2.21.0 for forgejo-runner.
* **Limits:**
  * `fallback` exists only for ScaledObjects.
  * A failing scaler reads as zero, so no Jobs start: a silent stall. No alert covers Ploeg's ScaledJobs; the first ADR-0072 wrongly said one did.

### What was actually wrong, and how it is fixed with KEDA

| Problem | Fix |
| --- | --- |
| A copy of the claim predicate in Helm (`scaledjob.yaml:64-78`) | KEDA's `metrics-api` scaler reads a ploegd demand endpoint computed by `store.PendingRuns`, which a test already ties to `ClaimRole` (`pkg/store/shift.go:325-330`) |
| KEDA holds a database role in `pg_read_all_data` | The scaler holds a bearer token that can read only demand counts; KEDA needs no database user |
| Empty pods above the cap | ploegd's demand is `min(pending, maxRunning − running − reserved)`, so the cap applies in one place |
| The launcher's 600 s timeout fails whichever Run is pending next (`pkg/worker/unstarted.go:16-34`, WI-138 Run 192) | The launcher **reserves** a specific Run before creating the claim. An `Unschedulable` sandbox is a wait, reported to ploegd, never a failure. Only real start faults fail, and they fail the reserved Run |
| Warm pools unusable | The reservation id travels in the claim's `additionalPodMetadata`, and the worker binds that Run from a downwardAPI volume |

The external scaler over gRPC was rejected:
* `external-push` does not support ScaledJob ([scale_handler.go](https://github.com/kedacore/keda/blob/main/pkg/scaling/scale_handler.go)).
* Plain `external` polls just like `metrics-api`.
* It would add gRPC to the binary that mints credentials.

### Why push launch is withdrawn

Push launch would have given ploegd:
* create and delete rights on SandboxClaims, and API connectivity;
* an outbox and a reconciler that can race a slow bind;
* agent-sandbox schema knowledge in core.

It would also lose KEDA's pause, gradual rollout and visibility. All of that buys about 2,5 s of average poll delay plus a launcher pod start, while Kata scheduling and boot take minutes. It contradicts the owner's direction that Ploeg should know as little as possible about the systems around it.

### Scores

From 1 (worst) to 5:

| | As is | KEDA + demand endpoint + reservations | Push launch |
| --- | --- | --- | --- |
| Correct when the cluster is full | 1 | 5 | 5 |
| Start latency | 3 | 3 (5 with a warm pool later) | 4 |
| ploegd least privilege | 5 | 5 | 2 |
| Credential exposure | 1 | 4 | 5 |
| Kubernetes knowledge in ploegd | 5 | 5 | 1 |
| Operability | 4 | 4 | 2 |
| Effort | — | 5–7 days | 2–2,5 weeks plus trial |

## 2. Integrations

### How much core knows

About 7,500 production lines are vendor-aware: about 5,000 in `pkg/provider` and about 2,500 in core. Examples in core:
* `cmd/ploegd/main.go:25-28,97-180` (vendor environment variables);
* `cmd/ploegd/webhooks.go` (a Vikunja-only webhook check surfaced in `/readyz`);
* `cmd/ploegd/forgecreds.go` and `pkg/forgebroker/forgejo.go` (Forgejo token minting);
* `pkg/httpapi/server.go:307-310` (`X-Forgejo-Delivery` headers);
* `pkg/worker/forge.go` and `forgeproxy.go:219-255` (REST dialects);
* `pkg/worker/task.go:228-246` (the agent is briefed with vendor REST calls);
* `pkg/harness/contract.go:143-149` (forge dialects in `taskspec.v1`);
* `pkg/work/branch.go:13` (a Vikunja branch-name case);
* `pkg/config/config.go` (vendor sections).

### What Ploeg actually needs

Ploeg needs a git remote: smart-HTTP is a protocol, not a vendor. It also needs two abstractions:
* **A Work Source** emits normalized work-item events and answers item read, comment upsert and state set.
* **A Change Destination**:
  * mints, revokes and lists a per-Run git credential (ADR-0013, ADR-0016);
  * checks a repository;
  * ensures and observes a change request for a branch (ADR-0059);
  * upserts a note by marker;
  * emits change events.

### Options

* **In-process modules.** Small effort, but still a compiled-in connector matrix.
* **Out-of-process adapters behind a published contract.** This is the KEDA external-scaler, HashiCorp go-plugin and Argo CD plugin pattern. Adapters hold vendor credentials; ploegd holds none.
* **Wasm plugins.** Rejected again: no Component Model in wazero, and the HTTP and secrets host ABI is large.
* **Events at the edges.** Out-of-process adapters, with the event direction made explicit: vendor webhooks become [CloudEvents](https://github.com/cloudevents/spec) into ADR-0060's inbox, keyed by `source`+`id`, and effects go out through the outbox.

Chosen: the last option, with adapters as sidecars, migrated in phases behind an in-process shim. Estimated 8–10 weeks; about 7,500 lines move or are deleted.

### CI checks

ADR-0070 gates review on checks the worker runs itself, not on forge CI. Forge commit status is read only to record facts (`pkg/forgefacts/forgefacts.go:88`). Under the adapter model:
* Ploeg receives one normalized event, `change.checks_completed {destination, repo, branch, headSha, state, checks[]}`.
* It decides only on the stored head.
* It keeps a deadline on its own state for checks that never arrive.

Whether an adapter learns of checks by webhook or by polling the heads Ploeg announced is the adapter's business. **Ploeg core polls nothing.**

## 3. Card collection

The card belongs to a separate service in the Unfold monorepo, `apps/collector`:
* **Inputs.** It follows Ploeg's operator event cursor with a read-only token, and receives forge, tracker and deploy webhooks itself.
* **Computation.** It reads pull request activity, CI, changed files, reverts and relations. It computes grade, rarity, KPIs, gates and flow, and renders and posts the card comment under its own forge identity.
* **Serving.** It serves cards to the Unfold app.

**Language: Go, not Rust.** The service is bound by input and output, and its CPU work is arithmetic and SVG text. Ploeg's formula packages can be lifted with their tests: `pkg/rarity`, `pkg/playkpi`, `pkg/flow`, `pkg/gate` and `pkg/cardimage`, about 3,900 lines, all importing only the standard library or each other. Rust has no foothold in the monorepo, and the worker sandbox cannot fetch crates. [ADR-0073](../adrs/0073-ploeg-stays-in-go-and-admits-rust-only-as-a-separately-deployed-component.md) admits Rust only for a measured CPU or untrusted-input need.

**Isolation:**
* its own Deployment and NetworkPolicy;
* its own Postgres, because Unfold's SQLite has a single writer;
* Ploeg never calls it.

An outage loses only what cannot be re-read: tracker status transitions, and deploys seen once.

**What Ploeg exposes for it:**
* Work Item, Shift and Run resources;
* the pull requests Ploeg produced, with head SHAs;
* live and settled spend, and the trace alias;
* the pull request facts Ploeg decides on;
* a versioned event stream with an action enum and a retention promise.

**Migration** takes about 7–10 engineer-weeks:
1. Ploeg ships the facts and a one-off export.
2. The collector is built.
3. Both collect for 2–4 weeks, with a compare job.
4. Cutover.
5. Ploeg deletes about 13,000 production lines.

A different forge identity cannot edit the comments Ploeg already posted, so the collector posts fresh ones.

## 4. Alerting

The homelab stopgap rules added on 2026-10-06 were reverted the same day. Ploeg's chart carries its Run-health rules (ADR-0076). The rules for what Ploeg observes of its dependencies also move into the chart:
* KEDA errors on its own ScaledJobs;
* reservation and capacity waits;
* gateway failures seen by the worker and broker.

The log shipper's `trace` mapping stays in homelab-cluster, because the shipper's configuration lives where the shipper runs.
