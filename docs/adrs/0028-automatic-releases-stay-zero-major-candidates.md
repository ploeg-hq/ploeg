---
status: proposed
date: 2026-09-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Automatic releases stay zero-major candidates

## Context and Problem Statement

An accurate breaking-change footer caused the shared release analyzer to publish `v1.0.0-rc.1`. Ploeg is still experimental. A compatibility warning does not authorize a maturity milestone or a stable release.

## Decision Drivers

* Preserve accurate upgrade warnings.
* Require an explicit human decision before 1.x or stable publication.
* Keep the shared release toolchain and standard artifact tags.

## Considered Options

* Override the breaking rule locally and guard both release entry and computed version.
* Append a lower-priority rule to the shared analyzer.
* Remove breaking-change notices from commits.

## Decision Outcome

Chosen option: "Override the breaking rule locally and guard both release entry and computed version", because it separates compatibility information from permission to publish a stable or major release.

The repository maps breaking changes to minor increases and automatically publishes only `0.x.y-rc.N` versions from `development`, with `ploeg-v${version}` tags (renamed from `v${version}` by [0029](0029-qualify-unfold-before-changing-distribution.md) when Ploeg moved into Unfold). A local `verifyConditions` guard blocks promotion from other branches before semantic-release can add an existing release to a channel. A `verifyRelease` guard rejects invalid versions and tags before preparation, tagging and publishing. There is no environment bypass. A future human-approved policy change must explicitly update the code, tests and decision record.

The artifact-publishing workflow independently rejects stable and major tags for release events and manual dispatch, before downstream jobs receive a version. Candidate publication never moves a `latest` image alias.

### Consequences

* Good, because compatibility warnings remain in the release notes.
* Good, because a dependency or history change that computes 1.x stops publication.
* Bad, because the mistakenly published 1.x tag requires a deliberate external withdrawal before automatic 0.x releases can resume.

### Confirmation

The [source-change workflow](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/.forgejo/workflows/on_source_change.yml) runs `node --test scripts/release-policy.test.cjs` against its installed toolchain before the release composite. Tests execute the real analyzer and semantic-release version calculator with the historical managed-authentication breaking commit and `v0.3.0-rc.4`, and check both release guards. `mise exec -- go test ./internal/ledger/` validates this record and its index entry.

## Pros and Cons of the Options

### Append a lower-priority rule

* Bad, because the analyzer chooses the highest matching rule and the existing major result wins.

### Remove compatibility notices

* Bad, because operators would lose the required managed-authentication migration warning.

## More Information

* Technical story: [guard-zero-major-prereleases](../../openspec/changes/archive/2026-09-23-guard-zero-major-prereleases/proposal.md).
* Evidence: [2026-09-11 release-policy verification](../research/2026-09-11-release-policy-verification.md).
* 2026-09-11 — Recorded before implementation; proposed for human ratification.
* 2026-09-27 — [Unfold ADR-0004](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/adr/adr-0004-unfold-releases-one-version.md) applies this policy to the single Unfold release train. The guard is now [scripts/release-policy.cjs](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/scripts/release-policy.cjs) with `unfold-v${version}` tags, and the suite runs from the repository root.
