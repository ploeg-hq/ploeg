# Blackboard

## ADDED Requirements

### Requirement: Findings travel by OutcomeReport, never by agent-side tooling

A reading Run SHALL return its findings in the existing `OutcomeReport`. Ploeg
SHALL publish them to the pull request and inject earlier Rounds' findings into
the next Round's prompt. No agent SHALL gain a new client, tool or credential
to participate (R6 — durable state lives in Postgres and git/forge state,
never inside an agent process).

#### Scenario: A security specialist's finding reaches the writer

- **GIVEN** a reading Run that reported findings in Round 1
- **WHEN** the writing Run of Round 2 is composed
- **THEN** its prompt contains those findings attributed to their Role
- **AND** the reading agent never called a forge or Ploeg API itself

#### Scenario: Findings survive the pod

- **GIVEN** a reading Run that reported and exited
- **WHEN** its pod is gone
- **THEN** the findings are still readable on the PR and in `agent_runs`

#### Scenario: A human sees the same thread

- **WHEN** a person opens the PR
- **THEN** each Role's findings are visible as comments, without a Ploeg UI

### Requirement: Readers cannot write the branch they review

A reading Run SHALL receive a read-only forge credential or none at all
(ADR-0013 tier 1). The writer/reader split SHALL be enforced by credentials as
well as by scheduling.

#### Scenario: A reviewer pod cannot push

- **GIVEN** a reading Run on a Shift with an open PR
- **WHEN** it attempts to push to the Shift's branch
- **THEN** the forge rejects it, independently of Ploeg's scheduling

#### Scenario: The writer can push

- **GIVEN** a writing Run holding the Lease
- **THEN** it can push to the Shift's branch and open or update the PR

### Requirement: A person is asked to merge

When a Shift closes without a further Round, Ploeg SHALL write back to the
tracker — a comment carrying the PR link and a status change — so the human
handoff is an event rather than something noticed later.

#### Scenario: The plan completes and the human is notified

- **WHEN** the final Round completes and the Shift closes
- **THEN** the Work Item is `needs_human`
- **AND** the tracker item carries a comment linking the PR

#### Scenario: Write-back failure does not lose the outcome

- **GIVEN** the tracker is unreachable
- **WHEN** the Shift closes
- **THEN** the Work Item state and audit rows are still correct
- **AND** the failed write-back is logged, not swallowed silently
