# Tasks — run-multi-agent-shifts

Ordered by dependency; groups 1–9 map to the delivery order in design.md D11.
Every group ends green on the full gate set from
`.forgejo/workflows/on_pull_request.yml`, and plan-less teams stay
byte-identical until group 9.

## 1. Store completions (dead code until group 3)

- [ ] 1.1 Migration `0009`: `agent_runs.findings TEXT NOT NULL DEFAULT ''` — new numbered file, no edits to applied migrations
- [ ] 1.2 `CloseShift(ctx, shiftID, reason)` — sets `closed_at`/`close_reason`, finishes the Shift's leftover `pending` runs, audits both
- [ ] 1.3 `LiveShifts(ctx)` and `LiveShiftForItem(ctx, workItemID)` enumeration
- [ ] 1.4 CAS round-open: `OpenRound(ctx, shiftID, fromRound, roles)` — second advance from the same round is refused; test proves the double-advance race is closed
- [ ] 1.5 `ReportOutcome` returns `OutcomeResult{WorkItemID, ShiftID}`, persists `findings`, and skips the work_items transition when `shift_id` is non-null; legacy runs keep today's transition — regression test pins both paths
- [ ] 1.6 `RoundReports(ctx, shiftID)` — prior rounds' `{role, round, writes, outcome, summary, findings, links}`
- [ ] 1.7 `ShiftsBelowFloor(ctx)` for sweeper parking
- [ ] 1.8 Bug fix + failing-first regression test: `ClaimRole` RETURNING gains the `target_*` columns and populates `Item.Target` (prove the test fails against the unfixed query)
- [ ] 1.9 Bug fix + failing-first regression test: `Renew` extends `agent_runs.expires_at` for shift runs so lease-less readers can renew
- [ ] 1.10 Bug fix + failing-first regression test: `Checkpoint` falls back to a running `agent_runs` row when no lease exists

## 2. Team plan config (renders dark)

- [ ] 2.1 `pkg/plan`: types (`TeamPlan{Pool, Rounds[]{Roles[]{Name, Writes, Cap, Model, Image, Harness, Dind, MaxReplicaCount}}}`), `Parse`, `Validate` — all-readers or exactly-one-writer per round, DNS-label-safe role names, caps ≥ 0
- [ ] 2.2 `cmd/ploegd`: parse `PLOEG_TEAM_PLANS` at boot, fail fast like `PLOEG_TARGET_MAP`
- [ ] 2.3 Chart: `values.schema.json` gains `plan` on team entries (currently `additionalProperties: false` — a hard render failure); `deployment.yaml` renders `PLOEG_TEAM_PLANS` via `toJson`; no fixture sets a plan, renders stay byte-identical

## 3. Shift engine (inert in prod — no plans configured live)

- [ ] 3.1 `pkg/shiftengine`: `EnsureShift` (planned teams only; idempotent via `LiveShiftForItem` + unique-index race; derives branch `agent/vik-<externalID>`; `OpenShift(pool)` + first `OpenRound`)
- [ ] 3.2 `Evaluate(shiftID)`: round complete → terminal-outcome close / next planned round via CAS / plan exhausted → `CloseShift` + `needs_human`
- [ ] 3.3 `EvaluateAll(ctx)` + sweeper tick gains `ExpireRuns` and below-floor parking (`needs_human`, reason names pool and spend, no attempt burned)
- [ ] 3.4 Hooks: `handleTrackerWebhook` calls `EnsureShift` after queued commit; `handleOutcome` calls `Evaluate` after report (errors logged, never returned)
- [ ] 3.5 Crash-state tests: queued-without-Shift, round-complete-not-advanced, terminal-not-closed, pool-below-floor, expired reader blocking a round — each provably repaired by `EvaluateAll`

## 4. Run API (role-scoped claim; contracts change with types)

- [ ] 4.1 `claimRequest` gains optional `role`; role set → `ClaimRole` (with plan cap); `ErrBudgetExhausted` → 204; role empty → legacy `Claim` fallthrough, pinned by test while no synthesized plans exist
- [ ] 4.2 `claimResponse` gains `shift, role, round, writes, branch, authorized, briefing[]{role, round, findings}` (briefing from `RoundReports`)
- [ ] 4.3 `handleOutcome` accepts and persists `findings`; `handleQueueDepth` gains `role` → `PendingRuns`
- [ ] 4.4 `docs/contracts/run-api.v1.schema.json`, `outcomereport.v1.schema.json`, `taskspec.v1.schema.json` edited additively in the same change as the Go types; `contract_test.go` assertions extended
- [ ] 4.5 Depth==drainable parity test extended over the role dimension

## 5. Worker (role-aware runs)

- [ ] 5.1 `PLOEG_ROLE` env → `Config.Role` → claim carries it; `TaskSpec.Role` populated at last
- [ ] 5.2 Prefer `claimed.Branch` with legacy local derivation as fallback (same string — rollout no-op)
- [ ] 5.3 Mint with `ClaimedRun.Authorized` when > 0, else `LITELLM_KEY_BUDGET` (legacy identical) — ADR-0012's missing half
- [ ] 5.4 Reorder: `findPR` snapshot before `ComposePrompt`; prompt gains "a PR already exists on this branch — update it, never open a second one" clause
- [ ] 5.5 `ComposePrompt`: role preamble, size-capped briefing injection attributed per role/round, and a reader variant (no push/PR contract; deliver findings in the outcome report)

## 6. Chart: one workload per (team, role)

- [ ] 6.1 Golden renders of the existing CI fixtures committed + CI diff step — plan-less output pinned byte-identical before any template edit
- [ ] 6.2 `scaledjob.yaml`/`cronjob.yaml`: teams with a plan render one workload per distinct role, `ploeg-worker-<team>-<role>`, label `ploeg.webgrip.dev/role`, env `PLOEG_ROLE`
- [ ] 6.3 Role→team→global override tier in `ploeg.workerPodTemplate` (`model`, `image`, `harness`, `dind`, `maxReplicaCount`); `values.schema.json` role-level keys
- [ ] 6.4 Role workloads' KEDA query = the `agent_runs_claimable` predicate (`team`,`role`,`state='pending'`); quote the index definition beside the template query; store test asserts `ClaimRole`↔`PendingRuns` agreement (KEDA-sync rule)
- [ ] 6.5 `executor.forgejo.readTokenSecret` — reader-role workloads render `AGENT_BUILDER_TOKEN` from it when set; values.yaml documents the interim shared-token gap loudly
- [ ] 6.6 CI fixture gains one planned team so every new template branch renders on each PR

## 7. Providers

- [ ] 7.1 `pkg/provider/forgejo`: `Name`/`Comment` (`POST /repos/{owner}/{repo}/issues/{pr}/comments`)/`ParseWebhook` (raw-body HMAC-SHA256 `X-Forgejo-Signature` before JSON; normalizes `review_submitted`/`check_failed`/`merge_state_dirty`; irrelevant events dropped quietly) + recorded-payload tests
- [ ] 7.2 Vikunja provider gains `BaseURL`/`Token`/HTTP client; real `FetchItem`; `Comment` via **PUT**; `SetStatus`; missing env keeps today's no-op stubs
- [ ] 7.3 `cmd/ploegd` + chart: `FORGEJO_URL` + agent-builder token on the ploegd deployment; `PLOEG_VIKUNJA_URL`/`PLOEG_VIKUNJA_TOKEN` wiring

## 8. Blackboard publication and human handoff

- [ ] 8.1 Engine publishes each reading Run's findings as a PR comment attributed to its Role, at round advance/close, when a PR is known (repo from the item's Target; unresolved Target → WARN + skip)
- [ ] 8.2 At Shift close: tracker `Comment` with the PR link + `SetStatus(needs_human)`; write-back failure logged, never blocks the state transition
- [ ] 8.3 At-least-once semantics documented; duplicate-comment tolerance tested

## 9. Uniform dispatch flip (the only live-behaviour change)

- [ ] 9.1 Plan-less teams get a synthesized one-writer plan (pool 0 = unmetered), behind an env kill-switch (default on); legacy `Claim` fallthrough deleted
- [ ] 9.2 Chart plan-less golden renders still byte-identical; role-less claims drain synthesized-plan runs; env budgets still honoured (`Authorized == 0`)
- [ ] 9.3 Spec scenario "a plan-less Team behaves exactly as today" demonstrated in tests end to end

## 10. Gates and closure (every group; listed once)

- [ ] 10.1 Per group/PR: `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`, `helm lint ops/helm/ploeg`, all three `helm template` renderings — output pasted in the PR body (via `docker run golang:1.25` / `alpine/helm`; `pkg/store` embedded-postgres needs non-root)
- [ ] 10.2 `go test ./internal/ledger/` wherever docs/adrs or its index is touched
- [ ] 10.3 `openspec validate --all` after artifact edits
- [ ] 10.4 architecture.md §9 updated: divergences 4 (teams with roles), 6 (tracker write-backs), and the "checkpoints written, never read" briefing note re-verified against the landed code
