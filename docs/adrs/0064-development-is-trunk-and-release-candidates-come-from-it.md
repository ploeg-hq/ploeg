---
status: accepted
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-04
---

# Development is trunk, and release candidates come from it

## Context and Problem Statement

[ADR 0062](0062-github-is-the-independent-project-home.md) made `main` the trunk, and [ADR 0063](0063-independent-releases-start-at-zero-point-one.md) permits reviewed stable tags and optional release candidates. `scripts/release.sh` refused every tag that is not on `main`, and CI ran on pushes to `main` only.

The owner pushes day-to-day work to a `development` branch. Unfold pins the latest Ploeg release, release candidates included, so it moves only when a release exists. Which branch is trunk, and where may each kind of tag sit?

## Decision Drivers

* The owner commits directly to the trunk.
* Consumers pin releases, not arbitrary commits, and a candidate is a release.
* A stable version is the merged, reviewed state of the project.

## Considered Options

* Keep `main` as trunk and tag candidates on `main`
* `development` is trunk: candidates on `development`, stable releases on `main`

## Decision Outcome

Chosen option: "`development` is trunk: candidates on `development`, stable releases on `main`", because it keeps `main` as the line of stable releases while day-to-day work and its candidates move on `development`.

* `development` is the trunk. Pull requests target it, and CI runs on every push to it and to `main`.
* A candidate `v0.x.y-rc.N` may be tagged on a commit of `development` or `main`. A stable `v0.x.y` may be tagged only on a commit of `main`, after `development` is merged into it.
* Everything else in ADR 0063 stands: tags are explicit and reviewed, published tags and artifacts never move, and no release is calculated from commit messages.
* This refines the trunk clause of ADR 0062; GitHub stays the project home.

### Consequences

* Good, because a consumer can follow candidates without waiting for a stable release.
* Good, because `main` only moves when a stable release is prepared.
* Bad, because `main` lags `development`, so a fix for a stable release lands on `development` first and reaches `main` with the next merge.

### Confirmation

[scripts/test-release-policy.sh](../../scripts/test-release-policy.sh) fails when `release.sh` refuses a candidate on `development`, accepts a stable tag that is only on `development`, or accepts a candidate on neither branch.

## Re-evaluation triggers

A second maintainer joins and wants reviewed pull requests into `main` as trunk, a consumer needs stable releases more often than `development` merges into `main`, or release automation calculates versions.

## More Information

The owner wants Unfold, Ploeg's first consumer, to pin the latest Ploeg release, candidates included, with Renovate proposing newer tags; Unfold records that in its own ledger ([Unfold system ADR-0019](https://forgejo.webgrip.dev/webgrip/unfold/src/branch/development/docs/adr/adr-0019-unfold-pins-ploeg-from-its-own-repository-and-releases-only-vloer.md)).
