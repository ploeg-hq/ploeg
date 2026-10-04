---
type: Pitfall
title: A new PLOEG_ setting needs the configuration reference regenerated
description: "Describe it in docs/reference/configuration-descriptions.yaml and run mise run docs-configuration, or docs-check fails."
resource: https://github.com/ploeg-hq/ploeg/blob/development/docs/reference/configuration-descriptions.yaml
tags: [setting, environment, config, env, docs, worker, ploegd]
timestamp: 2026-10-04T00:00:00Z
---

# A new PLOEG_ setting needs the configuration reference regenerated

`docs/reference/configuration.md` is generated from the `PLOEG_*` variables the
commands read and from `docs/reference/configuration-descriptions.yaml`. After adding
a setting, describe it in that file and run `mise run docs-configuration`.
`mise run docs-check` fails with "Stale configuration reference" otherwise; package
tests do not notice.

The setting is read in `cmd/*/main.go` and carried in a `Config` field; see
[where Run logic lives](../facts/where-run-logic-lives.md).
