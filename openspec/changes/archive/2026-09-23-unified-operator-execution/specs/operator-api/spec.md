## ADDED Requirements

### Requirement: Scoped operator read model

The Run API SHALL expose authenticated, team-authorized operator reads under a versioned contract. It MUST bound result sizes, preserve unknown values and exclude credentials and raw credential-bearing audit data.

#### Scenario: Read outside allowed team
- **WHEN** a consumer requests a Work Item outside its team scope
- **THEN** the API returns no object or identifying fields for that Work Item

#### Scenario: Reconnect to snapshot pagination
- **WHEN** a client paginates existing audit rows
- **THEN** the API identifies snapshot consistency without claiming committed durable stream ordering

### Requirement: Common execution admission

Governed operator work SHALL be admitted under Ploeg authority with stable Work Item and execution identity. Commands MUST be idempotent for the authenticated actor and MUST reject incompatible payload reuse or stale revisions. A disconnected human MUST NOT cause a second execution.

#### Scenario: Lost start response
- **WHEN** an identical start command is repeated after a lost response
- **THEN** it identifies the existing execution without submitting a second paid turn

### Requirement: Explicit stop and recovery

Consistent with R2, intentional pause or cancellation MUST survive process restart. Uncertain external submission MUST remain visible until reconciled. A stale execution MUST NOT acquire replacement authority while its external side effects remain unresolved.

#### Scenario: Crash after uncertain prompt acceptance
- **WHEN** an executor dies after a provider may have accepted its prompt
- **THEN** recovery retains uncertainty and requires reconciliation or explicit operator action instead of automatic resubmission
