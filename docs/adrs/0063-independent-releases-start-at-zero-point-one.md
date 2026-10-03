---
status: accepted
date: 2026-10-03
decision-makers: Ryan Grippeling
supersedes: 0028
review-by: 2027-01-03
---

# Independent releases start at 0.1.0

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

Start the new module `github.com/ploeg-hq/ploeg` at `v0.1.0`. Permit explicitly reviewed zero-major stable tags and optional release candidates. Do not automatically calculate or publish releases from conventional commits. Never move a published version tag, overwrite an artifact or publish a latest alias. Zero-major stable numbering does not imply production qualification. Older module versions and repository tags remain untouched.

Ryan explicitly approved this decision in the 2026-10-03 project work session. The [public separation record](https://github.com/webgrip/unfold/issues/1) records his approval and the remaining consensus evidence.

### Consequences

Existing imports and artifact coordinates require an explicit migration. No production deployment is changed by this repository cut. GitHub backlog hosting does not implement a GitHub runtime provider.

### Confirmation

`mise run verify` and `mise run docs-check` validate the source and ledger. The CI and release workflows verify the exact versioned commit. The provenance record names the frozen source. Unfold integration qualification and mirror equality are separate cutover gates.

## Re-evaluation triggers

CNCF changes hosting requirements, consumers cannot reproduce the pin, or public CI and mirroring fail to provide the required evidence.

## More Information

[Provenance](../../PROVENANCE.md); [releases](../ops/release-versioning.md).
