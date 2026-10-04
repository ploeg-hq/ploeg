---
type: Pitfall
title: A new OutcomeReport field must be carried through every copy
description: "MergeDropBox and resolveOutcome copy fields one by one; a field they skip never reaches ploegd, and the tests still pass."
resource: https://github.com/ploeg-hq/ploeg/blob/development/pkg/harness/dropbox.go
tags: [outcome, outcomereport, contract, harness, worker, field, dropbox]
timestamp: 2026-10-04T00:00:00Z
---

# A new OutcomeReport field must be carried through every copy

An agent reports through the drop box (`PLOEG_OUTCOME_FILE`). Between the agent and
ploegd the report is rebuilt twice, field by field:

1. `harness.MergeDropBox` in `pkg/harness/dropbox.go` merges the drop box into the
   adapter's report.
2. `resolveOutcome` in `pkg/worker/worker.go` replaces the report with the worker's
   own view of delivery and copies agent fields back in its `resolved` closure.

A field added only to `OutcomeReport`, its schema and `validateOutcomeReport` is
dropped at one or both steps. Schema and validation tests still pass. Add the field to
both copies, and test it from a drop box to the report the worker posts.

Seen 2026-10-04: in the knowledge-pack spike neither Run carried a new field all the
way through (`docs/research/2026-10-04-knowledge-pack-spike-local.md`). `learnings`
needed the same `resolveOutcome` line. See
[contracts change together](../conventions/contracts-change-together.md).
