---
type: Pitfall
title: Migration numbers collide across open branches
description: "Two branches that each add the next migration pick the same number; check open pull requests before numbering."
resource: https://github.com/ploeg-hq/ploeg/blob/development/pkg/store/migrations
tags: [store, migration, postgres, database, merge]
timestamp: 2026-10-04T00:00:00Z
---

# Migration numbers collide across open branches

A migration takes the next number after the highest in `pkg/store/migrations`. Two
branches started from the same trunk both take that number. Before numbering, look at
open pull requests that add migrations, and renumber on rebase when one lands first. The
directory already holds four files numbered 0036 from such collisions.

See [store migrations are append-only](../conventions/migrations-append-only.md).
