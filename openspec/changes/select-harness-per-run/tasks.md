## 0. Prerequisites

- [x] 0.1 Confirm `record-harness-per-run` is merged
- [ ] 0.2 Confirm `submit-work-items` (`POST /api/v1/operator/work-items`, ADR-0102) is merged before section 4; sections 1 to 3 may land before it (2026-10-14: section 4 is built on `feat/submit-work-items`, PR #140, which is not merged yet)
- [ ] 0.3 Ratify or reject ADR-0098, ADR-0099 and ADR-0103; on accepting 0103, append a dated "Narrowed by ADR-0103" note to the More Information of ADR-0039 and ADR-0091 (the ledger's narrowing convention, as ADR-0080 did) (2026-10-14: 0098, 0099 and 0103 are set to accepted with this change; the notes on 0039 and 0091 are left to the owner)

## 1. Configuration

- [x] 1.1 Add `executor.teams[].harnesses` (allowlist entries with today's `harness` fields, `default`, Role-level narrowing) and `allowUnqualifiedHarnesses` to `ops/helm/ploeg/values.yaml` and `values.schema.json`; a Team without it renders as a one-entry allowlist; add an `ops/helm/ploeg/ci/` values file with two allowed entries
- [x] 1.2 Add the qualified-Harness list helper next to `ploeg.isolationQualifiedHarnesses` (`_helpers.tpl:133-135`)
- [x] 1.3 Extend `PLOEG_TEAM_HARNESSES` with `allowed` and `qualification`; parse and validate it in ploegd

## 2. Store and claim

- [x] 2.1 Add the next numbered migration: nullable `agent_runs.harness_requested`, the requested Harness on the Work Item, and the partial index `(team, role, harness_requested, id) WHERE state = 'pending'`
- [x] 2.2 Store the resolved Harness when the Shift inserts pending Runs (`pkg/store/shift.go:130,295`); test builder, reviewer, retry and re-opened Round
- [x] 2.3 Add the Harness clause to `ClaimRoleWithin` and `PendingRuns` when the claim names a Harness, deriving the key from `harness` and `harnessProfile` as `ploeg.qualificationHarnessName` does; extend `TestClaimRoleAgreesWithPendingRuns`
- [x] 2.4 Add the same clause to the scaler query in `ops/helm/ploeg/templates/scaledjob.yaml` and render one workload per allowed entry (ScaledJob and CronJob); regenerate the goldens with `sh scripts/helm-golden.sh update` and check that a plan-less Team's render is unchanged
- [x] 2.5 If `reserve-runs-for-launchers` has landed, add `harness=` to its demand endpoint with the same predicate (2026-10-14: it has not landed; nothing to change)

## 3. Model per session

- [x] 3.1 Add `role.models` to `ops/helm/ploeg/values.yaml` and `values.schema.json`; render each policy in `_worker_control.tpl` with `models` (the list, default `[role.model | team.model]`) and `defaultModel`; parse and validate both in `pkg/httpapi/llm_control.go` (default must be listed); add an `ops/helm/ploeg/ci/` values file with a two-model Role and regenerate the goldens
- [x] 3.2 In the migration of 2.1, add nullable `agent_runs.model_selected` and the requested model on the Work Item; fill `model_selected` when the Shift inserts pending Runs (`pkg/store/shift.go:130,295`) from the request if the Role lists it, else the Role's default; test builder, reviewer, retry, re-opened Round and requeue
- [x] 3.3 Scope `LLMControl.Reserve` to `[model_selected]` when set, so the minted key allows only that model; test that the reserved account and the minted key carry only the resolved model
- [x] 3.4 Write `configured_model` from `model_selected` in `recordRunConfig` (`pkg/httpapi/run_config.go:29`); test a two-model Role and `modelMismatch` against the chosen model
- [x] 3.5 Return `model` in the claim response (`docs/contracts/run-api.v1.schema.json`); the worker passes it as `LLM_MODEL`, falling back to its environment when absent; test in `pkg/worker` and the claude-code alias pinning with a chosen model

## 4. Admission and reads

- [x] 4.1 Accept the optional Harness on `POST /api/v1/operator/work-items`; refuse it outside the allowlist, unqualified and not the Role's default without opt-in, or other than the configured Harness for a Team without a plan; include it in the R13 fingerprint; schema edit in `docs/contracts/operator-api.v1.schema.json`
- [x] 4.2 Accept the optional `model`; refuse with 422 `model_not_allowed` (naming the allowed models per Role) when no Role of the Team's plan lists it and `model_requires_managed_inference` without a policy; include it in the R13 fingerprint; handler tests for listed, unlisted, no policy and a replay with another model
- [x] 4.3 Return the resolved Harness and model per Role in the admission response
- [x] 4.4 Add allowlist, default and qualification per Role, the observed version per allowlisted Harness, pending Runs per stored Harness, and the allowed models and default model per Role, to the Teams read
- [x] 4.5 Alert when a pending Run's stored Harness is not in the current allowlist (`docs/ops/alerts.md`)

## 5. Docs and gates

- [x] 5.1 Update `docs/contracts/acp-profiles.md`, `docs/architecture.md` §3 and §6 (model per Run), `docs/ops/managed-workers.md` (policy model lists, key scope) and the operations guide for allowlists
- [ ] G.1 Run `mise run verify` (gofmt, vet, build, `go test ./...`, `openspec validate --all --strict`, Helm lint, template and golden checks); paste the output in the pull request
- [ ] G.2 Run `mise run docs-check` (ADR ledger, contracts, generated configuration, domain); paste the output in the pull request
