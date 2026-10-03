## ADDED Requirements

### Requirement: One launcher creates exactly one claim
The sandbox executor SHALL start one launcher per spawn decision of the
existing scaler, and each launcher MUST create exactly one `SandboxClaim`,
named after the launcher's pod and owned by the launcher's Job. The launcher
MUST NOT create a second claim for any reason, including a failed or finished
first one.

#### Scenario: Claimable count produces claims
- **WHEN** the KEDA scaler query for a Team and Role returns a positive count
- **THEN** each spawned launcher creates one `SandboxClaim` referencing that
  Team and Role's `SandboxWarmPool`, and the agent-sandbox controller creates
  one worker pod from the matching `SandboxTemplate`

#### Scenario: Claim creation fails
- **WHEN** the Kubernetes API rejects the claim
- **THEN** the launcher exits non-zero without retrying, the Job is not
  retried (`backoffLimit: 0`), and no Run was claimed

### Requirement: The launcher never retries a Run
The launcher SHALL wait until its claim reports the `Finished` condition, the
claim disappears, or its own deadline passes, and MUST exit without creating
new work. On `Finished` it SHALL delete the claim so the agent-sandbox
controller cannot recreate a finished worker pod.

#### Scenario: Worker finishes
- **WHEN** the worker pod exits and the claim reports `Finished=True`
- **THEN** the launcher deletes the claim and exits 0

#### Scenario: Claim deleted underneath the launcher
- **WHEN** the claim returns 404 while the launcher waits
- **THEN** the launcher exits 0

### Requirement: Wall-clock backstops survive a dead launcher
The sandbox executor MUST keep the executor contract's wall-clock backstop
even when the launcher dies. The worker pod SHALL carry
`activeDeadlineSeconds`; the claim SHALL carry `shutdownTime` beyond that
deadline with `shutdownPolicy: Delete` and a `ttlSecondsAfterFinished`; and the
claim's owner reference to the launcher's Job SHALL let Job garbage
collection remove the claim, its Sandbox and its pod.

#### Scenario: Launcher pod dies mid-Run
- **WHEN** the launcher pod is evicted while the worker is running
- **THEN** the worker keeps running and reporting under its Lease (R2), and
  the claim is still removed by `shutdownTime` or by Job garbage collection

#### Scenario: Worker pod dies mid-Run
- **WHEN** the worker pod is killed without reporting an Outcome
- **THEN** the Lease expires and the sweeper records recovery (R2, R3); the
  executor does not resubmit the Run

### Requirement: The worker pod keeps its authority boundary
The worker pod created from a `SandboxTemplate` MUST have the same shape as
under the ScaledJob executor: no service-account token, the same environment
contract and the same labels, including `ploeg.webgrip.dev/privileged-dind`
when the harness uses Docker. Only the launcher MAY hold Kubernetes API
authority, and it SHALL be limited to `sandboxclaims` create, get and delete
in its own namespace.

#### Scenario: Launcher permissions
- **WHEN** the chart renders with `executor.type: sandbox`
- **THEN** the launcher's Role grants only create, get and delete on
  `sandboxclaims` in `extensions.agents.x-k8s.io`, and the worker pod template
  has `automountServiceAccountToken: false`

#### Scenario: Claim cannot inject environment
- **WHEN** a `SandboxClaim` sets `env`
- **THEN** the controller rejects it, because the template sets
  `envVarsInjectionPolicy: Disallowed`
