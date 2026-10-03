---
status: accepted
date: 2026-10-03
decision-makers: Ryan Grippeling
supersedes: 0004
review-by: 2027-01-03
---

# GitHub is the independent project home

## Context and Problem Statement

Ploeg is separating from Unfold into the dedicated organization selected by its owner. The previous hosting and release policy does not describe this independent module.

## Decision Drivers

* One public contribution and release authority.
* Preserve attribution and immutable historical releases.
* Make the boundary with Unfold testable.

## Considered Options

* Continue the shared Unfold release train.
* Independent project with imported ancestry.
* Independent project with a documented fresh root.

## Decision Outcome

Chosen option: "Independent project with a documented fresh root", because it establishes one source and release authority while preserving historical attribution.

Use `ploeg-hq/ploeg` as the writable origin, `main` as trunk, GitHub Issues as the backlog, and GitHub Actions as the CI and release authority. Forgejo is a one-way pull mirror. Unfold consumes an immutable Ploeg source pin and keeps its own product backlog and integration tests. Start a fresh root commit while preserving source attribution and historical repositories.

Ryan explicitly approved this decision in the 2026-10-03 project work session. The [public separation record](https://github.com/webgrip/unfold/issues/1) records his approval and the remaining consensus evidence.

### Consequences

Existing imports and artifact coordinates require an explicit migration. No production deployment is changed by this repository cut. GitHub backlog hosting does not implement a GitHub runtime provider.

### Confirmation

`mise run verify` and `mise run docs-check` validate the source and ledger. The CI and release workflows verify the exact versioned commit. The provenance record names the frozen source. Unfold integration qualification and mirror equality are separate cutover gates.

## Re-evaluation triggers

CNCF changes hosting requirements, consumers cannot reproduce the pin, or public CI and mirroring fail to provide the required evidence.

## More Information

[Provenance](../../PROVENANCE.md); [releases](../ops/release-versioning.md).
