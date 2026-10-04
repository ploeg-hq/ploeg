---
type: Decision
title: The pull request is the blackboard
description: "ADR-0011: Runs share findings through the pull request; Ploeg carries them, the agent never fetches them."
resource: https://github.com/ploeg-hq/ploeg/blob/development/docs/adrs/0011-the-pull-request-is-the-blackboard.md
tags: [adr, findings, briefing, reader, reviewer]
timestamp: 2026-10-04T00:00:00Z
---

# The pull request is the blackboard (ADR-0011)

A reading Run returns its findings in the OutcomeReport. Ploeg publishes them to the
pull request and injects them into the next Round's `briefing`. No harness needs a new
tool, and nothing durable lives in an agent process (R6: durable state is Postgres and
git/forge only).

Consequence for code: anything a later Run must see is carried by Ploeg in the
TaskSpec, never fetched by the agent. See [the harness contract](/concepts/harness-contract.md).
