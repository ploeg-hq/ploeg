# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-13
- **Reviewer**: Claude (planning session for Ryan Grippeling)
- **Change**: `record-harness-per-run`

## In-Force ADR Context Reviewed

Reviewed the Records index through 0093 and the supersession graph. Those that constrained this change:

- `0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md` (proposed, implemented) — write-once, null is unknown, claim-time recording; this change extends it.
- `0018-the-outcome-drop-box-is-every-harnesss-return-path.md` (proposed) — the worker discards a drop-box `harness` value.
- `0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md` (proposed) — the model shown is the Role's default. The owner decided on 2026-10-13 that a session may choose another from the Role's policy; ADR-0103 (`select-harness-per-run`) records it and narrows 0039.
- `0045-keep-run-usage-and-merge-facts.md` (proposed), `0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md`, `0069-ploeg-names-none-of-its-consumers.md` — facts exposed, comparisons left to consumers.
- `0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md` — what a version means for a remote session.

## Repository-Level ADRs Created

- `docs/adrs/0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md` — the Harness version comes from the worker at claim and in the Outcome Report; ploegd learns each Role's configured Harness from the chart.

## Supersessions

- none (0097 builds on 0091 without replacing it)

## Validation

```sh
$ mise exec -- go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger	0.420s
```

## Notes

0097 is `proposed`; the owner ratifies it. Recommended to ratify 0091 at the same time (design Open Question 3).
