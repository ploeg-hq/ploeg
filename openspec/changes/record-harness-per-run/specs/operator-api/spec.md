## MODIFIED Requirements

### Requirement: Scoped operator read model

The Run API SHALL expose authenticated, team-authorized operator reads under a versioned contract. It MUST bound result sizes, preserve unknown values and exclude credentials and raw credential-bearing audit data. The Teams read SHALL additionally describe, for each Role of a Team in the consumer's scope, the Harness and agent profile the Role is configured with, the Role's default model from its managed inference policy, and the Harness version most recently recorded on a Run of that Role, which MUST be labelled as observed with the time it was recorded and the Harness name and profile of the Run that recorded it, so a consumer can tell when it belongs to a different Harness than the configured one. Each of these MUST be null when ploegd does not know it, and MUST NOT be derived from a worker's environment that ploegd cannot read.

#### Scenario: Read outside allowed team
- **WHEN** a consumer requests a Work Item outside its team scope
- **THEN** the API returns no object or identifying fields for that Work Item

#### Scenario: Reconnect to snapshot pagination
- **WHEN** a client paginates existing audit rows
- **THEN** the API identifies snapshot consistency without claiming committed durable stream ordering

#### Scenario: Role with configured Harness and observed version
- **WHEN** a consumer reads the Teams of a deployment whose chart configures a Role with the `acp` Harness and the `opencode` profile, and a Run of that Role recorded version `1.18.30`
- **THEN** the Role reads `acp`, `opencode`, its policy's model, and `1.18.30` as the observed version with its recording time and `acp`/`opencode` as the Harness that recorded it

#### Scenario: Role with no recorded Run
- **WHEN** a Role has a configured Harness and no Run of that Role recorded a version
- **THEN** the Role's configured Harness is present and its observed version is null

#### Scenario: Deployment that does not pass Harness configuration to ploegd
- **WHEN** ploegd runs without `PLOEG_TEAM_HARNESSES`
- **THEN** every Role's configured Harness and profile read as null and the Teams read is otherwise unchanged
