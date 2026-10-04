---
type: Pitfall
title: Registry pulls time out in the worker sandbox
description: A worker pod reaches only its model gateway and its forge; skip gates that need a missing toolchain.
resource: https://github.com/ploeg-hq/ploeg/blob/development/AGENTS.md
tags: [sandbox, ci, helm, toolchain, verify, docker]
timestamp: 2026-10-04T00:00:00Z
---

# Registry pulls time out in the worker sandbox

By design a worker pod reaches only its model gateway and its forge. `docker pull`,
`helm dependency update`, `npm install` against a public registry and similar calls
hang until they time out.

What to do: if a gate needs a toolchain the image lacks, skip it, do not retry the pull,
and list it in the pull request under "Checks left to CI". CI runs every gate.
