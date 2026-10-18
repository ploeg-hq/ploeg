---
type: Convention
title: Commits and staging
description: Conventional commits with a VIK trailer; stage only the paths you changed.
resource: https://github.com/ploeg-hq/ploeg/blob/development/AGENTS.md
tags: [git, commits]
timestamp: 2026-10-18T00:00:00Z
---

# Commits and staging

- Use conventional commits. Work from the board carries a `VIK-<taskID>` trailer.
- Stage only the paths you changed. Never `git add -A`, `git add .` or `git commit -a`:
  other sessions share checkouts, and a whole-tree commit absorbs their work.
- On pushed trunk, correct a mistake with a follow-up commit, never an amend.
- Before assuming your edits are uncommitted, run `git log --oneline -3 -- <paths>`.

A releasable commit (`feat`, `fix`, `perf`, `refactor`, `revert`, dependency bumps) merged to `development` cuts a `v0.x.y-rc.N` candidate; a stable `v0.x.y` stays a reviewed tag on `main`.
