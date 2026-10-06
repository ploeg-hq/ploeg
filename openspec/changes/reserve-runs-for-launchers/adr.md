# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-06
- **Reviewer**: Claude (agent session for Ryan Grippeling)
- **Change**: `reserve-runs-for-launchers`

## In-Force ADR Context Reviewed

These records constrained the change:
- `0002-go-as-the-implementation-language.md`: no new language.
- `0010-shift-owns-the-item-lease-owns-the-branch.md`:
  - one claim predicate, served by ploegd;
  - the Lease is created at bind.
- `0012-two-level-budgets-authorized-and-settled.md`: nothing is authorized at reservation.
- `0013-push-rights-are-minted-per-run.md`: rights are minted at bind, unchanged.
- `0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md`:
  - a capacity wait consumes no retry budget;
  - a start fault is `infra_node` against the reserved Run.

Proposed records also read:
- `0025-management-authority-stays-in-the-control-plane.md`: ploegd gains no Kubernetes rights.
- `0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md`: `launchRef` is opaque, so Ploeg learns nothing about Kubernetes.

## Repository-Level ADRs Created

- `docs/adrs/0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md` (proposed; rewritten 2026-10-06 from push launch)

## Supersessions

None.

## Validation

```
$ go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger
```

## Notes

Planning-only change: nothing in `tasks.md` is implemented yet. This change replaces `launch-sandboxes-from-ploegd`, which proposed push launch and was withdrawn.
