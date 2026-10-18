## 1. Release automation

- [x] 1.1 Release candidates with semantic-release on push to `development`, through the shared configuration's GitHub adapter, and call the release workflow with the cut tag.
- [x] 1.2 Map breaking changes to a minor bump and refuse non-candidate or existing versions in `scripts/release.sh check` before anything is written.
- [x] 1.3 Publish onto the draft release the candidate workflow opened; keep stable tags on the push trigger.

## 2. Decisions and proof

- [x] 2.1 Record ADR 0115 (supersedes 0063) and ADR 0116 (supersedes 0064) and run `go test ./internal/ledger/`.
- [x] 2.2 Prove the version line on a fixture remote and with a dry run against GitHub (docs/research/2026-10-18-release-candidates-from-semantic-release.md).
- [x] 2.3 Run `mise run verify` and `mise run docs-check` and record the results in the pull request.
