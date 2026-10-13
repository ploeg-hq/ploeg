# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-13
- **Reviewer**: Claude (agent), for ratification by Ryan Grippeling
- **Change**: `submit-work-items`

## In-Force ADR Context Reviewed

Supersession graph before this change: 0062→0004, 0063→0028, 0079→0074. Highest number in use: 0101.

- `0006-ahp-is-the-wrong-layer.md`: the interactive surface stays in the consumer. The Admission adds no live channel.
- `0010-shift-owns-the-item-lease-owns-the-branch.md`: an admitted Work Item gets a Shift and Leases like any other.
- `0012-two-level-budgets-authorized-and-settled.md`: the consumer ceiling caps the Shift pool, and Runs are still authorized per Run.
- `0014-work-target-is-a-work-item-attribute.md`: the registered target is pinned on the Work Item at Admission.
- `0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md`: "try again" is the existing requeue, unchanged.
- `0068-context-added-while-a-shift-runs-reaches-the-next-run.md`: a message is the existing note, unchanged.
- `0069-ploeg-names-none-of-its-consumers.md`: the provider is `manual`, not a consumer's name.
- `0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md`: Asks are untouched.
- Proposed records taken into account, not designed against: 0019, 0021, 0024, 0039, 0072, 0091, 0097–0101, 0103, 0104.
- `0068` stays in force for notes sent through this route's existing `/notes`; ADR-0104 (proposed, `interactive-runs`) narrows it only for messages to attended Runs.

## Repository-Level ADRs Created

- `docs/adrs/0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md`: an Operator Consumer admits ordinary manual-origin Work Items into the Team Queue, and `ploeg-worker` executes them. Status `proposed`.

## Supersessions

- `0102` supersedes `0024` where 0024 makes the consumer the delegated Executor. Both are proposed. The ledger derives "superseded by 0102" for 0024's Records row from the `supersedes:` field, so the index shows it before 0102 is ratified (design Open Question 5).

## Validation

```sh
$ go test -count=1 ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger	0.207s
$ go test -count=1 -v -run TestADRIndexParity ./internal/ledger/
    adr_test.go:250: 99 record(s), 99 Records row(s), 4 supersession(s)
--- PASS: TestADRIndexParity (0.01s)
```

## Notes

- 0102 stays `proposed` until the owner ratifies it. It records the owner's choice of 2026-10-13: Ploeg executes every session, the Harness is chosen per session, and interactivity is kept.
- Nothing is built. The record does not justify existing code after the fact.
- The design keeps these as change-local and records no ADR for them: the budget default (Open Question 2), the retirement as a separate change (Open Question 1), and the domain-language updates (Open Question 6).
