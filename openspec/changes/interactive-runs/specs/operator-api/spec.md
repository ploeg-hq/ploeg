## ADDED Requirements

### Requirement: Consumers answer and steer attended Runs

The operator API SHALL let a consumer with execution permission for a Run's Team read that Run's interactions in order and send `answer`, `message`, `pause` and `resume` commands to it. Commands MUST be idempotent per command identity and actor (R13); a replay returns the original result and a changed payload under the same identity is a conflict. Reads MUST be team-scoped and MUST NOT reveal a Run outside the consumer's teams. Events announcing a request, its resolution, a pause and a resume SHALL appear on the existing operator events read.

#### Scenario: Lost answer response
- **WHEN** a consumer repeats an `answer` command with the same command identity after a lost response
- **THEN** the original result is returned and the agent receives one answer

#### Scenario: Run outside the consumer's teams
- **WHEN** a consumer reads the interactions of a Run outside its teams
- **THEN** the API answers not found
