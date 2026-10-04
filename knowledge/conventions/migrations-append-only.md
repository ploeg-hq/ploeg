---
type: Convention
title: Store migrations are append-only
description: Add the next migration in pkg/store/migrations; never edit an existing one.
resource: https://github.com/ploeg-hq/ploeg/blob/development/pkg/store/migrations
tags: [store, postgres, migration, database]
timestamp: 2026-10-04T00:00:00Z
---

# Store migrations are append-only

`pkg/store/migrations/` is append-only. To change the schema, add the next numbered
migration. Editing an existing migration breaks every database that already ran it.

Durable state lives in Postgres and git/forge state only; see
[the pull request is the blackboard](/decisions/pr-is-the-blackboard.md).
