---
type: Convention
title: Contract schemas and Go types change together
description: A field added to TaskSpec or OutcomeReport goes into docs/contracts in the same commit.
resource: https://github.com/ploeg-hq/ploeg/blob/development/docs/contracts
tags: [contract, schema, harness, outcome, taskspec]
timestamp: 2026-10-04T00:00:00Z
---

# Contract schemas and Go types change together

The JSON schemas in `docs/contracts/` and the Go types in `pkg/harness/contract.go`
change in the same commit. `pkg/harness/contract_test.go` pins them: it validates
marshalled values against the schemas, so a field in one and not the other fails.

See [the harness contract](/concepts/harness-contract.md).
