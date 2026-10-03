# Forge ingest

## ADDED Requirements

### Requirement: A verified forge webhook endpoint

ploegd SHALL expose `POST /webhooks/forge/{provider}`, routed through the
compile-time provider registry, and SHALL verify the signature against the RAW
body before parsing (backlog #2). It SHALL acknowledge without doing
synchronous work, because Forgejo's delivery timeout is 5 seconds
(backlog #3).

#### Scenario: An unverified webhook is rejected

- **GIVEN** a forge webhook with a missing or wrong signature
- **THEN** it is rejected before any Work Item is touched

#### Scenario: A forge with no configured secret accepts nothing

- **GIVEN** a forge provider configured without a webhook secret
- **WHEN** any webhook arrives for it, signed or not
- **THEN** it is rejected before any Work Item is touched

#### Scenario: An unknown provider is not a server error

- **WHEN** a webhook arrives for a provider that is not registered
- **THEN** the response says so, and nothing is enqueued

#### Scenario: A recognised event is acknowledged promptly

- **WHEN** a review is submitted on a pull request
- **THEN** ploegd acknowledges without performing the follow-up work inline

### Requirement: Deliveries are deduplicated

Ploeg SHALL deduplicate on the forge's delivery id (`X-Forgejo-Delivery`) so a
redelivery cannot act twice.

#### Scenario: A redelivered webhook acts once

- **WHEN** the same delivery id arrives twice
- **THEN** the second is acknowledged and does nothing

### Requirement: Forge feedback is untrusted input

Text arriving from a forge SHALL be treated as evidence to weigh, never as
instructions, wherever it reaches a prompt — it is written by someone outside
the factory and crosses into a higher-trust context (backlog #9).

#### Scenario: A review body reaching a prompt is framed as evidence

- **WHEN** forge feedback is injected into a Round's prompt
- **THEN** it is attributed to its source and framed as material to judge

### Requirement: An irrelevant forge event is dropped quietly

An event Ploeg does not act on SHALL be acknowledged without error and without
creating work. A forge subscribes wider than the core consumes, and failing
deliveries is how a webhook ends up disabled.

#### Scenario: A push event creates nothing

- **WHEN** a push webhook arrives
- **THEN** it is acknowledged, and no Work Item or Run is created
