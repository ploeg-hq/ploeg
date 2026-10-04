---
type: Concept
title: The harness contract
description: "TaskSpec in, OutcomeReport out: the only seam between Ploeg and an agent harness."
resource: https://github.com/ploeg-hq/ploeg/blob/development/pkg/harness/contract.go
tags: [harness, contract, taskspec, outcome, adapter]
timestamp: 2026-10-04T00:00:00Z
---

# The harness contract

Every harness (OpenHands, Claude Code, opencode, an exec script) sits behind the
`harness.Adapter` interface and sees the same contract:

- **TaskSpec in**: the Work Item, the Role, the repository, the branch, the trace id,
  earlier Rounds' findings ([blackboard](/decisions/pr-is-the-blackboard.md)) and an
  optional OpenSpec brief.
- **OutcomeReport out**: written by the agent to the file named by
  `PLOEG_OUTCOME_FILE`. The worker discards the agent's delivery claims and reads the
  pull request on the forge instead.

Credentials never travel in the TaskSpec; see
[placeholders](/decisions/harness-gets-placeholders.md). Changing either shape:
[contracts change together](/conventions/contracts-change-together.md).
