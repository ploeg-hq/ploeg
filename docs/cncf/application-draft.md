---
type: reference
audience: [owner, reviewer]
owner: ploeg
last_verified: 2026-10-27
verified_by: "Field labels copied from cncf/sandbox .github/ISSUE_TEMPLATE/application.yml at 1c9e414 (last changed 2026-10-10); answers checked against development 080b144 and the GitHub API on 2026-10-27"
---

# Sandbox application draft

This is a draft of Ploeg's answers to the [CNCF Sandbox application form](https://github.com/cncf/sandbox/blob/1c9e414d2700e840cd50b03915dc59244a149eaf/.github/ISSUE_TEMPLATE/application.yml), one section per field, under the field's label as the form prints it. It is not filed. Ploeg files no earlier than **2027-04-03**, and only once a second maintainer is active ([eligibility](eligibility.md)). Before filing, read the form again (it changes: the version used here was last edited on 2026-10-10), recheck every answer, and replace relative links with links at the release tag.

An answer marked **Not yet** names a gap that has to close before filing, or that the form allows to stay open. Contact and signatory details are not drafted here: they stay private until the maintainer approves publishing them ([issue #35](https://github.com/ploeg-hq/ploeg/issues/35)).

The Day 0 technical review is [day-0.md](day-0.md); the comparison behind the overlap and similar-projects answers is [ecosystem-fit.md](ecosystem-fit.md).

## Basic project information

### Project summary

A self-hosted control plane that authorizes, budgets, leases and records AI coding-agent work on Kubernetes.

### Project description

Teams now hand tickets to AI coding agents that clone a repository, change it and open a pull request. Run unattended, that usually means a long-lived forge token, a model key without a ceiling and no clear owner when the agent dies halfway.

Ploeg sits between the work and the agent. Work arrives from a tracker webhook (Vikunja, ClickUp, GitHub Issues) or from an authenticated operator client. Ploeg authorizes a budget for each attempt before it starts, and KEDA or a CronJob starts a short-lived worker pod that must claim the attempt and hold a PostgreSQL lease on its branch. The worker gets a model-gateway key and a forge token minted for that attempt alone, and the agent beside it (OpenHands, an ACP agent such as OpenCode, or any command) sees only placeholders. When the attempt ends, Ploeg reads the result from the forge, settles the spend from the gateway's own logs and records everything. A person merges.

The agent is treated as untrusted, because repository files and ticket text can carry prompt injection. Ploeg bounds what a hijacked agent can reach and spend; it does not try to make the model behave.

What Ploeg adds to the ecosystem is a published contract for paying for and owning agent work, separate from which agent runs and how it is isolated: an executor interface that KEDA, a CronJob or kubernetes-sigs/agent-sandbox can implement, a run API, an operator API and harness schemas. It reaches self-hosted forges and trackers such as Forgejo, GitLab and Vikunja, which most agent tools do not.

Ploeg is experimental (0.x), written in Go, Apache-2.0, and installs with a signed Helm chart.

### Project vs Reference Architecture/Implementation

- [x] This is a reusable open source project designed for broad adoption, not a reference architecture, reference implementation, or demonstration of patterns

Ploeg is a generic controller with its own binaries, chart and versioned APIs. Nothing in it is specific to one organization: providers, Teams, budgets and repositories are configuration, and the code names none of its consumers ([ADR-0069](../adrs/0069-ploeg-names-none-of-its-consumers.md), enforced by `internal/boundary`).

## Project details

### Org repo URL (provide if all repos under the org are in scope of the application)

N/A. Only the repository below is in scope.

The form asks a subproject separating from a parent project for a public parent-maintainer vote. Ploeg's initial tree was extracted from another repository of the same maintainer on 2026-10-03; the approval record and the extraction are in [PROVENANCE.md](../../PROVENANCE.md). **Not yet:** PROVENANCE.md says the full parent roster and any further votes remain to be confirmed publicly. Link the record in "Additional information" once it shows the parent's maintainers approving.

### Project repo URL in scope of application

https://github.com/ploeg-hq/ploeg

### Additional repos in scope of the application

None.

### Website URL

https://github.com/ploeg-hq/ploeg (no separate website)

### Roadmap

https://github.com/ploeg-hq/ploeg/milestones

### Roadmap context

Roadmap decisions are made in public once a month: proposals in GitHub Discussions, a `Decisions YYYY-MM` issue open for seven days, each decision recorded with its reason ([governance](../../GOVERNANCE.md#the-monthly-decision-cycle)). As of 2026-10-27 the milestones are: M1 settle authority and quick wins (due 2026-11-01), M2 adoptable by an outsider (2026-11-29), M3 a qualified release (2027-01-31), M4 used and stewarded beyond the author (2027-03-28), M5 the Sandbox application (2027-04-30), and M6 work after acceptance. The integration roadmap (agent-sandbox qualification, KEDA demand signal, OpenTelemetry, MCP tools inside a Run, a second model gateway) is in [ecosystem fit](ecosystem-fit.md#integration-roadmap).

### Contributing guide

https://github.com/ploeg-hq/ploeg/blob/main/CONTRIBUTING.md

### Code of Conduct (CoC)

https://github.com/ploeg-hq/ploeg/blob/main/CODE_OF_CONDUCT.md

**Not yet:** the current code of conduct is Ploeg's own, with the maintainer as the only enforcement contact and no independent appeal contact. Adopt the CNCF Code of Conduct, or name an independent contact, before filing ([issue #7](https://github.com/ploeg-hq/ploeg/issues/7)).

### Adopters

**Not yet.** There is no ADOPTERS.md, and no organization other than the maintainer runs Ploeg. An independent operator pilot is planned in [issue #27](https://github.com/ploeg-hq/ploeg/issues/27). An adopter is listed only with that adopter's consent.

### Maintainers file

https://github.com/ploeg-hq/ploeg/blob/main/MAINTAINERS.md

The file has the Name, GitHub ID and Company/Organization columns the form requires. **Not yet:** it lists one maintainer, and Ploeg does not file until a second is active ([eligibility](eligibility.md), [issue #7](https://github.com/ploeg-hq/ploeg/issues/7)).

### Security policy file

https://github.com/ploeg-hq/ploeg/blob/main/SECURITY.md

### Standard or specification?

N/A. Ploeg publishes JSON Schemas for its own APIs ([contracts](../contracts/README.md)), versioned by major. They describe Ploeg's interfaces and are not proposed as an industry standard. Ploeg implements ACP and MCP and does not define a protocol of its own.

### Business product or service to project separation

Ploeg's sole rights holder is its maintainer. Its charter excludes the maintainer's hosted products, production infrastructure, customer data, credentials and deployment repositories from the project ([charter](charter.md)). Products that call Ploeg are consumers: they depend on its published, versioned contracts and the release's contract bundle, and Ploeg's code and documentation name none of them, which a repository test enforces ([ADR-0069](../adrs/0069-ploeg-names-none-of-its-consumers.md)). Ploeg's issues, decisions and releases are public in this repository; no product's roadmap decides Ploeg's.

### Dependency on paid cloud, community, and SaaS services or subscriptions

Development uses GitHub (free for public repositories), GitHub-hosted Actions runners and GHCR. No paid community tool is used.

The project pays for one thing: model calls during paid qualification Runs and live accounting checks, approved by the maintainer and capped at $5 per Run without a fresh approval ([capacity](capacity.md#paid-qualification-spend)). **Not yet:** a monthly estimate of that spend. Fill it in from the gateway's spend log for the three months before filing.

Using Ploeg needs no paid service from the project. An installer needs a model provider, which can be self-hosted behind the gateway.

## Cloud native context

### Why CNCF?

Agent workloads are arriving on Kubernetes faster than the shared practices for running them. Ploeg's design leans on CNCF projects (Kubernetes Jobs and NetworkPolicy, KEDA, Helm, Prometheus) and on the agent-sandbox API from Kubernetes SIG Apps, and its open questions are the ones CNCF groups are working on: how an untrusted agent's Run is isolated, how it is paid for and audited, and how execution backends such as agent-sandbox, Agent Substrate and OpenShell can be swapped under one control plane.

Neutral ownership of the name and repository would let contributors from other organizations trust that the accounting and executor contracts will not be bent toward one company's product, and the CNCF is where the projects next to Ploeg already work. The transfer that ownership needs is not decided yet (see Trademark and accounts). It also wants the review: a TAG reading its security model and the executor interface is worth more to adopters than the project's own self-assessment.

### Benefit to the landscape

The landscape has agent frameworks, agent runtimes and sandboxes, and model gateways. It has fewer projects that answer "may this agent work run, who owns it now, and what did it cost?" for unattended coding work on a cluster. Ploeg's contribution there is the authorize-then-settle accounting (a budget authorized before each Run and settled from the gateway's own spend logs, never guessed), a lease that the controller expires so a dying agent cannot leave a branch owned, credentials minted per Run that the agent's container never holds, and an executor contract that lets KEDA, a CronJob or agent-sandbox launch the same Run. It also brings self-hosted forges and trackers (Forgejo, GitLab, Vikunja) into reach of agent automation.

### Cloud native 'fit'

Ploeg is a control plane for batch work on Kubernetes. A Run is a short-lived Job (or an agent-sandbox `SandboxClaim`), scaled from zero by KEDA on queue depth, isolated by RuntimeClass, NetworkPolicy and Pod Security Admission, and fenced by a lease the controller expires. ploegd keeps no state in memory that a restart loses: coordination is in PostgreSQL, so a rollout can overlap two controllers safely. Configuration is declarative Helm values, releases are signed and pinned by digest, and the chart defaults to the strict isolation profile. The closest TAG is Workloads Foundation (batch, scheduling, orchestration, dynamic scaling), with Security and Compliance for the credential model; the [Day 0 review](day-0.md#where-ploeg-fits-in-the-cncf) explains the choice.

### Cloud native 'integration'

- **Kubernetes:** Jobs, CronJobs, NetworkPolicy, Pod Security Admission, RuntimeClass; worker pods have no service-account token.
- **KEDA:** the default executor is a `ScaledJob` per Team and Role, scaled from the pending count in PostgreSQL.
- **Helm:** the signed chart is the install path.
- **Prometheus:** `/metrics`, an optional ServiceMonitor and PrometheusRule alerts.
- **OpenTelemetry:** the chart passes an OTLP endpoint to harnesses; per-Team collectors are proposed ([ADR-0113](../adrs/0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md)).
- **kubernetes-sigs/agent-sandbox:** an experimental executor that runs each Run in a `Sandbox` from a warm pool.
- **gVisor and Kata Containers:** selected per Team or Role through RuntimeClass.
- **CloudNativePG:** the chart's network policy can name a CloudNativePG cluster as ploegd's database peer ([network](../ops/network.md)).
- **ORAS:** each release publishes its contracts as an OCI artifact with `oras`.

### Cloud native overlap

- **kagent** (Sandbox): also runs coding harnesses (Codex, Claude) on Kubernetes with PostgreSQL state and keeps credentials out of the agent. kagent defines long-lived agents as CRDs and talks A2A and MCP; it has no tracker or forge integration and no budget it authorizes and settles.
- **Hive Commons** (Sandbox, TOC vote passed 2026-09-28): the closest overlap. It also turns repository work into agent pull requests under limits, with scoped forge tokens and an audit trail. It is GitHub-centred, runs agent CLIs in one container, and estimates cost from token counts with a rolling budget. Ploeg runs each attempt as its own Kubernetes Job with a lease and a per-Run key whose budget is authorized and settled, and reaches Forgejo and GitLab.
- **Agent Substrate** and **OpenShell** (Sandbox): overlap only on keeping credentials out of the sandbox. Both are runtimes Ploeg could run on.
- **Argo Workflows** (graduated) and **Tekton** (incubating): overlap on running containers as retried steps. Ploeg does not run DAGs or CI; [ADR-0005](../adrs/0005-build-a-dedicated-dispatch-plane.md) records why Argo was not used.
- **Dapr Agents** (part of Dapr, graduated): overlaps on durable agent execution, but it is a framework for writing agents, and Ploeg runs agents others wrote.

The full table, with where each of these is ahead of Ploeg, is in [ecosystem fit](ecosystem-fit.md#comparison).

### Similar projects

In the CNCF: Hive Commons, kagent. Below Ploeg in the stack: Agent Substrate, OpenShell, kubernetes-sigs/agent-sandbox. Outside the CNCF: openai/symphony, github/gh-aw, multica-ai/multica and Paperclip were compared in an earlier survey ([2026-09-26](../research/2026-09-26-agent-orchestration-landscape.md), not rechecked for this draft); on that date none of them combined a per-Run Kubernetes Job, a controller-expired lease and authorize-then-settle spend on a self-hosted forge and tracker.

### Landscape

No. Ploeg is not listed on the Cloud Native Landscape. The nearest category is *AI Native Infra / Orchestration and Scheduling*.

### Insights

No. Ploeg is not listed on LFX Insights.

## CNCF policies

### Trademark and accounts

- [ ] If the project is accepted, I agree to donate all project trademarks and accounts to the CNCF

**Not yet.** The maintainer, as sole rights holder, has not decided this. The charter and governance say that transferring the name, mark, domains, accounts or repository needs a separate owner decision ([charter](charter.md), [trademark policy](../brand/TRADEMARK.md)). The box is ticked only after that decision.

### IP policy

- [ ] If the project is accepted, I agree the project will follow the CNCF IP Policy

**Not yet**, for the same reason. Code is already Apache-2.0 and documentation CC-BY-4.0, as section 11 of the CNCF charter asks ([licence inventory](license-inventory.md)).

### Will the project require a license exception?

N/A. Ploeg's code is Apache-2.0.

### Does the project utilize any dependencies with licenses not on the allowlist or an approved exception?

Two small MIT-licensed helpers of the pgx PostgreSQL driver, `github.com/jackc/pgpassfile` and `github.com/jackc/pgservicefile`. MIT is allowlisted, but on 2026-10-25 they did not clearly pass the policy's use test (12 months on GitHub and 10 stars or forks). The exception request is written and is filed during onboarding, because the request form needs a CNCF project to name ([exception request](exceptions/jackc-pgpassfile.md), [licence inventory](license-inventory.md)). Three further items are recorded for CNCF staff to confirm rather than as known gaps: the operating-system packages in the published images (GPL, LGPL, MPL-2.0 and others), reviewed under the CNCF container image guidance and not the allowlist; the chart's default `docker:dind` image, which the user's cluster fetches; and `github.com/modelcontextprotocol/go-sdk`, whose LICENSE also names CC-BY-4.0 for documentation. Every other shipped Go module is Apache-2.0 or allowlisted.

### Project "Domain Technical Review"

The Day 0 part of the General Technical Review is answered in [day-0.md](day-0.md). **Not yet:** no presentation to a TAG has been given, and no TAG has reviewed it. The form links the review questions at `toc_subprojects/project-reviews-subproject/`, which moved to [`operations/project-reviews/`](https://github.com/cncf/toc/blob/6119a69b942853d13ac988b288024afd49ce64ea/operations/project-reviews/general-technical-questions.md) on 2026-09-08; the answers follow the moved file, template v1.0.1.

## Contact information

### Application contact email(s)

Held privately until the maintainer approves publication ([issue #35](https://github.com/ploeg-hq/ploeg/issues/35)).

### Contributing or sponsoring entity signatory information

Held privately, as above. **Not yet:** whether the organization table or the individual table applies is part of the owner decision on transfer ([charter](charter.md)).

## Additional information

### CNCF contacts

None. No one in CNCF leadership has been asked to vouch for Ploeg, and none is named until someone agrees.

### Additional information

- Repository created 2026-10-03; the initial tree and its origin are recorded in [PROVENANCE.md](../../PROVENANCE.md), and earlier history is not claimed toward the six-month age.
- Security self-assessment: [docs/security/self-assessment.md](../security/self-assessment.md), with every release-blocking finding fixed and the rest given dispositions.
- Release verification without an account: [verify a release](../ops/verify-a-release.md).
- Baseline verification from a fresh clone: [evidence](evidence/baseline.md).

## Gaps before filing

| Gap | Field | Tracked in |
| --- | --- | --- |
| Filing date no earlier than 2027-04-03 | Pre-submission checklist | [eligibility](eligibility.md) |
| Second active maintainer | Maintainers file | [issue #7](https://github.com/ploeg-hq/ploeg/issues/7) |
| Code of conduct with an independent contact | Code of Conduct | [issue #7](https://github.com/ploeg-hq/ploeg/issues/7) |
| At least one consenting adopter or completed pilot | Adopters | [issue #27](https://github.com/ploeg-hq/ploeg/issues/27) |
| Public parent-maintainer approval record | Org repo URL; Additional information | [PROVENANCE.md](../../PROVENANCE.md) |
| Owner decision on trademark, account and IP transfer | Trademark and accounts; IP policy | [charter](charter.md) |
| Monthly estimate of paid qualification spend | Dependency on paid services | [capacity](capacity.md) |
| A TAG presentation or review of the Day 0 answers | Project "Domain Technical Review" | [issue #32](https://github.com/ploeg-hq/ploeg/issues/32) |
| Private contact and signatory appendix | Contact information | [issue #35](https://github.com/ploeg-hq/ploeg/issues/35) |
