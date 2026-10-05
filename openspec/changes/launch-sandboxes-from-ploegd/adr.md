# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-05
- **Reviewer**: Claude (agent session for Ryan Grippeling)
- **Change**: `launch-sandboxes-from-ploegd`

## In-Force ADR Context Reviewed

In force means accepted and not superseded, per the Records index in `docs/adrs/README.md`. These records constrained this change:

- `0002-go-as-the-implementation-language.md`: the launcher stays in Go, in ploegd's process.
- `0010-shift-owns-the-item-lease-owns-the-branch.md`:
  - the claim predicate exists once, in `pkg/store`;
  - the Lease still owns the branch;
  - the Lease is created at bind, not at dispatch.
- `0012-two-level-budgets-authorized-and-settled.md`: no authorization happens at dispatch, so a Run that never binds never authorized spend.
- `0013-push-rights-are-minted-per-run.md`: push rights are minted at bind, unchanged.
- `0019-a-failed-writing-run-reopens-its-round.md` and `0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md`:
  - a capacity wait is not a failure and consumes no retry budget;
  - template, image and pod failures are `infra_node` against the specific Run.

Proposed records also read:
- `0025-management-authority-stays-in-the-control-plane.md`: launch authority moves into the control plane, and no Executor pod keeps a Kubernetes token.
- `0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md`: the launch outbox follows the same pattern and merges into it if that outbox lands first.

## Repository-Level ADRs Created

- `docs/adrs/0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md` (proposed)
- Context: `docs/adrs/0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md` and `docs/adrs/0073-ploeg-stays-in-go-and-admits-rust-only-as-a-separately-deployed-component.md` (proposed)

## Supersessions

None. ADR-0072 partly revises ADR-0032's intent to use the agent-sandbox generated clientset. ADR-0032 is proposed, so it is revised by its own ratification, not superseded.

## Validation

```
$ go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger	0.020s
```

## Notes

Planning-only change: nothing in `tasks.md` is implemented yet.
