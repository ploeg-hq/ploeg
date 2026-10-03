---
# Copy this file to NNNN-kebab-title.md. Do not rename a record once merged.
status: proposed # proposed | accepted | rejected | deprecated — NEVER "superseded by N"
date: YYYY-MM-DD # last substantive change; must match the Records row
decision-makers: Ryan Grippeling
supersedes: none # or the zero-padded number of the record this replaces
review-by: none # or YYYY-MM-DD — a date REQUIRES a "Re-evaluation triggers" section
---

# Short present-tense title naming the decision, not the topic

<!--
  Superseding an accepted record: write a NEW record with `supersedes: NNNN`
  and update the old record's Records row to "superseded by NNNN". Never edit
  the superseded file, and never flip its own `status:` — the file is the
  historical artefact, the index is the current view.
-->

## Context and Problem Statement

Two or three sentences: what forced the decision, what breaks if it is not
made. State the question you are answering.

## Decision Drivers

* the property that actually decided it
* the constraint that ruled options out
* …

## Considered Options

* Option A
* Option B
* …

## Decision Outcome

Chosen option: "Option A", because …

### Consequences

* Good, because …
* Bad, because … — accepted cost, and why the alternative is worse

### Confirmation

**Required.** Name how compliance is actually checked: a CI gate (with the
workflow and command), a review step, or a script. "Reviewers will notice" is
not a confirmation. The presence of this section is enforced by
`go test ./internal/ledger/`.

## Pros and Cons of the Options

### Option B

* Good, because …
* Bad, because …

## Re-evaluation triggers

**Required when `review-by` carries a date.** List the concrete, observable
events that reopen this decision — a dependency shipping a feature, a spec
reaching a version, a project's governance changing. Prefer facts someone
could check in five minutes over judgements someone has to form.

* …

## More Information

* Evidence trail: `docs/research/YYYY-MM-DD-<topic>.md`
* Related ADRs, backlog items, domain rules cited by id
