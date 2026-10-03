## ADDED Requirements

### Requirement: Canonical source lookup

The operator API SHALL resolve only existing Work Items authorized by the consumer's Team scope. It MUST compare the configured singleton Tracker Provider API root, Scope, native revision and explicit open state against a fresh provider read. The current configured target resolver and container Team pin MUST also agree with the mirrored expectations for lookup and new admission. The additive contract is published in `docs/contracts/tracker-execution.v1.schema.json`. No browser hash SHALL substitute for the native revision.

#### Scenario: Source changed or unsupported
- **WHEN** a fresh provider read is unavailable, closed, outside the configured singleton or Scope, or differs from the mirrored revision
- **THEN** lookup and new admission fail without creating work, reserving budget or issuing credentials

#### Scenario: Routing changed before the mirror refreshes
- **WHEN** the current configured route no longer matches the mirrored Work Target, its Team pin changes, or routing is unavailable
- **THEN** lookup and new admission return a conflict even if the native revision and stored row are unchanged
- **AND** identical replay of a previously accepted admission still returns its original execution

### Requirement: Exclusive pristine adoption

Admission SHALL retain the Work Item's identity and origin. Only queued Work Items without started or finished Runs, Checkpoints, Leases, spend or unresolved accounting MAY be adopted. Entirely pending unstarted Runs MAY be retired in the same transaction before creating the operator Shift, Run and Lease. Current row timestamp, revision, Team and exact Work Target MUST match the accepted source snapshot.

#### Scenario: Claim races admission
- **WHEN** an unattended claimant and operator admission compete for the same Work Item
- **THEN** at most one acquires execution authority and the loser receives a bounded conflict or empty claim

#### Scenario: Lost admission response
- **WHEN** the same authenticated consumer, actor, session and admission payload are replayed
- **THEN** the original execution is returned without a second Work Item, Run, reservation or credential

### Requirement: Durable operator ownership

Tracker refreshes SHALL retain mirrored content while preserving the operator-owned dispatch Team, Work Target, state and retry eligibility. Unattended claims, Shift opening and evaluation MUST exclude operator-owned work. Completion, pause, cancellation, expiry and service restart SHALL NOT relinquish this ownership implicitly.

#### Scenario: Webhook replay after cancellation
- **WHEN** a tracker assignment is redelivered after operator cancellation
- **THEN** the canonical Work Item remains outside unattended dispatch and no replacement Run is created

#### Scenario: Process dies during adoption
- **WHEN** the transaction does not commit
- **THEN** the prior pending work remains intact and no operator authority exists
- **WHEN** it commits but the response is lost
- **THEN** the durable binding excludes unattended dispatch and session replay recovers the existing execution
