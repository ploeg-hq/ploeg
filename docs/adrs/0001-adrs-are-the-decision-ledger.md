---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# ADRs in docs/adrs/ are the single decision ledger

## Context and Problem Statement

Durable decisions were recorded in three shapes at once: verdict rows in
`docs/design.md` §8 and §9, evidence dossiers in `docs/research/`, and
`*[research]*`-tagged items in `docs/backlog.md`. Every shape was individually
reasonable and the combination was not: a reader had no single place that
answers "what has been decided, and is it still in force", and nothing
mechanically stopped two of them disagreeing.

The failure mode is not hypothetical in this estate. `webgrip/homelab-cluster`
ADR-0050 exists because two documented, individually correct delivery protocols
(a CLAUDE.md rule and ADR-0048) contradicted each other for two days and
produced zombie PRs. Prose cannot arbitrate between two ledgers.

Ploeg is also about to adopt OpenSpec with the `spec-driven-with-adr` schema,
which requires a ratified ADR set to design within. A workflow that says "read
the in-force ADRs first" against a repo with no ADRs is theatre.

## Decision Drivers

* **One place to look**, with supersession visible rather than inferred.
* **Convergence with the estate** — `erfbeeld` and `homelab-cluster` already run
  MADR 4.0 with the `adr-writer` skill; a third format costs a translation layer
  for every reader, human or agent.
* **Keep what design.md §8 did better than most ADR practice**: verdicts carry
  *dated re-evaluation triggers*, and the verdict is separated from its evidence.
* **Mechanically enforceable** — a convention no gate checks is a convention that
  drifts.

## Considered Options

* MADR 4.0 ADRs in `docs/adrs/` as the single ledger
* Keep design.md §8 + `docs/research/` and adopt OpenSpec without ADRs
* Run both: ADRs for new decisions, §8 for the existing corpus

## Decision Outcome

Chosen option: **MADR 4.0 ADRs in `docs/adrs/`**, migrated in full, with two
local extensions.

1. **Append-only supersession.** An accepted record's own `status:` is never
   flipped. A superseding record carries `supersedes: NNNN`; the Records index
   surfaces "superseded by NNNN". The file is the historical artefact, the index
   is the current view. Files are never renamed, numbers never reused.
2. **Dated re-evaluation triggers.** `review-by: YYYY-MM-DD` plus a
   `## Re-evaluation triggers` section naming the observable events that reopen
   the decision. This is the one thing §8 did that vanilla MADR has no slot for,
   and it is the reason a plain migration would have been a downgrade.

Evidence stays where it is. `docs/research/YYYY-MM-DD-<topic>.md` dossiers are
not moved or summarised into the ADRs; each ADR links its dossier under *More
Information*. An ADR carries the verdict and its triggers; the dossier carries
the working.

"Run both" is rejected outright — it is the ADR-0050 failure with extra steps.

### Consequences

* Good, because supersession becomes a queryable fact rather than a reading
  comprehension exercise.
* Good, because the OpenSpec `adr` artifact has something real to design within.
* Bad, because `design.md` loses its self-contained narrative — §8 and §9 become
  index sections pointing at `docs/adrs/`. Accepted: a design doc that also
  serves as a mutable decision log is exactly the ambiguity being removed.
* Bad, because every new decision now costs a file rather than a table row.
  Accepted, and the intent — a decision worth keeping is worth a Confirmation
  section.

### Confirmation

`internal/ledger`, which runs inside the existing `go test ./...` step of
`.forgejo/workflows/on_pull_request.yml`. It gates filename and number
discipline, status legality, `supersedes:` integrity, file↔index parity in both
directions, the status and date mirrors, the presence of a `### Confirmation`
subsection in every record, and that a dated `review-by` is accompanied by a
`## Re-evaluation triggers` section.

The gate is Go rather than the estate's usual Python script for one reason: the
Forgejo runner is guaranteed a Go toolchain and is not guaranteed `python3`, and
the workflow already carries two comments about network-fragile setup actions.
One implementation, inside a step that already runs.

The `adr-writer` skill's bundled `validate_adr_consistency.py` must **not** be
used here: it assumes vanilla status-flip supersession and would reject this
corpus wholesale.

## Pros and Cons of the Options

### Keep design.md §8 and adopt OpenSpec without ADRs

* Good, because zero migration cost, and §8's trigger discipline is already
  better than most ADR sets in the wild.
* Bad, because a table row has no room for drivers, options, consequences or a
  confirmation, so the *reasoning* stays in the author's head.
* Bad, because supersession has nowhere to live: §8 rows are edited in place, so
  the history is only in `git log`.
* Bad, because `spec-driven-with-adr` has no ADR set to point at.

### Run both

* Good, because no migration.
* Bad, because it is precisely the two-sanctioned-sources condition ADR-0050 was
  written to eliminate, and nothing would arbitrate.

## More Information

* Migrated from `docs/design.md` §8 (market and protocol verdicts) and §9
  (foundation decisions). Those sections become indexes into `docs/adrs/`.
* Format: [MADR 4.0.0](https://adr.github.io/madr/); authoring via the
  `adr-writer` skill, validation via the local script.
* Prior art in the estate: `webgrip/erfbeeld` `docs/adrs/`,
  `webgrip/homelab-cluster` `docs/techdocs/docs/adr/`.
* Conventions and the index: [README.md](README.md).
