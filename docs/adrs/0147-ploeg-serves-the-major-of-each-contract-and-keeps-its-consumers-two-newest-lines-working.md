---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Ploeg serves the major version of each contract and keeps its consumers' two newest lines working

## Context and Problem Statement

[ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) promises that an operator consumer built against the contract bundle of release N or the previous minor works with `ploegd` N, and `go test ./internal/compat/` holds each release to the one before it. Two gaps remain. A running consumer cannot ask a Ploeg which contracts it serves, so a consumer pointed at an incompatible Ploeg finds out request by request, as 404s or parse failures, instead of once and loudly. And the compatibility check compares Ploeg with its own previous release: it cannot tell a field no consumer reads from one a consumer depends on, so it treats both as the same break and has no evidence that any consumer still works. Ploeg is meant to be consumed by projects it does not know about, released on their own schedules. How does a consumer learn what a Ploeg serves, and how does Ploeg know a change keeps its consumers working?

## Decision Drivers

* A consumer must be able to refuse an incompatible Ploeg at startup, with a message naming both sides, rather than misbehave.
* The version a consumer checks must come from the published schemas, not from a number someone remembers to raise.
* Ploeg must learn of a consumer break in its own CI, before a release, from evidence the consumer supplied.
* Ploeg names no consumer in code: any project can record its expectations the same way.
* A release must not need a hand-kept list of every consumer version ever shipped.

## Considered Options

* A version route that lists each contract's served majors, plus consumer expectation sets for each consumer's two newest release lines, checked on every change
* A single API version for all of Ploeg, raised on every contract change
* Cross-version qualification: run each consumer's own test suite against each Ploeg candidate
* A version header on every response and no recorded expectations

## Decision Outcome

Chosen option: "**A version route that lists each contract's served majors, plus consumer expectation sets for each consumer's two newest release lines, checked on every change**", because it gives a consumer one cheap startup check, derives the version from the schema files, and tells Ploeg which changes break a real consumer without running that consumer.

1. **The contract version is the major of each published contract.** `docs/contracts/<name>.v<N>.schema.json` is major N of contract `<name>`; a file without `.v<N>.` names its major in its title (`operator-delivery`, `1.0`). [pkg/contracts](../../pkg/contracts/contracts.go) lists every contract and the majors served, and a test fails when that list and the files disagree. Within a major only optional additions land ([contracts](../contracts/README.md#versioning-policy)). A change that removes, renames or retypes something a recorded consumer reads adds major N+1 as a new file, and Ploeg serves both majors for one stable minor before removing N. ADR 0134's removal of a deprecated element inside a major stays allowed only when no recorded consumer expectation reads it, which point 4 checks.
2. **Ploeg advertises what it serves.** `GET /api/v1/operator/version` answers `versionResponse`: the release `ploegd` was built with and every served contract with its majors. Any configured operator consumer may read it whatever its team scope; an anonymous caller gets 401, so the release version is not offered to scanners. A Ploeg that answers 404 there predates the route and serves major 1 of every contract it has.
3. **The skew window is a consumer's two newest release lines.** A consumer records what each release line relies on in `docs/contracts/consumers/<consumer>/<major>.<minor>.json`: the contract majors it requires, and for each request it sends the method, the path, a body it actually sends with the request definition it must satisfy, and the response definition with every value it reads, the JSON types it accepts there and whether it needs the value present. At most two lines per consumer are kept: its current line and the one before it. Together with ADR 0134's promise this is an N/N-1 window in both directions: Ploeg N keeps the consumer's lines N and N-1 working, and a consumer line supports the Ploeg it was built against and the previous Ploeg minor.
4. **Every change runs every recorded expectation.** `TestEveryRecordedConsumerExpectationHolds` in `pkg/httpapi` loads every set and checks, against the tree under test: each required major is served; each method and path is routed by `ploegd`'s own mux; each request body still validates against its definition; each response definition exists; and each read still resolves, is still required when the consumer needs it, and sends no JSON type the consumer does not accept. Enum values are not checked, because consumers must already treat an unknown value as unknown. [pkg/consumers](../../pkg/consumers/consumers.go) holds the check so a consumer can run the same one against a Ploeg release from the Go module.
5. **A consumer owns its file.** A consumer adds or replaces its newest line by pull request, and removes its oldest when a third would appear; `Load` refuses a third line, an unknown field or a file whose name and content disagree. When an expectation fails, the change is fixed or turned into a new major (point 1). Editing a consumer's expectation to make a break pass needs that consumer's agreement in the pull request. The file names its consumer so a failure says whom to ask; nothing else in Ploeg may name a consumer, and this file may because the consumer wrote it. The [ADR 0069](0069-ploeg-names-none-of-its-consumers.md) boundary check exempts `docs/contracts/consumers/` and nothing else.

### Consequences

* Good, because a consumer can check compatibility once at startup and name both versions in its error.
* Good, because the version cannot drift from the schemas: a new `v2` file without a served-list entry, or the reverse, fails the build.
* Good, because a removal that no recorded consumer reads passes, and one a consumer reads fails with the consumer, the request and the pointer named, which the release-to-release comparison cannot say.
* Good, because the check is static and needs no consumer checkout, browser or toolchain in Ploeg's CI.
* Bad, because the check is only as good as the recorded expectations. A value a consumer reads but did not record can still break it; the consumer keeps its file honest with its own tests.
* Bad, because a breaking change now costs a second schema file and an overlap minor of serving both. Accepted: that is the cost the consumer would otherwise pay.
* Bad, because expectation files arrive by pull request from each consumer. Until a consumer's release automates that, a new line reaches Ploeg by hand.
* Neutral: the worker version handshake ADR 0134 point 7 decides stays separate; workers ship with `ploegd` and are not operator consumers.

### Confirmation

* `go test ./pkg/contracts/` fails when `contracts.Served` and the schema files under `docs/contracts` disagree.
* `go test ./pkg/httpapi/ -run 'TestVersion|TestServesRoute|TestEveryRecordedConsumerExpectationHolds'` checks the version route against `operator-api.v1.schema.json`, refuses an anonymous caller, and runs every recorded expectation set; it fails when the directory holds none.
* `go test ./pkg/consumers/` breaks each rule one way and checks it fires (removed and renamed field, changed type, newly nullable value, field no longer required, removed definition, new required request field, refused request field, removed route, unserved major) and applies compatible changes that must pass (optional response and request fields, new enum value, new definition, a field the consumer does not read made required, new route, a second major served).
* Mutation on the real tree, 2026-10-24: renaming `team.queueDepth`, making `item.title` nullable, adding a required `reason` to `requeueRequest`, and removing the `route-refusals` route each failed the expectation test with the consumer, request and pointer named; adding an optional field to `team` passed it and `go test ./internal/compat/`.
* `go test ./internal/boundary/` still fails on a consumer name anywhere else, including `docs/contracts/README.md`, and passes it inside a consumer's own expectation file.
* Gate: the `verify` job of `.github/workflows/ci.yml` runs `go test ./...` on every pull request and trunk push.

## Pros and Cons of the Options

### A single API version for all of Ploeg, raised on every contract change

* Good, because a consumer compares one number.
* Bad, because a change to the run API would mark every operator consumer incompatible, and nothing derives the number from the schemas, so it drifts.

### Cross-version qualification: run each consumer's own test suite against each Ploeg candidate

* Good, because it exercises real code on both sides.
* Bad, because Ploeg's CI would need every consumer's checkout and toolchain, and would have to know its consumers by name.
* Bad, because a failure shows up as a consumer's test output, not as the contract element that changed.

### A version header on every response and no recorded expectations

* Good, because a consumer sees an upgrade between two calls.
* Bad, because Ploeg still learns of a break only from a consumer's production.

## Re-evaluation triggers

* A consumer breaks on a Ploeg release that `TestEveryRecordedConsumerExpectationHolds` passed.
* A second consumer records expectations, or one asks for more than two lines to be kept.
* Ploeg adds a major 2 of any contract.
* The owner opens the 1.0 decision of ADR 0134.
* A consumer's release automates the pull request that adds its expectation set.

## More Information

* Refines [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) points 3 and 5 for the HTTP and harness contracts: the consumer pair it promises is now checked, and a removal a consumer reads needs a new major.
* Prior art: [Kubernetes version skew policy](https://kubernetes.io/releases/version-skew-policy/), consumer-driven contracts as in [Pact](https://docs.pact.io/) (expectations recorded by the consumer, verified by the provider), read 2026-10-24.
* Code: `pkg/contracts`, `pkg/consumers`, `pkg/httpapi/operator_version.go`, `docs/contracts/consumers/`.
* 2026-10-24: proposed with the version route, the served-contract list and the expectation check (VIK-2398).
