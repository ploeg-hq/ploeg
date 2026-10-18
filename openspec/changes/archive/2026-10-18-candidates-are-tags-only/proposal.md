## Why

The release candidate workflow committed the chart version and changelog back to `development`. The `Protect development` ruleset requires the `verify` check on every push and takes only the repository admin role as a bypass actor; GitHub Actions cannot be added, so that push would be refused and no candidate would ever publish.

## What Changes

- A candidate is a tag on the merged commit; nothing is written back to `development`.
- The release workflow packages the chart with the tag's version as `version` and `appVersion`.
- `scripts/release.sh validate` requires `Chart.yaml` to match only a stable tag.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `experimental-release-policy`: candidates are tags only and their chart is versioned at packaging.

## Non-goals

No Work Item, Run, Lease, Harness or Executor behaviour changes, nothing in design.md §2 is touched, and stable releases stay a reviewed tag with a matching `Chart.yaml`.

## Impact

Repository release automation only: `.releaserc.cjs`, `scripts/release.sh`, its policy test and the release docs; ADR 0115 and ADR 0116 carry dated amendments.
