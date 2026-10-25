# Baseline verification record

A fresh public clone ran the documented verification on 2026-10-24. This page records what was tested, how, where, and what came out, so the result does not depend on CI logs that expire. Record a new run as a new dated section below; do not edit an earlier one.

## 2026-10-24: commit da85221

### Tested commit

`da85221f751296116ea99746ce364085f1d2b16e` on branch `chore/provenance-check`, the commit that adds the [provenance check](../../../scripts/check-provenance.sh), one commit on top of `development` at `ad9d400`. The commit that adds this page changes only documentation.

### Commands

```sh
git clone --branch chore/provenance-check https://github.com/ploeg-hq/ploeg.git fresh
cd fresh
git rev-parse HEAD
mise trust .
mise run setup
mise run verify
mise run docs-check
mise run check-provenance
```

### Environment

| Item | Value |
| --- | --- |
| OS | Linux Mint 22.1, kernel 6.8.0-83-generic, x86_64 |
| Machine | 32 logical CPUs, 61 GiB memory |
| mise | 2026.9.18 |
| Git | 2.43.0 |
| Bash | 5.2.21 |
| Go | go1.27.3 (from `mise.toml`) |
| Helm | v4.3.0 (from `mise.toml`) |
| Node.js | v24.21.0 (from `mise.toml`) |

The clone was fresh; the mise tool installs and the Go module and build caches on the machine were not. The run did not set `PLOEG_COMPAT_REQUIRE_BASE` or `PLOEG_REQUIRE_PID_NAMESPACE`, which the CI `verify` job sets, so tests gated on them may have skipped here; CI runs them.

### Results

| Command | Exit | Wall time | Result |
| --- | --- | --- | --- |
| `mise run setup` | 0 | under 1 s | every pinned tool already installed |
| `mise run verify` | 0 | 72 s | see below |
| `mise run docs-check` | 0 | 4 s | decisions, contracts, generated configuration and domain views current |
| `mise run check-provenance` | 0 | under 1 s | `PROVENANCE.md holds for this repository and for 9c1d53f01fbfb65733800aa75e288341734dc23f:apps/ploeg` |

`mise run verify` ran [`scripts/verify.sh`](../../../scripts/verify.sh) to the end:

- provenance (local history): the history starts at the recorded root commit.
- `gofmt`, `go vet`, `go build`: clean.
- `go test -count=1 ./...`: 65 packages `ok`, 3 without test files, none failed.
- Brand marks (ADR-0022), licence consistency, release tag policy, chart packaging (every release image pinned by digest), contract bundle, Go module publication, release consumers, DCO check, Claude Code install and Go toolchain checks: all passed.
- `openspec validate --all --strict`: 25 passed, 0 failed.
- `helm lint`: 11 runs, 0 charts failed; every CI values variant and the quickstart values rendered.
- Helm goldens: every render matches its golden.
- Chart NetworkPolicies: every Ploeg pod is under a default-deny policy with its own allowlist.
