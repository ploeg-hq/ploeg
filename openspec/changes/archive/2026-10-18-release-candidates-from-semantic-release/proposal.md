## Why

Every Ploeg candidate took a hand-written preparation pull request and a hand-pushed tag; on 2026-10-17 a one-line fix needed three hand steps in Ploeg alone to become a release. The owner decided that every project releases from its home forge with the shared semantic-release configuration and that every releasable merge to `development` cuts a candidate (homelab-cluster ADR-0069, decisions D1 and D4).

## What Changes

- A merge to `development` with a releasable commit cuts the next `v0.x.y-rc.N`: semantic-release writes the chart version and changelog, commits, tags and opens a draft release, and the same run publishes it through the release workflow.
- Breaking changes bump the minor version; `scripts/release.sh check` refuses any version that is not a new zero-major candidate before anything is written.
- A stable `v0.x.y` stays a reviewed tag on `main`.
- **BREAKING** for maintainers: candidate tags are no longer pushed by hand, and the chart version and `CHANGELOG.md` are no longer edited by hand for a candidate.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `experimental-release-policy`: candidates are calculated from conventional commits; stable releases stay explicit.

## Non-goals

No Work Item, Run, Lease, Harness or Executor behaviour changes, and nothing in design.md §2 is touched. Stable releases are not automated, existing tags and artifacts are not moved, signing is not added, and consumers' pins are not changed here.

## Impact

The seam is repository release automation: `.releaserc.cjs`, `.github/workflows/release-candidate.yml`, `.github/workflows/release.yml`, `scripts/release.sh` and its policy test. Decisions: ADR 0115 (supersedes 0063) and ADR 0116 (supersedes 0064). Requires `@webgrip/semantic-release-config` with the GitHub adapter, and a bypass for the GitHub Actions identity on the `Protect development` ruleset.
