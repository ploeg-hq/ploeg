## ADDED Requirements

### Requirement: Bind an immutable Delivery Candidate

The Store SHALL admit one immutable Delivery Candidate for a completed, stop-confirmed operator execution at its current generation, bound to its Work Item and registered repository. The candidate SHALL identify the original base commit, canonical commit, tree, artifact digest and configured current policy digest. Changed replays SHALL fail.

#### Scenario: An agent changes its evidence

- **WHEN** a candidate already exists and a request changes its tree or policy
- **THEN** the request fails without replacing the candidate.

### Requirement: Admit independent verification authority

Only a separately configured verification-capable authenticated consumer SHALL submit a Verification Receipt. The server SHALL require exact candidate bindings, its configured verifier identity and policy, a recorded check result. Approval SHALL require successful checks and at least the configured positive test count. Failed Verification Receipts SHALL remain immutable and ineligible for approval. Worker Outcome claims SHALL provide no verification authority.

#### Scenario: An execution consumer self-reports success

- **WHEN** an execution-capable consumer without verification capability submits a passing receipt
- **THEN** admission fails.

### Requirement: Bind approval to reviewed evidence

Approval SHALL reference the exact Delivery Candidate, current policy and admitted Verification Receipt and SHALL record the authenticated acting human. Policy drift or mismatched references SHALL prevent publication.

#### Scenario: The policy changes after review

- **WHEN** publication references a candidate approved under a different configured current policy
- **THEN** no effect authority is granted.

### Requirement: Reserve publication before effects

A Publication Operation SHALL be durably reserved before an external effect. Only the response to its first committed reservation MAY grant effect authority. Identical replay SHALL return the same operation without granting effect authority again; changed operation identity or content SHALL fail. The barrier SHALL prevent generation advancement.

#### Scenario: A process dies after reservation

- **WHEN** the publisher dies after its reservation commits
- **THEN** the operation remains frozen and a replay does not repeat the external write.

### Requirement: Resolve uncertainty only with positive evidence

An uncertain or reserved Publication Operation MAY become published only through a trusted-control report positively identifying its exact branch and canonical commit with a remote identifier and URL. A timeout, negative lookup or expired lease SHALL NOT authorize another effect or remove the barrier. R2 and R17 apply independently of worker cooperation.

#### Scenario: A create response is lost

- **WHEN** a forge request times out and subsequent lookup finds nothing
- **THEN** the operation remains unresolved and no duplicate create is authorized.
