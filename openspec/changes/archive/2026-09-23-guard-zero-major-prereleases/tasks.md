## 1. Release policy

- [x] 1.1 Replace the shared breaking-to-major analyzer rule locally without losing release notes or standard tags.
- [x] 1.2 Guard release entry against promotion outside development and verify computed versions are zero-major rc releases.

## 2. Proof and CI

- [x] 2.1 Reproduce the original historical major result through the real analyzer and prove corrected analysis and next version from v0.3.0-rc.4.
- [x] 2.2 Test guarded lifecycle rejection and retained compatibility notes, and wire tests before the existing release composite.
- [x] 2.3 Run release policy, OpenSpec, ledger, Go, Helm and brand gates and record results.
- [x] 2.4 Reject non-experimental tags at the artifact-publishing entry point and execute its actual shell in regression tests.

## Validation evidence

[2026-09-11 release-policy verification](../../../docs/research/2026-09-11-release-policy-verification.md) records the exact CI image, 14 passing tests, actual-history calculation and repository gates. The independent review found the reusable workflow `enabled` bypass; guarded inputs and executable negative-path checks were added before final verification.
