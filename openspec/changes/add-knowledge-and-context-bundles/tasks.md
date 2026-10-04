# Tasks — add-knowledge-and-context-bundles

## 1. Decisions

- [x] 1.1 ADR-0065 to ADR-0068 with Records and review-calendar rows; `go test ./internal/ledger/` passes
- [x] 1.2 The owner accepts ADR-0067 and ADR-0068 (and Unfold ADR-0021, Vloer ADR-0038)
  Evidence: status `accepted` in both files and the Records index, 2026-10-04.
- [ ] 1.3 The owner accepts or rejects ADR-0065 and ADR-0066 (and Unfold ADR-0020) after the measurement spike VIK-1859

## 2. Knowledge pack

- [x] 2.1 `pkg/okf`: parse, validate, link graph, write with indexes
  Evidence: `pkg/okf/okf.go`, `pkg/okf/okf_test.go`.
- [x] 2.2 `pkg/knowledge`: selection, budget, digests, pack writing, Omnigraph conversion
  Evidence: `pkg/knowledge/pack.go`, `pkg/knowledge/omnigraph.go`, `pkg/knowledge/pack_test.go` (`TestOmnigraph_RoundTripsABundle`).
- [x] 2.3 Worker briefs from the repository bundle and configured bundles; prompt section; `TaskSpec.knowledge`
  Evidence: `pkg/worker/knowledge.go`, `pkg/worker/knowledge_test.go`, `pkg/harness/contract_test.go`.
- [x] 2.4 `knowledge/`: a first bundle about Ploeg
- [ ] 2.5 Worker reads the Tenant graph by stored query with commit provenance (after the Omnigraph spike)

## 3. Learnings

- [x] 3.1 `OutcomeReport.learnings` and schema; worker validation, cap and outbox bundle
  Evidence: `TestLearningConcepts_KeepsValidProposalsUnderLearned`, `TestKeepLearnings_WritesAReviewableBundlePerRun`, `TestResolveOutcome_KeepsTheAgentsLearnings`.
- [ ] 3.2 ploegd stores learnings; Vloer reviews them under Needs you

## 4. Context bundles

- [x] 4.1 `pkg/contextbundle`: sniff, inspect, safe unpack under the limits
  Evidence: `pkg/contextbundle/contextbundle.go`, `contextbundle_test.go` (traversal, absolute paths, links in zip and tar, bombs, too many files, too large, duplicates, nested archives, single files, empty archives, hardlinks, fifos, truncated archives).
- [x] 4.2 Migration and store methods for `work_item_context`
  Evidence: `pkg/store/migrations/0038_work_item_context.sql`, `pkg/store/context.go` (`AddWorkItemContext`, `ListWorkItemContext`, `GetWorkItemContext`, `RunContext`, `RunContextItem`), `pkg/store/context_test.go`.
- [x] 4.3 Operator routes (attach, list; execution attach) and the Run download route
  Evidence: `pkg/httpapi/context.go`, `pkg/httpapi/context_test.go`.
- [x] 4.4 Claim references; worker download, verify, unpack, prompt section, `TaskSpec.context`
  Evidence: `pkg/worker/context.go`, `pkg/worker/context_test.go`; `TestContextBundlesEndToEnd` in `pkg/httpapi/context_e2e_test.go`.
- [x] 4.5 OKF concepts inside context join knowledge selection
  Evidence: `knowledgeSources` extra sources in `pkg/worker/knowledge.go`; the OKF-in-zip case in `pkg/worker/context_test.go`.
- [x] 4.6 `ploeg-okf context inspect`
  Evidence: `cmd/ploeg-okf/main.go`, `cmd/ploeg-okf/main_test.go`.
- [ ] 4.7 `operator-api.v1.schema.json` describes the context routes
- [ ] 4.8 Context routes get their own read and write timeouts (ploegd's 30 s read timeout bounds a 20 MiB upload)
- [ ] 4.9 Helm values for `PLOEG_CONTEXT_MAX_BYTES` and `PLOEG_CONTEXT_MAX_TOTAL_BYTES`
- [ ] 4.10 Apply now: stop the running Run after the person confirms its spend, and retry with all context attached so far (ADR-0068)
- [ ] 4.11 Hold a context item the secret scan flags until a person removes it or confirms it (ADR-0067, after spike VIK-1866)

## 5. Gates

- [x] 5.1 `go vet ./...`, `go test ./...` (38 packages), `gofmt -l .` clean on 2026-10-04
