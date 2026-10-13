## ADDED Requirements

### Requirement: A Run is attended only by admission

A Run SHALL be attended only when an authenticated submission through `POST /api/v1/operator/work-items` (added by the `submit-work-items` change) marked its Work Item attended. Every Run the Work Item's Shifts create, including retries, re-opened Rounds and Runs after a requeue, SHALL inherit the flag. The Operator Execution admission MUST NOT accept the flag, and tracker-originated Runs MUST NOT be attended. A Run that is not attended SHALL behave exactly as before this change.

The same submission MAY set `autoApprove`; ploegd MUST refuse `autoApprove: true` without `attended`. It SHALL be stored on the Work Item and copied to every Run as `attended` is, and SHALL be part of the submission's idempotency fingerprint (R13). An attended Run with `autoApprove` true SHALL NOT relay permission requests, which its Team's permission mode decides at once; its questions, messages, pause and resume SHALL work as for any attended Run. An attended Run with `autoApprove` false SHALL relay every permission request its Team's mode does not refuse outright.

#### Scenario: Retry of an attended Run
- **WHEN** an attended writing Run fails and its Round re-opens
- **THEN** the new pending Run is attended

#### Scenario: Requeue keeps auto-approval
- **WHEN** an operator requeues a Work Item submitted attended with `autoApprove` true
- **THEN** the new Runs are attended with `autoApprove` true

#### Scenario: Auto-approval without attended
- **WHEN** a consumer submits `autoApprove: true` without `attended`
- **THEN** ploegd answers with a client error and records nothing

#### Scenario: Tracker Run asks for a permission
- **WHEN** an unattended Run's ACP agent requests a permission
- **THEN** the adapter answers from the Team's permission mode as today and nothing is relayed

### Requirement: The worker relays requests and pulls everything else

For an attended Run, the worker SHALL post each permission request it relays and each question to ploegd under the Run's control capability, and SHALL learn answers, messages, pause, resume and cancellation only by polling the Run's inbox. ploegd MUST NOT open a connection to a worker. Requests and inbox entries MUST be stored in PostgreSQL before they are acknowledged (R6), redacted with the transcript redaction of ADR-0090 and bounded in size.

#### Scenario: ploegd restarts while a request is open
- **WHEN** ploegd restarts after storing a request and before anyone answers
- **THEN** the request is still open afterwards, the worker's long-poll reconnects, and an answer given after the restart reaches the worker

### Requirement: Every wait has a deadline

Each request SHALL carry a deadline from the Team's answer timeout. When it passes unanswered, a permission request SHALL resolve to the decision the Team's permission mode makes, and a question SHALL resolve as refused, both recorded as `expired` with no actor. The first valid answer SHALL win and later answers MUST be refused with a conflict.

#### Scenario: Nobody answers a permission request
- **WHEN** an attended Run's permission request passes its deadline
- **THEN** the worker applies the Team's permission mode, the request reads `expired`, and the Run continues

#### Scenario: Two people answer
- **WHEN** two answers arrive for one request
- **THEN** the first is applied and the second receives a conflict naming the first

### Requirement: Crash-safety never waits for a person

Consistent with R2, the worker of a Run waiting on a person SHALL keep renewing the Run, and a writing Run's Lease, and expiry SHALL remain the only recovery mechanism. When the pod dies, ploegd SHALL mark the Run's open requests `expired`, refuse later answers with a conflict, and recover the Run by its lane's rules. A Run that was paused when its pod died MUST NOT be retried automatically (R3, R14); its Work Item SHALL go to `needs_human` with a reason (R4).

#### Scenario: Pod dies while waiting for an answer
- **WHEN** the worker pod is killed while a permission request is open
- **THEN** the Run expires, the request reads `expired`, an answer sent afterwards is refused, and the Run is recovered as an unanswered Run would be without this change

#### Scenario: Pod dies while paused
- **WHEN** a paused attended Run's pod is killed
- **THEN** the Run ends without an automatic retry and its Work Item is `needs_human` with reason `paused_run_lost`

### Requirement: Messages outlive the Run they were sent to

A message SHALL be delivered to the Run it was sent to while that Run runs, which narrows ADR-0068 for attended Runs only (ADR-0104); context items and messages to unattended Runs keep reaching the next Run. A message the worker did not acknowledge before the Run ended SHALL become an operator note for the Work Item's next Run, through the existing operator-notes path that hands notes to the next claim's briefing.

#### Scenario: Message sent as the Run finishes
- **WHEN** a person sends a message and the Run reports its Outcome before acknowledging it
- **THEN** the message appears as a note in the next Run's briefing and reads `carried_over` on the finished Run

### Requirement: Cancel withdraws every open interaction

Cancelling the Work Item or Run SHALL mark its open requests `withdrawn` and put a cancellation in the inbox. The worker SHALL stop the Harness on it, and Run expiry SHALL end the Run when the inbox cannot be read. Cancellation SHALL settle money exactly as cancelled work settles today.

#### Scenario: Cancel while a question is open
- **WHEN** a consumer cancels a Work Item whose Run has an open question
- **THEN** the question reads `withdrawn` and the Run ends as cancelled work ends today

### Requirement: Pause is bounded and confirmed

A pause SHALL take effect only when the worker confirms that the Harness has stopped working (R14). A paused Run SHALL keep its renewal, a writing Run its Lease, and its Inference Account's Hold (R15). A pause longer than the Team's pause bound, or a paused Run ended by the harness timeout or the Job deadline, SHALL end the Run as `stuck` with reason `paused_too_long` (R4): ploegd records the Outcome, puts a stop in the inbox, and settles the Hold through the existing settlement path. Resume SHALL be refused afterwards, and the Run MUST NOT be retried automatically (R3). Resume SHALL be accepted only for a confirmed pause. The pause bound SHALL be measured from the stored confirmation time, so a ploegd restart neither resets nor skips it.

#### Scenario: Resume before the stop is confirmed
- **WHEN** a consumer resumes a Run whose pause the worker has not confirmed
- **THEN** ploegd refuses the resume with a conflict

#### Scenario: Pause outlasts its bound
- **WHEN** a Run stays paused past the Team's pause bound
- **THEN** the Run ends `stuck` with reason `paused_too_long`, its Work Item is `needs_human`, its Hold is settled, and it is not retried automatically

#### Scenario: ploegd restarts during a pause
- **WHEN** ploegd restarts while a Run is paused
- **THEN** the pause bound still ends the Run at the time measured from the original confirmation
