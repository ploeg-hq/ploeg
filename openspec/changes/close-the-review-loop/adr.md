# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-07-29
- **Reviewer**: Claude (session with Ryan Grippeling)
- **Change**: `close-the-review-loop`

## In-Force ADR Context Reviewed

Reviewed 0001–0017 and the Records index. In force = accepted and not
superseded: 0001–0014. Those that constrained this change:

- `0010-shift-owns-the-item-lease-owns-the-branch.md` — a Round is readers or
  one writer, so a fix round is a PAIR (writer, then review), not a mixed
  round.
- `0011-the-pull-request-is-the-blackboard.md` — the verdict rides the same
  OutcomeReport the findings already do; no second channel.
- `0012-two-level-budgets-authorized-and-settled.md` — the pool is the first
  bound on the loop, and the derived-not-counted discipline is borrowed from
  its `reserved` sum.
- `0013-push-rights-are-minted-per-run.md` — a re-opened writer takes the
  Lease like any other; readers in the review round still get none.
- `0008-litellm-is-the-credential-and-metering-seam.md` — extra rounds mint
  extra per-run keys; nothing about the metering boundary changes.

`0015`/`0016`/`0017` are `proposed`, not in force. This change IMPLEMENTS 0017
and must not land before a human ratifies it (see Notes).

## Repository-Level ADRs Created

- `docs/adrs/0017-the-review-loop-is-verdict-driven-and-capped.md` — a
  reviewing Role's verdict re-opens the plan's writer; the pool, a cap and the
  verdict stop the loop, and the verdict may do nothing else.

## Supersessions

none

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/webgrip/ploeg/internal/ledger	0.011s
```

## Notes

ADR-0017 is left `proposed`. It is not the agent's to accept, and it is the
kind of decision that should be ratified deliberately: it is the first place
an agent's output influences what runs next, and the boundary it draws — one
bit, no authorship — is the whole safety argument. The implementation is
sequenced behind it.
