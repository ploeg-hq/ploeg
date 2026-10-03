# `spec-driven-with-adr`

The OpenSpec workflow schema for this repository. Resolved as a **project**
schema — `openspec schemas` lists it as `spec-driven-with-adr (project)`,
meaning this directory wins over anything the CLI ships.

```
proposal → specs → design → adr → tasks → apply
```

## Where it came from, and what changed

The base is the upstream `spec-driven` schema bundled with
`@fission-ai/openspec` (pinned to 1.6.0 in `mise.toml`). Two kinds of change
were applied:

1. **An `adr` artifact** between `design` and `tasks`, so a change that makes a
   durable architectural commitment cannot reach `tasks` without recording it.
   `tasks` requires `specs` + `adr`; `design` no longer gates `tasks` directly.
2. **Repo rules folded into every instruction** — the seam vocabulary, the
   append-only migration rule, the "never restate a published contract" rule,
   the gate set, and the regression-test-must-fail-first rule.

## Why the ADR instruction is long

It encodes the two local extensions to MADR 4.0 that
[`docs/adrs/README.md`](../../../docs/adrs/README.md) defines, because a model
that has not read that file will otherwise do the vanilla thing and be wrong:

- **Supersession is append-only.** Vanilla MADR (and the `adr-writer` skill's
  default) flips the superseded record's `status:`. This repo forbids that.
- **A decision that can change carries a dated review** — `review-by:` plus a
  `## Re-evaluation triggers` section.

Both are enforced by the `internal/ledger` Go tests, which is why the
instruction ends by telling the agent to run them and paste the output. The
`adr-writer` skill's own bundled validator assumes status-flip semantics and
must not be used here.

## Editing this schema

`openspec instructions <artifact> --change <name>` prints what a model actually
receives: this schema's `instruction`, plus `openspec/config.yaml`'s `context`
and per-artifact `rules`, plus the template. That command is the way to check a
change to any of the three before relying on it.

Keep the split honest: **the schema describes the workflow, `config.yaml`
describes this product.** A rule that would still apply if Ploeg were a
different Go service belongs here; a rule about leases or harnesses belongs in
`config.yaml`.
