---
type: how-to
audience: [maintainer]
owner: ploeg
last_verified: 2026-10-25
verified_by: "Read .github/workflows/{release-candidate,release,contract-bundle,release-consumers}.yml, scripts/release.sh and gh api rulesets on 2026-10-25; ran every command under Rehearse against v0.2.0-rc.72 and v0.1.0; verified v0.2.0-rc.72 with verify-a-release.md"
---

# Release runbook

This is the procedure a maintainer follows to cut, publish, check and recover a Ploeg release. [Releases](release-versioning.md) says what the policy is and [ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) and [ADR 0116](../adrs/0116-every-releasable-merge-to-development-cuts-a-candidate.md) say why. A second maintainer can [rehearse](#rehearse) everything here without publishing anything.

## Credentials

No release credential is long-lived except the consumer token, and none is a signing key.

| Who | Credential | Scope | Used for |
| --- | --- | --- | --- |
| `cut` job of [release-candidate.yml](../../.github/workflows/release-candidate.yml) | workflow `GITHUB_TOKEN` | `contents: write` | semantic-release's tag and draft release |
| `release` job of [release.yml](../../.github/workflows/release.yml) | workflow `GITHUB_TOKEN` | `contents: write`, `packages: write`, `id-token: write` | release assets, GHCR pushes, the OIDC token Fulcio turns into a signing certificate |
| [contract-bundle.yml](../../.github/workflows/contract-bundle.yml) | workflow `GITHUB_TOKEN` | `packages: write` | the contract bundle on GHCR |
| [release-consumers.yml](../../.github/workflows/release-consumers.yml) | repository secret `RELEASE_CONSUMER_TOKEN_FORGEJO` | a Forgejo user with write access to the consumer repositories only; `write:repository`, `write:issue`, `read:user` | consumer pull requests ([details](release-versioning.md#consumer-pull-requests)) |
| Maintainer | GitHub account with write access to `ploeg-hq/ploeg` | merge pull requests, dispatch workflows, push a stable tag | every step below |
| Maintainer with the admin role | the same account | bypass the `Protect development` and `Protect main` rulesets; set a new GHCR package public | only when a step says so |

Repository rules, as `gh api repos/ploeg-hq/ploeg/rulesets` lists them:

- `Protect development` requires `verify`, `dco`, `govulncheck`, `analyze`, `cve-gate-self-test` and `cve-budget` for each image.
- `Protect main` requires `verify`.
- `Release tags are immutable` forbids moving or deleting any `v*` tag, for everyone.

## Cut a candidate

A candidate needs no hand step. Merge a pull request with a releasable conventional title (`feat`, `fix`, `perf`, `refactor`, `revert`, or a dependency bump) into `development`, and:

1. `Release candidate` / `cut` runs semantic-release, which runs `scripts/release.sh check` (refuses anything but a new `v0.x.y-rc.N`), tags the merge commit and opens a draft release with the generated notes. A non-releasable merge ends here with no tag.
2. `publish` calls `release.yml` with the tag, which runs:
   1. `scripts/release.sh validate`: the tag's commit is on `development` (or `main`) and is the checkout
   2. `mise run verify`: build, vet, tests, Helm goldens, licence and policy checks, with the PID-namespace harness drill required
   3. `mise run docs-check`
   4. `scripts/release.sh publish`, which refuses to start if the tag already has a published release, a draft with assets, or any image or chart tag in GHCR. Then it:
      - builds the Linux amd64 binaries and tarball
      - builds and pushes each image by digest, untagged, with BuildKit SBOM and `mode=max` provenance
      - runs the CVE gate on each digest (enforce: zero Critical and High after OpenVEX) and checks its Go toolchain
      - only when every image passed, tags them with the version
      - packages the chart with the three digests and pushes it
      - signs the chart and each image with cosign and verifies each signature
      - attests and verifies OpenVEX and the CVE budget verdict on each image
      - writes `SHA256SUMS` and the other assets, uploads them to the draft and publishes it as a pre-release
3. `contract-bundle` publishes the contract bundle ([ADR 0117](../adrs/0117-every-release-publishes-a-contract-bundle-for-consumers.md)).
4. `consumers` opens each consumer's update when the `RELEASE_CONSUMERS` variable is set ([ADR 0127](../adrs/0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md)).

Watch it:

```sh
gh run list -R ploeg-hq/ploeg -w release-candidate.yml -L 3
gh run watch -R ploeg-hq/ploeg <run-id> --exit-status
gh release list -R ploeg-hq/ploeg -L 3
```

## Check what was published

Follow [Verify a release](verify-a-release.md) against the new version, as an outsider would: no login, from an empty directory. Then:

- `helm pull oci://ghcr.io/ploeg-hq/charts/ploeg --version <version>` and `docker pull ghcr.io/ploeg-hq/ploegd:<version>` work without a login. A package GHCR created for the first time is private until a maintainer with the admin role makes it public in the organization's package settings.
- The contract bundle job is green (`gh run list -R ploeg-hq/ploeg -w contract-bundle.yml -L 1` for a stable release; it is part of the candidate run otherwise).

## Make a stable release

A stable `v0.x.y` is a reviewed act (ADR 0115). Nothing below is automatic.

1. Choose the candidate. It must have passed [Verify a release](verify-a-release.md) and run in a deployment, and the [threat model](../security/threat-model.md#findings) must list no open release-blocking finding.
2. Prepare the release on a branch from `main` that merges `development` at that candidate's commit:

   ```sh
   VERSION=0.2.0
   CANDIDATE=v0.2.0-rc.72
   git fetch origin --tags
   git switch -c "release/v$VERSION" origin/main
   git merge --no-ff "$CANDIDATE" -m "Merge $CANDIDATE into main"
   ```

3. In one `chore(release): prepare $VERSION` commit, set `version` and `appVersion` in `ops/helm/ploeg/Chart.yaml` to `$VERSION`, and add a `## $VERSION` section to `CHANGELOG.md` from the candidates' release notes, with every `BREAKING CHANGE` and its upgrade note. Commit with `git commit -s`.
4. Run the gates the release job will run: `mise run verify` and `mise run docs-check`.
5. Open a pull request into `main`. `Protect main` requires `verify`. Merge it with a merge commit, so `main` gets the prepared commit.
6. Tag the merged commit and push the tag:

   ```sh
   git fetch origin main
   git switch --detach origin/main
   grep -E '^(version|appVersion): ' ops/helm/ploeg/Chart.yaml
   git tag -a "v$VERSION" -m "Ploeg $VERSION"
   bash scripts/release.sh validate "v$VERSION"
   git push origin "v$VERSION"
   ```

   `validate` must pass locally before the push: it checks that the tag is on `main`, is the checkout, and that `Chart.yaml` carries the version. The pushed tag cannot be moved or deleted.
7. The tag push runs `Release` on that tag, signed as `release.yml@refs/tags/v$VERSION`, and its success runs `Contract bundle`. Consumer pull requests are not opened for a stable release; dispatch `release-consumers.yml` with the tag if a consumer should get it.
8. [Check what was published](#check-what-was-published).

How `development` continues after the first stable since `v0.1.0`, a merge of `main` back or the fast-forward model of the [stable channel plan](stable-channel-plan.md), is an open owner decision in that plan; settle it before step 2.

## Recover a failed publication

Published tags and artifacts never move. Find where the run stopped (`gh run view <run-id> --log-failed`), then:

| Where it stopped | What exists | Recovery |
| --- | --- | --- |
| `cut`, before semantic-release tagged | nothing | `gh run rerun <run-id> --failed` |
| `release`, in `validate`, `verify` or `docs-check` | the tag and an empty draft | A flaky step: `gh workflow run release.yml -R ploeg-hq/ploeg --ref development -f tag=<tag>`. A real failure: fix it in a new pull request; its merge cuts the next candidate. Leave the failed tag and its draft. |
| `publish`, before the images were tagged (binary build, image build, CVE gate, toolchain check) | untagged image digests in GHCR, the tag and an empty draft | As above. An image over its CVE budget needs a fixed dependency or a reviewed OpenVEX statement first ([ops/vex](../../ops/vex/README.md)). |
| `publish`, after the images were tagged (chart push, signing, attestations, asset upload) | version tags in GHCR, possibly an unsigned chart or images | The version is spent: a re-dispatch refuses with `Refusing to overwrite`. Do not delete or re-tag anything. Add a line to the draft saying the version is incomplete and must not be used, and release the next version: for a candidate, the next releasable merge; for a stable, `v0.x.(y+1)` through [Make a stable release](#make-a-stable-release). |
| `contract-bundle` | the complete release | `gh workflow run contract-bundle.yml -R ploeg-hq/ploeg -f tag=<tag>` |
| `consumers` | the complete release and bundle | `gh workflow run release-consumers.yml -R ploeg-hq/ploeg -f tag=<tag>`; it runs the script from `development`, so a script fix reaches the failed release |

A release published by `workflow_dispatch` is signed by that run: its certificate names `refs/heads/<branch>` and the branch head's commit, not the tag's ([Verify a release](verify-a-release.md#3-check-the-signatures)).

## Rehearse

These commands publish nothing; a second maintainer runs them before the first real release they cut, from a clone of `development`:

```sh
git fetch origin --tags
bash scripts/release.sh check v0.2.0-rc.999
bash scripts/release.sh check v0.2.0
git worktree add --detach ../ploeg-rc v0.2.0-rc.72
(cd ../ploeg-rc && bash scripts/release.sh validate v0.2.0-rc.72)
git worktree remove ../ploeg-rc
git worktree add --detach ../ploeg-stable v0.1.0
(cd ../ploeg-stable && bash scripts/release.sh validate v0.1.0)
git worktree remove ../ploeg-stable
mise exec -- bash scripts/test-release-policy.sh
mise exec -- bash scripts/test-package-chart.sh
mise run verify
mise run docs-check
```

The first `check` exits 0; the second refuses with `needs a reviewed stable release`, because only candidates are cut automatically. Both `validate` calls exit 0 with no output. Then follow [Verify a release](verify-a-release.md) against the newest release, and try a consumer entry with `RELEASE_CONSUMERS_DRY_RUN=true` ([consumer pull requests](release-versioning.md#consumer-pull-requests)).

`publish` cannot be rehearsed without publishing: it writes to GHCR and the release. A rehearsal of it is a real candidate cut by a merge the second maintainer makes.
