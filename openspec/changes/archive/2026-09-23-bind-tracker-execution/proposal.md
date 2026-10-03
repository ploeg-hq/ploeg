## Why

A De Vloer tracker selection must reuse its canonical Ploeg Work Item. Creating a manual duplicate would permit an unattended Run and an operator Run to spend against the same task concurrently. This increment extends ADR 0024 at the tracker admission and Store seams, enforcing R1, R2, R7, R11, R12 and R13.

## What Changes

- Resolve an existing scoped Work Item through an explicitly configured singleton Tracker Provider, with fresh open-state, native revision, API-root and Scope checks.
- Admit a queued, pristine Work Item to an Operator Execution atomically; retire only unstarted pending Runs and their Shift.
- Preserve tracker identity and mirror content while retaining operator dispatch ownership across stop, completion and webhook replay.
- Fence unattended claims, Shift creation and evaluation using the operator binding.
- Publish the additive lookup and admission source contract alongside executable tests.

## Capabilities

### New Capabilities

- `tracker-execution-binding`: canonical identity, source freshness and exclusive admission of pristine tracker work.

### Modified Capabilities

None; this capability extends the unmerged operator API change through an optional admission field.

## Non-goals

Running-work takeover, importing historical repository state, automatic relinquishment, multi-instance tracker identity migration, tracker writes, publication, new Harnesses and live cluster qualification remain outside this increment. Missing mirrors are not created through the lookup or admission endpoint.

## Impact

The change touches `pkg/httpapi`, `pkg/provider`, `pkg/store`, `pkg/shiftengine`, published contracts and De Vloer's client. No claimable queue is added: adoption removes pending work in one transaction, preserving KEDA's existing scale predicates. The provider scope-retention regression is corrected because thin ClickUp events currently overwrite the fetched List scope with an empty value.
