## 1. Source contract

- [x] 1.1 Publish additive lookup and admission source types/schema, scoped reads and fresh provider validation.
- [x] 1.2 Retain fetched Scope for thin events and expose explicit provider open state and API-root identity.

## 2. Exclusive admission

- [x] 2.1 Add migration 0014 and transactional pristine Work Item adoption with idempotent source provenance.
- [x] 2.2 Fence unattended Shift creation, claims, evaluation and expiry; align legacy KEDA counts with the durable ownership fence and retire pending Runs atomically.
- [x] 2.3 Preserve operator ownership on tracker refresh through pause, cancellation, expiry and completion.

## 3. Qualification

- [x] 3.1 Add real PostgreSQL concurrency, replay, stale-source/target and no-paid-effects regressions; prove the scope-retention test fails before its fix.
- [x] 3.2 Run gofmt, go vet, go build, go test and internal/ledger through mise.
- [x] 3.3 Run Helm lint and existing chart golden checks; confirm all executor renderings remain aligned.
- [x] 3.4 Run De Vloer cross-service qualification and record evidence boundaries; validate OpenSpec.
