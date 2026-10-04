# Knowledge pack

## ADDED Requirements

### Requirement: A Run is briefed with the knowledge that bears on its Work Item

The worker SHALL select OKF concepts from the repository's knowledge directory,
every configured knowledge directory and the Work Item's context bundles,
SHALL write the selection as an OKF directory outside the clone, and SHALL put
the selection's index in the prompt before the delivery contract (ADR-0065).

#### Scenario: Relevant knowledge is selected

- **GIVEN** a repository whose `knowledge/` holds a convention about store migrations
- **WHEN** a Run is claimed for a Work Item about adding a store migration
- **THEN** the convention is in the pack and its index entry says which terms matched

#### Scenario: Linked knowledge follows a match

- **GIVEN** a matching concept that links to another concept in the same bundle
- **WHEN** the pack is selected
- **THEN** the linked concept is included with the reason "linked from" the matching one

#### Scenario: Unrelated work gets no knowledge section

- **WHEN** no concept matches the Work Item
- **THEN** the prompt has no knowledge section and the TaskSpec has no `knowledge`

#### Scenario: The pack cannot be committed

- **WHEN** a writing Run is briefed with a pack
- **THEN** the pack's directory is outside the clone

### Requirement: A pack fits its budget

The worker SHALL keep the best concepts that fit the configured byte budget and
SHALL count the matching concepts it left out.

#### Scenario: A small budget keeps the best concept

- **GIVEN** a budget that fits one matching concept of two
- **WHEN** the pack is selected
- **THEN** the better-scoring concept is kept and one is counted as omitted

### Requirement: A Run records the knowledge it was given

`TaskSpec.knowledge` SHALL carry the pack's index, its concept paths and a
content digest per source.

#### Scenario: The same knowledge has the same digest

- **WHEN** a bundle's concepts are read in a different order
- **THEN** its digest is unchanged, and any edit to a concept changes it

### Requirement: Knowledge round-trips through Omnigraph

`ToOmnigraph` and `FromOmnigraph` SHALL convert a bundle to the memory schema
and back without changing its digest.

#### Scenario: A bundle survives the round trip

- **WHEN** a bundle is loaded into Omnigraph and read back with the export query
- **THEN** the rebuilt bundle has the same digest
