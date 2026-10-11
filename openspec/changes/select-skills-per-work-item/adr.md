# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-11
- **Reviewer**: Claude (session with Ryan Grippeling)
- **Change**: `select-skills-per-work-item`

## In-Force ADR Context Reviewed

Reviewed the Records index through 0082. Those that constrained this change:

- `0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md` — role skills stay unconditional; this change adds selected skills beside them.
- `0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md` — selection reuses the pack's matcher.
- `0030-target-repository-instructions-rank-below-the-delivery-contract.md` — selected skills are listed before the delivery contract and rank below it.
- `0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md` — a context bundle is never a skill source.

## Repository-Level ADRs Created

- `docs/adrs/0083-a-run-is-told-which-skills-bear-on-its-work-item.md` — the worker selects skills with the knowledge matcher and names them in the prompt.

## Supersessions

- none; 0083 refines 0035.

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger	0.016s
```

## Notes

The owner accepted 0083 on 2026-10-11.
