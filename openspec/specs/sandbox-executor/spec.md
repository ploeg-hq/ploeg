# sandbox-executor Specification

## Purpose
How the experimental `executor.type: sandbox` runs each Run in a
kubernetes-sigs/agent-sandbox `v1beta1` Sandbox while keeping every executor
obligation of `docs/contracts/executor.md`. Archived from the change
`2026-09-27-add-agent-sandbox-executor`.
## Requirements
### Requirement: One launcher creates exactly one claim
The sandbox executor SHALL start one launcher per spawn decision of the
existing scaler, and each launcher MUST create exactly one `SandboxClaim`,
named `sbx-` plus the launcher's pod name (its tail kept within 63
characters) and owned by the launcher's Job. The claim MUST NOT share the
launcher pod's name, because the controller names the worker pod after the
claim and refuses a pod it does not own. The launcher
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

### Requirement: A team can run under the sandbox executor alone
A team's optional `executorType` SHALL choose `keda` or `sandbox` for that
team's workloads while `executor.type` is `keda` or `sandbox`, and the chart
MUST refuse it under the CronJob executor. Teams without it SHALL render
exactly as under `executor.type`.

#### Scenario: One team trials the sandbox executor
- **WHEN** `executor.type` is `keda` and one team sets `executorType: sandbox`
- **THEN** only that team's workloads render as launcher ScaledJobs with a
  `SandboxTemplate` and `SandboxWarmPool`, the launcher RBAC renders once, and
  every other team's ScaledJob is unchanged

#### Scenario: executorType under the CronJob executor
- **WHEN** `executor.type` is `cronjob` and a team sets `executorType`
- **THEN** the chart fails to render with a message naming the team

### Requirement: A sandbox that never starts fails one Run as infrastructure
A launcher whose claim is not Ready within `executor.sandbox.startTimeoutSeconds`
MUST delete the claim, claim one Run for its Team and Role, and report that Run
`failed` with failure reason `infra_node` and the controller's Ready reason.

#### Scenario: The controller refuses the claim
- **WHEN** a claim stays `Ready=False` past the start timeout
- **THEN** the launcher deletes it, one queued Run of its Team and Role ends
`failed` with `infra_node`, and the launcher Job fails

#### Scenario: Nothing is waiting
- **WHEN** the claim times out and the Team's queue is empty
- **THEN** the launcher deletes the claim, reports nothing, and the Job fails
