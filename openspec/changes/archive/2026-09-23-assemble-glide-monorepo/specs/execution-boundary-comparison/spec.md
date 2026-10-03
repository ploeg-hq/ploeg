## ADDED Requirements

### Requirement: Compare explicit execution authority

The comparison SHALL execute a deterministic standalone task without Ploeg and a Ploeg-admitted task through actual application code. It SHALL record evidence against the existing contracts without inventing a replacement wire format.

#### Scenario: Standalone execution

- **WHEN** Ploeg configuration is absent
- **THEN** standalone work completes with real checks and retained evidence without contacting Ploeg or a paid model

#### Scenario: Managed interruption

- **WHEN** a managed execution is paused, cancelled, disconnected or restarted
- **THEN** its configured authority and durable stop intent remain intact without an automatic standalone fallback or replacement execution

### Requirement: Extraction follows evidence

A shared runner implementation SHALL be extracted only when the comparison establishes a concrete common responsibility and verifies both consumers. Retaining separate engines is a valid result.

#### Scenario: Different lifecycle ownership

- **WHEN** the comparison finds that moving execution also moves scheduling or session policy
- **THEN** the decision records that coupling and keeps those responsibilities with their current application
