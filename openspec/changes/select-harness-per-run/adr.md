# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-13
- **Reviewer**: Claude (planning session for Ryan Grippeling)
- **Change**: `select-harness-per-run`

## In-Force ADR Context Reviewed

- `0012-two-level-budgets-authorized-and-settled.md` — budget per Run from the Shift pool is unchanged by the Harness.
- `0019-a-failed-writing-run-reopens-its-round.md` (proposed) — a re-opened Round's Run keeps the chosen Harness.
- `0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md`, `0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md` — isolation and egress stay per workload.
- `0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md` (proposed) — ploegd renders no workloads; the chart does.
- `0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md` (proposed, open change `reserve-runs-for-launchers`) — the demand endpoint takes the Harness.
- `0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md` — excluded from per-Run choice.
- `0024-operator-work-uses-one-execution-authority.md` (proposed) — its consumer-as-Executor path is what Unfold ADR-0023 retires; flagged in design Open Question 7.
- `0039`, `0091` (proposed) — narrowed by 0103: a Run still calls exactly one model, now the one resolved for it from its Role's policy, and `configured_model` records that model. `0097` (proposed) — recording and the Teams read; its `model` reads the Role's default.
- `0102` (proposed, in the `submit-work-items` change) — the submission route that carries the Harness; this change depends on it.

## Repository-Level ADRs Created

- `docs/adrs/0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md` — selection, allowlist, qualification gate, resolution per Run.
- `docs/adrs/0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md` — worker pool per Harness rather than one multi-Harness image; confirmed by the owner on 2026-10-13.
- `docs/adrs/0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md` — model per session from the Role's policy, resolved per Run, key scoped to it, recorded as `configured_model`.

## Supersessions

- none. ADR-0102 in `submit-work-items` supersedes 0024 (design Open Questions 1 and 7).
- 0103 narrows 0039 (point 1) and 0091 (the model rule) with `supersedes: none`: both keep the rest of their decisions, and the ledger allows one full replacement per record. This follows ADR-0080's narrowings; a dated note is appended to 0039 and 0091 when 0103 is accepted.

## Validation

```sh
$ mise exec -- go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger	0.420s
```

## Notes

All three records are `proposed`; the owner ratifies. The owner confirmed 0099's recommendation and decided 0103 on 2026-10-13.
