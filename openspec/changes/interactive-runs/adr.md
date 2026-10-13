# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-13
- **Reviewer**: Claude (planning session for Ryan Grippeling)
- **Change**: `interactive-runs`

## In-Force ADR Context Reviewed

- `0006-ahp-is-the-wrong-layer.md` (accepted) — AHP parked; its consequence says an interactive surface belongs in a separate service. The surface stays in the consumer; the relay is new. Its trigger "an interactive 'attach to a running agent' requirement becomes real" has fired. Not superseded; flagged in design Open Question 1.
- `0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md` (proposed, implemented) — redaction, polling, transcript stages reused.
- `0071` (proposed), R6 — interactions stored in PostgreSQL.
- `0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md` (accepted) — how a stopped or paused-out Run is restarted.
- `0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md` (accepted), R15 — Hold kept while waiting.
- `0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md` (proposed) — considered as the pause mechanism and not chosen.
- `0068-context-added-while-a-shift-runs-reaches-the-next-run.md` (accepted) — carried-over messages reach the next Run as operator notes; its rejected option "into the running Run between turns" is what messages to attended Runs now do. Narrowed, not superseded, by ADR-0104 for messages to attended Runs (design Decision 11, Open Question 8 resolved on 2026-10-13).
- `0010-shift-owns-the-item-lease-owns-the-branch.md` (accepted) — only writing Runs hold a Lease; every Run renews its own deadline.
- `0102` (proposed, in the `submit-work-items` change) — the submission route that carries the attended flag; this change depends on it.
- `0069-ploeg-names-none-of-its-consumers.md` (accepted) — no consumer-specific behaviour.
- `0024` (proposed) — Operator Execution pause and message, executed by the consumer.

## Repository-Level ADRs Created

- `docs/adrs/0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md` — pull-only relay through ploegd, deadlines with policy fallback, ACP first.
- `docs/adrs/0101-a-paused-run-keeps-its-pod-and-session-for-a-bounded-time-and-is-never-retried-automatically.md` — pause semantics, bounds and recovery.
- `docs/adrs/0104-a-message-to-an-attended-run-reaches-it-at-the-next-prompt-turn-and-context-items-still-wait.md` — narrows ADR-0068: messages reach an interactive attended Run at the next prompt turn; context items still wait for the next Run.
- ADR-0100 also records the owner's `autoApprove` decision of 2026-10-13.

## Supersessions

- none. ADR-0104 narrows ADR-0068 with `supersedes: none`: the ledger's `supersedes:` replaces a record whole and marks its row superseded, while 0068 stays in force for context items and unattended Runs. This follows ADR-0080's narrowings; a dated note is appended to 0068 when 0104 is accepted.

## Validation

```sh
$ mise exec -- go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger	0.420s
```

## Notes

All three records are `proposed`. They diverge from Unfold ADR-0023 by the owner's decision of 2026-10-13; the Unfold record is amended separately. Whether ADR-0006 needs a superseding record is for its 2026-10-31 review.
