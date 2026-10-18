## ADDED Requirements

### Requirement: The server runs inside the Run and dies with it

The `openhands-agent-server` Harness SHALL start one `openhands-agent-server` process per Run, inside the Run's own workload, listening only on a loopback address, with a session API key generated for that Run. The worker SHALL stop the process when the Run ends, when its context is cancelled because the Lease is lost, and when a limit stops the Run. The Harness SHALL NOT reuse a server across Runs.

#### Scenario: A Run starts and ends normally
- **WHEN** the worker runs the Harness for a claimed Run
- **THEN** it starts a server on a loopback port with a fresh session API key, waits until the server answers its health check, runs one conversation, and stops the server before returning the Outcome

#### Scenario: The worker pod dies mid-conversation
- **WHEN** the pod running the Run is killed while the conversation is running
- **THEN** the server dies with the pod, nothing outside the pod keeps the conversation, and the Lease's TTL releases the Work Item (R2)

#### Scenario: The Lease is lost
- **WHEN** the Run's context is cancelled
- **THEN** the Harness interrupts the conversation, stops the server and returns promptly with the cancellation error

#### Scenario: The server never becomes healthy
- **WHEN** the server does not answer its health check within the start timeout, or exits during start
- **THEN** the Harness returns a `stuck` Outcome whose reason names the start failure and the last lines the server wrote (R4)

### Requirement: One conversation carries the Run's prompt, model and credentials

The Harness SHALL open exactly one conversation per Run, in the Run's repository directory, with Ploeg's composed prompt as the first message, the Run's model and gateway base URL, and the key placeholder the worker hands every Harness. The real gateway key and forge token MUST NOT reach the server, its environment or its conversation.

#### Scenario: The conversation is created
- **WHEN** the server is healthy
- **THEN** the Harness creates the conversation with the Run's model, the gateway base URL from `RunEnv.LLM`, the key placeholder, the repository directory as workspace and the prompt as the initial message, and starts it running

#### Scenario: The server is at capacity
- **WHEN** creating the conversation is refused because the server's run capacity is full
- **THEN** the Harness returns a `stuck` Outcome naming the refusal, since a per-Run server has no other conversation

### Requirement: Server events become transcript entries and activity

The Harness SHALL follow the conversation's event stream and record each agent message, thought, tool call, tool result and plan update as a transcript entry under the transcript's redaction and size bounds. Every event SHALL touch the Run's activity clock. When the stream drops, the Harness SHALL reconnect and catch up from the last event it recorded, without recording an event twice.

#### Scenario: The agent runs a command
- **WHEN** the server emits a tool call and its result
- **THEN** the transcript gains one tool-call entry with the command and its status, and the activity clock moves

#### Scenario: The event stream drops
- **WHEN** the WebSocket closes while the conversation is still running
- **THEN** the Harness reconnects, reads the events after the last one it recorded, and continues

### Requirement: The Run's limits stop the conversation

The Harness SHALL report the prompt start and its limits when the conversation starts running (ADR-0114), using the Harness timeout and the idle timeout configured for the Role. When no event arrives within the idle timeout, or the prompt limit passes, the Harness SHALL interrupt the conversation, stop the server and return a `stuck` Outcome naming the limit.

#### Scenario: The agent goes quiet
- **WHEN** no event arrives for longer than the idle timeout
- **THEN** the Harness interrupts the conversation and returns `stuck` with a reason naming the idle timeout and the last event it saw

#### Scenario: The prompt runs out of time
- **WHEN** the prompt limit passes while the conversation is still running
- **THEN** the Harness interrupts the conversation and returns `stuck` with a reason naming the prompt limit

### Requirement: The Run's permission mode decides confirmations

The Harness SHALL set the conversation's confirmation policy from the Run's permission mode before the first message runs. A confirmation request the policy leaves open SHALL be answered from the Run's permission mode for an unattended Run, and relayed through ploegd for an attended Run, falling back to the Team's policy when no answer arrives (ADR-0100).

#### Scenario: An unattended Run allows every action
- **WHEN** the Run's permission mode allows every action
- **THEN** the conversation runs with a policy that asks for no confirmation

#### Scenario: An attended Run is asked to confirm
- **WHEN** the server asks to confirm an action during an attended Run
- **THEN** the Harness relays the request through ploegd and applies the answer, or the Team's policy when none arrives in time

### Requirement: The outcome comes from the drop-box and the conversation's end

The Harness SHALL read the Outcome from the outcome drop-box (ADR-0018). When the drop-box is empty, it SHALL derive the Outcome from how the conversation ended: a finished conversation with no report is `stuck` with a reason saying no outcome was reported, and an errored conversation is `failed` with the server's error.

#### Scenario: The agent writes an outcome
- **WHEN** the conversation finishes and the drop-box holds a valid OutcomeReport
- **THEN** the Harness returns that report

#### Scenario: The conversation errors
- **WHEN** the conversation ends in an error state with an empty drop-box
- **THEN** the Harness returns `failed` with the server's error as the reason

### Requirement: The Run records the server's version and trace context

The Harness SHALL report the server's name and version through `RunEnv.AgentInfo` (ADR-0097). When the Run has telemetry configured, the Harness SHALL pass the Run's trace context and resource attributes through the server's observability request headers and SHALL NOT set OTLP environment variables on the server process (ADR-0113).

#### Scenario: Telemetry is configured
- **WHEN** a Run with telemetry starts its conversation
- **THEN** the start request carries the Run's trace context in the observability headers and the server's environment carries no `OTEL_` variable

#### Scenario: The server identifies itself
- **WHEN** the server answers its version endpoint
- **THEN** the Run records `openhands-agent-server` and that version as its Harness version
