# Release candidates from semantic-release: fixture and dry-run

Evidence for [ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) and [ADR 0116](../adrs/0116-every-releasable-merge-to-development-cuts-a-candidate.md), recorded on 2026-10-18 against `development` at `bfe58d1` (`v0.2.0-rc.26`).

## Toolchain

The shared configuration was installed the way the release candidate workflow installs it: a clone of `webgrip/semantic-release-config` at the change adding the GitHub adapter (`feat/forge-adapter`, 37c8a86), then `npm ci --omit=dev --ignore-scripts` from its lockfile, so its `overrides` apply. That gave semantic-release 25.0.9, `@semantic-release/github` 12.0.10 and `@semantic-release/release-notes-generator` 15.0.0-beta.2, the notes pair the shared configuration requires. Node 24.21.0 came from `mise.toml`.

## Run 1: a fix on a local fixture remote

A bare clone of `ploeg-hq/ploeg` with all tags served as `origin`. On `development`: this change as one `ci(release)` commit, then `fix(fixture): a releasable change lands on development`. semantic-release ran with `.releaserc.cjs`, the GitHub publish step removed because the remote is a local path.

```text
Found git tag v0.2.0-rc.26 associated with version 0.2.0-rc.26 on branch development
Found 2 commits since last release
The release type for the commit is patch
The next release version is 0.2.0-rc.27
Call script bash scripts/release.sh check v0.2.0-rc.27
Prepared Git release: v0.2.0-rc.27
Created tag v0.2.0-rc.27
Call script echo "version=0.2.0-rc.27" >> "$GITHUB_OUTPUT" && echo "tag=v0.2.0-rc.27" >> "$GITHUB_OUTPUT"
```

The remote then held `chore(release): v0.2.0-rc.27 [skip ci]` on `development`, tagged `v0.2.0-rc.27`, with `version: 0.2.0-rc.27` and `appVersion: 0.2.0-rc.27` in `ops/helm/ploeg/Chart.yaml` and the new section under the existing `# Changelog` heading. `GITHUB_OUTPUT` received `version=0.2.0-rc.27` and `tag=v0.2.0-rc.27`. A fresh clone checked out at the tag passed `bash scripts/release.sh validate v0.2.0-rc.27`. The dead tag `v0.2.0-rc.22` played no part: semantic-release takes the highest candidate reachable from the branch.

## Run 2: nothing releasable

```text
Found git tag v0.2.0-rc.27 associated with version 0.2.0-rc.27 on branch development
Found 0 commits since last release
There are no relevant changes, so no new version is released.
```

## Run 3: a breaking change

`feat(fixture)!: a breaking change` with a `BREAKING CHANGE:` footer.

```text
The release type for the commit is minor
The next release version is 0.2.0-rc.28
Call script bash scripts/release.sh check v0.2.0-rc.28
```

## Run 4: why `channel: false`

The same fixture from `v0.2.0-rc.26`, dry run, with the shared default branch entry `{ name: 'development', prerelease: 'rc' }`. semantic-release gives a prerelease branch the channel `development` and counts only tags whose git note names that channel. Ploeg's hand-pushed tags have no note, so it fell back to `v0.1.0`:

```text
Found git tag v0.1.0 associated with version 0.1.0 on branch development
Found 295 commits since last release
The next release version is 0.2.0-rc.1
Call script bash scripts/release.sh check v0.2.0-rc.1
Failed step "verifyRelease" of plugin "@semantic-release/exec"
v0.2.0-rc.1 already exists
```

`channel: false` makes the branch's channel the default one, which matches tags without a note, and the line continues from `v0.2.0-rc.26`. The guard stopped the wrong version before any commit or tag was written.

## Run 5: dry run against GitHub with the full configuration

A clone of `ploeg-hq/ploeg` `development` with the change and `fix(dry): a releasable change` committed locally and not pushed, run as `semantic-release --dry-run --no-ci` with the owner's `gh` token in `GITHUB_TOKEN`.

```text
Run automated release from branch development on repository https://github.com/ploeg-hq/ploeg.git in dry-run mode
Allowed to push to the Git repository
Verify GitHub authentication
Completed step "verifyConditions" of plugin "@semantic-release/github"
Found git tag v0.2.0-rc.26 associated with version 0.2.0-rc.26 on branch development
The next release version is 0.2.0-rc.27
Call script bash scripts/release.sh check v0.2.0-rc.27
Skip step "publish" of plugin "@semantic-release/github" in dry-run mode
Release note for version 0.2.0-rc.27:
## 0.2.0-rc.27 (https://github.com/ploeg-hq/ploeg/compare/v0.2.0-rc.26...v0.2.0-rc.27) (2026-10-18)
### Fixed
* **dry:** a releasable change (f75330a)
### CI
* **release:** cut candidates with semantic-release (424e652)
```

## What these runs do not show

They did not push to GitHub, open a draft release or run the release workflow. The first releasable merge after this change is the first run of `release-candidate.yml`, its `workflow_call` into `release.yml`, and `release.sh publish` against a draft release. The `Protect development` ruleset's required `verify` check would reject the release commit until the GitHub Actions identity can bypass it.
