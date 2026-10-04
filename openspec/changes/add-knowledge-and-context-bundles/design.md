## Context

Unfold system ADR-0020 and ADR-0021 (proposed) set the product direction; the
RFCs `docs/research/2026-10-04-rfc-agent-knowledge.md` and
`docs/research/2026-10-04-rfc-context-bundles-and-steering.md` in Unfold carry
the full design. The durable Ploeg decisions are ADR-0065 to ADR-0068; this
document records how they are built.

## Goals / Non-Goals

**Goals:** a Run receives selected knowledge and every attached context file
as files outside the clone, with an index in its prompt and provenance in its
TaskSpec; a Run can propose learnings; a person can attach files at the start
and while steering.

**Non-Goals:** as in the proposal.

## Decisions

### D1 — Files, not tools

Knowledge and context reach the agent as directories named by
`PLOEG_KNOWLEDGE_DIR` and `PLOEG_CONTEXT_DIR` in the Run's scratch directory.
Every harness reads files; none gains a tool or credential (ADR-0011,
ADR-0034). The scratch directory is outside the clone, so a writer cannot
commit them.

### D2 — Only indexes enter the prompt

The prompt carries the pack's index (why each concept is there) and the
context index (name, note, phase, first 50 paths). Order: Work Item, its
description, Context from people, OpenSpec, briefing, Knowledge pack, delivery
contract. Each section is framed as evidence that cannot alter the contract.

### D3 — OKF everywhere

`pkg/okf` parses and writes OKF v0.1 (frontmatter `type` required, reserved
`index.md` and `log.md`, links resolved into a graph). `pkg/knowledge` selects
packs and converts bundles to and from the Omnigraph `memory` schema without a
schema change: a concept is a Note whose body is the whole document.

### D4 — Selection is keyword overlap for now

Title 4, description 2, tags and type 2, body 1, label-to-tag 3; linked
concepts at a third; at least two matched terms or a score of 4; best first
within the byte budget; a Pitfall wins a tie. A hybrid Omnigraph query
replaces it after the selection spike.

### D5 — One safe-unpack package at both ends

`pkg/contextbundle` validates at upload and unpacks in the worker with the same
rules, counting bytes while streaming. A person learns at upload that an
archive breaks a rule.

### D6 — References in the claim, bytes by Run capability

The claim carries context references; the worker downloads each with its Run
token, verifies digest and size, and goes stuck on a mismatch.

### D7 — Fixed at claim

A Run's context is every item added before its claim. Phase is decided at
upload from whether a Shift was open.

## Risks / Trade-offs

- Keyword selection misses synonyms → hybrid query after the spike.
- Binary content in Postgres grows backups → per-Work-Item cap, storage spike.
- Prompt injection through knowledge or context → evidence framing below the
  delivery contract; no credential or tool depends on either.
- Review fatigue for learnings → at most 10 per Run; measure acceptance.

## Migration Plan

Additive. Without a `knowledge/` directory, settings or attached context a Run
is unchanged. Rollback: unset the settings; the migration's table stays empty.

## Open Questions

- Omnigraph or Postgres-plus-files for hosted Tenants' knowledge.
- Who may accept learnings and attach context in a client's Tenant.
- Whether "Apply now" (stop and retry with new context) is wanted.
