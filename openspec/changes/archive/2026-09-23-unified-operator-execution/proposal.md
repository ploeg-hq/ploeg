## Why

De Vloer and Ploeg independently own execution while the workbench reads only queue depths. The owner requested one human workbench and one backend for interactive and unattended work, including durable intervention and agent coordination.

## What Changes

- Add an authenticated, team-scoped operator read model for existing Work Items, Shifts, Runs and audit events.
- Establish Ploeg admission and durable command identity for work executed through the workbench's existing Harness and Executor adapters.
- Remove LiteLLM management authority from Executor workloads; retain Run authorization while spend is unresolved after worker death.
- Connect De Vloer's existing interaction surfaces to Ploeg without introducing a board in Ploeg.

## Capabilities

### New Capabilities

- `operator-api`: versioned scoped reads and operator-originated execution commands.
- `worker-authority`: server-side inference capability issuance and conservative accounting.

### Modified Capabilities

No existing wire requirement is removed; new governed execution is explicitly configured.

## Non-goals

This change does not adopt another scheduler, replace the Harness loop, claim thousand-agent qualification, or authorize automatic publication. Tracker-originated content remains tracker-authoritative. Manual work is an explicitly identified origin authorized by the user's request, extending the former tracker-only admission constraint.

## Impact

Run API, Store, Executor and LLM broker seams are coordinated under one user-visible acceptance scenario; separate capability specs isolate their requirements. Rules R1, R2, R4 and R6 apply. Related planning records are De Vloer PV-022 through PV-026, PV-071 through PV-073 and PV-079 through PV-081; Ploeg backlog #96 covers the missing operator surface.
