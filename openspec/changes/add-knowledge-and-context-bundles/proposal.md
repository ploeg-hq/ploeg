# Add knowledge packs and context bundles

## Why

A Run starts with the Work Item, earlier Rounds' findings, an optional OpenSpec
brief, the repository's instruction files and Ploeg's skills. It gets nothing
of what earlier Runs or people learned about the repository, and a person has
no way to hand it files: Ploeg accepts no attachments, and steering is text
that Ploeg records only for audit. The owner asked on 2026-10-04 for agents to
draw on an OKF knowledge base in Omnigraph, and for people to attach "a zip
with context" at the start and while steering.

## What Changes

**Seams touched:** the TaskSpec and OutcomeReport contracts (additive optional
fields), the run API (an additive claim field and one Run route), the operator
API (context routes), the store (one migration) and the worker.

- **Knowledge pack (ADR-0065).** The worker selects OKF concepts bearing on the
  Work Item from the repository's `knowledge/` directory, configured bundles
  and context bundles, writes them outside the clone, and puts their index in
  the prompt. `TaskSpec.knowledge` records the concepts and source digests.
- **Learnings (ADR-0066).** `OutcomeReport.learnings`: at most 10 OKF-shaped
  proposals, kept for review and never briefed until accepted.
- **Context bundles (ADR-0067).** Operator routes to attach a zip, tar.gz or
  single file to a Work Item, stored in Postgres; claim references; a Run
  route to download; safe unpacking under fixed limits; a "Context from
  people" prompt section; `TaskSpec.context` provenance.
- **Steering timing (ADR-0068).** Context added while a Shift is open reaches
  the next Run, never the running one, and is marked as added while steering.
- **Tooling.** `pkg/okf`, `pkg/knowledge`, `pkg/contextbundle` and the
  `ploeg-okf` command (validate, pack, to-omnigraph, from-omnigraph,
  export-query, context inspect).

## Non-Goals

- Live delivery into a running Run's turn (spike).
- Reading the Tenant knowledge base from Omnigraph in the worker (spike; the
  proof of concept reads directories).
- Storing and reviewing learnings in ploegd and Vloer (next increment).
- Object storage for context (spike).
- Tracker attachments as a context source (follow-up).

## Impact

- Contracts: `taskspec.v1` (`knowledge`, `context`), `outcomereport.v1`
  (`learnings`), `run-api.v1` (`claimResponse.context`). Additive, optional.
- Store: migration adding `work_item_context`.
- Configuration: `PLOEG_REPO_KNOWLEDGE_DIR`, `PLOEG_KNOWLEDGE_DIRS`,
  `PLOEG_KNOWLEDGE_BUDGET_BYTES`, `PLOEG_KNOWLEDGE_OUTBOX`,
  `PLOEG_CONTEXT_MAX_BYTES`, `PLOEG_CONTEXT_MAX_TOTAL_BYTES`.
- With no `knowledge/` directory, no settings and no attached context, a Run
  is unchanged.
