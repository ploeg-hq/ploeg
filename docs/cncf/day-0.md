---
type: explanation
audience: [owner, reviewer, operator, integrator]
owner: ploeg
last_verified: 2026-10-27
verified_by: "Answers checked against development 080b144: README, architecture.md, security/self-assessment.md, the chart's values.yaml and templates, docs/contracts, docs/ops and the research records cited; questions taken from cncf/toc operations/project-reviews/general-technical-questions.md v1.0.1 at 6119a69"
---

# General Technical Review: Ploeg / Sandbox (Day 0)

- **Project:** Ploeg
- **Project version:** 0.2.0 release candidates (newest `v0.2.0-rc.81`, 2026-10-26); last stable `v0.1.0` (2026-10-03)
- **Website:** <https://github.com/ploeg-hq/ploeg> (no separate website)
- **Date updated:** 2026-10-27
- **Template version:** v1.0.1
- **Description:** Ploeg is a self-hosted control plane that admits AI coding-agent work from trackers and operator clients, authorizes a budget for it, runs each attempt as a short-lived Kubernetes Job with credentials minted for that attempt alone, and records the outcome and the spend in PostgreSQL.

This page answers the Day 0 questions of the CNCF [General Technical Review](https://github.com/cncf/toc/blob/6119a69b942853d13ac988b288024afd49ce64ea/operations/project-reviews/general-technical-questions.md). The [Sandbox application form](https://github.com/cncf/sandbox/blob/1c9e414d2700e840cd50b03915dc59244a149eaf/.github/ISSUE_TEMPLATE/application.yml) asks whether the project has completed it. Links below are relative to the repository; when the application is filed they are replaced with links at the release tag, as the review's own tips ask. No application has been filed and no CNCF status is claimed ([eligibility](eligibility.md)).

The comparison with other projects is on its own page, [ecosystem fit](ecosystem-fit.md).

## The problem

A team that hands tickets to an AI coding agent usually gives that agent a long-lived forge token, a model key with no ceiling and a shell on someone's machine. When it runs unattended, nobody owns the branch if the agent dies, nobody knows what it cost until the invoice, and a prompt injected through the repository can reach everything those credentials reach.

Ploeg puts a control plane between the work and the agent. Work is admitted, a budget is authorized before the agent starts, one worker holds a lease on the branch, the agent receives placeholders instead of credentials, and spend is settled afterwards from the model gateway's own records. The agent is treated as untrusted throughout ([self-assessment](../security/self-assessment.md#background)).

## Day 0: planning phase

### Scope

**Vision and goals.** Any team running coding agents on its own Kubernetes cluster can let them work unattended without handing them standing credentials or an open budget, and can show afterwards who authorized each Run, what it was allowed to spend, what it spent and what it delivered.

**Primary use cases.**

- A ticket assigned or labelled in a tracker (Vikunja, ClickUp or GitHub Issues) becomes a Work Item; a Team's plan of Roles (for example a builder and a reviewer) runs as Rounds of Runs; the result is a pull request on the forge (GitHub, Forgejo, or GitLab in part) that a person reviews and merges.
- An operator client admits manual work through the operator API, follows it on an event feed and cancels, requeues or annotates it ([architecture](../architecture.md#2-execution-paths)).
- A person asks about Ploeg's state from an AI client over MCP through `ploeg-mcp`, and confirms any action it takes in the browser ([ADR-0081](../adrs/0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md), [ADR-0153](../adrs/0153-a-person-confirms-a-ploeg-mcp-action-by-signing-in-afresh-at-a-ploeg-mcp-page.md)).

**Additional use cases.** A client-carried Run, where a person's own Claude Code login does the work and Ploeg still owns the lease and the record ([ADR-0112](../adrs/0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md)); a deploy endpoint a pipeline calls so Ploeg learns where a merged change went live ([ADR-0047](../adrs/0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)).

**Out of scope.** Ploeg does not:

- write or train agents, or make a model resist prompt injection;
- serve models (it calls a gateway);
- isolate processes itself (it relies on Kubernetes, the RuntimeClass and the installer's CNI) or enforce network egress on its own;
- review code or merge pull requests (agent reviewers are advisory; a person merges);
- own the backlog, grooming or priority (the tracker does);
- run CI (it reads CI results from the forge).

The self-assessment's [non-goals](../security/self-assessment.md#non-goals) state the security side of these limits.

**Roadmap process.** Anyone proposes work in GitHub Discussions. A maintainer opens a `Decisions YYYY-MM` issue on the first working day of each month, leaves it open for comments for seven days and records each decision with its reason. Architectural decisions become append-only ADRs ([governance](../../GOVERNANCE.md#the-monthly-decision-cycle)). Milestones M1 to M6 on [GitHub](https://github.com/ploeg-hq/ploeg/milestones) carry the mid-term plan: settle authority, become adoptable by an outsider, a qualified release, use and stewardship beyond the author, the Sandbox application, and work after acceptance. The maintainer ladder is in [governance](../../GOVERNANCE.md#becoming-a-maintainer). Today one person both sets the roadmap and makes most contributions, which is the main governance risk ([eligibility](eligibility.md)).

**Target personas.**

- *Platform engineer* who installs and operates Ploeg on a cluster, and owns the gateway, CNI and runtime classes.
- *Team lead* who defines Teams, Roles, budgets and which repositories a tracker board routes to.
- *Developer* who assigns tickets, reviews the pull requests Runs open and merges.
- *Integrator* who builds an operator client or a harness adapter against the published contracts.
- *Security reviewer* who needs to know what an agent can reach and what it cost.

**Organizations that benefit.** Software teams of any size that run their own Kubernetes and want unattended agent work with a spend ceiling per attempt; especially those on self-hosted forges and trackers (Forgejo, GitLab, Vikunja), which most agent tools do not reach. Organizations that must keep code and credentials inside their own infrastructure benefit from the self-hosted design.

**End user research.** Not yet. The only field report is the maintainer's own, on board onboarding ([2026-08-28](../research/2026-08-28-onboarding-friction.md)). An independent operator pilot with a usability review is planned in [issue #27](https://github.com/ploeg-hq/ploeg/issues/27) and has not started.

### Usability

**How personas interact.** Operators install with Helm and configure through chart values and a `PLOEG_CONFIG` file. Developers interact through the tools they already use: a tracker assignment starts work, and the outcome is a pull request with a status card. Integrators use the operator HTTP API, the run API and the MCP tools. There is no CRD API.

**UX and UI.** Ploeg ships no web UI. Its human surfaces are the tracker write-backs, the pull request card, Prometheus metrics with alert rules, and `ploeg-mcp` for AI clients. A richer workbench is left to operator clients built on the operator API, which Ploeg serves with versioned contracts ([contracts](../contracts/README.md)).

**Integration in production.** Ploeg needs four things beside itself: PostgreSQL, a LiteLLM gateway, a forge and either a tracker or an operator client. On Kubernetes it uses Jobs, NetworkPolicy, Pod Security Admission and RuntimeClass, and optionally KEDA, agent-sandbox, the Prometheus Operator and an OpenTelemetry collector. [Ecosystem fit](ecosystem-fit.md#what-ploeg-owns-and-what-it-consumes) lists each layer.

### Design

**Design principles.**

- *The agent is untrusted.* Credentials stay in the worker container, behind loopback proxies; the agent's container gets placeholders and no mounted Secret ([ADR-0138](../adrs/0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md)).
- *Authorization before spend.* Every Run is budgeted before it starts and settled from the gateway's evidence; unknown spend stays unresolved, never guessed ([ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md)).
- *Safety lives in the controller.* Lease expiry is detected by ploegd's sweep, so a dying agent cleaning up is never assumed.
- *Coordination lives in PostgreSQL.* Claims use `FOR UPDATE SKIP LOCKED` and advisory locks, so two ploegd processes overlapping during a rollout are safe.
- *Facts come from the system that owns them.* Delivery facts are read from the forge, priority from the tracker, spend from the gateway.
- *Secure by default.* The chart's default is the strict isolation profile, and privileged Docker-in-Docker is an acknowledged opt-in ([ADR-0136](../adrs/0136-the-chart-default-is-the-strict-isolation-profile-and-privileged-dind-is-an-acknowledged-opt-in.md)).
- *Decisions are recorded.* 156 ADRs in an append-only ledger, checked by `go test ./internal/ledger/`.

**Architecture.** [docs/architecture.md](../architecture.md) describes the components, execution paths, executors, claims and recovery, credentials and the HTTP trust boundary. The components:

| Component | Runs as | Holds |
| --- | --- | --- |
| `ploegd` | One Deployment replica (a rollout overlaps two) | Admission, Shift engine, claims and Leases, credential minting, settlement, the sweep, operator and run APIs, webhooks; every management credential |
| PostgreSQL | Installer's choice | Work Items, Shifts, Runs, Leases, accounts, audit |
| `ploeg-worker` | Main container of a short-lived Job pod per Run | The Run's signed capability, its model key and forge token, the credential proxies |
| Harness | Sidecar container in the same pod | Nothing secret: placeholders and an allowlisted environment |
| `ploeg-mcp` | Optional Deployment | Consumer tokens per group; verifies OAuth access tokens |
| KEDA | Installer's | Read access to the pending count |

*Proof of concept and development:* the [quickstart](../quickstart.md) runs everything on kind with an in-cluster PostgreSQL, Forgejo and a LiteLLM with a fake model, and needs no account. *Test:* `mise run verify` runs the Go tests against embedded PostgreSQL, the Helm lint and golden renders, the network-policy and worker-boundary checks and the contract bundle reproducibility check; a weekly attack drill runs every harness against a hostile repository ([attack drill](../ops/attack-drill.md)). *Production:* an external, backed-up PostgreSQL, a CNI that enforces NetworkPolicy, a sandboxing RuntimeClass (gVisor or Kata) for agent pods, and the support profile P1 the chart checks ([self-hosting security](../ops/self-hosting-security.md)).

**Service dependencies in the cluster.** PostgreSQL; a LiteLLM gateway reachable from ploegd and from worker pods; DNS. Optional: KEDA, agent-sandbox v1.0.x with extensions, the Prometheus Operator CRDs for the ServiceMonitor and PrometheusRule, an OTLP collector.

**Identity and access management.**

- *Workers* present a per-Team bootstrap token, matched by SHA-256 in constant time, and receive an HMAC-signed capability bound to the Run, worker, Team, Role and expiry ([worker control](../contracts/worker-control.md)).
- *Operator clients* use bearer tokens stored as hashes, each scoped to Teams with separate `execute` and `verify` flags. A client can prove the person it acts for with a token exchanged for Ploeg's audience ([ADR-0150](../adrs/0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)); otherwise the person is asserted by the client and recorded as `asserted`.
- *`ploeg-mcp`* verifies OAuth access tokens from the installer's OIDC provider and calls ploegd with a consumer token per group.
- *Trackers and forges* authenticate their webhooks with HMAC; every delivery is rejected when no secret is configured.
- *Kubernetes:* ploegd and the worker run without a service-account token. Only the sandbox launcher has a Role, limited to creating and watching `SandboxClaim`s.

**Sovereignty.** Ploeg is self-hosted and has no hosted service, telemetry or licence check. Code, tickets, transcripts and spend records stay in the installer's cluster and database. The only outside calls are the ones the installer configures: the tracker, the forge and the model provider behind the gateway. A deployment with a self-hosted forge, tracker and model can run without leaving its own network. The harness adapters switch off agents' own telemetry where the agent allows it ([ACP profiles](../contracts/acp-profiles.md)).

**Compliance.** Ploeg makes no regulatory compliance claim. What it does provide is evidence an auditor can use: an audit record of every admission, claim, mint, settlement and operator action with the person and channel ([ADR-0108](../adrs/0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)), logs that carry identifiers and never the text people or agents wrote, signed release artifacts with SBOMs and OpenVEX, and a licence inventory against the CNCF policy ([licence inventory](license-inventory.md)).

**High availability.** ploegd runs as one replica. It can overlap with a second during a rollout because all coordination is in PostgreSQL, but running several replicas permanently has not been qualified. While ploegd is down, running workers cannot renew their Leases and stop themselves before a Lease can be reclaimed; no paid work starts. PostgreSQL availability is the installer's: Ploeg needs one primary. The [restore drill](../research/2026-10-25-restore-drill.md) and [upgrade drill](../research/2026-10-26-upgrade-drill.md) measured recovery from a backup and from interrupted migrations.

**Resource requirements.** Chart defaults, from [values.yaml](../../ops/helm/ploeg/values.yaml): ploegd 100m CPU and 256 MiB, with requests equal to limits; `ploeg-mcp` 10m and 32 MiB requested, 128 MiB limit. Each Run pod uses `executor.workerResources` (1 CPU, 1 GiB) and `executor.workerContainerResources` (250m, 512 MiB), plus `executor.dindResources` (1 CPU, 1.5 GiB) only when Docker-in-Docker is opted into. Pods exist only while a Run does; an idle Team runs no worker pods. Network: ploegd talks to PostgreSQL, the gateway, trackers and forges; a worker pod reaches only ploegd, the gateway, its forge, DNS and an OTLP collector when one is configured, as the chart's NetworkPolicies allow ([network](../ops/network.md)).

**Storage.** PostgreSQL holds all durable state, with migrations that are append-only and applied at startup under an advisory lock and a five-second lock timeout. Worker pods use only `emptyDir` volumes for the clone and scratch space, so nothing a Run writes survives it except what it pushes and reports.

**API design.**

- *Topology and conventions.* HTTP+JSON under `/api/v1/`: the run API for workers (`claim`, `renew`, `checkpoint`, `outcome`, `transcript`), the operator API for clients (team-scoped reads, admission, steering, an event feed), the deploy API for pipelines, and tracker and forge webhooks. `/healthz`, `/readyz` and `/metrics` are unauthenticated and aggregate only. The run, operator and deploy APIs have JSON Schemas in [docs/contracts](../contracts/README.md), and tests pin the Go types to them.
- *Defaults.* Managed workers (the controller holds management credentials), the strict isolation profile, default-deny NetworkPolicies, webhooks rejected without a secret, harness output kept out of pod logs.
- *Configuration for reasonable use.* A PostgreSQL URI Secret, the LiteLLM master key, forge and tracker credentials and webhook secrets, and at least one Team with a model and a budget ([configuration reference](../reference/configuration.md), [managed workers](../ops/managed-workers.md)).
- *New API types and calls.* Ploeg adds no Kubernetes API types. It creates Jobs (or KEDA ScaledJobs, or agent-sandbox `SandboxTemplate`, `SandboxWarmPool` and `SandboxClaim` objects), NetworkPolicies, and optionally a ServiceMonitor and PrometheusRule. Outward, it calls the LiteLLM key and spend APIs and the tracker and forge APIs.
- *Compatibility with API servers.* It uses only stable Kubernetes APIs through Helm, plus the `extensions.agents.x-k8s.io/v1beta1` and `keda.sh/v1alpha1` resources when those executors are chosen. The quickstart is tested with kind 0.33.0 and kubectl 1.36.1.
- *Versioning.* Each contract's major version is in its file name; `GET /api/v1/operator/version` lists the majors served; a consumer can record its expectations in the repository and CI fails any change that breaks them ([ADR-0147](../adrs/0147-ploeg-serves-the-major-of-each-contract-and-keeps-its-consumers-two-newest-lines-working.md)). While Ploeg is 0.x, a breaking change raises the minor version, needs a `BREAKING CHANGE:` footer and an ADR, and only removes what the previous stable minor deprecated ([compatibility](../ops/compatibility.md)).

**Release process.** Every releasable merge to `development` cuts a `v0.x.y-rc.N` candidate from Conventional Commits with no hand step: the release workflow runs the full gates, publishes each image by digest only under its CVE budget, signs images and chart keyless with cosign, attaches SBOMs, provenance and OpenVEX, and publishes a contract bundle. A stable `v0.x.y` is a reviewed act: a public release issue lists the promotion gates, then `main` is fast-forwarded to the soaked candidate ([ADR 0158](../adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md), [releases](../ops/release-versioning.md)). There is no 1.x yet, and no major release is planned until a separate maturity decision.

### Installation

**How it is installed.** `helm install` of the signed chart from `oci://ghcr.io/ploeg-hq/charts/ploeg`, with Secrets for PostgreSQL, the gateway and the forge. The [quickstart](../quickstart.md) does it on kind from nothing to one completed Run in about four minutes, with no outside account and no spend; a scheduled workflow repeats it against the pinned and the newest candidate. A real deployment takes more: a tracker webhook, Team definitions, and the cluster-side controls in [self-hosting security](../ops/self-hosting-security.md).

**How an adopter validates it.** The quickstart's last step reads the completed Run back through the operator API. [Verify a release](../ops/verify-a-release.md) checks signatures, SBOMs and attestations without an account. `scripts/worker-boundary-probe.sh` checks from inside a worker pod that the metadata endpoint and the Kubernetes API are unreachable and the process is unprivileged ([record](../research/2026-10-26-worker-boundary-qualification.md)). The chart refuses values that leave support profile P1 unless the deployment declares itself unsupported ([ADR-0154](../adrs/0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md)).

### Security

**Self-assessment.** [docs/security/self-assessment.md](../security/self-assessment.md), following the TAG Security and Compliance outline, with its [threat model](../security/threat-model.md). It is written by the project and has not been reviewed independently; the reviewer sign-off is pending in [issue #34](https://github.com/ploeg-hq/ploeg/issues/34).

**Cloud Native Security Tenets.**

- *Secure by default:* strict isolation profile, default-deny network policies, no service-account token in Ploeg's pods, distroless non-root images with read-only root filesystems.
- *Least privilege:* credentials minted per Run for one model and one repository with a budget and a time limit, revoked or blocked when the Run ends or its Lease lapses.
- *Defence in depth:* placeholders and proxies inside the pod, NetworkPolicy and RuntimeClass outside it, a leak scan of pushed commits, and a person merging.
- *Loosening the defaults:* each loosening is an explicit, named value. Privileged Docker-in-Docker needs `harness.dind: true` and `harness.acknowledgePrivilegedDind: true`; turning off a credential proxy or sharing one container keeps the agent behind a process boundary only; an unqualified harness needs `allowUnqualifiedHarnesses`. A deployment that leaves P1 must set `supportProfile: unsupported`. The self-assessment and [self-hosting security](../ops/self-hosting-security.md) describe the effect of each.

**Security hygiene.** Private vulnerability reporting ([SECURITY.md](../../SECURITY.md)); DCO on every commit; CI on every pull request with Go tests, vet, CodeQL, licence and brand checks; an OpenSSF Scorecard workflow; Renovate for dependencies; a CVE budget that blocks publishing an image over it; reproducible contract bundles; a weekly attack drill. Features that would be a risk if left unmaintained are kept out of the supported profile: the delegated operator execution path is being retired, and the delegated publisher executor is not enabled ([architecture](../architecture.md#9-where-the-code-diverges-from-designmd)).

**Least privilege required.** ploegd needs no Kubernetes permissions at run time. Installing the chart needs rights to create its Deployments, CronJobs or ScaledJobs, Services, ServiceAccounts and NetworkPolicies in one namespace, and to read the Secrets it references. The sandbox launcher needs a namespaced Role to create and watch `SandboxClaim`s. Worker pods need nothing from the Kubernetes API. The chart can also render an optional admission policy for worker pods.

**Certificate rotation.** ploegd serves plain HTTP inside the cluster and holds no certificate of its own; TLS for public endpoints (webhooks, `ploeg-mcp`) is terminated by the installer's ingress or gateway ([serve ploeg-mcp over HTTPS](../how-to/serve-ploeg-mcp-over-https.md)). Ploeg's own secrets (the LiteLLM master key, worker bootstrap tokens, the worker signing key, forge tokens) have documented rotations ([rotate credentials](../how-to/rotate-credentials.md)). Per-Run credentials expire with the Run.

**Supply chain.** Images and the chart are signed keyless with cosign from the release workflow's identity, each image index carries a BuildKit SPDX SBOM and build provenance, and each digest has a signed OpenVEX document and a signed CVE budget verdict. Images are published by digest and the chart pins its own images by digest. Go dependencies are pinned in `go.sum`; the `claude-runner` image installs Claude Code from the vendor's signed release when the pod starts, so Ploeg never redistributes it. Release binaries and the chart archive have no separate SBOM yet ([F-20](../security/threat-model.md#findings)).

## Where Ploeg fits in the CNCF

- **TAG Workloads Foundation** is the closest home. Its [charter](https://github.com/cncf/toc/blob/6119a69b942853d13ac988b288024afd49ce64ea/tags/tag-workloads-foundation/charter.md) covers batch, scheduling, orchestration, dynamic scaling and CI/CD, and a Ploeg Run is a scheduled batch job with a lease.
- **TAG Security and Compliance**, for the self-assessment and the credential model.
- The **Artificial Intelligence Technical Community Group** ([page](https://contribute.cncf.io/community/tcgs/artificial-intelligence/), read 2026-10-27) has an AI Agents focus that includes agent orchestration. It is a discussion forum, not a TAG, so it does not perform domain reviews.

On the [Cloud Native Landscape](https://github.com/cncf/landscape/blob/c239ac1c6d2e5e096c6cf9f12b9330ae8c832438/landscape.yml), the nearest category is *AI Native Infra / Orchestration and Scheduling*, where Hive Commons is listed. Ploeg is not listed.

No presentation to a TAG has been given yet.

## Principal risks

| Risk | Status on 2026-10-27 |
| --- | --- |
| One maintainer | Blocks filing; [issue #7](https://github.com/ploeg-hq/ploeg/issues/7) |
| No independent adopter or pilot | [issue #27](https://github.com/ploeg-hq/ploeg/issues/27) |
| Egress depends on the installer's CNI | Documented non-goal; the chart renders default-deny policies |
| Seccomp off inside the Kata guest in the one tested cluster | Open finding F-41 |
| Hard dependency on LiteLLM's key and spend APIs | [ADR-0008](../adrs/0008-litellm-is-the-credential-and-metering-seam.md); a second gateway is on the [integration roadmap](ecosystem-fit.md#integration-roadmap) |
| Spend in flight can exceed a key's cap | Documented non-goal; settlement records the excess |
| Close neighbours moving fast (Hive Commons, kagent) | [Ecosystem fit](ecosystem-fit.md#when-to-read-this-again) names the triggers |
