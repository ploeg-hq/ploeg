## Why

The owner decided on 2026-10-13 that a person chooses the Harness per session in an Operator Consumer, and that Ploeg records which Harness and which version every Run ran with. Ploeg already records the Harness name and agent profile from the worker's claim (ADR-0091, migration `0050_run_config.sql`), but not the Harness version, and it cannot show a consumer which Harness, model and version each Role of a Team uses: `GET /api/v1/operator/teams` returns each Role as `{id, queueDepth}` only (`pkg/store/operator.go:45-48`). This change records the missing fact and exposes the configured ones, so the next two changes (`select-harness-per-run`, `interactive-runs`) have something to select from and to show.

## What Changes

- The Outcome Report gains one optional `harness` object (name, profile, version, and the agent's self-reported name and version). Only the worker sets it, like `verification` and `delivery` (`pkg/harness/contract.go:265-272`). Additive under v1.
- The claim request gains an optional `harnessVersion`, so a version the worker knows before it starts the Harness is recorded even when the Run dies before reporting (ADR-0091's reason for recording at claim).
- `agent_runs` gains `harness_version` and `harness_agent` in a new append-only migration. Both are written once and stay null when unknown, the same rule ADR-0091 uses for its five fields.
- ploegd learns each Role's configured Harness from its own configuration: the chart renders the role → team → global harness chain (`ops/helm/ploeg/templates/_helpers.tpl:181-184,217`) into a new `PLOEG_TEAM_HARNESSES` value, following `PLOEG_TEAM_PLANS` and `PLOEG_TEAM_MAX_RUNNING`.
- `GET /api/v1/operator/teams` returns per Role the configured Harness and profile, the configured model (from the Role's managed inference policy, as ADR-0091 reads it), and the Harness version most recently recorded for that Role, marked as observed. Run reads (`run`, `runListItem`) gain `harnessVersion` and `harnessAgent`.

## Capabilities

### New Capabilities

- `run-harness-record`: what a Run records about the Harness it ran with, where each fact comes from, and how the operator API reads it back per Run and per Role.

### Modified Capabilities

- `operator-api`: the scoped read model adds per-Role Harness, model and observed version to the Teams read.

## Seams

Harness (the worker fills the Outcome Report's `harness`) and Store (the migration and the write-once record). The Teams read is a Run API read of what the Store holds. The two seams stay in one change because the field is useless without its column and its read; the specs keep them in separate capabilities.

## Dependencies

- None. It builds on ADR-0091 as implemented (migration `0050_run_config.sql`). `select-harness-per-run` and `interactive-runs` depend on this change; neither this change nor its Teams read needs the `submit-work-items` submission route.

## Non-goals

- Choosing a Harness per Run. That is `select-harness-per-run`.
- Any interaction with a running Run. That is `interactive-runs`.
- Changing what ADR-0091 records or when; this change adds to it and keeps its write-once rule.
- Backfilling earlier Runs. They read as unknown.
- Probing a Harness binary inside ploegd. Versions come only from the worker; ploegd never executes an agent (`docs/design.md` §2: Ploeg does not host the language model; agents run only in worker pods).
- Choosing a model per session. The owner decided on 2026-10-13 that a session may choose one from its Role's policy; `select-harness-per-run` builds it (ADR-0103). This change shows the Role's default model, which is the policy's single model until that change lets a policy list more.

## Impact

- `pkg/harness/contract.go`, `docs/contracts/outcomereport.v1.schema.json`, `pkg/harness/contract_test.go`.
- `pkg/worker` (version probe at startup, ACP `agentInfo` from `initialize`, `pkg/harness/adapters/acp/acp.go:209-227`), `pkg/worker/api.go` (claim field), `docs/contracts/run-api.v1.schema.json`.
- The next numbered migration in `pkg/store/migrations/` (`0053_run_harness_version.sql` while `0052` is the newest), `pkg/store/run_config.go`, `pkg/store/operator.go`.
- `pkg/httpapi/run_config.go`, `pkg/httpapi/operator.go:203-228`, `docs/contracts/operator-api.v1.schema.json`.
- `cmd/ploegd/operator.go`, `ops/helm/ploeg/templates/deployment.yaml`, chart tests.
- Backlog #66 (usage in the Outcome Report) is the precedent for a worker-filled optional object; no backlog item covers this.
