---
type: Decision
title: The harness gets placeholders; the worker keeps the credentials
description: "ADR-0034 (proposed): the worker holds every credential and proxies the harness's authenticated traffic over loopback."
resource: https://github.com/ploeg-hq/ploeg/blob/development/docs/adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md
tags: [adr, security, credential, token, proxy, litellm, forge]
timestamp: 2026-10-04T00:00:00Z
---

# The harness gets placeholders; the worker keeps the credentials (ADR-0034, proposed)

- `ploeg-worker` marks itself non-dumpable first (`pkg/worker/conceal_linux.go`).
- The per-Run model key stays in the worker; the harness gets a random placeholder and
  a loopback base URL, and a reverse proxy attaches the real key.
- A writer's forge token is isolated the same way behind a loopback proxy scoped to its
  repository.

Any new external service a Run needs follows the same pattern: the token stays in the
worker. See [the harness contract](/concepts/harness-contract.md).
