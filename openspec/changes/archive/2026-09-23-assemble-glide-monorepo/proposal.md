## Why

The owner chose Glide and authorized the reviewed monorepo plan. Vloer and Ploeg share product language and integration contracts, but separate repositories make cross-application changes and documentation validation harder.

## What Changes

- Import both Git histories and the reviewed working-tree documentation into Glide under `apps/vloer` and `apps/ploeg`.
- Compare standalone and Ploeg-admitted execution using real application code and deterministic fixtures before deciding whether to extract a runner.
- Move shared product documentation to the root, preserve service-owned contracts and ADR identities, and repair build and source links.
- Establish root development commands, cross-application checks and independent application release namespaces.

## Capabilities

### New Capabilities

- `glide-repository`: history-preserving import, application isolation, shared documentation and validation.
- `execution-boundary-comparison`: executable evidence for standalone independence and managed authority across interruption and restart.

### Modified Capabilities

None. Existing wire formats and domain invariants remain unchanged.

## Non-goals

No universal Ploeg dependency, new model-serving platform, database migration, automatic production cutover, or forced shared engine. Existing application identities and deployed artifact coordinates stay stable.

## Impact

This migration touches repository layout, CI and documentation. It verifies the executor and harness seams without changing their published contracts. Recovery must retain [R2, R3 and R15](../../../docs/domain/rules.md). Application behavior is compared against [architecture](../../../docs/architecture.md) and the existing [operator qualification](../../../pkg/httpapi/operator_workbench_qualification_test.go). It implements the owner's migration request rather than a numbered tracker backlog item.
