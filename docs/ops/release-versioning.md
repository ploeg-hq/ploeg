# Independent releases

The new `github.com/ploeg-hq/ploeg` module starts at **v0.1.0**. Ploeg remains experimental. The version reset applies only to this new module and artifact namespace. Never rewrite old versions in `webgrip/ploeg`.

What each kind of release may change, and for how long a deprecated element keeps working, is the [compatibility policy](compatibility.md).

## Release candidates

Every releasable merge to `development`, the trunk, cuts a candidate with no hand step ([ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md), [ADR 0116](../adrs/0116-every-releasable-merge-to-development-cuts-a-candidate.md)).

1. The pull request title and commits are conventional. `feat`, `fix`, `perf`, `refactor`, `revert` and dependency bumps release; `docs`, `ci`, `test`, `chore` and the rest do not. A breaking change bumps the minor version, never the major.
2. On the push to `development`, [the release candidate workflow](../../.github/workflows/release-candidate.yml) runs semantic-release with [`.releaserc.cjs`](../../.releaserc.cjs) and the shared `@webgrip/semantic-release-config`. `scripts/release.sh check` refuses anything but a new `v0.x.y-rc.N`. It then tags the merged commit and opens a draft GitHub release with the generated notes. Nothing is written back to `development`: `Chart.yaml` and `CHANGELOG.md` in git keep the last stable values, because the `Protect development` ruleset refuses a push that skips its `verify` check and cannot exempt GitHub Actions.
3. The same run calls [the release workflow](../../.github/workflows/release.yml), which checks tag ancestry, runs the full standalone gates, pushes the images, packages the chart with the tag's version as `version` and `appVersion` and the two pushed image digests as its defaults ([scripts/package-chart.sh](../../scripts/package-chart.sh)), uploads the assets to the draft and publishes it as a prerelease.

## Stable releases

A stable `v0.x.y` stays a reviewed act. Merge `development` into `main`, set `version` and `appVersion` in `ops/helm/ploeg/Chart.yaml` and the changelog in a release preparation commit on `main`, then push an annotated tag on that commit. The tag push starts the release workflow. Do not use 1.x without a separate maturity decision.

A [plan for the stable channel](stable-channel-plan.md) proposes how a candidate becomes a stable release, with the owner's open decisions; it is not in force.

## The published chart pins its images

`release.sh` reads the digests BuildKit reports for `ploegd`, `claude-runner` and `ploeg-worker` and hands them to [scripts/package-chart.sh](../../scripts/package-chart.sh), which writes them into a copy of the chart as `image.digest`, `claudeRunner.image.digest` and `worker.image.digest` before `helm package`. A cluster that sets no image therefore runs the release's own builds by digest: ploegd, its workers' worker containers, `ploeg-mcp`, `claude-runner` and a `claude-environment` worker. `values.yaml` in git keeps all three digests empty. [scripts/test-package-chart.sh](../../scripts/test-package-chart.sh), run by `mise run verify`, fails when the packaged chart renders an image without a digest or when `release.sh` packages the chart any other way.

## After publication

1. Verify the published release assets, image digest, Helm pull and an unauthenticated image pull. Set newly created GHCR packages public if the organization's package policy creates them private.
2. Consumers update their pins through their own pull requests and run their own integration qualification. A release does not update a deployment.

## Consumer pull requests

When the repository variable `RELEASE_CONSUMERS` is set, the `consumers` job of the release candidate workflow calls [the release consumers workflow](../../.github/workflows/release-consumers.yml), which opens each consumer's update after the contract bundle is published ([ADR 0127](../adrs/0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md)). The variable is a JSON list:

```json
[{"forge": "forgejo", "url": "https://forge.example", "repo": "owner/name", "base": "development", "recipe": "scripts/consume-release.sh", "message": "fix(deps): update ploeg to {version}"}]
```

For each entry, [scripts/release-consumers.py](../../scripts/release-consumers.py) clones `base`, runs `<recipe> <version>` from the checkout root, commits what changed on `ploeg/<version>` as the token's user, opens or updates the pull request and asks the forge to merge it when the base branch's required checks succeed. It closes the same user's older `ploeg/` pull requests. `message` and `merge_style` (`merge` by default) are optional. A recipe gets git, a POSIX shell, Python 3 and mise, and never the token.

The token is the repository secret `RELEASE_CONSUMER_TOKEN_FORGEJO`: a Forgejo access token of a user with write access to the consumer repositories only, with the `write:repository`, `write:issue` (the superseded comment) and `read:user` scopes, allowed to merge into `base` by its branch protection. Without the variable the job is skipped.

To try an entry without writing anything, run the script with `RELEASE_CONSUMERS_DRY_RUN=true`: it clones, runs the recipe, commits locally, prints the commit and stops.

```sh
RELEASE_CONSUMER='{"forge":"forgejo","url":"https://forge.example","repo":"owner/name","base":"development","recipe":"scripts/consume-release.sh"}' \
RELEASE_CONSUMERS_DRY_RUN=true python3 scripts/release-consumers.py v0.2.0-rc.28
```

A failed `consumers` job is retried by dispatching the release consumers workflow with the release tag. It runs the script from `development`, so a fix to the script reaches a release that already failed, which rerunning the old job cannot do. The retry updates the same branch and pull request.

The script sends its own `User-Agent`. A forge behind Cloudflare's Browser Integrity Check refuses Python's default one with error 1010, which stopped the first delivery at `GET /user`.

A maintainer can dispatch the release workflow with an existing tag whose publication is missing; publication refuses a published release, a draft that already carries assets, and existing artifacts.

The initial artifacts are Linux amd64 controller/worker binaries, `ghcr.io/ploeg-hq/ploegd`, `ghcr.io/ploeg-hq/claude-runner`, `ghcr.io/ploeg-hq/ploeg-worker`, the OCI Helm chart `oci://ghcr.io/ploeg-hq/charts/ploeg`, SHA256 checksums and source metadata. BuildKit attaches image SBOM and build provenance, and every image and the chart carry a keyless cosign signature from the release workflow ([CI and infrastructure](ci-and-infra.md#credentials-and-signing)). Additional architectures are follow-up work. No `latest` alias is published.

If a job fails after pushing an artifact, do not overwrite it by rerunning the entire publication. Inspect the existing digest and evidence; finish only missing publication through a reviewed recovery, or use a new version. Keep published tags immutable.

[ADR 0063](../adrs/0063-independent-releases-start-at-zero-point-one.md) set the version policy, and [ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) replaced its rule that no release is calculated from commits.
