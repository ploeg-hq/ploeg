## ADDED Requirements

### Requirement: A Team governs which Harnesses its Runs may use

Each Team SHALL have an allowlist of Harnesses, each named by Harness and, for `acp`, agent profile, and each Role SHALL have a default Harness from that allowlist. A Role MAY narrow the Team's allowlist. When a deployment configures no allowlist, the allowlist SHALL be the Role's single configured Harness, so behaviour is unchanged.

#### Scenario: Deployment without an allowlist
- **WHEN** a Team's chart values name one Harness and no allowlist
- **THEN** that Harness is the Role's default and the only one admission accepts for it

### Requirement: An admission may request a Harness from the allowlist

A submission through the operator work-item route that `ploeg-worker` executes (`POST /api/v1/operator/work-items`, added by the `submit-work-items` change) MAY name a Harness; the Operator Execution admission (`POST /api/v1/operator/executions`) SHALL NOT accept one. ploegd MUST refuse the submission with a client error, and create no Work Item, Shift or Run, when the named Harness is not in the Team's allowlist, when it is unqualified, is not the Role's default and the Team has not opted into unqualified Harnesses, or when the Team has no plan and the Harness is not its configured one. A Role's default Harness SHALL always be admissible. Without a named Harness, each Run SHALL use its Role's default. The request's Harness SHALL be part of the admission's idempotency fingerprint (R13), so a replay with a different Harness is a conflict, not a second admission.

#### Scenario: Harness outside the allowlist
- **WHEN** a consumer admits work naming `acp/goose` for a Team whose allowlist is `openhands` and `acp/opencode`
- **THEN** ploegd answers with a client error naming the allowed Harnesses and records nothing

#### Scenario: Unqualified Harness without the opt-in
- **WHEN** a consumer submits work naming `acp/qwen-code`, which is in the Team's allowlist, unqualified and not a default, and the Team has not set `allowUnqualifiedHarnesses`
- **THEN** ploegd answers with a client error naming the reason and records nothing

#### Scenario: Replay with another Harness
- **WHEN** a consumer repeats an admission's request identity with a different Harness
- **THEN** ploegd answers with a conflict and the first admission is unchanged

### Requirement: The resolved Harness is fixed on each Run when the Run is created

When a Shift creates a pending Run, ploegd SHALL store the Harness that Run must use: the admission's Harness when the Run's Role allows it, otherwise the Role's default. A retry, a re-opened Round and a later Round of the same Shift SHALL resolve the same way from the same admission. A Run created without ploegd knowing any Harness configuration SHALL store none.

#### Scenario: Reviewer Role that does not allow the requested Harness
- **WHEN** a session requests `acp/opencode` and the plan's reviewer Role allows only `openhands`
- **THEN** the builder Run stores `acp/opencode`, the reviewer Run stores `openhands`, and the admission response lists both

#### Scenario: Retry after a failed writing Run
- **WHEN** a writing Run that stored `acp/opencode` fails and its Round re-opens (ADR-0019)
- **THEN** the new pending Run stores `acp/opencode`

### Requirement: The Teams read shows what a person can choose

The Teams read SHALL list, per Role, the allowlist, the default, each Harness's qualification status and, for each allowlisted Harness, the newest version a Run of that Role recorded with that Harness (null when none did), in addition to the fields `record-harness-per-run` adds.

#### Scenario: Unqualified profile
- **WHEN** a Team's allowlist contains `acp/qwen-code`
- **THEN** the Teams read lists it with qualification `unqualified`

### Requirement: Runs a workload can no longer claim are visible

When a pending Run stores a Harness that the Team's current allowlist for its Role no longer contains, ploegd SHALL show that Run's count per stored Harness on the Teams read and SHALL raise an alert. It MUST NOT change the stored Harness or cancel the Run on its own; a person decides, for example by cancelling or requeueing the Work Item.

#### Scenario: Allowlist entry removed while Runs are pending
- **WHEN** a Team removes `acp/opencode` from its builder allowlist while one builder Run storing `acp/opencode` is pending
- **THEN** the Teams read shows one pending Run for `acp/opencode` outside the allowlist, the alert fires, and the Run keeps its stored Harness
