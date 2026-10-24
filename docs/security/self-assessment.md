---
type: explanation
audience: [owner, operator, contributor, reviewer]
owner: ploeg
last_verified: 2026-10-24
verified_by: "Source read at development c37de7f; repository settings, rulesets, releases and signatures read through the GitHub API and cosign on 2026-10-23"
---

# Self-assessment for Ploeg

This document follows the [CNCF TAG Security and Compliance self-assessment outline](https://github.com/cncf/tag-security/blob/main/community/assessments/guide/self-assessment.md) (read 2026-10-23). Its threat analysis is the [Ploeg threat model](threat-model.md). It is a self-assessment written by the project. It is not an audit, no one independent of the project has reviewed it, and the project holds no security badge.

## Table of contents

* [Metadata](#metadata)
  * [Security links](#security-links)
* [Overview](#overview)
  * [Actors](#actors)
  * [Actions](#actions)
  * [Background](#background)
  * [Goals](#goals)
  * [Non-goals](#non-goals)
* [Self-assessment use](#self-assessment-use)
* [Security functions and features](#security-functions-and-features)
* [Project compliance](#project-compliance)
* [Secure development practices](#secure-development-practices)
* [Security issue resolution](#security-issue-resolution)
* [Appendix](#appendix)

## Metadata

| | |
| -- | -- |
| Assessment Stage | Incomplete. Release-blocking findings are open, and the reviewer sign-off asked for in issue #34 is pending. |
| Software | <https://github.com/ploeg-hq/ploeg> |
| Security Provider | No. Ploeg authorizes, budgets and records AI-agent work. Credential scoping and isolation are central to its design, but securing other systems is not its primary function. |
| Languages | Go (controller, worker, MCP server), Helm templates, Shell, Python (documentation generators), SQL (migrations) |
| SBOM | Each released image (`ghcr.io/ploeg-hq/ploegd`, `ghcr.io/ploeg-hq/claude-runner`, `ghcr.io/ploeg-hq/ploeg-worker`) carries a BuildKit SPDX SBOM and build provenance as OCI attestations in its signed index. Source dependencies are pinned in [go.mod](../../go.mod) and [go.sum](../../go.sum). Release binaries and the chart archive have no separate SBOM asset ([F-20](threat-model.md#findings)). |

### Security links

| Doc | url |
| -- | -- |
| Security file | [SECURITY.md](../../SECURITY.md) |
| Threat model | [docs/security/threat-model.md](threat-model.md) |
| Default and optional configs | [Helm values](../../ops/helm/ploeg/values.yaml), [values schema](../../ops/helm/ploeg/values.schema.json), [configuration reference](../reference/configuration.md) |
| Worker authority and credentials | [Managed worker control](../contracts/worker-control.md), [managed-worker operations](../ops/managed-workers.md) |
| Hostile-repository testing | [The attack drill](../ops/attack-drill.md) |
| Release signing and verification | [CI and infrastructure](../ops/ci-and-infra.md#credentials-and-signing), [releases](../ops/release-versioning.md) |
| Credential rotation | [Rotate credentials](../how-to/rotate-credentials.md) |
| Governance | [GOVERNANCE.md](../../GOVERNANCE.md), [MAINTAINERS.md](../../MAINTAINERS.md), [charter](../cncf/charter.md) |

## Overview

Ploeg is a Kubernetes dispatch plane for AI coding agents. It turns tracker tickets into budgeted, leased Runs. Each Run executes in an ephemeral worker pod with a model-gateway key and a forge credential minted for that Run alone, and ends in a pull request that a person reviews.

### Background

Teams now hand tickets to AI coding agents that read a repository, change it and open a pull request. Done by hand, this hands an agent a long-lived token, a model API key with no ceiling, and a shell on someone's machine. Ploeg puts a control plane between the work and the agent:

- **Admission.** Work enters from a tracker (Vikunja, ClickUp, GitHub Issues) through a signed webhook, or from an authenticated operator consumer.
- **Budget.** Spend is authorized before it happens (a Shift pool and a cap per Role) and settled afterwards from the model gateway's own spend logs.
- **Lease.** One worker at a time owns a Work Item's branch, renews its Lease, and loses it when it stops.
- **Isolation.** The agent ("harness") runs on a repository Ploeg does not control. It receives placeholders instead of credentials, while the worker process next to it holds the real ones behind loopback proxies.
- **Delivery.** Ploeg reads the facts of a delivery from the forge, never from the agent. It never merges; a person does.

The agent is treated as untrusted. Its inputs (repository files, ticket text) may carry prompt injection, so the design assumes the agent may act on an attacker's instructions and bounds what it can reach. The [OWASP Top 10 for LLM Applications 2025](https://genai.owasp.org/llm-top-10/) and the [OWASP Top 10 for Agentic Applications 2026](https://genai.owasp.org/2025/12/09/owasp-top-10-for-agentic-applications-the-benchmark-for-agentic-security-in-the-age-of-autonomous-ai/) describe this risk class. The threat model maps each threat to them.

### Actors

The actors below are separated by an isolation mechanism. The [threat model's trust boundaries](threat-model.md#trust-boundaries) give the detail.

| Actor | Role | Isolation from the others |
| --- | --- | --- |
| **ploegd** (controller) | Admission, Shift engine, claims and Leases, credential minting, settlement, sweeps, operator API, webhooks | Its own pod: distroless, non-root, read-only root filesystem, no capabilities, no service-account token. Sole holder of management credentials: LiteLLM master key, worker signing key, bootstrap registry, forge bot password or GitHub App key, tracker tokens, webhook secrets, operator consumer token hashes. |
| **PostgreSQL** | Durable state: Work Items, Shifts, Runs, Leases, accounts, audit | Reached only by ploegd and by KEDA's scaler role. Minted keys are stored as hashes or ids, not plaintext. |
| **ploeg-worker** | Claims one Run, prepares the clone, starts the harness through the launcher, runs the credential proxies, scans for leaks, verifies, reports | The Job's main container of an ephemeral pod with no service-account token, from the ploeg-worker image. Holds only its team's bootstrap token, the forge token, the Run's signed capability and the Run's keys. Marks itself non-dumpable and sheds `CAP_SYS_PTRACE`. |
| **Harness** (the agent: OpenHands, an ACP agent, or `exec`) | Reads the task, edits the clone, calls the model, pushes the Run's branch | Runs in its own container of the pod (a native sidecar from the harness image) behind `ploeg-worker harness-serve`, with no Secret mounted and no environment of its own ([ADR-0138](../adrs/0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md)). It gets an allowlisted environment, a private HOME and placeholders. A workload with DinD, a proxy off, `claude-environment` or `harnessContainer: shared` keeps it as a child process of ploeg-worker in one container, a process boundary only. |
| **LiteLLM gateway** | Model routing, per-key budgets, spend logs | Separate deployment. Workers hold only per-Run keys scoped to one model, a budget and a TTL. |
| **Forge** (GitHub, Forgejo; GitLab partially) | Repositories, pull requests, CI facts | External. Per-Run tokens limited to one repository; the push fence limits pushes to the Run's branch. |
| **Tracker** | Source of requested work and write-backs | External. Events are HMAC-verified. |
| **Operator consumer** and **ploeg-mcp** | People's applications and AI clients: admit, steer, cancel, read | Authenticated by bearer tokens with team scope and `execute`/`verify` flags. ploeg-mcp verifies OAuth access tokens and calls ploegd with a consumer token per group. |
| **KEDA** | Scales worker Jobs from the pending count | Reads PostgreSQL as a separate role; holds no Ploeg credential. |

### Actions

**Admitting tracker work.** A tracker posts a webhook to ploegd. ploegd verifies the HMAC signature in constant time and rejects every delivery when no secret is configured. It deduplicates by delivery id where the provider sends one and re-reads the item through the tracker API. It then queues a Work Item for the team the assignee or label maps to. No model call happens.

**Opening a Shift and authorizing budget.** The Shift engine opens a Shift with the team's plan. Under the Shift row lock it authorizes each Run at the smaller of the Role cap and the pool's remaining budget. A team cap (`maxRunning`) is enforced inside the claim transaction under an advisory lock.

**Claiming a Run.** KEDA starts a worker pod. The worker presents its team's bootstrap token and a worker id. ploegd matches the token by SHA-256 in constant time and checks that the requested team and Role equal the token's. It claims a pending Run with `FOR UPDATE SKIP LOCKED` and reserves the inference account. It then returns the Run and a control capability: HMAC-SHA256 over the run token, worker id, team, Role and expiry, valid for at most 24 hours.

**Minting credentials.** With the capability, the worker asks ploegd for the Run's model key. ploegd mints it on LiteLLM with the master key, scoped to one model, the authorized budget and a TTL. For a writing Run in a Shift, ploegd also mints a forge credential: a GitHub App installation token for the one repository, or a Forgejo token restricted to that repository. Mint intent is recorded durably first, so a lost response never mints twice.

**Running the harness.** The worker clones the repository and scans instruction files for hidden Unicode and escaping symlinks. It starts the harness with an allowlisted environment. With both proxies on, the harness gets a random placeholder and a loopback URL for the model, and a placeholder forge URL that git reaches through `insteadOf`. The proxies swap in the real credentials. The forge proxy allows only the Role's API calls in the Run's repository, and only pushes to the Run's branch. Every check is made on each request, and a request without the placeholder is refused.

**Renewing and stopping.** The worker renews its Lease every third of the TTL. Three failures or a definitive rejection cancel the harness. The harness is also stopped by a total timeout and an idle timeout, after which the model key is blocked. ploegd's sweep expires Runs whose Lease lapsed, blocks their keys and revokes their forge tokens.

**Reporting and delivering.** The worker scans the pushed commits for the real key, the real token and a canary. A hit fails the Run, parks the Work Item and closes the pull request. The worker reads delivery facts from the forge and runs the configured verification commands on a fresh clone of the pushed commit, with no credential in their environment. It then reports the outcome. ploegd accepts an outcome only while the Run is running, and accepts a replay only with the same digest. A pull request is marked ready for review only when the forge's CI passed on its head. A person merges.

**Settling.** ploegd's sweep settles each finished Run from the gateway's spend logs after a quiet period. It never settles below a recorded observation, and it charges later corrections as deltas.

### Goals

- **No management authority in the execution boundary.** Workers and harnesses never hold the LiteLLM master key, the worker signing key, the bootstrap registry, the forge bot password, the App key or operator credentials.
- **Per-Run, revocable credentials.** A Run's model key and forge token are minted for that Run, scoped to one model and one repository, bounded by budget and time, and blocked or revoked when the Run ends or its Lease lapses.
- **The harness never sees a real credential** in the supported profile. It gets placeholders, and the worker adds the credential on the way out.
- **No paid work without authorization.** Every Run is admitted, budgeted before it starts and settled from the gateway's evidence. A failed or interrupted Run never authorizes another paid attempt by itself.
- **One owner per branch.** A Lease fences a Work Item's branch to one worker. A stale or superseded attempt cannot report an authoritative outcome.
- **Repository content cannot start code or read credentials** through instruction files, configs, plugins, hooks or git configuration.
- **Ploeg never publishes a change on its own.** It never merges, and delivery facts come from the forge.

### Non-goals

- **Ploeg does not make a model resist prompt injection.** It bounds what a hijacked agent can reach. It does not prove the agent will ignore injected instructions.
- **Ploeg is not a sandbox.** The supported profile relies on Kubernetes, the container runtime, Pod Security Admission and the installer's NetworkPolicies for confinement. A namespace alone is not a sandbox.
- **Ploeg does not enforce network egress.** The chart renders no NetworkPolicy for workers ([F-02](threat-model.md#findings)). The installer owns egress.
- **Ploeg does not prove an exact monetary ceiling.** Gateway budgets bound spend, but requests already in flight can exceed a key's cap.
- **Ploeg does not verify the person an operator consumer names.** A consumer is trusted to name its signed-in user truthfully.
- **Ploeg does not review code.** Agent reviewers are advisory, and a person decides what merges.
- **Ploeg does not protect a deployment outside the supported profile.** For example, a deployment that opts into privileged Docker-in-Docker (`harness.dind: true` with `harness.acknowledgePrivilegedDind: true`) gives the agent node-level control ([F-01](threat-model.md#findings); the default no longer does since PR #231).

## Self-assessment use

This self-assessment is created by the Ploeg project to perform an internal analysis of the project's security. It is not intended to provide a security audit of Ploeg, or function as an independent assessment or attestation of Ploeg's security health.

This document serves to provide Ploeg users with an initial understanding of Ploeg's security, where to find existing security documentation, Ploeg's plans for security, and a general overview of Ploeg's security practices, both for the development of Ploeg and for the security of Ploeg itself.

This document provides the CNCF TAG Security and Compliance with an initial understanding of Ploeg to assist in a joint assessment, should Ploeg reach incubation. Taken together, this document and a joint assessment serve as a cornerstone for if and when Ploeg seeks graduation and is preparing for a security audit.

Ploeg is not a CNCF project. It is preparing a possible Sandbox application ([PROVENANCE.md](../../PROVENANCE.md)).

## Security functions and features

### Critical

These are design elements the product's security depends on and that a deployment cannot switch off without leaving the supported profile. The threat model's STRIDE rows trace each one to code and tests.

| Component | Why it matters | Evidence |
| --- | --- | --- |
| Controller-only management credentials | Keeps master, signing and minting authority out of the execution boundary | [_worker_control.tpl](../../ops/helm/ploeg/templates/_worker_control.tpl); worker refuses administrative env vars at start ([main.go](../../cmd/ploeg-worker/main.go)) |
| Worker bootstrap and signed Run capability | Authenticates every worker call and binds it to one Run, worker, team and Role | [worker_auth.go](../../pkg/httpapi/worker_auth.go); [worker_auth_test.go](../../pkg/httpapi/worker_auth_test.go) |
| Lease and outcome fencing | One writer per branch; stale attempts cannot report | Run-token CAS in [store.go](../../pkg/store/store.go) and [shift.go](../../pkg/store/shift.go); `TestSweptRunCannotReport` |
| Inference accounts | Authorize before mint; one durable mint; settlement from gateway evidence | [llm_accounts.go](../../pkg/store/llm_accounts.go); [worker control](../contracts/worker-control.md#accounting-states) |
| Per-Run forge credential broker | Per-repository push rights, revoked on settle and on Lease lapse | [pkg/forgebroker](../../pkg/forgebroker/); [ADR-0013](../adrs/0013-push-rights-are-minted-per-run.md) |
| Credential proxies and push fence | The harness sees placeholders; pushes only to its branch; Role allowlist | [llmproxy.go](../../pkg/worker/llmproxy.go), [forgeproxy.go](../../pkg/worker/forgeproxy.go), [pushfence.go](../../pkg/worker/pushfence.go); [ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) |
| Separate harness container, environment allowlist and process concealment | Keeps the worker's own credentials, and its credentialed `git` children, out of the harness | [pkg/harness/launch](../../pkg/harness/launch/), chart `executor.harnessContainer`, [environment.go](../../pkg/worker/environment.go), [conceal_linux.go](../../pkg/worker/conceal_linux.go), [capabilities_linux.go](../../pkg/worker/capabilities_linux.go); `TestHarnessContainerSeesNoCredential` |
| Worker-private git | Harness-written git configuration cannot redirect or run inside the worker's credentialed git after the harness exits | [workergit.go](../../pkg/worker/workergit.go) |
| Leak watch | Stops a Run that wrote a credential into a commit, request or learning | [leakwatch.go](../../pkg/worker/leakwatch.go); [ADR-0105](../adrs/0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md) |
| Webhook authentication | Only the configured tracker or forge can admit or settle work | Provider `ParseWebhook` implementations in [pkg/provider](../../pkg/provider/) |
| Delivery facts from the forge | Agent claims about pull requests are not trusted | [ADR-0059](../adrs/0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md); [ADR-0119](../adrs/0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) |

### Security relevant

These are settings and components a deployment configures. The threat model's [supported profile](threat-model.md#the-supported-deployment-profile) fixes their values.

| Setting or component | Effect |
| --- | --- |
| `executor.harness.dind` | `false` (chart default since PR #231, [ADR-0136](../adrs/0136-the-chart-default-is-the-strict-isolation-profile-and-privileged-dind-is-an-acknowledged-opt-in.md)) keeps the strict profile. `true` adds a privileged Docker sidecar and turns both proxies off, and renders only with `harness.acknowledgePrivilegedDind: true`. P1 requires `false`. |
| `executor.litellm.keyIsolation`, `executor.forgeTokenIsolation`, `harness.credentialIsolation` | Turn the proxies on or off; an override wins over the chart's safety logic |
| `executor.workerAuth.mode` | `managed` (default) or `legacy`, which leaves worker routes unauthenticated |
| `executor.workerSecurityContext` | Restricted by default ([ADR-0120](../adrs/0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md)); `{}` removes it |
| `executor.forgejo.botPasswordSecret`, `tracker.github.appId` | Enable per-Run forge credentials; without them writers share one token |
| `executor.<forge>.readTokenSecret` | Read-only credential for reading Roles; the chart refuses a reading Role without it |
| Webhook secrets (`webhook.existingSecret`, `executor.<forge>.webhookSecret`, `tracker.*.webhookSecret`) | Without a secret every delivery is rejected |
| `operator.consumers[].teams`, `execute`, `verify`, `maxBudgetUsd` | Operator consumer scope; omitting `teams` means all teams |
| `executor.harness.timeout`, `idleTimeout`, `executor.activeDeadlineSeconds` | Bound a wedged or silent harness |
| `executor.teams[].maxRunning`, plan `pool` and Role `cap` | Concurrency and spend ceilings |
| `forgeFollowUps` | Opt-in rework and repair Runs from forge events; rework only from listed or writing reviewers, capped per Work Item ([F-36](threat-model.md#findings), fixed) |
| `executor.sandbox.*` (experimental) | agent-sandbox executor with an optional runtime class (Kata, gVisor) and managed NetworkPolicy |
| `mcp.*` | ploeg-mcp OAuth settings and optional NetworkPolicies |
| `monitoring.prometheusRule.alerts` | Alerts such as `PloegCredentialLeak`, `PloegModelKeyPastTTL`, spend spikes and expired Leases ([alerts](../ops/alerts.md)) |

## Project compliance

Ploeg has not been assessed against, and does not claim compliance with, any security or regulatory standard (PCI-DSS, ISO 27001, SOC 2, GDPR or others). Data protection for the ticket text, transcripts and repository content Ploeg handles is the operator's responsibility.

The project does not claim any OpenSSF Best Practices badge or SLSA level.

## Secure development practices

### Development pipeline

The facts below were read from the repository and the GitHub API on 2026-10-23.

- **Source control.**
  - Trunk is `development`; stable releases are reviewed tags on `main` ([ADR-0064](../adrs/0064-development-is-trunk-and-release-candidates-come-from-it.md)).
  - Rulesets on both branches block deletion and force pushes. `development` requires the `verify` and `dco` checks; `main` requires `verify`.
  - No ruleset requires a pull request review, CODEOWNERS approval or signed commits. Repository administrators may bypass both rulesets.
  - Release tags `v*` cannot be deleted, updated or force-pushed by anyone.
- **Contributor sign-off.** Every commit in a pull request must carry a `Signed-off-by` matching its author ([DCO](../../CONTRIBUTING.md)). `scripts/dco-check.sh` enforces this and has its own self-test. Bot authors are exempt.
- **Review.** [GOVERNANCE.md](../../GOVERNANCE.md) requires a maintainer other than the author to approve. While there is one maintainer, that maintainer merges their own changes after CI passes. In practice, recent pull requests were merged without a second reviewer.
- **CI checks on every pull request** (`.github/workflows/ci.yml`, read-only `GITHUB_TOKEN`):
  - `go test ./...` with PostgreSQL-backed tests, `go vet` and `gofmt`
  - Helm lint, template and golden renders over all fixture values, plus negative fixtures that must fail
  - license and brand checks, OpenSpec validation, documentation checks
  - a byte-for-byte reproducibility check of the published contract bundle
- **Security tests in CI.**
  - The binary-free attack drill: `TestAttackDrill`, which proves that neither credential reaches the harness environment under both proxies, and `TestHostileConfig`, which proves each ACP profile neutralises a hostile repository's configuration. These run on every pull request.
  - The real-binary tier (OpenCode and Goose driven to `session/new` against a hostile repository) runs weekly, on dispatch and on harness or worker changes, and opens an issue on a scheduled failure ([attack drill](../ops/attack-drill.md)).
  - The worker-auth, forge-proxy, key-proxy, push-fence, instruction-scan, worker-git and leak-watch suites run with `go test`.
- **Vulnerability scanning and SAST in CI** ([ADR 0145](../adrs/0145-a-release-publishes-no-image-over-its-cve-budget-and-attaches-signed-openvex-to-every-digest.md)):
  - Every published image is built on each pull request, on each push and daily, then scanned with Grype (pinned, with a fresh database under 120 hours old). It fails on any Critical or High finding that a reviewed OpenVEX statement in `ops/vex/statements/` does not cover. The budget is zero for every image ([cve-budgets.yaml](../../ops/security/cve-budgets.yaml)). A missing scan or statement fails closed.
  - `govulncheck ./...` fails on a known vulnerability the code can reach.
  - CodeQL (`security-extended`) fails on a finding of security severity 7.0 or more.
  - The OpenSSF Scorecard workflow publishes the repository's score.
  - The release scans each image's pushed digest and tags and signs it only when every image is within budget.
- **Not in CI:** secret scanning in CI, fuzzing and the race detector. GitHub secret scanning with push protection is enabled on the repository.
- **Workflow hardening.**
  - No workflow uses `pull_request_target`, and every checkout sets `persist-credentials: false`.
  - Third-party actions are pinned by full commit SHA. The repository does not enforce pinning.
  - Only the release job holds `id-token: write`, and no protected environment gates it.
- **Dependencies.**
  - Renovate proposes updates. Security fixes merge themselves when `verify` is green; minor and patch updates do the same after a soak.
  - Majors, the Docker-in-Docker image and the release configuration need a manual approval ([ci-and-infra](../ops/ci-and-infra.md#dependency-updates)).
  - The toolchain is pinned with checksums in `mise.lock`.
- **Release integrity.**
  - Release candidates are cut automatically from `development`; a stable release is a reviewed tag on `main` ([releases](../ops/release-versioning.md)).
  - Images are built with `-trimpath` and `CGO_ENABLED=0` on digest-pinned base images (`ploegd`: distroless static, non-root; `ploeg-worker`: Wolfi git on an empty base without a shell, non-root; `claude-runner`: Wolfi, [ADR 0144](../adrs/0144-every-published-image-is-built-on-a-base-that-scans-clean-and-pulls-without-credentials.md)).
  - The release job signs every image and the OCI chart by digest with keyless cosign (GitHub OIDC identity, Fulcio certificate, Rekor log) and verifies each signature before publishing. Verified on 2026-10-23 for `ploegd` 0.2.0-rc.47.
  - BuildKit SBOM and provenance attestations ride in each signed image index.
  - Each image digest carries a keyless cosign attestation of type `openvex`, and the same document is a release asset. It lists every finding the project judged not exploitable, with its justification. Consumers verify it and apply it with `grype --vex` or `trivy --vex` ([ops/vex](../../ops/vex/README.md#verify-and-apply-it)).
  - The chart is stamped with both image digests.
  - Release tarballs, the chart archive and `SHA256SUMS` are not signed, and the only stable release, v0.1.0, predates signing ([F-20](threat-model.md#findings)).
- **Container images.** `ploegd` runs as uid 65532 on distroless with a read-only root filesystem. The worker container is restricted by default. Ploeg publishes no agent image; the installer supplies one.

### Communication channels

- **Internal.** One maintainer today. Decisions are recorded as ADRs in pull requests ([docs/adrs](../adrs/README.md)); priority lives in GitHub Issues and milestones.
- **Inbound.** [GitHub Issues](https://github.com/ploeg-hq/ploeg/issues) for bugs and requests. Private vulnerability reports go through [GitHub private vulnerability reporting](https://github.com/ploeg-hq/ploeg/security/advisories/new), which is enabled. Discussions, a mailing list and a chat channel do not exist yet.
- **Outbound.** GitHub Releases with per-candidate notes and [CHANGELOG.md](../../CHANGELOG.md). Security advisories will be published as GitHub Security Advisories. There is no announcement list yet.

### Ecosystem

Ploeg runs on Kubernetes and composes with other cloud native components rather than replacing them:

- **KEDA** scales worker Jobs from the queue.
- **PostgreSQL**, typically through CloudNativePG, holds state.
- **The Helm chart** installs it, with Prometheus Operator objects for metrics and alerts.
- **OpenTelemetry** carries harness traces and metrics to a collector.
- **External Secrets** or any Secret source supplies credentials.
- **kubernetes-sigs/agent-sandbox**, with Kata or gVisor runtime classes, backs an experimental stronger executor.
- **The LiteLLM gateway** is the credential and metering seam for models ([ADR-0008](../adrs/0008-litellm-is-the-credential-and-metering-seam.md)).
- **Forges and trackers** are reached through provider interfaces.

## Security issue resolution

### Responsible disclosures process

[SECURITY.md](../../SECURITY.md) asks reporters to use GitHub's private vulnerability reporting (a draft security advisory) and never to put credentials, exploit details or private deployment data in a public issue. A reporter who cannot use GitHub opens a public issue that only asks for contact.

### Vulnerability response process

- **Who responds.** The maintainer listed in [MAINTAINERS.md](../../MAINTAINERS.md) is the only responder; there is no backup responder yet.
- **Targets.** The maintainer aims to acknowledge a report within seven days and agree severity with the reporter. A fix is prepared in a private fork when needed. An advisory with credit is published once a fixed release is out. These are targets, not guarantees.
- **Not yet defined:** a fix-time target, an embargo period, a disclosure deadline, CVE assignment (expected through GitHub Security Advisories), and a supported-versions policy beyond "the latest 0.x is the development baseline".

### Incident response

There is no written incident-response procedure yet. Operator-facing response paths exist for the incidents the product itself detects:

- **Leaked credential.** A credential leak parks the Work Item and closes its pull request, and the `PloegCredentialLeak` alert fires.
- **Credential rotation.** Rotation runbooks exist ([rotate credentials](../how-to/rotate-credentials.md)).
- **Stuck work and spend.** Recovery procedures cover stuck Leases and spend reconciliation ([recover a stuck lease or Run](../how-to/recover-a-stuck-lease-or-run.md), [managed workers](../ops/managed-workers.md#reconcile-uncertainty)).

A project-level procedure for triage, confirmation, notification and patch release is open work. It should be modelled on the [TAG incident-response template](https://github.com/cncf/tag-security/blob/main/community/resources/project-resources/templates/incident-response.md).

## Appendix

### Known issues over time

- **Advisories.** No vulnerability has been reported to the project, and no advisory or CVE has been published.
- **Issues caught by tests or review.** The issues the project's own tests and reviews caught are recorded in its decision ledger:
  - The first real-binary attack drill found that OpenCode 1.18.30 started a repository's plugin and MCP server at `session/new`; the profile now sets `OPENCODE_DISABLE_PROJECT_CONFIG=true` ([ADR-0126](../adrs/0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md)).
  - The proxies were changed to refuse any request that does not present the Run's placeholder (Ploeg #64).
  - The worker container gained a restricted security context after a server dry run reported three `restricted` violations ([ADR-0120](../adrs/0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md)).
- **This assessment's findings.** This assessment produced 39 findings, listed in the [threat model](threat-model.md#findings). Seven are release-blocking for the supported profile, two of them only if the profile admits a feature it currently excludes:

| ID | Severity | Summary |
| --- | --- | --- |
| F-01 | Critical | Chart default privileged DinD hands the harness node-level control and turns both credential proxies off (fixed in PR #231: the default is `dind: false`, and `dind: true` needs an explicit acknowledgement) |
| F-24 | Critical | The board watch sent the real forge token from inside the harness-writable clone while the harness ran (fixed in PR #230: the `ls-remote` now runs outside any repository; its token in that process's environment is out of the harness's reach since ADR-0138, #221) |
| F-02 | High | No NetworkPolicy for ploegd or worker pods |
| F-03 | High | Shared builder forge token and bootstrap token sat in the harness's container, guarded by one in-process control that did not cover the worker's `git` children (fixed in PR #240, ADR-0138: the harness runs in its own container with no credential, and `shared` is deprecated) |
| F-04 | Medium | Proxy qualification predates the current proxy code; no Run has used it |
| F-08 | High | ploeg-mcp action confirmation can be skipped by the client (blocking only if ploeg-mcp `execute` groups join the profile) |
| F-36 | High | Any reviewer could re-queue paid Shifts without limit when rework was on (fixed in PR #261: rework only from listed or writing reviewers, capped by `maxReworks`) |

Each finding still needs a disposition, an owner and a date (issue #34). They are not filed as issues by this change.

### OpenSSF Best Practices

Ploeg has no [OpenSSF Best Practices](https://www.bestpractices.dev/en) badge. The Scorecard API returned 404 on 2026-10-23; the [Scorecard workflow](../../.github/workflows/scorecard.yml) publishes a result from its first run on `development`. Against the passing criteria, the project already has:

- a public repository with version control, Apache-2.0 and a contribution guide
- a documented private reporting process
- automated tests in CI with a policy that bug fixes land with a regression test ([AGENTS.md](../../AGENTS.md))
- signed releases
- dependency update automation

To reach the passing badge it still needs to:

- state a vulnerability response time it meets, with a working private channel (enabled) and a backup
- address warnings from CodeQL and govulncheck, which CI now runs and which block on High findings and reachable vulnerabilities
- complete the badge questionnaire

Scorecard would also reward:

- required code review
- branch protection without admin bypass
- fuzzing
- a pinned-dependency policy enforced by the repository
- token permissions at the minimum, which already largely holds

### Case studies

1. **Single-maintainer homelab estate.** The reference deployment runs on the maintainer's own Kubernetes cluster. It ingests tickets from a Vikunja board and delivers pull requests to an in-cluster Forgejo. Every worker runs without DinD and with both credential proxies on. Two teams run under the sandbox executor with Kata micro-VMs. A NetworkPolicy limits worker egress to DNS, ploegd, LiteLLM and Forgejo. This is the environment the [isolation qualification](../research/2026-10-04-isolation-qualification.md) drew its evidence from.
2. **A team on GitHub.** A repository owner labels an issue `ploeg:<team>`. Ploeg mints a GitHub App installation token for that repository alone. A writing Run pushes its branch through the forge proxy and opens a pull request that a person reviews. Reading Roles review with a read-only fine-grained token.
3. **An AI client steering work.** A person's AI client reaches `ploeg-mcp` over HTTPS with an OAuth access token. It reads Work Items and Runs, and (outside the supported profile today) admits or cancels work after the person confirms.

### Related projects / vendors

| Project | How Ploeg differs |
| --- | --- |
| Hosted coding agents (GitHub Copilot coding agent, OpenHands Cloud, others) | Ploeg is self-hosted and tracker- and forge-neutral, and the installer owns every credential. Ploeg is the dispatch, budget and lease layer around agents, not an agent. |
| Agent harnesses (OpenHands, OpenCode, Qwen Code, Goose, Claude Code) | Ploeg runs them as the harness of a Run; it does not replace them ([ACP profiles](../contracts/acp-profiles.md)). |
| kubernetes-sigs/agent-sandbox | A Kubernetes primitive for isolated agent pods. Ploeg can use it as an executor; Ploeg adds admission, budgets, Leases and delivery. |
| LiteLLM | The model gateway Ploeg mints per-Run keys on; Ploeg adds authorization before spend and settlement after it. |
| Board-based agent control planes | Mined for design and never depended on ([ADR-0033](../adrs/0033-board-control-planes-are-mined-for-design-never-depended-on.md), [ADR-0009](../adrs/0009-paperclip-mine-for-design-never-integrate.md)). |
