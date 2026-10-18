# Independent releases

The new `github.com/ploeg-hq/ploeg` module starts at **v0.1.0**. Ploeg remains experimental. The version reset applies only to this new module and artifact namespace. Never rewrite old versions in `webgrip/ploeg`.

## Release candidates

Every releasable merge to `development`, the trunk, cuts a candidate with no hand step ([ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md), [ADR 0116](../adrs/0116-every-releasable-merge-to-development-cuts-a-candidate.md)).

1. The pull request title and commits are conventional. `feat`, `fix`, `perf`, `refactor`, `revert` and dependency bumps release; `docs`, `ci`, `test`, `chore` and the rest do not. A breaking change bumps the minor version, never the major.
2. On the push to `development`, [the release candidate workflow](../../.github/workflows/release-candidate.yml) runs semantic-release with [`.releaserc.cjs`](../../.releaserc.cjs) and the shared `@webgrip/semantic-release-config`. `scripts/release.sh check` refuses anything but a new `v0.x.y-rc.N`. It then writes `version` and `appVersion` in `ops/helm/ploeg/Chart.yaml` and the notes in `CHANGELOG.md`, commits `chore(release): v0.x.y-rc.N [skip ci]` to `development`, tags it and opens a draft GitHub release.
3. The same run calls [the release workflow](../../.github/workflows/release.yml), which checks tag ancestry and chart versions, runs the full standalone gates, pushes the images and chart, uploads the assets to the draft and publishes it as a prerelease.

## Stable releases

A stable `v0.x.y` stays a reviewed act. Merge `development` into `main`, set `version` and `appVersion` in `ops/helm/ploeg/Chart.yaml` and the changelog in a release preparation commit on `main`, then push an annotated tag on that commit. The tag push starts the release workflow. Do not use 1.x without a separate maturity decision.

## After publication

1. Verify the published release assets, image digest, Helm pull and an unauthenticated image pull. Set newly created GHCR packages public if the organization's package policy creates them private.
2. Consumers update their pins through reviewed changes and run their own integration qualification. A release does not update a deployment.

A maintainer can dispatch the release workflow with an existing tag whose publication is missing; publication refuses a published release, a draft that already carries assets, and existing artifacts.

The initial artifacts are Linux amd64 controller/worker binaries, `ghcr.io/ploeg-hq/ploegd`, the OCI Helm chart `oci://ghcr.io/ploeg-hq/charts/ploeg`, SHA256 checksums and source metadata. BuildKit attaches image SBOM and build provenance. Cryptographic signing and additional architectures are follow-up work. No `latest` alias is published.

If a job fails after pushing an artifact, do not overwrite it by rerunning the entire publication. Inspect the existing digest and evidence; finish only missing publication through a reviewed recovery, or use a new version. Keep published tags immutable.

[ADR 0063](../adrs/0063-independent-releases-start-at-zero-point-one.md) set the version policy, and [ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) replaced its rule that no release is calculated from commits.
