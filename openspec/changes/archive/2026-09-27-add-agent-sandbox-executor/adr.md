# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-09-26
- **Reviewer**: Claude (for Ryan Grippeling)
- **Change**: `add-agent-sandbox-executor`

## In-Force ADR Context Reviewed

- `0002-go-as-the-implementation-language.md` — thin glue; ruled out the
  client-go dependency in favour of plain REST.
- `0005-build-a-dedicated-dispatch-plane.md` — its trigger "`agents.x-k8s.io`
  graduates past alpha → accelerate backlog #58" fired on 2026-08-28.
- `0010-shift-owns-the-item-lease-owns-the-branch.md` and
  `0012-two-level-budgets-authorized-and-settled.md` — unchanged: the worker
  claims, renews and settles exactly as before.
- `0013-push-rights-are-minted-per-run.md` — the worker pod keeps its
  environment contract and forge credentials unchanged.
- `0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md`
  (proposed) — names agent-sandbox v1.0.x `v1beta1` as the second executor's
  runtime. This change implements that decision.

## Repository-Level ADRs Created

none — the durable decision (agent-sandbox as the second executor's runtime)
is already recorded in proposed ADR-0032. The launcher-in-ScaledJob shape,
cold claims and plain REST are change-local choices in design.md.

## Supersessions

none

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/webgrip/ploeg/internal/ledger	0.523s
```

## Notes

ADR-0032 is `proposed`. A human must accept it before this executor is
released as supported; until then `executor.type: sandbox` is experimental.
