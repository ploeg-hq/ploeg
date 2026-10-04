# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-04
- **Reviewer**: Claude (session with Ryan Grippeling)
- **Change**: `add-knowledge-and-context-bundles`

## In-Force ADR Context Reviewed

Reviewed the Records index through 0064. Those that constrained this change:

- `0011-the-pull-request-is-the-blackboard.md` — Ploeg injects; the agent
  never fetches. Knowledge and context are injected as files.
- `0030-target-repository-instructions-rank-below-the-delivery-contract.md` —
  the same ranking applies to knowledge and context.
- `0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md` — any
  future knowledge-store credential stays in the worker.
- `0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md`
  — the pack sits beside skills and toolchains in the Run's sandbox.
- `0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md` —
  learnings are proposals, never facts the worker acts on.

## Repository-Level ADRs Created

- `0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md`
- `0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md`
- `0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md`
- `0068-context-added-while-a-shift-runs-reaches-the-next-run.md`

0067 and 0068 were accepted by the owner on 2026-10-04. 0065 and 0066 stay
`proposed` until the measurement spike (VIK-1859) reports, so the knowledge
pack and learnings parts must not merge to trunk before then.

## Notes

Unfold system ADR-0020 and ADR-0021 and Vloer ADR-0038 (proposed) are the
product-level decisions this change serves.
