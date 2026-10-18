---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: 0063
review-by: 2027-01-18
---

# Candidates are calculated from conventional commits, and a stable release stays a reviewed tag

## Context and Problem Statement

[ADR 0063](0063-independent-releases-start-at-zero-point-one.md) started the independent module at `v0.1.0` and ruled that no release is calculated from conventional commits. Every candidate since then took a hand-written preparation pull request and a hand-pushed tag, and a one-line fix needed three of the eight hand steps it took to reach the homelab on 2026-10-17. The owner's estate decision ([homelab-cluster ADR-0069](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0069-one-delivery-contract.md)) puts every project on semantic-release with the shared `@webgrip/semantic-release-config`, released from its home forge. Which Ploeg versions may be calculated from commits, and which still need a person?

## Decision Drivers

* The one human step per change is reviewing the code.
* Ploeg is experimental: a breaking-change notice is not permission to publish 1.x or a stable release.
* Published tags and artifacts never move; no `latest` alias.

## Considered Options

* Calculate candidates from conventional commits; keep stable releases a reviewed tag
* Calculate candidates and stable releases from conventional commits
* Keep every release hand-prepared and hand-tagged

## Decision Outcome

Chosen option: "Calculate candidates from conventional commits; keep stable releases a reviewed tag", because it removes every hand step from a candidate while a stable release, the only maturity claim Ploeg makes, still needs a person.

* semantic-release, configured by [`.releaserc.cjs`](../../.releaserc.cjs) through the shared configuration's GitHub adapter, calculates the next `v0.x.y-rc.N` from the conventional commits since the previous tag. `feat`, `fix`, `perf`, `refactor`, `revert` and dependency bumps release; other types do not.
* A breaking change bumps the minor version, not the major, and keeps its notice in the release notes. The configuration refuses to load if any analyzer rule still cuts a major.
* Before anything is written, `scripts/release.sh check` refuses a version that is not a `0.x.y-rc.N` candidate or whose tag already exists.
* A candidate is a tag only. semantic-release tags the merged commit on `development` and writes nothing back to the branch: no release commit, no `Chart.yaml` bump and no `CHANGELOG.md` entry. The release workflow packages the chart with the tag's version as `version` and `appVersion`, and the candidate's notes live on its GitHub release. `.releaserc.cjs` refuses to load if a release commit would be made.
* A stable `v0.x.y` is still tagged by a person on `main`, and published by the release workflow, as before.
* Everything else in ADR 0063 stands: the module starts at `v0.1.0`, zero-major numbering does not claim production qualification, published tags and artifacts never move, and no `latest` alias is published.

### Consequences

* Good, because a candidate needs no preparation pull request and no hand-pushed tag.
* Good, because a breaking change can no longer turn into `v1.0.0-rc.1`, which happened once under the shared rules (ADR 0028).
* Bad, because every releasable merge is a candidate and tags multiply; candidates were already disposable.
* Bad, because a candidate's notes are generated from commit subjects and kept only on its GitHub release, so the hand-written "Breaking changes" and "Upgrade notes" sections stop unless the commits carry them, and `CHANGELOG.md` and `Chart.yaml` in git move only with a stable release.

### Confirmation

`bash scripts/test-release-policy.sh`, run by `mise run verify`, fails when `release.sh check` accepts a stable tag, a major or malformed tag, or a tag that already exists. The release candidate workflow runs that check as semantic-release's `verifyRelease` step, before the tag is written, and `release.sh validate` requires a matching `Chart.yaml` only for a stable tag. `.releaserc.cjs` throws at load time when an analyzer rule still releases a major. [The 2026-10-18 fixture runs](../research/2026-10-18-release-candidates-from-semantic-release.md) record a fix cutting `0.2.0-rc.27` as a tag only, with the packaged chart at `0.2.0-rc.27`, a breaking change cutting `0.2.0-rc.28`, and the guard refusing the version the shared defaults would compute.

## Pros and Cons of the Options

### Calculate stable releases from commits too

* Good, because merging `development` into `main` would be the whole promotion, as ADR-0069 describes.
* Bad, because a stable release is Ploeg's maturity claim, and ADR 0028's rule that it needs a person has not been revisited.

### Keep every release hand-prepared

* Bad, because it is the process that produced the hand steps.

## Re-evaluation triggers

The owner decides stable releases follow a merge into `main` automatically, the shared configuration changes its analyzer rules or its notes toolchain, a candidate is cut that should not have been, or Ploeg declares 1.0.

## More Information

* Ryan decided on 2026-10-17 that each project releases from its home forge, Ploeg from GitHub, and that every releasable Ploeg merge to `development` cuts a candidate (decisions D1 and D4 of homelab-cluster ADR-0069 and its RFC).
* [Evidence: fixture and dry-run](../research/2026-10-18-release-candidates-from-semantic-release.md); [releases](../ops/release-versioning.md); [ADR 0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md) for where candidates are cut.
* 2026-10-18 — Candidates became tags only. The first version of this record had semantic-release commit the chart version and changelog back to `development`, but the `Protect development` ruleset requires the `verify` check on every push and accepts only the repository admin role as a bypass actor; GitHub Actions cannot be added, so the release commit would be refused. The owner chose a tag on the merged commit, with the chart versioned when it is packaged.
