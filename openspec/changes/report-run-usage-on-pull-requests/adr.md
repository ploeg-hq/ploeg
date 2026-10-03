# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-09-28
- **Reviewer**: Claude (agent session for Ryan Grippeling)
- **Change**: `report-run-usage-on-pull-requests`

## In-Force ADR Context Reviewed

In force = accepted and not superseded: 0001–0014, 0020, 0022. Those that
constrained this change:

- `0008-litellm-is-the-credential-and-metering-seam.md` — the report reads what
  settlement already stored; it makes no gateway call and the metering seam is
  untouched.
- `0010-shift-owns-the-item-lease-owns-the-branch.md` — the report is transport
  over a Shift's results; no claim, Lease or Round changes.
- `0011-the-pull-request-is-the-blackboard.md` — the report is one more thing
  Ploeg transports to the pull request, not a new channel.
- `0012-two-level-budgets-authorized-and-settled.md` — the totals render the two
  existing levels (Shift pool, per-Run authorization); no budget semantics
  change.
- `0013-push-rights-are-minted-per-run.md` — no new credential; a reading Run has
  no account and the report says so rather than inventing a cost for it.
- `0014-work-target-is-a-work-item-attribute.md` — the report resolves the forge
  and repository through the Work Item's Target, exactly as findings do.

The proposed records this change renders the output of, but does not implement
or depend on: `0017` (the verdicts the report prints), `0035` (the worker
verification the report cites as evidence). They are not in force; the report is
inert until the behaviour they describe exists, and it reads those fields as
empty rather than assuming them.

## Repository-Level ADRs Created

none — the behaviour is contained in `pkg/shiftengine` and two additive SPI
methods; its state is read, not stored, and it is reversible by a chart value.
`design.md` records the local choices. Open Questions names when it would earn a
record (becoming the surface a human bills from, or a scaled-out ploegd).

## Supersessions

none

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/webgrip/ploeg/internal/ledger	0.029s
```

## Notes

Nothing to ratify. No accepted record is contradicted. Ticket `#1305` was not
retrievable from the sandbox, so the criteria checked against 0008, 0011 and
0012 are the ones `VIK-1306` states of it; proposal.md "Open questions" records
that the owner should confirm the mapping.
