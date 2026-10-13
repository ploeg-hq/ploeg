## ADDED Requirements

### Requirement: An attended ACP Run asks instead of deciding

In an attended Run whose `autoApprove` is false, the ACP adapter SHALL use the `ask` permission mode: every permission request is relayed with the agent's options, title, kind and an input summary, and the person's selection among the offered options is returned; when the Team's permission mode is `deny_all`, the adapter SHALL refuse without relaying. In an attended Run whose `autoApprove` is true, the adapter SHALL decide every permission request at once by the Team's permission mode and MUST NOT relay it. The storm caps (`pkg/harness/adapters/acp/permission.go`) MUST still apply. Waiting MUST NOT block the protocol read loop or any notification handler.

#### Scenario: Write tool in an attended Run
- **WHEN** an attended Run's agent requests permission for an `edit` tool call
- **THEN** the request is relayed, and the person's chosen option is returned to the agent

#### Scenario: Read tool in an attended Run without auto-approval
- **WHEN** an attended Run with `autoApprove` false requests permission for a `read` tool call under the Team mode `allow_read_only`
- **THEN** the request is relayed, and on its deadline the Team's mode allows it

#### Scenario: Auto-approved attended Run
- **WHEN** an attended Run with `autoApprove` true requests permission for an `edit` tool call
- **THEN** the adapter answers from the Team's permission mode at once, nothing is relayed, and a later elicitation request from the same agent is still relayed

#### Scenario: Storm while relaying
- **WHEN** an attended Run's agent exceeds the permission storm cap
- **THEN** the adapter cancels the session as today, regardless of open requests

### Requirement: Questions are relayed in attended Runs only

In an attended Run, the ACP adapter SHALL accept the agent's elicitation requests, relay them as questions and return the answer. In an unattended Run it MUST refuse them as today.

#### Scenario: Agent asks a question in an unattended Run
- **WHEN** an unattended Run's agent sends an elicitation request
- **THEN** the adapter refuses it exactly as before this change

### Requirement: Messages reach the agent at a turn boundary

The ACP adapter SHALL deliver a message as a new ACP prompt turn when the current prompt turn ends, and SHALL then wait for that turn's stop reason before finishing. A message marked as an interruption SHALL cancel the current turn first. The adapter SHALL acknowledge each message once it has sent it.

#### Scenario: Message during a turn
- **WHEN** a person sends a message while the agent is mid-turn
- **THEN** the agent receives it as the next prompt after its current turn ends, and the Run finishes after that turn

### Requirement: Pause cancels the turn and keeps the session

On pause, the ACP adapter SHALL cancel the current prompt turn and report the stop confirmed once the agent returns its stop reason. On resume, it SHALL start a new prompt turn in the same session. While a request is open or the Run is paused, the adapter's idle watchdog and prompt wall MUST NOT count; the worker's harness timeout MUST keep counting.

#### Scenario: Resume after pause
- **WHEN** a paused attended ACP Run is resumed
- **THEN** the agent receives a new prompt turn in the same session and the idle watchdog restarts

### Requirement: Harnesses without interaction degrade visibly

Harnesses other than `acp` SHALL report that they do not support interaction. For an attended Run on such a Harness, ploegd SHALL refuse `answer`, `pause` and `resume` with a conflict naming the reason, and SHALL accept `message` as an operator note for the next Run.

#### Scenario: Pause on a native OpenHands Run
- **WHEN** a consumer pauses an attended Run executing the native `openhands` Harness
- **THEN** ploegd refuses with reason `not_interactive` and the Run continues
