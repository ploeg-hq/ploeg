# Tasks — scoped-knowledge-and-work-item-notes

The owner chose on 2026-10-12 to build behind an off-by-default setting while the measurement spike runs, starting with Work Item Notes end to end (slice 1). Slice 2 serves graph concepts; Promotions and operator routes are slice 3.

## 1. Decisions

- [x] 1.1 ADR-0087 to ADR-0089 with Records and review-calendar rows; `go test ./internal/ledger/` passes
- [x] 1.2 Domain terms Tenant, Client, Knowledge Scope, Work Item Note and Promotion in `docs/domain/model.yaml`; `mise run domain` regenerated the pages
- [ ] 1.3 The owner accepts or rejects ADR-0087 to ADR-0089 after the measurement spike VIK-1859 (owner, 2026-10-11); on accepting 0088, set `supersedes: 0066`

## 2. Tenant knowledge (Run API seam)

- [x] 2.1 ploegd configuration: per-Tenant graph ids and token references, no worker setting — slice 1: `PLOEG_OMNIGRAPH_URL`, `PLOEG_NOTES_GRAPH`, `PLOEG_OMNIGRAPH_RUN_TOKEN` for the default Tenant; a per-Tenant map waits for VIK-1876
- [x] 2.2 Omnigraph client in ploegd: stored-query reads with commit ids; writes are keyed upserts through stored mutations with read-back (`pkg/omnigraph`, `pkg/knowledgegraph`)
- [x] 2.3 `GET /api/v1/runs/{token}/knowledge` with scopes from the Lease's Work Item; `run-api.v1` schema and contract test — notes in slice 1, concepts in slice 2
- [x] 2.4 Worker: graph concepts and notes as pack sources, notes under their own heading, graph commit ids in `TaskSpec.knowledge`
- [x] 2.5 Tests: `TestKnowledgeRead_ServesOnlyTheRunsScopes`, `TestKnowledgeRead_ScopeComesFromTheWorkItemNotTheRequest`, `TestWorkerAndHarnessNeverHoldAGraphToken`, `TestKnowledgeRead_UnavailableGraphDoesNotStopTheRun`

## 3. Work Item Notes (Harness contract seam)

- [x] 3.1 `learnings[].scope` in `outcomereport.v1` and the Go type, with the refusing schema case
- [ ] 3.2 Migration: `knowledge_note_outbox` and `knowledge_promotions` — slice 1 added `work_item_note_outbox` (0051); Promotions get their own migration
- [x] 3.3 Outbox writer with read-back and the unwritten-row metric (`PloegWorkItemNotesUnwritten`)
- [ ] 3.4 Promotion acceptance through a short-lived branch, resumable after a restart
- [ ] 3.5 Repository Promotion creates an approved knowledge Work Item; the pack keeps the repository's file over the same graph concept (D8) — the pack rule is in slice 2 (`TestKnowledgePack_RepositoryFileWinsOverTheSameGraphConcept`)
- [ ] 3.6 Note retention: 90 days after the root closes unless promoted
- [ ] 3.7 Tests (slice 1 has the first and third): `TestWorkItemNote_ReachesLaterRunsOfTheSameRootOnly`, `TestPromotion_NothingReachesOtherWorkBeforeAcceptance`, `TestNoteOutbox_WritesOnceAcrossARestart`, `TestPromotion_AcceptanceSurvivesARestart`, `TestPromotion_RepositoryScopeCreatesAKnowledgeWorkItem`, `TestKnowledgePack_RepositoryFileWinsOverTheSameGraphConcept`

## 4. Operator knowledge (operator API seam)

- [ ] 4.1 Knowledge, notes and promotion routes with the acting person; `operator-api.v1` schema
- [ ] 4.2 Tests: `TestOperatorKnowledge_ClientUserSeesOnlyTheirClient`, `TestOperatorKnowledge_PersonOutsideTheConsumersTenantIsRefused`, `TestOperatorNotes_WriteIsIdempotentAndAudited`, `TestOperatorPromotions_OnlyATenantAdminAccepts`

## 5. Conversion

- [ ] 5.1 `ploeg-okf to-omnigraph`/`from-omnigraph` target the `Concept` and `Note` types of design D2, digest round trip kept

## 6. Gates

- [ ] 6.1 Every increment runs the full gate set of `.forgejo/workflows/on_pull_request.yml` and pastes the output in its pull request
