# Ploeg

Developed independently at [ploeg-hq/ploeg](https://github.com/ploeg-hq/ploeg). See [provenance](PROVENANCE.md).

Ploeg is a self-hosted service for authorizing and coordinating agent work. Tracker assignments can start unattended workers; an operator consumer can run interactive sessions under the same execution authority. PostgreSQL retains the work, leases, outcomes, evidence and accounting.

*Ploeg* is Dutch for a work crew or shift. The software is experimental and starts its independent release line at **0.1.0**. Qualification applies to specific tested paths, not every provider or deployment.

`Ploeg` is the project. `ploegd` is its long-running controller/server (the `d` means daemon). `ploeg-worker` claims authorized work and executes it. Both binaries ship in the controller image.

## Start here

- [Quickstart](docs/quickstart.md): install a signed release on a local kind cluster and complete your first Run, with no outside accounts.
- [Documentation](docs/index.md): current guides, contracts and design history.
- [Architecture](docs/architecture.md): what runs where and who holds authority.
- [Managed workers](docs/ops/managed-workers.md): required configuration and recovery.
- [Network policies](docs/ops/network.md): what every Ploeg pod may reach, and what your CNI must enforce.

## How it works

Verified tracker webhooks enqueue work. KEDA or the CronJob executor starts workers that must claim authorized work. Configured Shift plans coordinate roles and review rounds. Workers invoke a harness, report results and renew their leases. KEDA polls queue depth; idle queue checks do not require model calls.

An operator consumer supplies the human workbench and delegated workspace execution through the operator API. Its admitted path retains one Ploeg Work Item, Shift and operator Run across changes in supervision. Supported [tracker selections](docs/contracts/tracker-execution.md) can bind an existing queued Work Item. Manual-origin admission need not create a tracker ticket.

Management credentials remain in the controller. [Scoped worker capabilities](docs/contracts/worker-control.md) authorize control and inference. Unknown spending stays unresolved until trusted reconciliation.

The source includes Vikunja, ClickUp and GitHub Issues tracker integrations, Forgejo, GitLab and GitHub forge integrations, team plans, tracker write-backs and multiple harness adapters. Capabilities differ by provider. See the [implementation map](docs/architecture.md#6-providers-harnesses-and-delivery) and [published contracts](docs/contracts/README.md).

Delegated [candidate delivery](docs/contracts/operator-delivery.md) records verified evidence and candidate-bound approval. Its live publisher executor is not enabled. A completed Run is not automatically a published or accepted result.

## Develop

Run `mise run setup`, `mise run verify` and `mise run docs-check`. Run tooling through mise and follow the [repository instructions](AGENTS.md). The [CI workflow](.github/workflows/ci.yml) defines Go build, vet and tests, Helm validation and golden renders, and brand/license checks. The [backlog](docs/backlog.md) records planning history; [GitHub Issues](https://github.com/ploeg-hq/ploeg/issues) owns Ploeg priority.

GitHub Issues can be a runtime tracker: a `ploeg:<team>` label on an issue in a configured repository dispatches it ([how to connect it](docs/how-to/connect-github-issues.md)). GitHub can also be the forge: a Run pushes and opens its pull request with its own GitHub App installation token ([how to connect it](docs/how-to/connect-github-as-a-forge.md)). Hosting Ploeg's own backlog on GitHub configures nothing; a deployment opts in.

## Project

[Governance](GOVERNANCE.md) · [Maintainers](MAINTAINERS.md) · [Contributing](CONTRIBUTING.md) · [Code of conduct](CODE_OF_CONDUCT.md) · [Security](SECURITY.md) · [Scope and stewardship](docs/cncf/charter.md) · [Provenance](PROVENANCE.md)

## Releases

See [release instructions](docs/ops/release-versioning.md) and [CI and infrastructure](docs/ops/ci-and-infra.md). Version 0.1.0 remains experimental; it does not claim production qualification or CNCF membership.

## License

Code: [Apache-2.0](LICENSE).

The name *Ploeg* and the Ploeg mark are trademarks — §6 of that licence grants
no rights in them, deliberately. [docs/brand/TRADEMARK.md](docs/brand/TRADEMARK.md)
says what you may do with them without asking (reproduce them, link, say your
software works with Ploeg) and the two things that need permission (shipping a
fork under the name, implying endorsement). Settled in
[ADR-0022](docs/adrs/0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md).
