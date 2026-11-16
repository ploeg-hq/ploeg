---
status: accepted
date: 2026-11-16
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-31
---

# Contract schemas are identified by their URL on the project's GitHub Pages site

## Context and Problem Statement

Every JSON Schema in `docs/contracts/` carries an `$id`, and a validator resolves `$ref`s between the schemas against it. The `$id`s pointed at `https://webgrip.dev/ploeg/contracts/<file>`, a host Ploeg does not own as a project, which serves nothing at that path, and which predates the move to an independent project ([ADR-0062](0062-github-is-the-independent-project-home.md)). A consumer that compiles a schema by its `$id` therefore depends on a third party's domain, and nobody can fetch a schema from its identifier.

Which host do the contract schemas' identifiers name, and does that host serve them?

## Decision Drivers

* The identifier names a host the project controls, not one organisation's domain ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* An identifier resolves: fetching it returns the schema at that path.
* No new account, bill or DNS to keep alive for a project with one maintainer.
* Changing identifiers again is a cost every consumer pays, so it should happen rarely.

## Considered Options

* GitHub Pages of the `ploeg-hq` organisation: `https://ploeg-hq.github.io/ploeg/contracts/<file>`
* A project domain bought for Ploeg, served from GitHub Pages or another static host
* Keep `https://webgrip.dev/ploeg/contracts/<file>`
* `urn:` identifiers that resolve nowhere

## Decision Outcome

Chosen option: "GitHub Pages of the `ploeg-hq` organisation", because it is a host the project already controls, costs nothing, and serves the schemas at their identifiers without new infrastructure.

1. Every schema in `docs/contracts/` has `$id` `https://ploeg-hq.github.io/ploeg/contracts/<file name>`. A schema refers to another by a relative `$ref`, so it resolves against that prefix.
2. The [contract pages workflow](../../.github/workflows/contract-pages.yml) publishes `docs/contracts/*.json`, with an index page, to the repository's GitHub Pages site on every push to `development` that touches `docs/contracts/`, and on demand. Pages serves trunk's schemas; a consumer that needs a released version reads the contract bundle of that release ([ADR-0117](0117-every-release-publishes-a-contract-bundle-for-consumers.md)), which stays the pinned source of truth.
3. The repository's Pages source is "GitHub Actions". The owner sets it in the repository settings; no workflow enables Pages.
4. The identifier is not covered by the schema compatibility check, because it changes no message a consumer sends or reads. A consumer that compiles schemas by `$id` updates its loader's prefix when it moves to a release with the new identifiers.

### Consequences

* Good, because every identifier resolves to the schema it names, on a host the project controls.
* Good, because no organisation outside the project appears in a published contract.
* Bad, because a consumer whose tests map the old prefix to local files must change that prefix on its next upgrade. Accepted: the change is one string, and it happens before 1.0.
* Bad, because `github.io` ties the identifiers to GitHub. A custom domain later means changing every `$id` again, with the same consumer cost; this record would then be superseded.
* Bad, because Pages serves trunk, so a schema fetched by its identifier can be newer than the release a consumer runs. The contract bundle remains what a consumer pins.

### Confirmation

`go test ./pkg/harness/` in CI (`mise run verify`): `TestEverySchemaIDIsItsPublishedURL` requires every `docs/contracts/*.json` to carry `$id` `https://ploeg-hq.github.io/ploeg/contracts/<file name>`, and `TestTheContractsLoaderRefusesAnIdentifierOutsideThePublishedPrefix` keeps the test loader from resolving any other prefix, so every contract test compiles the schemas by their published identifiers. The `Contract pages` workflow run on `development` shows the deployment; a reviewer fetches one identifier after it.

## Pros and Cons of the Options

### A project domain bought for Ploeg

* Good, because the identifiers would survive a move away from GitHub.
* Bad, because it adds a yearly renewal and DNS that a lapse turns into a hijackable identifier, for a project with one maintainer.

### Keep `https://webgrip.dev/ploeg/contracts/<file>`

* Good, because no consumer changes anything.
* Bad, because the identifiers name a domain the project does not control and resolve to nothing.

### `urn:` identifiers that resolve nowhere

* Good, because no host is needed at all.
* Bad, because nobody can fetch a schema by its identifier, and consumers still change their loaders.

## Re-evaluation triggers

* The owner buys a domain for Ploeg, or the project moves off GitHub.
* GitHub Pages stops serving `.json` files with a JSON media type, or changes `github.io` project site paths.
* A consumer reports that fetching an identifier returned a schema newer than the release it runs and that broke it.

## More Information

* The schemas and their contract tests: [docs/contracts/](../contracts/README.md) and `pkg/harness/contract_test.go`.
* [Compatibility policy](../ops/compatibility.md): what the schema check compares.
