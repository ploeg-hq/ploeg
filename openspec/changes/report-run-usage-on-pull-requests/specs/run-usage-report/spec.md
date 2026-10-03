# Run usage report

## ADDED Requirements

### Requirement: Every agent pull request carries one up-to-date usage and evidence report

Ploeg SHALL publish, on each pull request a writing Run opens or updates, exactly
one report comment identified by the marker `<!-- ploeg:usage-report -->`, and
SHALL update that comment in place rather than post another whenever the figures
change. The report SHALL render only state Ploeg already records: for each Run
its Role, Round, whether it wrote, its Outcome and verdict, the models the
gateway settled it against, its input and output tokens, its cost and its
duration; and in total the Shift's authorized, reserved, settled and remaining
pool. It SHALL name the writing Run's trace alias `ploeg-<12hex>` so a dashboard
can join on it. It SHALL state the evidence Ploeg observed for the writing Run —
its verification result and the commit — rather than copy the full output. When
Ploeg did not verify that Run — the operator configured no checks, the Run did
not write, or the Shift predates worker verification — the report SHALL state
that the verification was not recorded, and SHALL NOT render a blank or a
result that could read as a pass.

The report's links SHALL be rendered from configuration, never hard-coded hosts:
the Unfold — Loop dashboard filtered by the Team, the Run Explorer per Run
filtered by the trace alias, Spend & Attribution, and the Work Item's page in
Vloer. When the
base URL for a link is unset the link SHALL be omitted, and when every base URL
is unset the links section SHALL be omitted and the rest of the report SHALL
still render. A Run whose gateway account is not reconciled SHALL have its cost
and tokens marked provisional; a Run whose spend could not be read SHALL have
them marked unavailable, never guessed from the authorization. The report MUST
NOT call the model gateway, MUST NOT create, settle or block an account, and
MUST NOT print a key value.

Publishing the report is best-effort and outside the lifecycle (R2, R3): a
failed accounting read or forge call SHALL be logged and skipped, and SHALL NOT
change an Outcome, a close reason or a Work Item state. A pod that dies while
publishing SHALL leave the Shift exactly as any other publish failure does; the
next refresh reconciles the comment.

#### Scenario: A writing Run opens a pull request

- **WHEN** a writing Run reports `pr_opened` with a pull request link
- **THEN** the pull request carries a comment marked `<!-- ploeg:usage-report -->`
- **AND** it names each Run so far with its Role, Round, Outcome and verdict

#### Scenario: Usage is reported per Run and in total

- **GIVEN** a Shift with a settled writing Run and a reading Run
- **WHEN** the report is published
- **THEN** each Run's models, input and output tokens, cost and duration are listed
- **AND** the total states authorized, reserved, settled and remaining pool
- **AND** the writing Run's trace alias is shown so a dashboard can join on it

#### Scenario: A later Round or a fix round updates the same comment

- **GIVEN** a pull request that already carries the report comment
- **WHEN** another Round completes and the report is refreshed
- **THEN** the existing comment is edited in place
- **AND** the pull request still carries exactly one report comment

#### Scenario: Spend logs cannot be read

- **GIVEN** an account whose gateway spend the settlement sweep could not read
- **WHEN** the report renders that Run
- **THEN** its cost and tokens are marked unavailable or provisional, not guessed
- **AND** the Run's Outcome, the Shift's state and the account's hold are unchanged
- **AND** the pull request still receives the report

#### Scenario: Settlement reconciles the account

- **GIVEN** a report comment carrying provisional cost for an unsettled Run
- **WHEN** the settlement sweep reconciles that account
- **THEN** the same comment is edited to show the settled figures

#### Scenario: No pull request yet

- **WHEN** a Shift has no pull request to publish to
- **THEN** no comment is posted and nothing else about the Shift changes

#### Scenario: The forge or the database is unreachable

- **GIVEN** a forge call or accounting read that fails
- **WHEN** the report is published
- **THEN** the failure is logged and skipped
- **AND** no Outcome, close reason or Work Item state changes

#### Scenario: A human reads the evidence

- **WHEN** a person opens the pull request
- **THEN** the report states the writing Run's verification result and commit
- **AND** it points to the writing Run's findings comment for the detailed verification
- **AND** it prints no key or token value

#### Scenario: Verification was not recorded

- **GIVEN** a writing Run whose stored report has no verification section
- **WHEN** the report is published
- **THEN** the evidence section says the verification was not recorded
- **AND** it states no commit and no pass

#### Scenario: Pod dies mid-publish

- **WHEN** the publisher pod is killed while the report is being written
- **THEN** no partial state is recorded in Ploeg
- **AND** the next refresh publishes or edits the one comment as usual
