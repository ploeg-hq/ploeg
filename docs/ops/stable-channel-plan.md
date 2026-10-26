---
type: explanation
audience: [owner, operator, integrator, contributor]
owner: ploeg
last_verified: 2026-10-26
verified_by: "Owner decisions of 2026-10-26 recorded in ADR 0158; gh issue view 221-227 on 2026-10-26; git tag and git rev-list against origin on 2026-10-24; gh release view v0.2.0-rc.52 and v0.1.0; gh api rulesets; read scripts/release.sh, .github/workflows/release.yml, release-candidate.yml, contract-bundle.yml; gh issue view 12, 13, 14, 23, 30, 34, 221-227"
---

# Plan: the stable release channel

This is a plan. The owner answered its [decisions](#decisions) on 2026-10-26 in [ADR 0158](../adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md); nothing in it is enforced until the [work to land](#work-to-land) is done. Until then [releases](release-versioning.md) and [the compatibility policy](compatibility.md) describe what Ploeg does.

The goal is a stable channel that people outside the project can run: a stable `v0.2.0` that is a specific, tested candidate, a rule for when the next one comes, how long a stable keeps getting fixes, and what an upgrade between stables promises.

## Where things stand on 2026-10-24

- `v0.1.0`, tagged on 2026-10-03 at the separation, is the only stable release. `main` is at `7678b8f`, 440 commits behind `development`, and is an ancestor of it, so `main` can still be fast-forwarded.
- `development` has cut 52 candidates of `0.2.0`. 34 of them came after candidates became automatic on 2026-10-17 ([ADR 0116](../adrs/0116-every-releasable-merge-to-development-cuts-a-candidate.md)): about four a day, 15 on 2026-10-18.
- The documented stable path merges `development` into `main`, adds a release preparation commit that sets `Chart.yaml` and `CHANGELOG.md`, and tags it. The tag push starts [the release workflow](../../.github/workflows/release.yml), which builds every artifact again from that commit. The estate's release recipe, which Ploeg follows for candidates, does two things differently: a stable is a fast-forward of `main` to a candidate's commit with no release commit, and a stable re-tags the candidate's image digests instead of rebuilding them.
- `CHANGELOG.md` stops at `0.2.0-rc.26`. Since [ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) a candidate's notes live only on its GitHub release.
- [ADR 0134](../adrs/0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) counts deprecation windows in stable minors. With no stable after `v0.1.0`, nothing deprecated since can be removed. The stable channel is also what makes that policy usable.
- `v0.2.0-rc.52` publishes binaries, a chart, image metadata, `SHA256SUMS`, `signed-artifacts.txt`, `PROVENANCE.md` and source metadata. Images and the chart carry keyless cosign signatures. `SHA256SUMS`, the binary tarball and the [contract bundle](../adrs/0117-every-release-publishes-a-contract-bundle-for-consumers.md) are unsigned, and no signature or attestation file is attached to the release.
- [SECURITY.md](../../SECURITY.md) says no supported release line exists.

## Channels

| Channel | Cut from | Tags | Who follows it |
| --- | --- | --- | --- |
| Candidate | `development`, on every releasable merge | `v0.Y.Z-rc.N` | the maintainers' reference deployment and consumers that choose to |
| Stable | `main`, by fast-forward to a candidate | `v0.Y.Z` | everyone else |
| Maintenance | `0.Y.x`, cut from a stable tag when a fix cannot wait for the next minor | `v0.Y.(Z+1)` | users of that minor |

A stable release is a candidate that was promoted. It has the candidate's commit and runs the candidate's image digests. No `latest` alias is published on any channel.

## When a candidate is eligible

A candidate `v0.Y.Z-rc.N` may be promoted when all of these hold. G1 to G3 and G5 can be checked by a script; G4, G6 and G7 need recorded evidence.

1. **G1 Release blockers closed.** Every issue in the GitHub milestone `v0.Y.Z` is closed. For `v0.2.0` the milestone holds the findings the [threat model](../security/threat-model.md) marked release-blocking, [#221](https://github.com/ploeg-hq/ploeg/issues/221) to [#225](https://github.com/ploeg-hq/ploeg/issues/225), all closed by 2026-10-25, and the quickstart gate [#12](https://github.com/ploeg-hq/ploeg/issues/12) (G7). [#226](https://github.com/ploeg-hq/ploeg/issues/226) (F-08) and [#227](https://github.com/ploeg-hq/ploeg/issues/227) (F-36) do not block it: MCP `execute` groups and rework by outside reviewers stay outside the supported profile for `0.2` (Q4).
2. **G2 The candidate's own gates passed.** Its `Release` run completed (`mise run verify`, `mise run docs-check`, signature verification), its contract bundle was published, and the attack drill passed on its commit.
3. **G3 Contract tests green against the previous stable.** `PLOEG_COMPAT_BASE=<previous stable> go test ./internal/compat/` passes, or every failure is an entry in [breaking-changes.yaml](../contracts/breaking-changes.yaml) with its ADR. Today the gate compares with the newest tag, which is usually the previous candidate; a stable has to be compared with the stable before it. Once the consumer skew tests from the contract-version work (VIK-2398) exist, they are part of this gate.
4. **G4 Soak.** The exact candidate digests ran in a reference deployment for the soak period (Q3) with no signal of the `DeliveryCandidateUnhealthy` class: the release not Ready, replicas unavailable, a container restarting twice in 15 minutes, or a route probe failing. No `PloegCredentialLeak`, `PloegShiftStuck`, `PloegLeaseExpired` or `PloegRunsFailingRepeatedly` alert ([alerts](alerts.md)) fired in that time either. A reference deployment that follows the candidate channel replaces a candidate within hours, so a soak needs a hold: the deployment is pinned to the chosen candidate for the soak period while new candidates keep being cut.
5. **G5 The upgrade path works.** For `v0.2.0` this gate does not apply: going from `v0.1.0` is a fresh install (Q8). From `v0.2.0` on, on a kind cluster, the previous stable is installed, upgraded to the candidate, completes one Run, and is rolled back one release without a database restore, as [compatibility](compatibility.md#database) promises. The [upgrade drill](../research/2026-10-26-upgrade-drill.md) already rehearses the database and run-API half of this against the previous tag on every pull request; the kind job adds the chart and the images.
6. **G6 Notes are written.** The stable notes described [below](#changelog-upgrade-notes-and-deprecations) are drafted on the stable's GitHub release.
7. **G7 An outsider can start from it.** For `v0.2.0`, the quickstart pins the candidate and a person who did not write it has completed it ([#12](https://github.com/ploeg-hq/ploeg/issues/12)). Later stables need the quickstart to pass in CI at the candidate (decision Q4).

## Who promotes and how

A maintainer promotes. While Ploeg has one maintainer, that maintainer promotes after filing the evidence publicly; when there are two, the second approves the release issue before the push ([GOVERNANCE.md](../../GOVERNANCE.md) already names a stable release a maintainer's decision).

GitHub's merge button cannot fast-forward, and the workflow token cannot push past the `Protect main` ruleset, so promotion is a push by a maintainer:

1. Open a release issue from a template for `v0.Y.Z` that names the candidate and holds the evidence for G1 to G7.
2. Run `scripts/release.sh promote v0.Y.Z-rc.N` (to be written). It checks G1 to G3 and G5 against GitHub and the tag, checks that `main` is an ancestor of the target commit, checks that the target is the candidate's commit or a descendant whose diff touches only `CHANGELOG.md` and `docs/`, prints the result, and pushes `<target>:refs/heads/main`. The ruleset refuses anything but a fast-forward.
3. The push to `main` starts the stable release. It tags `v0.Y.Z` on the pushed commit (Q1), copies the candidate's image digests to the stable tags, packages the chart at `0.Y.Z` with those same digests, signs everything with the stable identity, attaches the notes and publishes the release. The contract bundle is published for the stable tag.
4. Run the post-publication checks in [releases](release-versioning.md#after-publication) and close the release issue with links to the release and its verification.

`development` is never reset or rewritten by any of this. A fix found after promotion lands on `development` first.

## Cadence

Before 1.0 there is no fixed release train. The maintainers look at promoting a candidate every four weeks, and promote when G1 to G7 hold. A security fix or a regression in a stable gets a patch release as soon as its fix is tested (Q5).

## Version numbers

### Before 1.0

| Release | May contain | Comes from |
| --- | --- | --- |
| `v0.Y.0` | additions, fixes, recorded breaks, removals of what a previous stable minor deprecated | `main`, fast-forwarded to a candidate of `0.Y.0` |
| `v0.Y.Z`, Z > 0 | fixes, no break | `main` when `development` has only fixes since `v0.Y.0`; otherwise the maintenance branch `0.Y.x` |
| `v0.Y.Z-rc.N` | what [compatibility](compatibility.md#versions) allows a candidate | `development` |

After `v0.2.0`, semantic-release computes `0.2.1-rc.N` while `development` holds only fixes and `0.3.0-rc.N` once a `feat` lands. A breaking change still raises the minor ([ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md)). A stable `0.x` does not claim production readiness.

### At and after 1.0

1.0 is a separate decision ([ADR 0134](../adrs/0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md), item 6). When it is taken, a break needs a major, a removal from a frozen contract needs a new contract version, and the 1.x deprecation windows in [compatibility](compatibility.md#deprecation) apply. The analyzer rule that turns a breaking change into a minor, and the guard that refuses 1.x, are removed by the record that decides 1.0. This plan does not propose when.

## Support window and backports

Before 1.0 (Q6):

| Line | Gets |
| --- | --- |
| Newest stable minor | bug fixes and security fixes |
| Previous stable minor | security fixes until the newer minor has been out four weeks |
| Older minors | nothing; upgrade |

A backport is a fix that is merged to `development` first and then cherry-picked with `git cherry-pick -x` onto `0.Y.x`, a branch cut from the `v0.Y.0` tag when the first backport is needed. The branch gets the same ruleset as `main`. A pull request is labelled `backport/0.Y` when it qualifies: a security fix, or a fix for a regression or data loss in that minor. New features are never backported. A push to `0.Y.x` cuts `v0.Y.(Z+1)` through the same release workflow and gates (Q7).

SECURITY.md then lists the supported lines in a table, kept up to date by the stable release.

## Version skew with consumers

[Compatibility](compatibility.md#version-pairs) already sets the pairs: an operator consumer built against the contract bundle of N or the previous minor works with `ploegd` N, and a worker is never newer than `ploegd`. The stable channel changes what "the previous minor" means for consumers that follow it: the previous stable minor. A consumer on the stable channel can therefore upgrade Ploeg one stable minor at a time without changing its own code.

How Ploeg advertises the contract versions it serves, how a consumer checks them, and how Ploeg's CI runs recorded consumer expectations are separate work in flight (VIK-2398). This plan only depends on it through G3. The worker version handshake that ADR 0134 lists as follow-up is also separate.

## Changelog, upgrade notes and deprecations

- Notes live on GitHub releases only (Q9). `CHANGELOG.md` is not continued; it points to the GitHub releases. The promoted commit is the candidate's commit, with no notes commit.
- A stable's notes are written by a person from the candidates' notes since the previous stable, on the stable's GitHub release.
- Each stable's notes have these sections, empty ones stated as "None":
  - **Breaking changes**, one line per entry of `breaking-changes.yaml` between the two stables, with the ADR.
  - **Deprecations**, each with its replacement and the earliest minor it may be removed in.
  - **Removals**, each naming the stable minor that deprecated it.
  - **Upgrade notes**: the migrations that run, new required values, whether the previous stable can still roll back, and the upgrade order from [compatibility](compatibility.md#version-pairs).
  - **Security**, linking each advisory fixed.
  - **Changes**.
  - **Qualification**: the supported profile and what was tested, with links ([#23](https://github.com/ploeg-hq/ploeg/issues/23)).
- The deprecation policy stays as [compatibility](compatibility.md#deprecation) has it. One clarification: a deprecation counts from the first stable minor whose notes list it. A candidate may mark something deprecated, but its removal waits until a stable has carried the deprecation.

## Signing, SBOM and provenance

What a stable release adds to what candidates already have:

1. **A stable signer.** The promoted digests are signed again by the release workflow running on `refs/heads/main`. The documented stable verification command matches that identity only, so a verifier who wants stables can refuse a digest that only a candidate run signed. The candidate's signature stays.
2. **Signed checksums.** `SHA256SUMS` is signed with `cosign sign-blob --bundle`, and `SHA256SUMS.sigstore.json` is attached. Every other asset is then verifiable through its checksum. The OpenSSF Scorecard `Signed-Releases` check looks for such a file among release assets; none of the current releases has one.
3. **Provenance.** GitHub artifact attestations (`actions/attest-build-provenance`) for the binary tarball, the images and the chart, so `gh attestation verify` works for each. Ploeg claims a SLSA level only after a verification run shows it (Q10).
4. **SBOM.** The SPDX SBOMs BuildKit already makes are exported and attached as `*.spdx.json` assets, with an SBOM attestation, so a reader does not have to pull an image index to find them.
5. **The contract bundle is signed** like the chart.
6. **Verification is documented** in the quickstart for stables and tested by a CI job that verifies the newest stable from a clean runner without credentials ([#13](https://github.com/ploeg-hq/ploeg/issues/13)).

## What CNCF Sandbox expects

Sandbox admission does not require a stable release, a release cadence or a support window, and Ploeg should not claim one exists because of the application. What the application and CNCF's repository checks look at, and where this plan touches them:

- **A documented release process another person can run.** This page, the promote script and the release issue template; the second-maintainer rehearsal is [#30](https://github.com/ploeg-hq/ploeg/issues/30). With one maintainer, the application states that the release process depends on one person ([#5](https://github.com/ploeg-hq/ploeg/issues/5)).
- **Verifiable releases.** An unaffiliated reviewer downloads and verifies every advertised artifact ([#13](https://github.com/ploeg-hq/ploeg/issues/13)). The signed checksums and attestations above make that possible for binaries as well as images.
- **A security posture without known release-blocking findings** in the supported path ([#34](https://github.com/ploeg-hq/ploeg/issues/34)), which is gate G1.
- **A support matrix tied to a release** ([#23](https://github.com/ploeg-hq/ploeg/issues/23)): the Qualification section of each stable entry.
- **Repository checks** of the kind CLOMonitor runs: a recent release, signed releases, an SBOM, a security policy with supported versions, the OpenSSF Best Practices badge and a Scorecard result. The [self-assessment](../security/self-assessment.md#openssf-best-practices) lists what the badge still needs. The badge is required at graduation, not at Sandbox.

## Decisions

The owner decided these on 2026-10-26 ([ADR 0158](../adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md)). The owner took every recommendation except Q9: notes live on GitHub releases only.

| # | Question | Recommendation |
| --- | --- | --- |
| Q1 | Who creates the stable tag after `main` is fast-forwarded: semantic-release on the push to `main`, as the estate recipe does, or a maintainer pushing a tag, as ADR 0115 keeps? | semantic-release on `main`. The fast-forward is the reviewed human act, so a second hand step adds nothing. Needs a record that supersedes ADR 0115's stable clause. |
| Q2 | Does a stable re-tag the candidate's image digests or rebuild from the commit? The binaries are built with `-X main.version=<version>` and the images carry `org.opencontainers.image.version`, so a re-tagged image reports `0.2.0-rc.N`. | Re-tag the images and copy the binary tarball, so the stable runs the bytes that soaked. Accept that the binaries report the candidate version, and record the candidate in the stable release's notes and in an annotation on each stable tag. Package the chart again at `0.Y.Z`, because Helm requires the chart version to match its OCI tag; it pins the same digests. |
| Q3 | How long is the soak, and how does a reference deployment hold one candidate while candidates keep coming? | Three days for a minor, one day for a patch, by pinning the reference deployment to the candidate. A security patch may skip the soak if the release issue says why. |
| Q4 | Which issues block `v0.2.0`? | #221, #224 and #225. Keep MCP `execute` groups and rework by outside reviewers out of the supported profile for `0.2`, which leaves #226 and #227 non-blocking, and say so in the Qualification section. Keep #12 (an outsider completes the quickstart) as a `v0.2.0` gate, since the quickstart will pin the first stable. Close #14 against ADR 0134 and compatibility.md. |
| Q5 | Cadence before 1.0: a fixed train or promote-when-ready? | Promote when ready, with a promotion review every four weeks; patches when needed. |
| Q6 | Support window before 1.0. | Newest stable minor fully; previous minor security fixes for four weeks after the next minor ships. Revisit when a second maintainer joins. |
| Q7 | Does a push to a maintenance branch release a stable patch directly, or cut a candidate that is promoted? | Directly, after the release workflow's gates: the fix already ran as a candidate on `development`. |
| Q8 | Is `v0.2.0` compared with `v0.1.0` for compatibility and upgrades? | No. Nobody outside runs `v0.1.0` (the quickstart pins a candidate). Record one exception against `v0.1.0`, state in the upgrade notes that `0.1` to `0.2` is a reinstall, and start the upgrade promise at `v0.2.0`. |
| Q9 | Where are stable notes written, given that nothing is committed to `main`? | In `CHANGELOG.md` through a docs pull request to `development`, with the promote check allowing a docs-only diff between the candidate and the promoted commit. |
| Q10 | How far does stable signing go for `v0.2.0`? | The stable signer, signed `SHA256SUMS`, GitHub provenance and SBOM attestations and a signed contract bundle. No SLSA level claimed until verified. |
| Q11 | What is the reviewed record of a promotion while there is one maintainer? | A public release issue from a template holding G1 to G7. When a second maintainer exists, their approval of that issue is required before the push. |
| Q12 | Should stables reach consumers through the consumer pull requests of [ADR 0127](../adrs/0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md)? | Yes: add an optional `channel` (`candidate` or `stable`) to each `RELEASE_CONSUMERS` entry, defaulting to `candidate`, and run the job after a stable release. Ploeg still names no consumer. |

## Work to land

In order, after the decisions:

1. ~~A record that supersedes ADR 0115's and ADR 0116's stable clauses~~: [ADR 0158](../adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md). Still to do: update the `experimental-release-policy` OpenSpec spec.
2. `scripts/release.sh`: `check` accepts a stable on `main`; `validate` stops requiring a matching `Chart.yaml` for a stable, since the chart is versioned when it is packaged; a `promote` subcommand; a publication path that copies digests instead of building. `scripts/test-release-policy.sh` gains a case for each that must fail and one that must pass.
3. `.github/workflows/release.yml`: run on pushes to `main` and `0.*.x`; sign with the stable identity; sign `SHA256SUMS`; attach SBOMs and attestations; sign the contract bundle.
4. The `v0.2.0` milestone with the blockers, the release issue template, and the `backport/*` labels.
5. A kind job for G5 (install the previous stable, upgrade, one Run, roll back).
6. SECURITY.md's supported-versions table and the stable verification commands in the quickstart.
7. A pointer in `CHANGELOG.md` to the GitHub releases, the `v0.2.0` notes on its release, then the first promotion.
