# Tasks — close-the-review-loop

## 1. The decision

- [x] 1.1 `docs/adrs/0017-the-review-loop-is-verdict-driven-and-capped.md` plus its Records row; `go test ./internal/ledger/` passes
- [ ] 1.2 A human ratifies ADR-0017 (status proposed → accepted) before group 3 merges
  Open: ADR-0017 is still `status: proposed` in `docs/adrs/0017-the-review-loop-is-verdict-driven-and-capped.md` and the ADR index.

## 2. The verdict on the contract

- [x] 2.1 Migration `0010`: `agent_runs.verdict TEXT NOT NULL DEFAULT ''` with a CHECK constraining it to `''`/`approve`/`request_changes`
  Evidence: `pkg/store/migrations/0010_verdict.sql` adds the column and the `agent_runs_verdict` CHECK.
- [x] 2.2 `harness.OutcomeReport.Verdict` + the matching `docs/contracts/outcomereport.v1.schema.json` enum, edited together, plus a `contract_test.go` case
  Evidence: `harness.OutcomeReport.Verdict` in `pkg/harness/contract.go`, the `verdict` enum in `docs/contracts/outcomereport.v1.schema.json`, and the approve/request_changes and unknown-verdict cases in `pkg/harness/contract_test.go`.
- [x] 2.3 `validateOutcomeReport` rejects an unknown verdict at the API boundary (the closed-enum rule the failureReason taxonomy already follows)
  Evidence: `validateOutcomeReport` in `pkg/httpapi/server.go` rejects anything `harness.ValidVerdict` refuses.
- [x] 2.4 `Store.ReportOutcome` persists it; `RoundReports` returns it
  Evidence: `Store.ReportOutcome` in `pkg/store/store.go` writes `verdict` (blanked for writers); `RoundReports` in `pkg/store/shift.go` scans it into `RunReport.Verdict`.

## 3. The loop

- [x] 3.1 `pkg/plan`: `maxFixRounds` (default 2), and boot-time refusal of `maxFixRounds > 0` with no writing Round
  Evidence: `TeamPlan.MaxFixRounds` and the boot-time refusal in `pkg/plan/plan.go`. The shipped chart default is 0, which keeps the loop inert until a plan opts in (design.md, Migration Plan); `ops/helm/ploeg/values.yaml` shows 2 as the example.
- [x] 3.2 `pkg/shiftengine`: derive the fix-round count; re-open the plan's last writing Round then its review Round on `request_changes`
  Evidence: `fixRoundsRun` and `Engine.nextFixRound` in `pkg/shiftengine/reviewloop.go`.
- [x] 3.3 Bounds in order — verdict, then pool, then cap — each closing with a reason that names which one stopped it
  Evidence: `Engine.nextFixRound` in `pkg/shiftengine/reviewloop.go` reads the verdict first (`review_approved`, or `plan_exhausted` when no reader gave one), then closes a wanted fix round with `budget_exhausted_before_fix_round` or `fix_round_cap_reached`. The original order, pool then cap then verdict, closed an approved final fix round as `fix_round_cap_reached` (backlog #115).
- [x] 3.4 A verdict from a WRITING Role is ignored
  Evidence: `Store.ReportOutcome` stores an empty verdict for a writing Run; `TestLoop_WriterVerdictIsIgnored`.
- [x] 3.5 Tests: fix round opens with findings in the briefing, approve closes, cap stops a never-approving reviewer, pool parks before the cap, writer verdict ignored, count survives a restart mid-loop
  Evidence: `TestLoop_RequestChangesReopensTheWriter`, `TestLoop_ApproveCloses`, `TestLoop_CapStopsANeverApprovingReviewer`, `TestLoop_BudgetStopsItBeforeTheCap`, `TestLoop_WriterVerdictIsIgnored` and `TestLoop_FixRoundCountIsDerived` in `pkg/shiftengine/reviewloop_test.go`; `go test ./pkg/shiftengine/` passed on 2026-09-23.
- [x] 3.6 The reviewer prompt asks for a verdict and says what each value means
  Evidence: the reviewer prompt in `pkg/worker/task.go` asks for `approve` or `request_changes` and explains each.
- [x] 3.7 Regression tests for backlog #115: an approval on the last allowed fix round, or with a pool too thin for another, closes as `review_approved` and reaches awaiting_review; a missing verdict closes as `plan_exhausted`, not as an approval
  Evidence: `TestLoop_ApprovalOnTheLastAllowedFixRoundIsAnApproval`, `TestLoop_ApprovalWithAnEmptyPoolIsAnApproval` and `TestLoop_NoVerdictEndsThePlan` in `pkg/shiftengine/reviewloop_test.go`. Before the fix the first two failed on the pool-cap-verdict order and the third on a missing verdict counting as approval; all three pass after it.

## 4. The forge route

- [x] 4.1 Migration `0011`: `forge_deliveries` (delivery id, seen_at) for dedup
  Evidence: `pkg/store/migrations/0011_forge_deliveries.sql`.
- [x] 4.2 `POST /webhooks/forge/{provider}`: raw-body HMAC before parsing, dedup on the delivery id, 202 without synchronous work
  Evidence: `POST /webhooks/forge/{provider}` routes to `handleForgeWebhook` in `pkg/httpapi/server.go`, which dedups on `X-Forgejo-Delivery`, lets `ParseWebhook` verify the signature on the raw body, and answers 202.
- [x] 4.3 Audit every accepted event; drop irrelevant ones without error
  Evidence: `handleForgeWebhook` calls `Store.AuditForgeEvent` for every accepted event; `TestForgeWebhook_IrrelevantEventCreatesNothing`.
- [x] 4.4 Sweep expired delivery ids alongside the leases
  Evidence: `Store.SweepDeliveries` in `pkg/store/shift.go`, called from `cmd/ploegd/sweep.go`.
- [x] 4.5 Tests: bad signature rejected, unknown provider handled, redelivery acts once, push creates nothing
  Evidence: `TestForgeWebhook_RejectsBadSignature`, `TestForgeWebhook_UnknownProviderIs404`, `TestForgeWebhook_RedeliveryActsOnce` and `TestForgeWebhook_IrrelevantEventCreatesNothing` in `pkg/httpapi/forge_test.go`; they passed on 2026-09-23.

## 5. Gates and closure

- [x] 5.1 Per PR: `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`, `helm lint`, three `helm template` renderings, and `./scripts/helm-golden.sh check` — output in the PR body
  Evidence: the gate output for the original pull requests cannot be reconstructed after the monorepo import, and the 2026-09-27 commits went to `development` without a pull request. Instead, `mise run verify`, which runs every one of these gates, passed on 2026-09-27 on the tree of `6c4a4a5` (pushed as `2191afb`) with the Ploeg (gofmt, vet, build, test), Helm (lint, template, golden check), integration and docs groups green. `mise run release-check` passed on the same tree.
- [x] 5.2 `go test ./internal/ledger/` wherever docs/adrs changes
  Evidence: `go test ./internal/ledger/` passed on 2026-09-23.
- [x] 5.3 `openspec validate --all`
  Evidence: `openspec validate --all --strict` passed on 2026-09-23.
- [x] 5.4 architecture.md §9.1 updated: the forge webhook route exists, and what it does and does not yet do
  Evidence: `docs/architecture.md` states that forge webhooks record and deduplicate every event, settle merged or closed pull requests, act further only under a Team's `forgeFollowUps` switches, and are rejected without a webhook secret. The numbered §9.1 no longer exists.
- [x] 5.5 Runbook note: the Forgejo→ploegd network path is blocked today, so the route is inert until ops wires it
  Evidence: "Forge webhooks" in `docs/ops/ci-and-infra.md` names the two prerequisites and records what homelab-cluster showed on 2026-09-27: Forgejo's egress policy allows `ploeg:8080`, but Ploeg's ingress policy admits nothing from the `forgejo` namespace and the HelmRelease sets no webhook secret. The design's "both directions" was stale by then: only Ploeg's ingress side blocks it.
- [x] 5.6 The route fails closed: a forge provider with no webhook secret rejects every delivery instead of accepting it unverified, and the chart can set `executor.forgejo.webhookSecret`
  Evidence: `forgejo.Provider.ParseWebhook` and `gitlab.Provider.ParseWebhook` reject when `Secret` is empty; `TestParseWebhook_WithoutASecretRejectsEveryDelivery` and `TestParseWebhookWithoutASecretRejectsEveryDelivery` failed before the fix. `executor.forgejo.webhookSecret` renders `PLOEG_FORGEJO_SECRET` in `ops/helm/ploeg/templates/deployment.yaml`.
