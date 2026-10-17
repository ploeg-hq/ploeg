# Work Item Notes

## ADDED Requirements

### Requirement: A learning may suggest a Knowledge Scope

`OutcomeReport.learnings[]` MAY carry `scope`, one of `work-item`,
`repository`, `client` or `tenant`; absent means `work-item`. The schema SHALL
refuse any other value. The cap and required fields stay as ADR-0066 sets
them.

#### Scenario: An unknown scope is refused

- **WHEN** a learning carries `scope: everyone`
- **THEN** the OutcomeReport schema refuses it

### Requirement: Every kept learning becomes a Work Item Note without review

ploegd SHALL write each kept learning as a Work Item Note in the Tenant's notes
graph, carrying the Work Item, its root, repository, Client when it has one,
the Run's trace and the time (ADR-0088).

#### Scenario: The next Run of the same work reads the note

- **GIVEN** a Run of Work Item X reported a learning
- **WHEN** the next Run of X, or of a Work Item created under X, fetches its knowledge
- **THEN** the note is among its notes

#### Scenario: Unrelated work does not read the note

- **WHEN** a Run of a Work Item under another root in the same repository fetches its knowledge
- **THEN** the note is absent

### Requirement: Notes are written once across restarts

ploegd SHALL store the outcome and one outbox row per kept learning in one
transaction, and SHALL write pending rows to the notes graph with a merge keyed
on the Run's trace and the learning's index until the write is confirmed by
reading the key back.

#### Scenario: ploegd dies before the graph write

- **WHEN** ploegd stops after storing the outcome and before the note is written
- **THEN** after restart the note is written exactly once

#### Scenario: The graph answer is lost

- **WHEN** the graph committed the note but ploegd lost the response
- **THEN** ploegd reads the key, finds the note and marks the row written without a second note

#### Scenario: The graph stays down

- **WHEN** an outbox row is still unwritten after a day
- **THEN** ploegd reports it as a metric an alert can fire on, and the Run's outcome is unaffected

### Requirement: A Run sends its notes while it works

A Run with a notes graph SHALL be able to send each finding as it makes it,
through the notes file the worker names in `PLOEG_NOTES_FILE`, and ploegd
SHALL store it in the note outbox at once, so it reaches later Runs of the
same root before the Run ends and is kept if the Run is stopped. A line that
carries one of the Run's credentials MUST NOT be sent. Resending a note's key
MUST NOT store it twice.

#### Scenario: A finding reaches the next Run before the first ends

- **GIVEN** a running Run that appended a learning to its notes file
- **WHEN** the next Run of a Work Item under the same root asks for its knowledge
- **THEN** that learning is among its notes, while the first Run still runs

#### Scenario: A Run that is stopped keeps what it sent

- **GIVEN** a Run that sent two notes and was then stopped by its idle watchdog
- **WHEN** its outcome reports no learnings
- **THEN** both notes are in the notes graph

### Requirement: A stopped Run leaves a note

When a Run ends failed or stuck, except for a credential leak, the worker
SHALL send one note of its own, marked as the worker's, with the stop reason,
the plan and how far it got, its last commands and whether it opened a pull
request. No model SHALL write it.

#### Scenario: The next Run starts where the stopped one ended

- **GIVEN** a Run stopped by its idle watchdog with step 2 of 6 in progress
- **WHEN** the next Run of the work is briefed
- **THEN** its notes include the worker's note naming the stop, the plan with step 2 in progress and the last command

### Requirement: A wider scope reaches other work only through an accepted Promotion

A learning with a scope wider than `work-item` SHALL also create a pending
Promotion naming the note and the suggested scope. No Run outside the note's
root SHALL read its content before a Tenant admin accepts the Promotion. A
rejected Promotion SHALL be kept, and an equal suggestion for the same
repository SHALL not create a new pending Promotion.

#### Scenario: A suggested repository convention waits

- **GIVEN** a Run suggested a learning with `scope: repository`
- **WHEN** a Run under another root in that repository fetches its knowledge before acceptance
- **THEN** the learning is absent

#### Scenario: Acceptance publishes it

- **WHEN** a Tenant admin accepts the Promotion, optionally edited or narrowed to another scope
- **THEN** the knowledge graph's `main` holds one concept with that scope, `from_note` and `accepted_by`, and later Runs in that scope read it

### Requirement: Accepting a Promotion leaves no open branch

ploegd SHALL apply an accepted Promotion as the Tenant's review actor by
creating a branch from the knowledge graph's `main`, writing the concept,
merging and deleting the branch, after recording the branch name.

#### Scenario: ploegd dies mid-acceptance

- **WHEN** ploegd stops after the merge and before deleting the branch
- **THEN** on restart it finds the concept on `main`, deletes the branch and records the Promotion accepted, with one concept

### Requirement: An accepted repository Promotion opens a knowledge pull request

Accepting a Promotion with `repository` scope SHALL create one approved Work
Item on that repository whose Run adds the concept as an OKF file under
`knowledge/` and opens a pull request. ploegd SHALL NOT write to the forge
itself. When a pack finds the same concept in the repository's `knowledge/`
and in the graph, it SHALL keep the repository's file.

#### Scenario: Acceptance creates the Work Item once

- **WHEN** a Tenant admin accepts a repository-scoped Promotion, and the accept is repeated after a lost response
- **THEN** exactly one approved Work Item exists for that concept

#### Scenario: The merged file wins

- **GIVEN** the concept's file is on the repository's default branch
- **WHEN** a Run of that repository is briefed
- **THEN** its pack holds the repository's file and not the graph copy

### Requirement: Notes expire with their work

ploegd SHALL delete a Work Item Note 90 days after its root Work Item closes,
unless a Promotion references it.

#### Scenario: A promoted note is kept

- **WHEN** a root closed 91 days ago and one of its notes has an accepted Promotion
- **THEN** that note is kept and the others are deleted
