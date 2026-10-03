# ADR Review Manifest

## ADR Review Completed

- Date: 2026-09-11
- Change: guard-zero-major-prereleases

## In-Force ADR Context Reviewed

[ADR 0004](../../../docs/adrs/0004-forgejo-leading-home-github-mirror-module-path.md) and [ADR 0020](../../../docs/adrs/0020-published-artifacts-name-the-mirror-as-source.md) retain the existing release authority and artifact source attribution.

## Repository-Level ADRs Created

[ADR 0028](../../../docs/adrs/0028-automatic-releases-stay-zero-major-candidates.md) records the automatic zero-major candidate restriction. It remains proposed and was written before implementation.

## Supersessions

None.

## Validation

Run `mise exec -- go test ./internal/ledger/` and `mise exec -- openspec validate --all` before delivery.
