# Local qualification, 2026-09-11

Implementation was validated on the owner's shared uncommitted `development` checkout after the planning gate. No live model, tracker mutation, forge publication or deployment was performed.

| Check | Result |
|---|---|
| `mise exec -- go test ./...` | Passed, including real PostgreSQL Store, HTTP and Shift engine suites; HTTP 13.388s, Store 16.026s, Shift engine 14.301s. |
| `mise exec -- go vet ./...` | Passed. |
| `mise exec -- go build ./...` | Passed. |
| `mise exec -- helm lint ops/helm/ploeg` | Passed. |
| `mise exec -- sh scripts/helm-golden.sh check` | Four checked renders passed. |
| `mise exec -- go test ./pkg/config -run TestOperatorChart -count=1` | Passed after adding separate verifier and delivery-policy render assertions. |
| `mise exec -- openspec validate bind-tracker-execution` | Passed. |
| `git diff --check` | Passed. |

The [scope regression](../../../pkg/httpapi/tracker_scope_test.go) first failed with `thin event discarded authoritative scope: got ""`; it passed after retaining the authoritative fetched Scope. The [Store tests](../../../pkg/store/operator_source_test.go) cover canonical identity, one winner across sessions, idempotent replay, native revision/timestamp/scope/target/team fences, non-pristine work, claim-lock inversion, retired pending Runs, legacy scheduler exclusion and an upsert deliberately waiting on a concurrent ownership transaction. The [HTTP tests](../../../pkg/httpapi/operator_source_test.go) exercise registered source and repository matching, provider freshness, explicit open state, team scope and schema validation with real PostgreSQL and local provider HTTP fixtures.

After review, the HTTP source suite passed again in 9.382s with current routing changes, absent routing and changed container Team pins rejected before admission. Identical accepted admission replay remained valid with routing subsequently removed. The real cross-service tracker qualification then passed again in 8.381s. HTTP vet and the full build also passed after that correction.

The final combined `TestOperator(Delivery|Tracker)WorkbenchQualification` run passed in 10.830s after the delivery UI review. Tracker admission again produced one canonical Work Item and operator Run. The separate delivery flow used real Git objects and a fresh Docker verifier, ran two tests, retained one verifier invocation across restart and replay, and recorded candidate-bound approval with publication disabled. Both paths recorded zero model calls and zero spend.

The opt-in [cross-service test](../../../pkg/httpapi/operator_tracker_qualification_test.go) passed using the sibling De Vloer `scripts/qualify-tracker-authority.ts`. Set `PLOEG_WORKBENCH_PATH` to its absolute checkout and run `mise exec -- go test ./pkg/httpapi -run TestOperatorTrackerWorkbenchQualification -v -count=1`. Its structured result recorded two admission requests after deliberately losing the first committed response, one created admission, one Work Item before and after, one operator Run, zero model calls and zero spend. It also checked separate content/native revisions, stale preview rejection, import deduplication, repeated Start rejection and background supervision identity. Ploeg's pre-existing pending writer roster was retired and could not be claimed afterward.

That cross-service test uses actual PostgreSQL, Ploeg HTTP and De Vloer application import/start behavior. Vikunja is a local HTTP fixture and the workbench runtime is deterministic; it produces no repository changes and attempts no publication. Native provider live-service behavior and a real inference run remain outside this qualification.
