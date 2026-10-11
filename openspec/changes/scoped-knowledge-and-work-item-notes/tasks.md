# Tasks — scoped-knowledge-and-work-item-notes

## 1. Decisions

- [x] 1.1 ADR-0085 to ADR-0087 with Records and review-calendar rows; `go test ./internal/ledger/` passes
- [x] 1.2 Domain terms Tenant, Client, Knowledge Scope, Work Item Note and Promotion in `docs/domain/model.yaml`; `mise run domain` regenerated the pages
- [ ] 1.3 The owner accepts or rejects ADR-0085 to ADR-0087; on accepting 0086, set `supersedes: 0066`

## 2. Tenant knowledge (Run API seam)

- [ ] 2.1 ploegd configuration: per-Tenant graph ids and token references, no worker setting
- [ ] 2.2 Omnigraph client in ploegd: stored-query reads with commit ids, merge loads with read-back
- [ ] 2.3 `GET /api/v1/runs/{token}/knowledge` with scopes from the Lease's Work Item; `run-api.v1` schema and contract test
- [ ] 2.4 Worker: graph concepts and notes as pack sources, notes under their own heading, graph commit ids in `TaskSpec.knowledge`
- [ ] 2.5 Tests: `TestKnowledgeRead_ServesOnlyTheRunsScopes`, `TestKnowledgeRead_ScopeComesFromTheWorkItemNotTheRequest`, `TestWorkerAndHarnessNeverHoldAGraphToken`, `TestKnowledgeRead_UnavailableGraphDoesNotStopTheRun`

## 3. Work Item Notes (Harness contract seam)

- [ ] 3.1 `learnings[].scope` in `outcomereport.v1` and the Go type, with the refusing schema case
- [ ] 3.2 Migration: `knowledge_note_outbox` and `knowledge_promotions`
- [ ] 3.3 Outbox writer with read-back and the unwritten-row metric
- [ ] 3.4 Promotion acceptance through a short-lived branch, resumable after a restart
- [ ] 3.5 Note retention: 90 days after the root closes unless promoted
- [ ] 3.6 Tests: `TestWorkItemNote_ReachesLaterRunsOfTheSameRootOnly`, `TestPromotion_NothingReachesOtherWorkBeforeAcceptance`, `TestNoteOutbox_WritesOnceAcrossARestart`, `TestPromotion_AcceptanceSurvivesARestart`

## 4. Operator knowledge (operator API seam)

- [ ] 4.1 Knowledge, notes and promotion routes with the acting person; `operator-api.v1` schema
- [ ] 4.2 Tests: `TestOperatorKnowledge_ClientUserSeesOnlyTheirClient`, `TestOperatorKnowledge_PersonOutsideTheConsumersTenantIsRefused`, `TestOperatorNotes_WriteIsIdempotentAndAudited`, `TestOperatorPromotions_OnlyATenantAdminAccepts`

## 5. Conversion

- [ ] 5.1 `ploeg-okf to-omnigraph`/`from-omnigraph` target the `Concept` and `Note` types of design D2, digest round trip kept

## 6. Gates

- [ ] 6.1 Every increment runs the full gate set of `.forgejo/workflows/on_pull_request.yml` and pastes the output in its pull request
