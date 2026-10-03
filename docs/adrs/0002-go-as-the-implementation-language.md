---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Go is the implementation language

## Context and Problem Statement

Ploeg is a long-lived control-plane service that runs on Kubernetes, talks to
Postgres, spawns and supervises subprocesses, and must ship as a container image
small enough to be a sidecar-adjacent init payload. The language choice
determines the contributor pool, the operator ergonomics, and whether the v2
graduation to a CRD/operator is a rewrite or a refactor.

## Decision Drivers

* **Kubernetes contributor pool** — the people who would extend a dispatch plane
  already write Go.
* **A controller-runtime path for v2** — the CRD/operator graduation on the
  roadmap should not be a language migration.
* **Static binary** — the worker binary is copied out of the ploegd image by an
  init container and executed *inside the team's harness image*
  (`ops/helm/ploeg/templates/_helpers.tpl`). That only works if it has no
  runtime dependencies.
* **Thin glue, not a platform** — the value is in semantics (leases, claims,
  outcomes), so the language should stay out of the way.

## Considered Options

* Go
* Rust
* TypeScript / Node

## Decision Outcome

Chosen option: **Go**.

The static-binary requirement is close to decisive on its own. The worker does
not get its own container: `_helpers.tpl` renders the worker container *using
the agent image*, with an init container copying `/usr/local/bin/ploeg-worker`
into an emptyDir. A runtime that needed an interpreter, a shared library, or a
package manager present in every third-party harness image would make the
"bring your own harness image" promise unkeepable.

### Consequences

* Good, because `controller-runtime` is available unchanged when the operator
  graduation happens.
* Good, because the harness images stay unconstrained — an adapter author ships
  whatever base image they like.
* Bad, because Go's error handling makes the failure-path density in
  `pkg/worker` verbose. Accepted: this codebase mints and revokes real
  credentials, and explicit error paths are the point.

### Confirmation

Structural, and checked by the existing CI gate set in
`.forgejo/workflows/on_pull_request.yml` (`gofmt`, `go vet`, `go build`,
`go test`). A change of language would not pass them.

## Pros and Cons of the Options

### Rust

* Good, because the strongest guarantees on a money-handling path.
* Bad, because the Kubernetes-operator ecosystem is materially thinner, and the
  contributor pool for a self-hosted dispatch plane is smaller.

### TypeScript / Node

* Good, because the harness ecosystem (ACP SDKs, most agent CLIs) is
  TypeScript-first, so adapters would share a runtime with their targets.
* Bad, because there is no static binary, which breaks the init-container copy
  into arbitrary harness images.
* Bad, because fronting a small dispatcher with a Node runtime inverts the
  thin-glue rationale — the argument recorded against Paperclip in
  [0009](0009-paperclip-mine-for-design-never-integrate.md) applies to our own
  stack too.

## More Information

* Migrated from `docs/design.md` §9 on 2026-07-29. The decision predates this
  ledger; its original date is unrecorded.
* The init-container copy that depends on it:
  `ops/helm/ploeg/templates/_helpers.tpl`, `cmd/ploeg-worker/main.go`.
