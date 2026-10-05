---
status: proposed
date: 2026-10-05
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Shared formulas are pinned by published conformance vectors

## Context and Problem Statement

Some Ploeg computations are reimplemented by consumers:

* **The rarity score** ([ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md), `pkg/rarity/rarity.go`). Unfold has a TypeScript copy for its deterministic demo (`src/rarity.ts`) and a browser copy for the card breakdown (`public/cards/card-model.js`).
* **The working-time calendar** (`pkg/flow/calendar.go`). It is copied in Unfold's demo (`src/ploeg-demo-kpis.ts`).

Each copy has its own tests, and none shares an input with another. The copies already differ subtly: the TypeScript percentile counts the card in its own cohort, while Go excludes it. They agree today only because the call sites line up.

A shared Rust core compiled to WebAssembly was proposed as the fix. How should Ploeg keep the formulas it owns from drifting in the copies it does not?

## Decision Drivers

* **Ploeg owns the formula.** It does not name its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* **A formula change must fail every copy at once.** A version bump such as 2026.2 is the case that matters.
* **No new toolchain** for about 60 lines of arithmetic. wazero has no Component Model, and a Wasm boundary needs a hand-rolled ABI.
* **Unfold's frontend has no build step** (Unfold ADR 0002).

## Considered Options

* Ploeg publishes conformance vectors that every implementation's tests run
* A shared Rust core compiled to WebAssembly, called through wazero from Go and wasm-bindgen from the browser
* Go compiled to WebAssembly for the browser
* Consumers call Ploeg for every computed value and keep no copy

## Decision Outcome

Chosen option: "**Ploeg publishes conformance vectors that every implementation's tests run**". It pins every copy for no runtime cost and needs no new toolchain.

How it works:

* **Ploeg publishes one JSON file per versioned formula** under `docs/contracts/fixtures/`:
  * `rarity-2026.1.json`: inputs and the expected score, tier and percentile;
  * `working-time-v1.json`: intervals and the expected working seconds.

  A JSON Schema describes the vector format.
* **Ploeg's own tests load the same file.** `pkg/rarity` and `pkg/flow` read it, so the published vectors cannot drift from Go.
* **Consumers run it as a fixture.** Any consumer that reimplements a formula runs the same file in its own tests. Ploeg publishes the file and does not know who runs it.
* **A formula change ships a new vector file.** A new version, for example 2026.2, adds `rarity-2026.2.json`; old files stay, because frozen cards keep their version.

The computation that matters for money or delivery remains server-side in Ploeg. A consumer's copy is for demos and display only.

### Consequences

* Good, because a formula bump fails every implementation that has not followed it, in each repository's own CI.
* Good, because the vectors double as documentation of edge cases, such as the cohort-inclusion rule.
* Good, because there is no new language, toolchain or runtime boundary.
* Bad, because copies still exist. Vectors catch drift on the cases they list, not on every possible input.
* Bad, because adding a formula means adding its vector file and keeping it complete.

### Confirmation

* **Ploeg's tests.** `mise exec -- go test ./pkg/rarity/ ./pkg/flow/` loads every file in `docs/contracts/fixtures/` that names those formulas, and fails on any mismatch.
* **Schema.** A test validates each vector file against its schema, alongside the existing contract tests in `pkg/harness/contract_test.go`.
* **Review.** A change to `pkg/rarity` or `pkg/flow` that alters an output without adding or editing a vector file fails the Go test above.

## Pros and Cons of the Options

### A shared Rust core compiled to WebAssembly

* Good, because there is one implementation.
* Bad, because it adds a third toolchain and two Wasm build targets for about 60 lines of arithmetic.
* Bad, because wazero has no Component Model (wazero issue #2200), so calls from Go cross a hand-rolled linear-memory ABI.
* Bad, because it adds a build step to a frontend that has none.

### Go compiled to WebAssembly for the browser

* Good, because `pkg/rarity` would be the single source.
* Bad, because standard Go output is several megabytes, and TinyGo supports only a subset of Go. Glue is still needed.

### Consumers call Ploeg for every computed value

* Good, because there would be no copies at all.
* Bad, because a deterministic demo must run without Ploeg, and the browser breakdown would need a round trip per card.

## Re-evaluation triggers

* A third formula gets a consumer copy, or one copy exceeds 200 lines. Generating the copy may then pay.
* wazero ships Component Model support, or Unfold's frontend adopts a build step.
* A drift between Ploeg and a consumer copy reaches a user despite the vectors.

## More Information

* Evidence: [Substrate, language and Run bottlenecks](../research/2026-10-05-substrate-language-and-run-bottlenecks.md), §5–6.
* Related: [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md), [ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md).
* 2026-10-05: proposed instead of a shared Rust or WebAssembly core.
