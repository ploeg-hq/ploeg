## ADDED Requirements

### Requirement: Scoped inference capability

Ploeg SHALL issue inference capability at the Run boundary without supplying management credentials to Executor workloads. Harness environments MUST use an explicit allowlist and MUST exclude operator and administrative capabilities.

#### Scenario: Worker launches a Harness
- **WHEN** a writer or reader Harness starts
- **THEN** its environment contains only its intended inference and repository capabilities and configured non-secret runtime settings

### Requirement: Authorization survives unknown settlement

Consistent with R2 and ADR 0012, worker expiry MUST NOT release a Run's authorized budget while its spend is unknown. Key blocking MUST preserve the metering identity needed for later reconciliation.

#### Scenario: Worker dies before spend report
- **WHEN** a worker expires after inference capability was issued and before settlement
- **THEN** the Shift retains the authorization hold and cannot spend it again as though the expired Run were free
