## Why

The release of `v1.0.0-rc.1` interpreted an accurate breaking-change notice as permission to leave Ploeg's experimental 0.x series. Compatibility notices must remain visible without declaring product maturity.

## What Changes

- Map breaking changes to a minor increase in Ploeg's existing release configuration.
- Reject automatic stable releases, major versions above zero, and release promotion outside `development`.
- Exercise the real release analyzer and version calculator before the existing CI release step.

## Capabilities

### New Capabilities

- `experimental-release-policy`: automatic Ploeg releases remain 0.x release candidates until an explicit human-approved policy change.

### Modified Capabilities

None.

## Non-goals

This changes only release automation. It does not change Work Item, Run, Lease, Harness, or Executor behavior, rewrite compatibility history, change the shared release package, or withdraw external tags and artifacts. It adds none of the product features excluded by [design.md](../../../docs/design.md).

## Impact

The seam is repository release automation: `.releaserc.cjs`, a local release plugin, executable tests, and the existing source-change workflow. Artifact tags retain `v${version}`. The mistaken 1.x tag must be removed from version discovery through an explicitly authorized withdrawal before a replacement release can proceed.
