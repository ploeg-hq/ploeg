# Tasks — report-run-usage-on-pull-requests

Planning-only change: nothing below is implemented in this PR. Each step is at
most a day and names the test that checks it.

## 1. The forge can find and edit a comment

- [x] 1.1 `provider.Comment` type and `Comments`/`EditComment` on the `ForgeProvider` SPI (`pkg/provider/provider.go`)
  Test: `pkg/provider` interface conformance — `TestForgeProvider_CommentMethodsInSPI` (a compile-time assertion plus a fake provider implementing all four methods).
- [x] 1.2 Forgejo implementation on the issues endpoints: list `GET .../issues/{pr}/comments` following `page`/`limit` to exhaustion, edit `PATCH .../issues/comments/{id}` (`pkg/provider/forgejo/forgejo.go`)
  Test: `pkg/provider/forgejo/forgejo_test.go`'s `TestComments_PagesToExhaustion` (a multi-page `httptest` server) and `TestEditComment_SendsBody`, including a non-2xx that fails without leaking the token.
- [x] 1.3 GitLab implementation on the merge-request notes endpoints: list `GET .../merge_requests/{iid}/notes` following pagination to exhaustion, edit `PUT .../notes/{note_id}` (`pkg/provider/gitlab/gitlab.go`)
  Test: `pkg/provider/gitlab/gitlab_test.go`'s `TestComments_PagesToExhaustion` and `TestEditComment_SendsBody` against `net/http/httptest`, including a non-2xx that fails without leaking the token.
- [x] 1.4 `Comments` is complete: every implementation returns the whole thread, so the core's find-by-marker scan sees a marker on any page and stops at the first match
  Test: `TestComments_PagesToExhaustion` plus `TestPublishUsage_FindsMarkerOnALaterPage` (the marker on page 2 is edited, not duplicated).

## 2. The store answers the report in one read

- [x] 2.1 `store.RunUsage`, `store.ShiftUsage` and `Store.ShiftUsage(ctx, shiftID)` (`pkg/store`), joining `agent_runs` and `run_llm_accounts` and reading the `usage` JSON keys `ReconcileLLMAccountWithUsage` writes
  Test: `TestStore_ShiftUsageReadsTokensModelsAndAccountState` — a settled Run, an unreconciled Run, a reading Run with no account, and a Run with an empty `usage`.
- [x] 2.2 `UnsettledLLMAccount` gains `ShiftID`, selected from the existing join, so the sweep knows which report to refresh; a NULL `shift_id` is skipped
  Test: `TestStore_UnsettledLLMAccountsCarryShiftID` in `pkg/store/llm_settlement_test.go`, including a NULL-`shift_id` row that is omitted.

## 3. The renderer

- [x] 3.1 `usageReport(usageReportInput) string` (`pkg/shiftengine/usage.go`), pure: header with the `<!-- ploeg:usage-report -->` marker, per-Run rows, totals from the ledger, evidence section, trace alias
  Test: `TestUsageReport_RendersPerRunAndTotals` — a golden body for a two-Run Shift.
- [x] 3.2 Provisional and unavailable are explicit; the authorization is never rendered as cost
  Test: `TestUsageReport_MarksUnreconciledProvisional` and `TestUsageReport_UnreadableSpendIsUnavailable`.
- [x] 3.3 The alias is printed and no key value or `gateway_key_id` is; a Run without an account renders no account state
  Test: `TestUsageReport_PrintsAliasOnly` (asserts a key-shaped string cannot appear) and `TestUsageReport_ReadingRunHasNoAccountState`.
- [x] 3.4 `parseEvidence(reports)` takes the last Run with `Writes`, reads the result from its `summary` marker (`passed`/`failed: <cmd>`/`incomplete: <reason>`) and the commit from its `findings` section; the evidence section states them and points to the writing Run's findings comment; a Run with no verification section renders "verification: not recorded" with no commit
  Test: `TestUsageReport_EvidenceParsesVerificationAndCommit` (fixture byte-identical to `pkg/worker/verify_test.go`'s `markdown()` output) and `TestUsageReport_EvidenceNotRecordedIsExplicit` (no heading, a non-writing last Run, and a pre-ADR-0035 empty `findings`).

## 4. The publish path

- [x] 4.1 `Engine.publishUsageReport(ctx, si)` (`pkg/shiftengine/publish.go`): read `RoundReports` (the thread and the writing Run's evidence) and `ShiftUsage`, reuse `pullRequestThread`, render, find the marker, edit or create
  Test: `TestPublishUsage_CreatesOnceThenEditsInPlace` — a fake forge records one create and subsequent edits, and the thread holds exactly one marker comment.
- [x] 4.2 No pull request, no target, no provider, or the flag off → skip silently, exactly as findings do
  Test: `TestPublishUsage_SkipsWithoutAPullRequest` and `TestPublishUsage_SkipsWhenDisabled`.
- [x] 4.3 A failed read, list or write is logged and skipped; a failed list never posts blind (no duplicate)
  Test: `TestPublishUsage_ReadFailureChangesNothing` and `TestPublishUsage_ListFailureDoesNotPost`.
- [x] 4.4 Call sites: `evaluate` after `publishRound`; `close` in the terminal branch (its `reports` may be nil, so the method loads them itself); the evidence section degrades to "not recorded" when findings are absent
  Test: `TestEngine_PublishesReportAfterARoundWithoutFindings` and `TestEngine_PublishesReportOnClose`.
- [x] 4.5 The report changes no Outcome, close reason or Work Item state
  Test: `TestPublishUsage_DoesNotChangeOutcome` — the Shift and item rows are read before and after.

## 5. The settlement sweep refreshes it

- [x] 5.1 `ShiftEngine` gains `RefreshUsageReport(ctx, shiftID) error`, implemented on `Engine` and wired in `cmd/ploegd/main.go`
  Test: `TestEngine_RefreshUsageReportEditsTheComment`.
- [x] 5.2 `managedSettlementSweep` calls it after a successful `Settle`, and only then
  Test: `TestSettlementSweep_RefreshesAfterSettlementAndNotOnFailure` in `cmd/ploegd`.
- [x] 5.3 A settle for a Run with no pull request is a no-op, not an error
  Test: `TestSettlementSweep_RefreshWithoutPullRequestIsSilent`.

## 6. One chart value

- [x] 6.1 `PLOEG_USAGE_REPORT: "true"`, `PLOEG_REPORT_GRAFANA_URL: ""` and `PLOEG_REPORT_VLOER_URL: ""` added to the existing generic `env:` map in `ops/helm/ploeg/values.yaml` (no `values.schema.json` change, since the map is untyped) and rendered by `templates/deployment.yaml`
  Test: `ops/helm/ploeg` golden render (`scripts/helm-golden.sh check`) plus `helm template -f ci/executor-values.yaml` shows the env.
- [x] 6.2 `cmd/ploegd/main.go` reads it into `Engine.UsageReport`, defaulting on when unset
  Test: `TestEngineUsageReportFromEnv` in `cmd/ploegd`.

## 7. Gates and closure

- [ ] 7.1 `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`, the Helm checks, `openspec validate report-run-usage-on-pull-requests --type change --strict`, and `openspec validate --all --strict` — output in the PR body
  Check: `mise run verify` (or the per-gate commands where the sandbox lacks a toolchain, listed under "Checks left to CI").
- [x] 7.2 No code path mints, settles or blocks an account, and no print path carries a secret — self-review against ADR-0008 and R8
  Check: `go vet` plus the D8 tests in group 3; a reviewer re-reads the diff for a key-shaped string.
- [ ] 7.3 The owner reads the proposal and design and approves, or requests changes
  Check: the pull request's review state; a human merges — the agent does not.
