# ADR Review Manifest

## ADR Review Completed

- Date: 2026-09-10
- Reviewer: Codex, implementing the owner's requested unification baseline
- Change: unified-operator-execution

## In-Force ADR Context Reviewed

The Records index and decision outcomes for 0001 through 0023 were reviewed. In-force 0001 governs this ledger; 0002 through 0004 and 0020/0022 preserve language/license/source/brand; 0005 preserves the dispatch role; 0006/0007 keep client and federation protocols outside core scheduling; 0008 governs inference; 0009 excludes a second orchestration dependency; 0010 through 0014 govern Run/Shift authority, evidence and credentials. Existing proposed records remain historical context, not invented accepted policy.

## Repository-Level ADRs Created

- [0024](../../../docs/adrs/0024-operator-work-uses-one-execution-authority.md): operator work uses Ploeg execution authority.
- [0025](../../../docs/adrs/0025-management-authority-stays-in-the-control-plane.md): management authority and unresolved accounting remain in the control plane.

## Supersessions

None. Manual admission extends the earlier tracker-only scope under explicit user authorization; accepted records are unchanged.

## Validation

`mise exec -- go test ./internal/ledger/` passed before implementation: `ok github.com/webgrip/ploeg/internal/ledger 0.447s`.

## Notes

Both new records remain proposed for explicit human ratification. They were written before implementation rather than to justify code after the fact.
