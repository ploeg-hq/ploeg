## 1. Tag-only candidates

- [x] 1.1 Drop the release commit from `.releaserc.cjs` and refuse to load if one would be made.
- [x] 1.2 Package the chart at the tag's version; require a matching `Chart.yaml` only for stable tags.
- [x] 1.3 Amend ADR 0115 and ADR 0116 and rerun the fixture (docs/research/2026-10-18-release-candidates-from-semantic-release.md).
- [x] 1.4 Run `mise run verify`, `mise run docs-check` and actionlint.
