---
type: landing
audience: [owner, operator, integrator, contributor, agent]
owner: ploeg
last_verified: 2026-09-23
verified_by: "mise run docs-check (every link resolves); llms.txt links this page"
---

# Ploeg documentation

Use these sources for the current implementation. The [architecture](architecture.md) distinguishes implemented behavior from remaining limits; the [ADR index](adrs/README.md) distinguishes accepted decisions from proposals.

## Try it

- [Quickstart](quickstart.md): verify a release's signatures, install it on a local kind cluster and complete one Run with a fake model and a local forge

## Operate

- [Self-hosting security contract](ops/self-hosting-security.md): chart controls, cluster responsibilities, identity gaps and version-specific qualification

- [Operator runbooks](how-to/index.md): stuck Leases and Runs, credential rotation, draining, spend, database recovery
- [Managed workers and recovery](ops/managed-workers.md)
- [Network policies](ops/network.md): what every Ploeg pod may reach, how the chart derives it, and what your CNI must enforce
- [Metrics and alerts](ops/alerts.md)
- [Briefings: versions, catch-up, usefulness and replay](ops/briefings.md)
- [Tracker configuration](ops/board.md)
- [Configuration reference](reference/configuration.md): every environment variable and Helm value, generated from the source
- [Accounting invariants](reference/accounting-invariants.md): the rules Ploeg keeps for inference budgets, holds and settlement, and the tests that check them
- [CI and infrastructure](ops/ci-and-infra.md)
- [Experimental releases](ops/release-versioning.md)
- [Compatibility policy](ops/compatibility.md): what each release keeps working, deprecation, rollback and version pairs
- [Stable channel plan](ops/stable-channel-plan.md): proposed gates, promotion, support window and signing for stable releases, with open decisions

## Integrate

- [Published contracts and schemas](contracts/README.md)
- [Managed worker control](contracts/worker-control.md)
- [Tracker execution binding](contracts/tracker-execution.md)
- [Operator candidate delivery](contracts/operator-delivery.md)
- [Executor launch](contracts/executor.md)
- [ACP harness profiles](contracts/acp-profiles.md)

## Security

- [Threat model](security/threat-model.md): the supported deployment profile, trust boundaries, STRIDE per element and findings
- [Security self-assessment](security/self-assessment.md), following the CNCF TAG Security and Compliance outline
- [Reporting a vulnerability](../SECURITY.md)

## Understand and change

- [Architecture](architecture.md)
- [Domain source](domain/model.yaml), generated [overview](domain/overview.md), [glossary](domain/glossary.md), [rules](domain/rules.md), [entities](domain/entities.md) and [events](domain/events.md)
- [Decision ledger](adrs/README.md)
- [Product intent and design history](design.md)
- [Brand identity](brand/README.md) and [trademark policy](brand/TRADEMARK.md)
- [CNCF Sandbox eligibility](cncf/eligibility.md): the earliest filing date and the maintainer blocker
- [Capacity and work in progress](cncf/capacity.md): weekly hours, the work-in-progress cap and who approves paid qualification spend
- [Licence inventory](cncf/license-inventory.md): every distributed component against the CNCF licence policy
- [Baseline verification record](cncf/evidence/baseline.md): the commit, commands, environment and results of a fresh-clone `mise run verify`

The [backlog](backlog.md), [OpenSpec changes](../openspec/changes/) and [research dossiers](research/) retain plans and evidence. Their existence does not establish implemented behavior or current priority.

Human readers and agents use these same Markdown pages. JSON schemas define published wire shapes; YAML is the source for generated domain views. The [machine discovery index](../llms.txt) links here without copying the specification.
