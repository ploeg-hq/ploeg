---
status: accepted
date: 2026-10-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg reaches its first consumer only through published contracts, and the consumer rolls back by pinning an earlier release

## Context and Problem Statement

Ploeg was extracted on 2026-10-03 from `apps/ploeg` in Unfold, an operator workbench that is still Ploeg's only known consumer ([PROVENANCE](../../PROVENANCE.md)). [ADR 0062](0062-github-is-the-independent-project-home.md) and [ADR 0063](0063-independent-releases-start-at-zero-point-one.md) moved the home, module path and release line. Unfold's side moved too: Unfold ADR-0019 replaced its single-version ADR-0004, so Unfold releases only its application and pins a Ploeg release. Unfold ADR-0043 then made an Unfold release one chart that carries the Ploeg chart it was tested with.

Nothing yet listed every place where the two still touch, or said which of them Ploeg keeps as a public contract. CNCF-004 ([#4](https://github.com/ploeg-hq/ploeg/issues/4)) asks for that map, a disposition for each coupling, and a rollback point that keeps Unfold working. What does Ploeg owe its first consumer, what does that consumer own, and where does it go back to when a release breaks it?

## Decision Drivers

* A CNCF Sandbox project has to stand on its own. Its code, tests and releases cannot depend on one downstream product ([charter](../cncf/charter.md)).
* A consumer that uses only covered surfaces gets the compatibility promise of [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) and [ADR 0147](0147-ploeg-serves-the-major-of-each-contract-and-keeps-its-consumers-two-newest-lines-working.md). A consumer that uses anything else gets no promise.
* Unfold has to keep working through the transition, with a named release it can return to.
* [ADR 0069](0069-ploeg-names-none-of-its-consumers.md) still holds outside dated records. This record may name Unfold; the code and guides may not.

## Considered Options

* Map every coupling, keep only the covered ones as public contract, and file the rest as follow-up issues
* Freeze the current coupling as is and document it as supported
* Leave the coupling unmapped until a second consumer appears

## Decision Outcome

Chosen option: "Map every coupling, keep only the covered ones as public contract, and file the rest as follow-up issues". A frozen coupling would make Unfold's use of internal Go packages and database tables a promise that no compatibility check covers. An unmapped coupling cannot show the CNCF that Ploeg stands on its own.

### What independence means

Ploeg is independent of a consumer when all four of these hold:

1. **No consumer-only code path.** No code, test, configuration default, chart value or guide in Ploeg exists for one consumer. A consumer-specific value is configuration. The one file a consumer writes about itself in Ploeg is its expectation record under `docs/contracts/consumers/` (ADR 0147).
2. **Published contracts only.** The consumer depends only on surfaces that [the compatibility policy](../ops/compatibility.md) covers: the HTTP contracts in `docs/contracts/`, `ploeg-mcp` tool names, Helm values, environment variables and the contract bundle. It pins them by version and digest.
3. **No consumer in Ploeg's build.** Ploeg builds, tests and releases without a consumer's checkout, credential or infrastructure. The opt-in `consumers` job of [ADR 0127](0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md) runs only after a candidate's tag and bundle exist, and it only opens pull requests in the consumer's repository.
4. **Removable.** Deleting a consumer's expectation directory is the only change Ploeg needs when that consumer goes away.

### Version, module path and ownership

The policy of ADRs 0062, 0063, [0064](0064-development-is-trunk-and-release-candidates-come-from-it.md) and [0158](0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md) is confirmed, not changed. The Go module is `github.com/ploeg-hq/ploeg`. Releases are `v0.x.y`: candidates `v0.x.y-rc.N` come from `development`, and stable releases come from `main`. Every artifact lives under `ghcr.io/ploeg-hq`: the images, the chart `oci://ghcr.io/ploeg-hq/charts/ploeg` and the contract bundle `ghcr.io/ploeg-hq/ploeg-contract`. Ploeg owns the schemas in `docs/contracts/` and keeps each consumer's two newest recorded lines working. The consumer owns its pins, its expectation record, its own version line (Unfold is on `0.4.0-rc.N`) and its choice to ship Ploeg's chart inside its own.

### Coupling map

The evidence comes from Ploeg `development` at `080b144e` (tag `v0.2.0-rc.81`) and from Unfold `development` at `8ec1854e` (forgejo.webgrip.dev/webgrip/unfold, 2026-10-27). Unfold paths are relative to that repository's root. *Keep* means the coupling stays as a public contract. *Remove* means it leaves Ploeg. *Consumer* means it moves to the consumer, or is already the consumer's alone, and Ploeg promises nothing about it.

| # | Area | Coupling | Ploeg evidence | Unfold evidence | Disposition |
| --- | --- | --- | --- | --- | --- |
| 1 | Runtime | Operator reads: Teams, Work Items, Runs, transcripts, events, summary, facts, allowances, connections, route refusals, unsettled accounts | `docs/contracts/operator-api.v1.schema.json`, `pkg/httpapi` | `apps/unfold/src/ploeg.ts` | Keep |
| 2 | Runtime | Operator Work Item mutations: admit, approve, reject, cancel, requeue, notes, answers, client claim, Asks, chats | `operator-api.v1.schema.json`, ADR 0102, ADR 0082, ADR 0128 | `apps/unfold/src/ploeg.ts` | Keep |
| 3 | Runtime | Contract-major handshake on `GET /api/v1/operator/version`, refusing a Ploeg that lacks a required major | ADR 0147, `pkg/contracts` | `apps/unfold/src/ploeg-compatibility.ts`, Unfold ADR-0044 | Keep |
| 4 | Runtime | Operator Executions (admit, read, commands, events, credential, block, spend, context) and Work Item context | `pkg/httpapi/operator_execution.go:39-45`, `pkg/httpapi/context.go:51`, ADR 0024; no schema definitions | `apps/unfold/src/execution-authority.ts` | Keep; schema missing ([#342](https://github.com/ploeg-hq/ploeg/issues/342)) |
| 5 | Runtime | Operator delivery: candidates, verification, approval, publication | `docs/contracts/operator-delivery.schema.json` | `apps/unfold/src/delivery.ts` | Keep |
| 6 | Runtime | Tracker lookup and the `source` sent on execution admission | `docs/contracts/tracker-execution.v1.schema.json`, ADR 0026 | `apps/unfold/src/task-binding.ts` | Keep |
| 7 | Runtime | Person and channel attribution (`X-Ploeg-Actor`, `X-Ploeg-Acting-User`, `X-Ploeg-Via`, `X-Ploeg-Confirmation`) and the exchanged person token | ADR 0108, ADR 0150, ADR 0160 | `apps/unfold/src/action-records.ts`, `apps/unfold/src/person-proof.ts` | Keep |
| 8 | Runtime | Take-over of a running Run | ADR 0142, `takeOverRequest` | `apps/unfold/src/take-over.ts`; recorded only in the consumer's copy | Keep; record it ([#343](https://github.com/ploeg-hq/ploeg/issues/343)) |
| 9 | Runtime | Worker control for a client-carried Run: the control token and worker id from the claim, then `POST /api/v1/runs/{token}/claude-session` | `docs/contracts/run-api.v1.schema.json`, ADR 0084, ADR 0112 | `apps/unfold/src/linked-handoff.ts:232-235`; `run-api` absent from `requires` | Keep; record it ([#343](https://github.com/ploeg-hq/ploeg/issues/343)) |
| 10 | Runtime | `ploeg-mcp` named to editors by URL and public OAuth client id | `cmd/ploeg-mcp`, ADR 0081 | `apps/unfold/ops/helm/unfold/templates/workbench.yaml:113-117`, `apps/unfold/extensions/vscode` | Keep |
| 11 | Runtime | A demo server that runs ploegd in process through `pkg/httpapi` and `pkg/store` | Go API under `pkg/` is not covered (`docs/ops/compatibility.md`) | `apps/unfold/scripts/unified-demo/main.go:33-58`, its `go.mod` requiring `v0.2.0-rc.81` | Consumer ([#344](https://github.com/ploeg-hq/ploeg/issues/344)) |
| 12 | Build | Ploeg's chart as a Helm dependency at the pinned release | `ops/helm/ploeg` | `apps/unfold/ops/helm/unfold/Chart.yaml`, `Chart.lock` (`0.2.0-rc.81`) | Keep |
| 13 | Build | Values passed through unchanged under `ploeg:`. Unfold sets `database.existingSecret.name` and `executor.litellm.adminUrl` and leaves `supportProfile` at its `P1` default | `ops/helm/ploeg/values.yaml`, `values.schema.json`, ADR 0154 | `apps/unfold/ops/helm/unfold/values.yaml`, `values.live.example.yaml` | Keep |
| 14 | Build | Images: Unfold's image carries no Ploeg binary. Ploeg's images reach a cluster only through the digests in Ploeg's chart | `ops/helm/ploeg/values.yaml` (`ghcr.io/ploeg-hq/ploegd`, `ploeg-worker`, `claude-runner`), ADR 0141 | `apps/unfold/Dockerfile` | Keep |
| 15 | Build | Qualification tests in Ploeg that run TypeScript scripts at fixed paths in a consumer checkout named by `PLOEG_WORKBENCH_PATH`; Unfold's integration check compiles Ploeg's `pkg/httpapi` test binary to run them | `pkg/httpapi/operator_workbench_qualification_test.go`, `operator_delivery_qualification_test.go`, `operator_tracker_qualification_test.go` | `scripts/integration.mjs:26-32`, `apps/unfold/scripts/qualify-*.ts` | Remove ([#345](https://github.com/ploeg-hq/ploeg/issues/345)) |
| 16 | Schema | No shared database. Unfold keeps its own SQLite store, and Ploeg's PostgreSQL schema is Ploeg's alone | `pkg/store/migrations/` | `apps/unfold/src/store.ts` (`node:sqlite`) | Keep |
| 17 | Schema | KPI SQL run against Ploeg's tables through the read-only observer role | `pkg/store/migrations/0072_observer_role.sql`; observer tables not covered | `docs/reference/kpis.md` | Consumer ([#346](https://github.com/ploeg-hq/ploeg/issues/346)) |
| 18 | Schema | The contract bundle pinned by digest and unpacked for contract tests and docs | ADR 0117, `scripts/contract-bundle.sh` | `apps/unfold/ploeg-release.json` (`sha256:c0c6d88e…`), `scripts/ploeg-contract.py` | Keep |
| 19 | Schema | The consumer's expectation record, run on every Ploeg change | `docs/contracts/consumers/unfold/0.4.json`, `TestEveryRecordedConsumerExpectationHolds` | `apps/unfold/src/ploeg-expectations.json` | Keep |
| 20 | UI | Run cards rendered by the consumer from delivery facts and Ploeg's keyed pull request comment | ADR 0079, `PUT /api/v1/operator/work-items/{id}/pull-request-comments/{key}` | `apps/unfold/src/ploeg.ts:1187` | Keep |
| 21 | UI | Opaque card skin and theme names, and report links built from URL templates | ADR 0069, `PLOEG_REPORT_*` in `cmd/ploegd/main.go` | `apps/unfold/src/card-worlds.ts` | Keep |
| 22 | UI | Demo-mode Ploeg projections that the consumer fabricates | none | `apps/unfold/src/ploeg-demo.ts` | Consumer |
| 23 | UI | The table translating Ploeg's words into the consumer's screen words | none | `docs/domain/ploeg-words.json`, Unfold system ADR-0036 | Consumer |
| 24 | Release | The opt-in `consumers` job runs the consumer's recipe and opens its pin pull request | ADR 0127, `.github/workflows/release-consumers.yml`, `scripts/release-consumers.py` | `scripts/consume-ploeg-release.sh`, Unfold ADR-0041 | Keep |
| 25 | Release | Bundle digest, Go module, chart dependency and `Chart.lock` move together; Renovate is off for them; a nightly check fails when the pin falls behind | none | `scripts/ploeg-contract.py` (`pin`, `check`, `behind`), `renovate.json:218` | Consumer |
| 26 | Release | An Unfold release vendors the Ploeg chart it was tested with | ADR 0063 gives Ploeg no shared-version duty | Unfold ADR-0019, ADR-0043, `apps/unfold/ops/helm/unfold/README.md` | Consumer |
| 27 | Docs | The consumer republishes Ploeg's ADRs, domain model and guides from the bundle, and builds its glossary with a generator that Ploeg copied byte for byte | bundle `docs/`, `scripts/generate-domain.py` (`copied-unchanged` in PROVENANCE) | `scripts/renovate-ploeg-docs.sh`, `scripts/generate-domain.py` | Keep |
| 28 | Identity | Ploeg names no consumer outside dated records | `internal/boundary/boundary_test.go`, ADR 0069 | none | Keep |
| 29 | Identity | The origin organization's names are still in Ploeg's tree: the label prefix `ploeg.webgrip.dev/`, the Renovate presets and the semantic-release configuration. They name the parent organization, not the consumer | `ops/helm/ploeg/values.yaml:393`, `renovate.json5`, `.releaserc.cjs` | none | Keep; ownership is CNCF-002's question |
| 30 | Identity | Consumer names, routes, dashboards, skins and the operator branch prefix in Ploeg's code | removed in `60772d1`, ADR 0069 | none | Remove (done 2026-10-04) |
| 31 | Release | Ploeg built, signed and published by the consumer's release under one version | ADR 0062, ADR 0063 | Unfold ADR-0019 supersedes ADR-0004 | Remove (done 2026-10-03) |

Totals: 22 keep, 3 remove, 6 consumer. The minimum reusable core is `ploegd`, `ploeg-worker`, the migrations, the contracts in `docs/contracts/` and the chart. `ploeg-mcp`, `claude-runner`, client-carried Runs, the tracker providers and forges, and the `consumers` release job are optional adapters a deployment can leave out.

Rows 11, 15 and 17 break independence today: row 15 is a consumer-only code path in Ploeg, and rows 11 and 17 are a consumer depending on uncovered surfaces. Rows 4, 8 and 9 are covered couplings that Ploeg's checks cannot see yet. Each one has a follow-up issue.

### Rollback point

The rollback point is **Ploeg `v0.2.0-rc.81`**: commit `080b144e`, contract bundle `ghcr.io/ploeg-hq/ploeg-contract:0.2.0-rc.81@sha256:c0c6d88e9494e72894ca608b23cb44cfc7e1352a5c91f1dd54c7d78463dc424b` and chart `oci://ghcr.io/ploeg-hq/charts/ploeg:0.2.0-rc.81`. Unfold `development` pins exactly this release since `ef8438e1` (2026-10-26). At `8ec1854e`, Unfold's `ploeg-pin` job passed (run 2651, job 2). In the `checks` job of the same run, the Ploeg gates and the integration check, which qualifies a managed execution against this release, passed. The only failure was a 40-second timeout in Unfold's own `test/api-tasks.test.ts`. That run skipped the Helm gates, so `helm template` of Unfold's chart with `values.live.example.yaml` and the `0.2.0-rc.81` dependency was run on 2026-10-27: it renders 12 objects and no P1 refusal. When the first stable `v0.2.0` exists, it replaces this candidate as the rollback point.

A consumer rolls back like this:

1. On a branch of the consumer, run `scripts/consume-ploeg-release.sh 0.2.0-rc.81` (or revert the pin merge). It rewrites `apps/unfold/ploeg-release.json`, the `unified-demo` Go module, the chart dependency and `Chart.lock` together, and `scripts/ploeg-contract.py check` confirms they agree.
2. Merge the change and release the consumer. Its chart then deploys Ploeg `0.2.0-rc.81` by digest.
3. If the release being left migrated the database, `0.2.0-rc.81` runs on that schema only when it is the release directly before. Anything older needs the restore from the backup taken before the upgrade ([compatibility](../ops/compatibility.md#database), [recover from a failed upgrade](../how-to/recover-from-a-failed-upgrade.md)).
4. The consumer's nightly `behind` check stays red until a forward fix. Close the `ploeg/<version>` pull request that Ploeg's `consumers` job opens for the broken release.

### Transition

1. **Done.** Separation and fresh history (ADRs 0062, 0063). Consumer names removed (0069). Contract bundle (0117). Consumer recipe (0127). Compatibility policy (0134). Expectation records (0147). Stable channel (0158).
2. **Now.** This map. Unfold pins `v0.2.0-rc.81`.
3. **Next.** Close [#342](https://github.com/ploeg-hq/ploeg/issues/342) to [#346](https://github.com/ploeg-hq/ploeg/issues/346). Each issue closes when its row reaches the disposition above.
4. **Then.** Unfold pins the first stable release, and that release becomes the rollback point.

### Consequences

* Good, because every place where Ploeg and its first consumer touch has an owner, a disposition and file evidence in both repositories.
* Good, because the consumer has a named release, and a one-command way back to it.
* Bad, because rows 11, 15 and 17 stay out of line with independence until their issues close, and the consumer has to change its demo, integration check and KPI queries.
* Bad, because the map is a snapshot. A new consumer call or Ploeg route can drift from it unnoticed until the next review, except where the expectation check catches it.

### Confirmation

* `go test ./internal/boundary/` fails when a consumer's name appears outside dated records (row 28).
* `go test ./pkg/httpapi/ -run TestEveryRecordedConsumerExpectationHolds` and `go test ./internal/compat/` run in `mise run verify` and CI. They fail on a break of any recorded interaction (rows 1 to 9 and 19).
* The consumer's `ploeg-pin` job runs `scripts/ploeg-contract.py check`. It fails when the pin, the Go module, the chart dependency and the unpacked bundle disagree (rows 12, 18 and 25).
* Review: a pull request that adds an operator route, a chart value or a test that reads a consumer's checkout is checked against this map, and at `review-by` the map is rebuilt from both repositories' current commits.

## Pros and Cons of the Options

### Freeze the current coupling as is and document it as supported

* Good, because nothing changes for the consumer.
* Bad, because it would promise the Go API of `pkg/httpapi` and `pkg/store` and the observer tables, which the compatibility policy deliberately excludes, and it keeps consumer-only tests in Ploeg.

### Leave the coupling unmapped until a second consumer appears

* Good, because it costs nothing now.
* Bad, because CNCF-004's acceptance criteria and the Sandbox review both need the map, and Unfold would have no named rollback point.

## Re-evaluation triggers

* All of #342 to #346 are closed, which turns rows 4, 8, 9, 11, 15 and 17 into kept or removed couplings.
* Ploeg publishes a stable `v0.2.0`, which moves the rollback point.
* Unfold's `sessions-run-in-ploeg` change lands and its sessions execute in `ploeg-worker`, a new runtime coupling.
* A second consumer records expectations under `docs/contracts/consumers/`.
* The compatibility policy starts covering any part of the Go API under `pkg/` or the observer tables.

## More Information

* Source issue: [CNCF-004 #4](https://github.com/ploeg-hq/ploeg/issues/4). Follow-ups: [#342](https://github.com/ploeg-hq/ploeg/issues/342), [#343](https://github.com/ploeg-hq/ploeg/issues/343), [#344](https://github.com/ploeg-hq/ploeg/issues/344), [#345](https://github.com/ploeg-hq/ploeg/issues/345), [#346](https://github.com/ploeg-hq/ploeg/issues/346).
* Unfold records cited: ADR-0002 (Ploeg is the only engine), ADR-0004 (superseded by ADR-0019), ADR-0019, ADR-0041, ADR-0043 and ADR-0044 under `docs/adr/` in forgejo.webgrip.dev/webgrip/unfold.
* 2026-10-27: proposed by the maintainer for CNCF-004. It takes effect when the owner merges it into `development`.
