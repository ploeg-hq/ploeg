---
type: Pitfall
title: Do not run the adr-writer validator here
description: Ploeg's ADR rules are enforced by go test ./internal/ledger/, which differs from the generic validator.
resource: https://github.com/ploeg-hq/ploeg/blob/development/internal/ledger/adr_test.go
tags: [adr, decision, ledger, docs]
timestamp: 2026-10-04T00:00:00Z
---

# Do not run the adr-writer validator here

Ploeg's ADRs (MADR 4.0) carry `review-by:` and re-evaluation triggers where a decision
can change, and every record has a `### Confirmation` section. Supersession is
append-only: a new record with `supersedes: NNNN`; never flip an accepted record's status.

`go test ./internal/ledger/` enforces these rules. The `adr-writer` skill's generic
validator disagrees with them; do not run it in this repository.
