# Independent releases

The new `github.com/ploeg-hq/ploeg` module starts at **v0.1.0**. Ploeg remains experimental. The version reset applies only to this new module and artifact namespace. Never rewrite old versions in `webgrip/ploeg` or Unfold.

## Cut a release

1. Work lands on `development`, the trunk ([ADR 0064](../adrs/0064-development-is-trunk-and-release-candidates-come-from-it.md)); CI runs on every push and pull request. For a release, update both `version` and `appVersion` in `ops/helm/ploeg/Chart.yaml` in a release preparation commit with compatibility and migration notes.
2. Create an immutable annotated tag on that commit and push the tag. A candidate `v0.x.y-rc.N` may sit on `development` or `main`; a stable `v0.x.y` only on `main`, after `development` is merged into it. Do not use 1.x without a separate maturity decision.
3. The [release workflow](../../.github/workflows/release.yml) checks tag ancestry and chart versions, then runs the full standalone gates before publication. A maintainer can dispatch the workflow with the same existing tag; publication refuses existing releases or artifacts.
4. Verify the published release assets, image digest, Helm pull and an unauthenticated image pull. Set newly created GHCR packages public if the organization's package policy creates them private.
5. Consumers update their pins through reviewed changes and run their own integration qualification. A release does not update a deployment.

The initial artifacts are Linux amd64 controller/worker binaries, `ghcr.io/ploeg-hq/ploegd`, the OCI Helm chart `oci://ghcr.io/ploeg-hq/charts/ploeg`, SHA256 checksums and source metadata. BuildKit attaches image SBOM and build provenance. Cryptographic signing and additional architectures are follow-up work. No `latest` alias is published.

If a job fails after pushing an artifact, do not overwrite it by rerunning the entire publication. Inspect the existing digest and evidence; finish only missing publication through a reviewed recovery, or use a new version. Keep published tags immutable.

[ADR 0063](../adrs/0063-independent-releases-start-at-zero-point-one.md) records the owner's explicit version policy decision.
