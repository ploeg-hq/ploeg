## Why

The owner decided on 2026-10-13 that a person chooses the Harness for each session in an Operator Consumer's New-session dialog, and that Ploeg's worker executes every session (Unfold ADR-0002, Unfold ADR-0023). Today the Harness is fixed per worker deployment (`cmd/ploeg-worker/main.go:146-161,188`, `pkg/worker/adapters.go:119`), resolved by the chart per Role, team and global (`ops/helm/ploeg/templates/_helpers.tpl:181-184`), and no admission carries a Harness: `AdmitOperatorExecution` has no Harness, profile or model field (`pkg/store/operator_execution.go:41-53`). A person therefore cannot choose, and a Team cannot govern which Harnesses may be chosen.

## What Changes

- A Team declares an allowlist of Harnesses (name and, for `acp`, profile) and a default per Role, in the chart. ploegd reads it through `PLOEG_TEAM_HARNESSES`, which `record-harness-per-run` introduces, extended with the allowlist.
- The operator submission route that creates work for `ploeg-worker` to execute (`POST /api/v1/operator/work-items`, from the `submit-work-items` change) MAY name a Harness. ploegd refuses one outside the Team's allowlist, and refuses an unqualified Harness other than the Role's default unless the Team opted in. The resolved Harness is stored on each pending Run when the Shift creates it, so retries and later Rounds use the same choice.
- The executor runs one workload per (Team, Role, Harness) in the allowlist. A worker's claim already names its Harness (ADR-0091); the claim and the KEDA scale signal now also select on it, with one predicate.
- The Teams read (`record-harness-per-run`) adds the allowlist, the default and each Harness's qualification status per Role.
- **Model per session (owner decision 2026-10-13).** A Role's managed inference policy lists its allowed model aliases and names a default. The same submission MAY name a `model`; ploegd accepts it only when a Role of the Team's plan lists it, otherwise refuses with a client error naming the allowed models. Each Role whose policy lists it uses it, the others their default, and the response lists the resolved model per Role. The resolved model is stored on each pending Run, recorded as ADR-0091's `configured_model`, sent to the worker in the claim response, and is the only model the Run's key is scoped to. Retries, re-opened Rounds and requeues keep it. The Teams read lists each Role's allowed models and default. ADR-0103 (proposed) records it and narrows ADR-0039.

## Capabilities

### New Capabilities

- `harness-selection`: how an admission requests a Harness, how a Team's allowlist and default govern it, how qualification gates it, and what a consumer reads back.
- `model-selection`: how an admission requests a model from the models a Role's policy lists, how it resolves per Role and is fixed on each Run, how the Run's key and record follow it, and what a consumer reads back.

### Modified Capabilities

- `role-claim`: the claim and the scale signal select pending Runs by Harness as well as Team and Role, and the claim response carries the Run's resolved model.

## Seams

Run API (operator admission and the Teams read), Executor (workloads per Harness and the claim predicate) and LLM control (the per-Run key scoped to the resolved model). They stay one change because a Harness that can be requested but never claimed stalls the Work Item silently, the worst failure `role-claim` names; the specs keep them in separate capabilities.

## Dependencies

- Requires `record-harness-per-run` (the configured-Harness map and the Teams read fields).
- Requires the `submit-work-items` change (ADR-0102, proposed): its `POST /api/v1/operator/work-items` is the only route through which an operator session becomes work that `ploeg-worker` executes, so the Harness field belongs on that route. It does not go on `AdmitOperatorExecution` (`POST /api/v1/operator/executions`), whose Run the consumer executes and no worker claims. Today no `POST /api/v1/operator/work-items` route is registered (`pkg/httpapi/operator.go:177-201` and the files it delegates to).
- Independent of `interactive-runs`, which works for any attended Run whose Harness supports interaction and also depends on `submit-work-items`; this change is how a person picks such a Harness.

## Non-goals

- A model outside the Role's policy, or more than one model per Run. A Run still calls exactly one model (ADR-0039 as narrowed by ADR-0103); the advisor stays off.
- Selecting a Harness for tracker-originated work. Those Runs use their Role's default.
- Selecting a Harness for a Team without a plan. Its one workload scales on queued `work_items` (`ops/helm/ploeg/templates/scaledjob.yaml:79`), not on pending Runs per Role, so it cannot scale a workload per Harness; a submission for such a Team may name only its configured Harness.
- Building or publishing agent images. Ploeg publishes none (`_helpers.tpl:186-188`).
- Making `claude-environment` selectable per Run (it is paid by the Team's plan, ADR-0084). See Open Question 5.
- Qualifying `qwen-code` or `goose` (`docs/contracts/acp-profiles.md` §"Before a team uses"). This change only reports and gates on qualification.

## Impact

- `pkg/httpapi` admission handler(s), `docs/contracts/operator-api.v1.schema.json`; `pkg/store` (Shift Run creation at `pkg/store/shift.go:130,295`, `ClaimRoleWithin` at `:342`, `PendingRuns` at `:516`), a new migration with the Run column and a partial index.
- `ops/helm/ploeg/templates/_helpers.tpl`, `scaledjob.yaml` (scaler query at `:64-75`), `cronjob`, `values.yaml`, `deployment.yaml`; `_worker_control.tpl:21` (policy `models` becomes the Role's list plus a `defaultModel`); chart tests.
- `pkg/httpapi/llm_control.go` (`Reserve` scopes the account to the Run's resolved model, not `p.Models`), `pkg/httpapi/run_config.go:29` (`configured_model` from the resolved model), the claim response, `pkg/worker` (`LLM_MODEL` from the claim, `cmd/ploeg-worker/main.go:94` as fallback).
- The open change `reserve-runs-for-launchers` replaces the SQL scaler with a demand endpoint; whichever lands second adds the Harness parameter to it.
- Backlog #64 (ACP profiles still unqualified) and #69 (conformance suite) supply qualification.
