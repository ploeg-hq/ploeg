# ADR Review Manifest

## ADR Review Completed

- **Date**: YYYY-MM-DD
- **Reviewer**: `<who>`
- **Change**: `<change-id>`

## In-Force ADR Context Reviewed

List the accepted, non-superseded records read before designing, and why each
was relevant (or state "none" if the corpus is empty).

- `NNNN-kebab-title.md` — how it constrained this change

## Repository-Level ADRs Created

- `docs/adrs/NNNN-kebab-title.md` — one line on the decision
- …or "none — no durable architectural decision was introduced by this change."

## Supersessions

- `NNNN` supersedes `MMMM` — why the prior decision was revisited
- …or "none"

## Validation

```sh
$ go test ./internal/ledger/
# paste the output here
```

## Notes

Anything a human must ratify — in particular any record left `proposed` because
the decision is not the agent's to accept.
