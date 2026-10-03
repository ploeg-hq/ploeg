# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-09-27
- **Reviewer**: Claude (agent session for Ryan Grippeling)
- **Change**: `execute-openspec-work-items`

## In-Force ADR Context Reviewed

In force = accepted and not superseded. Also read the proposed records the
worker already implements.

- `0010-shift-owns-the-item-lease-owns-the-branch.md` — the gate adds no state
  a Run is claimed on; claims and Leases are untouched.
- `0011-the-pull-request-is-the-blackboard.md` — a reader's failed gate reaches
  the pull request as findings, the existing blackboard path.
- `0013-push-rights-are-minted-per-run.md` — the CLI gets no forge token; the
  gate's fetch uses the Run's existing credential the same way the reader
  fetch does.
- `0014-work-target-is-a-work-item-attribute.md` — the change is a property of
  the Work Item's content, read after the Target is resolved.
- `0017` (proposed) — reused unchanged: a failing gate on a reader is a
  `request_changes` verdict.
- `0030` (proposed) — the brief and the change's own instruction rank below the
  delivery contract; reviewers compare the change's files against the base.

## Repository-Level ADRs Created

none — the behaviour is contained in the worker and reversible by removing the
directive; design.md records the local choices. Open Questions names when it
would earn a record.

## Supersessions

none

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/webgrip/ploeg/internal/ledger
```

## Notes

Nothing to ratify.
