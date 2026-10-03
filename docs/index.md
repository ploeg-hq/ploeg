---
type: landing
audience: [owner, operator, integrator, contributor, agent]
owner: ploeg
last_verified: 2026-09-23
verified_by: "mise run docs-check (every link resolves); apps/ploeg/llms.txt links this page"
---

# Ploeg documentation

Use these sources for the current implementation. The [architecture](architecture.md) distinguishes implemented behavior from remaining limits; the [ADR index](adrs/README.md) distinguishes accepted decisions from proposals.

## Try it

The [local shared demonstration](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/workflows/local-demo.md) runs Ploeg, De Vloer and PostgreSQL without model calls.

## Operate

- [Operator runbooks](how-to/index.md): stuck Leases and Runs, credential rotation, draining, spend, database recovery
- [Managed workers and recovery](ops/managed-workers.md)
- [Metrics and alerts](ops/alerts.md)
- [Tracker configuration](ops/board.md)
- [Configuration reference](reference/configuration.md): every environment variable and Helm value, generated from the source
- [CI and infrastructure](ops/ci-and-infra.md)
- [Experimental releases](ops/release-versioning.md)

## Integrate

- [Published contracts and schemas](contracts/README.md)
- [Managed worker control](contracts/worker-control.md)
- [Tracker execution binding](contracts/tracker-execution.md)
- [Operator candidate delivery](contracts/operator-delivery.md)
- [Executor launch](contracts/executor.md)
- [ACP harness profiles](contracts/acp-profiles.md)

## Understand and change

- [Architecture](architecture.md)
- [Domain source](domain/model.yaml), generated [overview](domain/overview.md), [glossary](domain/glossary.md), [rules](domain/rules.md), [entities](domain/entities.md) and [events](domain/events.md)
- [Decision ledger](adrs/README.md) and the [decision register](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/reference/decisions.md) across all three ledgers
- [Product intent and design history](design.md)
- [Shared product questions](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/landscape/questions.md)
- [Documentation maintenance](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/documentation.md)
- [Brand identity](brand/README.md) and [trademark policy](brand/TRADEMARK.md)

The [backlog](backlog.md), [OpenSpec changes](../openspec/changes/) and [research dossiers](research/) retain plans and evidence. Their existence does not establish implemented behavior or current priority. The [documentation audit](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/vloer/docs/research/2026-09-12-documentation-audit.md) records review coverage.

Human readers and agents use these same Markdown pages. JSON schemas define published wire shapes; YAML is the source for generated domain views. The [machine discovery index](../llms.txt) links here without copying the specification.
