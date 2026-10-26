---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: 0064
review-by: 2027-01-18
---

# Every releasable merge to development cuts a candidate

## Context and Problem Statement

[ADR 0064](0064-development-is-trunk-and-release-candidates-come-from-it.md) made `development` the trunk and let a person tag a candidate on it. [ADR 0115](0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) now calculates candidates from commits. Where is a candidate cut, by what, and how does it reach publication when a tag pushed with the workflow's own token starts no other workflow?

## Decision Drivers

* A merge to `development` should be published as a candidate with no hand step.
* One publication path, so a candidate and a stable release are built and checked the same way.
* Least privilege: the workflow's own short-lived token, no long-lived release secret.

## Considered Options

* One workflow on every push to `development` cuts the candidate and calls the release workflow
* Cut the tag with a GitHub App token so that the tag push starts the release workflow
* Keep hand-pushed candidate tags

## Decision Outcome

Chosen option: "One workflow on every push to `development` cuts the candidate and calls the release workflow", because it needs no secret beyond the workflow's token and leaves exactly one path to publication.

* `development` stays the trunk, and `main` the line of stable releases. A stable `v0.x.y` may be tagged only on `main`; a candidate is tagged only by the release candidate workflow, on `development`.
* On every push to `development`, [the release candidate workflow](../../.github/workflows/release-candidate.yml) checks out the branch head and runs semantic-release. When there is a releasable commit, it tags that commit `v0.x.y-rc.N` and opens a draft GitHub release, and writes nothing back to `development` ([ADR 0115](0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md)).
* The same run calls [the release workflow](../../.github/workflows/release.yml) with the tag. It validates ancestry with `scripts/release.sh validate`, runs `mise run verify` and `mise run docs-check`, pushes the images and the chart packaged at the tag's version, uploads the assets to the draft and publishes it as a prerelease. A pushed stable tag still starts the release workflow itself; a pushed candidate tag does not.
* Candidate tags never move. A candidate whose publication failed stays a dead tag, and the next releasable merge cuts the next number, as `v0.2.0-rc.22` did.

### Consequences

* Good, because a merge to `development` produces a published candidate with no hand step.
* Good, because nothing is pushed to `development`, so the `Protect development` ruleset and its required `verify` check stay as they are, and the tag pushed with the workflow token starts no further workflow.
* Bad, because the gates run after the tag exists, so a candidate that fails them burns its number; the pull request's own CI is what stands in front of that.

### Confirmation

`bash scripts/test-release-policy.sh`, run by `mise run verify`, fails when `release.sh` refuses a candidate on `development`, accepts a stable tag only on `development`, or accepts a candidate on neither branch. `scripts/release.sh publish` refuses a published release at the tag, and a draft that already carries assets. The first releasable merge after this change shows the run: one `Release candidate` workflow with a `cut` job and a `publish` job, and a published prerelease whose packaged chart version matches its tag, and no new commit on `development`.

## Pros and Cons of the Options

### A GitHub App token

* Good, because the tag push would start the release workflow as a hand-pushed tag does.
* Bad, because it adds a long-lived App key to store and rotate, for the same result.

### Hand-pushed candidate tags

* Bad, because it is the hand step this record removes.

## Re-evaluation triggers

GitHub lets a workflow token's tag push start other workflows, the `Protect development` ruleset accepts GitHub Actions as a bypass actor, two candidates are cut from one merge, or a candidate's publication fails twice in a month.

## More Information

* Owner decisions D1 and D4 of [homelab-cluster ADR-0069](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0069-one-delivery-contract.md), 2026-10-17.
* [Evidence: fixture and dry-run](../research/2026-10-18-release-candidates-from-semantic-release.md); [releases](../ops/release-versioning.md).
* 2026-10-18 — Candidates became tags only (ADR 0115): the `Protect development` ruleset cannot take GitHub Actions as a bypass actor, so a release commit to `development` would be refused.
* 2026-10-26 — The stable clause (a pushed stable tag starts the release workflow) is replaced by [ADR 0158](0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md): the push to `main` that fast-forwards it to a candidate starts the stable release. Candidates are unchanged.
