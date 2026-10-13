## ADDED Requirements

### Requirement: A Run records the Harness version it ran with

Each Run SHALL record, in addition to the facts ADR-0091 records, the version of the Harness it ran with and, when the agent identifies itself, the agent's own name and version. Each fact MUST be written at most once: a value already recorded is kept, and an empty or malformed value leaves the fact unknown (null). Runs recorded before this change, and Runs whose worker does not report these facts, MUST read as unknown, never as a value derived from current configuration.

#### Scenario: Version known before the Harness starts
- **WHEN** a worker claims a Run and names a Harness version in its claim request
- **THEN** ploegd records that version on the claimed Run

#### Scenario: Agent identifies itself during the Run
- **WHEN** an ACP agent answers `initialize` with `agentInfo` and the worker reports it in the Outcome Report's `harness` object
- **THEN** ploegd records the agent's name and version on the Run if they are not already recorded

#### Scenario: A recorded value is never overwritten
- **WHEN** an Outcome Report names a Harness version different from the one recorded at claim
- **THEN** the recorded version is kept and the difference is logged without the report being refused

#### Scenario: Older worker
- **WHEN** a worker that predates this change claims and reports
- **THEN** the claim and the Outcome Report are accepted unchanged and the new facts read as null

### Requirement: Recording the Harness never decides a Run

Recording the Harness facts MUST NOT refuse, delay or change a claim or an Outcome. A failure to record SHALL be logged and SHALL leave the facts unknown. Consistent with R2, when the pod dies after its claim and before its Outcome Report, the Run SHALL keep whatever was recorded at claim, and expiry and recovery of the Run (and of a writing Run's Lease) SHALL proceed exactly as without this record.

#### Scenario: Pod dies before reporting
- **WHEN** a worker claimed with a Harness version and its pod is killed before it reports an Outcome
- **THEN** the Run is recovered by expiry as today and still reads the version recorded at claim, with the agent name and version unknown

#### Scenario: Store write fails
- **WHEN** writing the Harness facts fails during an Outcome report
- **THEN** the Outcome is stored as it would be without the facts and the failure is logged

### Requirement: Only the worker fills the Outcome Report's harness object

The Outcome Report's optional `harness` object (`docs/contracts/outcomereport.v1.schema.json`) SHALL be set by the worker from its own configuration, its version probe and the adapter's observation of the agent. The worker MUST discard any `harness` value a Harness wrote into its outcome drop box (ADR-0018). The Go type and the schema MUST change together and additively.

#### Scenario: Agent writes a harness object into the drop box
- **WHEN** the outcome file an agent wrote contains a `harness` object
- **THEN** the worker replaces it with its own before reporting

### Requirement: Each Run read carries the Harness version

The operator API's `run` and `runListItem` (`docs/contracts/operator-api.v1.schema.json`) SHALL carry `harnessVersion` and `harnessAgent`, each present and null when unknown, alongside the fields ADR-0091 added.

#### Scenario: Run recorded before this change
- **WHEN** a consumer reads a Run recorded before this change
- **THEN** `harnessVersion` and `harnessAgent` are present and null
