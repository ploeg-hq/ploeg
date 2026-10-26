---
status: accepted
date: 2026-10-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-26
---

# A stable release is a soaked candidate, promoted by fast-forwarding main

## Context and Problem Statement

`development` cuts a candidate on every releasable merge ([ADR 0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md)), and 77 candidates of `0.2.0` exist, but `v0.1.0` is still the only stable release. [ADR 0115](0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) keeps a stable as a tag a person pushes on `main`, built again from that commit, and [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) counts deprecation windows in stable minors, so nothing deprecated since `v0.1.0` can be removed. The [stable channel plan](../ops/stable-channel-plan.md) left twelve questions to the owner. How does a candidate become a stable release, how often, and what does a stable promise?

## Decision Drivers

* A stable runs the bytes that were tested, not a rebuild of them.
* One deliberate human act per promotion, with a public record of the checks, from one maintainer now and two later.
* The work fits about 20 hours a week ([capacity](../cncf/capacity.md)).
* A verifier can tell a stable from a candidate by its signature alone.

## Considered Options

* **Fast-forward `main` to a soaked candidate; the release workflow tags it and re-tags the candidate's digests**
* Keep ADR 0115's stable path: a person tags `main` and the workflow rebuilds
* A fixed release train with a stable on set dates

## Decision Outcome

Chosen option: "**Fast-forward `main` to a soaked candidate**", because the fast-forward is the reviewed act, and re-tagging ships exactly what ran during the soak. The owner answered the plan's questions on 2026-10-26:

1. **Tag (Q1).** Fast-forwarding `main` to a candidate's commit is the promotion. semantic-release on the push to `main` creates the tag `v0.Y.Z`; no person pushes it. This replaces ADR 0115's clause that a person tags a stable, and ADR 0116's clause that a pushed stable tag starts the release workflow.
2. **Bytes (Q2).** A stable re-tags the candidate's image digests and copies its binary tarball. Binaries and images report the candidate's version; the stable release's notes and an annotation on its tag name that candidate. The chart is packaged again at `0.Y.Z` because Helm requires the chart version to match its tag, and it pins the same digests.
3. **Soak (Q3).** A minor's candidate runs at least three days, a patch's at least one, on the reference deployment pinned to that candidate. A security patch may skip the soak when its release issue says why.
4. **Gate for `v0.2.0` (Q4).** `v0.2.0` waits for an outsider to complete the quickstart ([#12](https://github.com/ploeg-hq/ploeg/issues/12)), because the quickstart will pin the first stable. `ploeg-mcp` `execute` groups and rework requested by outside reviewers stay outside the supported profile for `0.2`, so F-08 and F-36 do not block it.
5. **Cadence (Q5).** No fixed train before 1.0. Every four weeks the maintainers review whether a candidate is ready, and promote it when it is. A patch ships when its fix is tested.
6. **Support window (Q6).** The newest stable minor gets bug and security fixes. The previous minor gets security fixes until the newer minor has been out four weeks. Older minors get nothing. Revisited when a second maintainer joins.
7. **Maintenance patches (Q7).** A push to a maintenance branch `0.Y.x` releases `v0.Y.(Z+1)` directly, after the release workflow's gates; the fix already ran as a candidate on `development`.
8. **`v0.1.0` (Q8).** `v0.2.0` is not held to compatibility with `v0.1.0`. Going from `0.1` to `0.2` is a fresh install, recorded as one exception, and the upgrade promise starts at `v0.2.0`.
9. **Notes (Q9).** A stable's hand-written notes live on its GitHub release only. `CHANGELOG.md` is not continued; it points to the GitHub releases.
10. **Signing (Q10).** A stable is signed again by the release workflow running on `refs/heads/main`, so a verifier can require that identity. `SHA256SUMS` is signed, GitHub build provenance and SBOM attestations are attached, and the contract bundle is signed. No SLSA level is claimed until a verification run shows it.
11. **Review record (Q11).** Each promotion has a public release issue from a template listing gates G1 to G7. Once a second maintainer exists, their approval on that issue is required before the fast-forward.
12. **Consumers (Q12).** Each consumer of [ADR 0127](0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md) may choose the `candidate` channel (the default) or `stable`; the consumer job also runs after a stable release. Ploeg still names no consumer.

### Consequences

* Good, because the stable runs the digests that soaked, and a re-tag cannot introduce a difference a rebuild might.
* Good, because the promotion is one fast-forward plus a public issue, which a second maintainer can take over unchanged.
* Good, because stables give ADR 0134's deprecation windows something to count.
* Bad, because a stable binary reports a candidate version. Accepted: the notes and the tag annotation name the candidate, and the stable signature identifies the release.
* Bad, because notes on GitHub releases are not reviewed in a pull request. Accepted by the owner to keep one place for notes.
* Bad, because the reference deployment holds one candidate for the soak while newer candidates are cut.

### Confirmation

`bash scripts/test-release-policy.sh`, run by `mise run verify`, gains one case per rule that must fail and one that must pass: a stable tag accepted only on `main` at a candidate's commit, digests copied rather than built, and a promotion refused while the release issue's gates are open. Until those land, this record is accepted but not enforced, and [the stable channel plan](../ops/stable-channel-plan.md#work-to-land) lists the work.

## Pros and Cons of the Options

### Fast-forward `main` to a soaked candidate

* Good, because it matches how candidates already publish, so stables and candidates share one path.
* Bad, because it needs a promote script and a release workflow path that copies instead of builds.

### Keep ADR 0115's stable path

* Good, because it exists today.
* Bad, because it rebuilds what soaked and adds a release commit and a hand-pushed tag.

### A fixed release train

* Good, because users can plan around dates.
* Bad, because one maintainer would ship on a date whether or not a candidate is ready.

## Re-evaluation triggers

* A second maintainer joins, which turns the release issue into a required approval and reopens the support window.
* Ploeg declares 1.0 ([ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md), item 6).
* A soaked candidate's re-tagged digest fails in a stable that the candidate never showed.
* An adopter asks for a longer support window or for notes in the repository.

## More Information

* Plan and gates G1 to G7: [stable channel plan](../ops/stable-channel-plan.md); [releases](../ops/release-versioning.md); [compatibility](../ops/compatibility.md).
* [ADR 0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md) keeps candidates as they are; only its stable clause is replaced.
* ADR 0115's re-evaluation trigger "the owner decides stable releases follow a merge into `main` automatically" fired with item 1.
* 2026-10-26: accepted. Decided by the maintainer and sole rights holder.
