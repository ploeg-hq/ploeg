## ADDED Requirements

### Requirement: A Work Item names an OpenSpec change in its description
A Work Item SHALL name an OpenSpec change with a line of its description that
reads `openspec: <change-id>`, where the key is case-insensitive, the
identifier MAY be wrapped in backticks, and HTML markup from a Tracker Provider
is ignored. A change identifier MUST be kebab-case: lowercase letters, digits
and single hyphens, starting with a letter or digit, at most 100 characters.
The worker MUST treat a Work Item with no such line exactly as before.

#### Scenario: Plain-text directive
- **WHEN** a Work Item description contains the line `openspec: add-widget`
- **THEN** the Run is executed from the OpenSpec change `add-widget`

#### Scenario: Vikunja HTML directive
- **WHEN** a Vikunja description contains `<p>OpenSpec: <code>add-widget</code></p>`
- **THEN** the Run is executed from the OpenSpec change `add-widget`

#### Scenario: Malformed identifier
- **WHEN** a directive line names `../secrets` or any value that is not kebab-case
- **THEN** the Run reports `stuck` with a reason naming the directive, and no harness starts

#### Scenario: Two different changes
- **WHEN** the description names two different change identifiers
- **THEN** the Run reports `stuck` with a reason naming both, and no harness starts

### Requirement: The worker locates the change in the clone
The worker SHALL look for a directory `openspec/changes/<change-id>` at the
repository root and in nested directories, and SHALL use the directory that
contains that `openspec` directory as the OpenSpec root. It MUST NOT follow
symbolic links while looking, and MUST NOT look inside `.git`,
`node_modules`, `vendor` or an `archive` directory. When no change or more
than one is found, the Run SHALL report `stuck` with the reason and no harness
SHALL start.

#### Scenario: Nested application
- **WHEN** the change exists only at `apps/ploeg/openspec/changes/<id>`
- **THEN** the OpenSpec root is `apps/ploeg`

#### Scenario: Missing change
- **WHEN** no `openspec/changes/<id>` directory exists in the checkout
- **THEN** the Run reports `stuck` saying the change was not found

### Requirement: The Role is briefed from the change
The worker SHALL put an OpenSpec brief on the Task Spec's `openSpec` field
(docs/contracts/taskspec.v1.schema.json) and the prompt SHALL present it as the
specification for the Work Item, ranked below the delivery contract. When an
`openspec` executable is on the worker's PATH the brief SHALL be derived from
`openspec instructions apply --change <id> --json` run in the OpenSpec root;
otherwise, or when that command fails, it SHALL be derived from the change's
`proposal.md`, `design.md` and `tasks.md`, and the brief SHALL say which
source it came from. The brief MUST be size-capped and MUST name files by
repository-relative path.

#### Scenario: CLI available
- **WHEN** the image carries `openspec` and the command succeeds
- **THEN** the prompt lists the change's context files, its task progress, its pending tasks and the change's own instruction

#### Scenario: CLI absent
- **WHEN** no `openspec` executable is on PATH
- **THEN** the prompt carries the change's proposal, design and tasks, and says the CLI was unavailable

### Requirement: Strict validation gates the hand-off for review
For a Work Item that names an OpenSpec change, the worker SHALL run
`openspec validate <id> --type change --strict --json --no-interactive` in the
OpenSpec root of the branch's pushed commit after a writing Run that opened or
updated a pull request, and after a reading Run that stood on the branch under
review. The gate passes only when the command exits zero and its JSON reports
the change valid with no failed items.

- A writing Run whose gate fails, or cannot run, SHALL report `stuck` with the
  validation output or the reason, keeping its pull request link, so the Work
  Item does not reach `awaiting_review` (R4).
- A reading Run whose gate fails SHALL return verdict `request_changes` with
  the validation output ahead of its findings, whatever verdict the agent gave.
  A reading Run whose gate cannot run SHALL report `stuck`.
- A passing gate SHALL be recorded in the Run's summary.

#### Scenario: Writer breaks the change
- **WHEN** a writer opens a pull request whose branch fails strict validation
- **THEN** the Run reports `stuck` with the `openspec validate` output and the pull request link

#### Scenario: Reviewer approves an invalid change
- **WHEN** a reading Run returns `approve` and the branch fails strict validation
- **THEN** the reported verdict is `request_changes` and the findings start with the validation output

#### Scenario: Reader before any writer
- **WHEN** a reading Run runs before the branch exists
- **THEN** no gate runs and its verdict is unchanged

### Requirement: The OpenSpec CLI runs without network or credentials
The worker MUST only execute an `openspec` binary already on its PATH and MUST
NOT download one. It SHALL run the CLI with `OPENSPEC_TELEMETRY=0` and
`DO_NOT_TRACK=1`, without the forge token or the model key in its environment,
and with a bounded timeout.

#### Scenario: Sandbox without registry access
- **WHEN** the worker pod reaches only its model gateway and its forge
- **THEN** briefing and validation make no network request

### Requirement: Crash behaviour is unchanged
Briefing and validation SHALL run inside the Run before its Outcome is
reported. A pod that dies during either SHALL be recovered by Lease expiry and
the sweeper exactly as any other Run (R2); the gate SHALL run again on the
retried Run.

#### Scenario: Pod dies during validation
- **WHEN** the worker pod is killed while `openspec validate` runs
- **THEN** no Outcome is reported, the sweeper records the Run as failed on expiry, and a retry validates again
