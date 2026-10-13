## ADDED Requirements

### Requirement: A Role's policy lists the models its Runs may call

Each Role's managed inference policy SHALL list the model aliases its Runs may call and SHALL name one of them as the Role's default model. A Role configured with a single model SHALL allow only that model, so behaviour is unchanged. ploegd MUST refuse to start with a policy whose default model is not in its list.

#### Scenario: Role configured with one model
- **WHEN** a Role's chart values name one `model` and no `models` list
- **THEN** that model is the Role's default and the only model an admission may choose for it

### Requirement: An admission may request a model the Team lists

A submission through `POST /api/v1/operator/work-items` MAY name one `model`; the Operator Execution admission SHALL NOT accept one. ploegd MUST refuse the submission with a client error that names the allowed models per Role, and create no Work Item, Shift or Run, when no Role of the Team's plan lists the model, or when the Team's Roles have no managed inference policy. The model SHALL be part of the admission's idempotency fingerprint (R13), so a replay with a different model is a conflict, not a second admission.

#### Scenario: Model no Role lists
- **WHEN** a consumer submits work naming `glm-5` for a Team whose builder lists `deepseek-v4` and `kimi-k3` and whose reviewer lists `glm-4.7`
- **THEN** ploegd answers with a client error naming each Role's allowed models and records nothing

#### Scenario: Team without managed inference
- **WHEN** a consumer names a model for a Team whose Roles have no managed inference policy
- **THEN** ploegd answers with a client error and records nothing

#### Scenario: Replay with another model
- **WHEN** a consumer repeats an admission's request identity with a different model
- **THEN** ploegd answers with a conflict and the first admission is unchanged

### Requirement: The resolved model is fixed on each Run when the Run is created

When a Shift creates a pending Run, ploegd SHALL store the model that Run must call: the admission's model when the Run's Role's policy lists it, otherwise the Role's default model. Without a requested model, each Run SHALL store its Role's default. A retry, a re-opened Round, a later Round and a Run created after a requeue SHALL resolve the same way from the same admission and so keep the model. The admission response SHALL list the resolved model per Role. A Run whose Role has no managed inference policy SHALL store none.

#### Scenario: Reviewer Role that does not list the requested model
- **WHEN** a session requests `kimi-k3`, which the builder's policy lists and the reviewer's does not
- **THEN** the builder Run stores `kimi-k3`, the reviewer Run stores the reviewer's default model, and the admission response lists both

#### Scenario: Requeue after needs_human
- **WHEN** an operator requeues a Work Item whose builder Runs stored `kimi-k3`
- **THEN** the new builder Run stores `kimi-k3`

### Requirement: A Run's key, claim and record follow its resolved model

The inference account ploegd reserves for a Run, and the key it mints from it, SHALL allow only the Run's resolved model, never the Role's whole list. The claim response SHALL carry the resolved model, and the worker SHALL pass it to the Harness as the model to call. The Run's `configured_model` SHALL record the resolved model, also when the Role's policy lists more than one. The Run's cap SHALL remain its Role's cap from the Shift pool, whatever model it resolved to.

#### Scenario: Key scoped to the chosen model
- **WHEN** a builder Run that stored `kimi-k3` is claimed and its key is minted
- **THEN** the key allows only `kimi-k3`, the claim response names `kimi-k3`, and the Run's `configuredModel` reads `kimi-k3`

#### Scenario: Harness asks for another model
- **WHEN** the Harness of a Run scoped to `kimi-k3` calls `deepseek-v4` through the gateway
- **THEN** the gateway refuses the call with the Run's key

### Requirement: The Teams read shows which models a person can choose

The Teams read SHALL list, per Role, the models its managed inference policy allows and its default model, and SHALL read both as null for a Role without a policy.

#### Scenario: Role with two models
- **WHEN** a builder's policy lists `deepseek-v4` and `kimi-k3` with `deepseek-v4` as default
- **THEN** the Teams read lists both as allowed and `deepseek-v4` as the default
