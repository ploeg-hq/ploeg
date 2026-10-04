# Run learnings

## ADDED Requirements

### Requirement: A Run may propose learnings

A Run MAY return up to 10 `learnings` in its OutcomeReport, each with a type,
title and body. The worker SHALL drop entries without them and SHALL keep the
first 10 (ADR-0066).

#### Scenario: Learnings are capped

- **WHEN** a Run reports 15 learnings
- **THEN** 10 are kept

#### Scenario: An incomplete learning is dropped

- **WHEN** a learning has no body
- **THEN** it is not kept and the schema refuses it

### Requirement: A learning is a proposal

The worker SHALL write each kept learning as an OKF concept marked
`status: proposed` with the Run's trace and the Work Item's reference, and no
pack SHALL include a proposed concept.

#### Scenario: A learning names its source

- **WHEN** a Run's learning is kept
- **THEN** its concept carries `proposed-by` with the Run's trace and `work-item` with the Work Item's reference

#### Scenario: The outcome keeps the agent's learnings

- **WHEN** the worker resolves a Run's outcome from the forge
- **THEN** the learnings the agent reported are still on the report
