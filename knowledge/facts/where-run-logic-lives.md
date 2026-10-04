---
type: Fact
title: Where Run logic lives
description: cmd/* only wires environment to config; Run logic lives in pkg/worker.
resource: https://github.com/ploeg-hq/ploeg/blob/development/pkg/worker/worker.go
tags: [worker, cmd, config, environment]
timestamp: 2026-10-04T00:00:00Z
---

# Where Run logic lives

- `cmd/ploegd` and `cmd/ploeg-worker` only turn environment variables into config.
- What a Run does (clone, prompt, harness, verification, delivery) lives in
  `pkg/worker`. The prompt is composed in `pkg/worker/task.go`.
- A new `PLOEG_*` setting is read in `cmd/ploeg-worker/main.go` and carried in
  `worker.Config`.

See [the harness contract](/concepts/harness-contract.md).
