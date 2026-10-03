# Forgejo forge provider

## ADDED Requirements

### Requirement: The first ForgeProvider implementation

`provider.ForgeProvider` has been declared since the SPI was carved and has
zero implementations. This change SHALL add a Forgejo one covering `Name`,
`Comment` and `ParseWebhook`, giving the interface its first caller (R7 —
vendor specifics live behind the SPI, never in core semantics).

#### Scenario: A comment lands on the PR

- **WHEN** Ploeg publishes findings for a PR
- **THEN** they appear as a comment on that PR

#### Scenario: Core code never speaks Forgejo REST

- **WHEN** ploegd publishes findings or parses a forge webhook
- **THEN** it does so through the SPI
- **AND** no Forgejo-specific field name appears outside the provider package

#### Scenario: An unverified webhook is rejected

- **GIVEN** a forge webhook with a missing or wrong signature
- **THEN** it is rejected before any Work Item is touched

### Requirement: Forge events parse into normalized ForgeEvents

`ParseWebhook` SHALL emit `review_submitted`, `check_failed` and
`merge_state_dirty` with repo, PR, branch and body, so R9 Follow-Ups become
possible without further provider work.

#### Scenario: A submitted review is normalized

- **WHEN** a reviewer submits a review on Forgejo
- **THEN** a `review_submitted` ForgeEvent carries the repo, PR number, branch
  and feedback body

#### Scenario: An irrelevant event is dropped quietly

- **WHEN** a forge event Ploeg does not act on arrives
- **THEN** it is dropped without error and without creating work
