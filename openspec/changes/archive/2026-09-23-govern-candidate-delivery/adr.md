# ADR Review Manifest

## ADR Review Completed

- Date: 2026-09-11
- Change: govern-candidate-delivery
- Reviewer: Codex, implementing the authorized delivery baseline

## In-Force ADR Context Reviewed

The existing ledger governs immutable accepted decisions. ADRs 0005, 0008 and 0010–0014 retain the dispatch, credential, Run, evidence and budget boundaries. Proposed ADRs 0024–0026 and De Vloer ADR 0006 provide related implementation intent without invented acceptance.

## Repository-Level ADRs Created

[ADR 0027](../../../docs/adrs/0027-candidate-delivery-uses-trusted-evidence-and-a-publication-barrier.md) records candidate-bound trusted verification and conservative publication authority before implementation.

## Supersessions

None. Legacy worker publication remains separate and gains no trusted verification label.

## Validation

The parent agent registered ADR 0027 in the ledger. `mise exec -- openspec validate govern-candidate-delivery --strict` and `mise exec -- go test ./internal/ledger/` passed. The full Go suite subsequently passed, including delivery binding, authorization and publication replay regressions.
