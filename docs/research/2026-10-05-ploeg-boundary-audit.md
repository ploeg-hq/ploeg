# Ploeg boundary audit

Date: 2026-10-05. Source: ploeg `development` at `34e6d57`. Read-only audit.

The owner asked two questions:
1. What is Ploeg doing outside its business of admitting, budgeting and executing agent work?
2. Where do its dependencies point the wrong way?

The decision is [ADR-0074](../adrs/0074-ploeg-publishes-execution-facts-and-consumers-own-presentation-and-delivery-analytics.md).

## Summary

Almost everything outside the business is a **card feed dressed as facts**. ADRs 0047 and 0051–0058, and 0061, each added an ingest pipeline: tracker status history, epics, deploys, pull request activity and CI runs, changed paths and reverts, plus indexes by person.

Only the card reads them. `pkg/shiftengine`, `pkg/worker` and `pkg/harness` import none of `rarity`, `playkpi`, `flow`, `gate` or `cardimage`. The only readers are `pkg/store/card*.go`, `cracks.go`, `epics.go`, `play_pipeline.go`, `gates.go`, `statuses.go` and `deployments.go`. Remove the card, and almost all the pipelines go with it.

| Measure | Out of business |
| --- | --- |
| Production lines | about 13,000 |
| Test lines | about 10,000 |
| `pkg/store` production lines | about 5,400 of 12,087 (45%) |
| `operator-api.v1.schema.json` | 44 of 80 definitions, 3,205 of 5,033 lines |
| Tables or column groups | 15 |

## Package classification

| Package | Production / test lines | Verdict |
| --- | --- | --- |
| `pkg/cardimage` | 1030 / 385 | Out: SVG rendering, palette, comment body, `<!-- ploeg:run-card -->` |
| `pkg/rarity` | 513 / 272 | Out: tiers from common to legendary |
| `pkg/playkpi` | 1274 / 567 | Out: pull request timeline, slow CI jobs, indentation complexity, change shape |
| `pkg/flow` | 783 / 471 | Out: team working calendar and flow figures "for the Run card" |
| `pkg/gate` | 265 / 131 | Out: delivery gates and bounces from tracker statuses |
| `pkg/store` | 12087 / 8842 | Mixed: card.go 1161, card_rarity 645, cracks 737, play_pipeline 452, epics 376, crack_facts 325, card_grade 308, deployments 241, card_list 240, card_condition 213, statuses 157, changed_paths 157, card_comments 156, gates 123, card_flow 106 |
| `pkg/httpapi` | 5135 | Mixed: about 1,380 card-only lines (card_comment, card_rarity, cracks, epics, gates, play_pipeline, operator_card(s), deploys) |
| `pkg/config` | 1202 | Mixed: card style, rarity, shape, bots, referees, gates, status kinds, working hours |
| `pkg/provider` | 5016 | Mixed: about 1,800 lines of capabilities used only by cards:<br>• activity readers (1147)<br>• `AttachToComment` (255)<br>• `RelationReader` (102)<br>• ancestry (103)<br>• `BoardReader` (99)<br>• change readers (301) |
| `pkg/forgefacts` | 186 | Mixed: `Facts()` serves the review loop; `RecordChangedPaths` serves cards |
| `pkg/knowledge` | 484 | Mixed: `pack.go` is in business (ADR-0065); `omnigraph.go` (171) adapts a third-party graph no Run uses |
| `cmd/ploeg-okf` | 195 | Mixed: validate and pack are in business; the graph import and export commands are not |
| `cmd/ploegd` | 1291 | Mixed: card wiring in `operator.go:67-140`, `main.go:362,451`, `sweep.go:100-101` |
| `pkg/shiftengine` | 1928 | In, with one wrong-way call (below) |

**In business:**
* `pkg/harness`, `pkg/worker`, `pkg/plan`, `pkg/work`, `pkg/target`
* `pkg/followup`, `pkg/contextbundle`, `pkg/okf`
* `pkg/litellm`, `pkg/llmbroker`, `pkg/forgebroker`, `pkg/sandboxlaunch`
* `cmd/ploeg-worker`, `internal/ledger`, `internal/boundary`

## Findings, most wrong first

1. **Card assembly and its API.** The routes are `operator.go:197-198` (card, cards) and `cracks.go:29-35` (seven crack routes). A card carries style, finish ("always matte"), grade, rarity, demo, set and evolved. Unfold already consumes these (`apps/unfold/src/ploeg.ts:795,1063-1155`). ADRs 0046, 0049, 0050 and 0061.
2. **People analytics.** These attribute outcomes to people, which is not execution:
   * `CardSteward`, `CardPerson`, `Roster` and `Crew`, with merger, reviewer, qa, acceptor and cosigner roles (`card.go:44-46,209-225`);
   * a card list by `member=<login>` (ADR-0054);
   * migration 0029, which exists only to add indexes by person;
   * `TeamCards.Referees` (`config.go:228`).

   The pull request's `mergedBy` and its reviews stay as facts.
3. **Cracks** (ADR-0052). A blame workflow with confirm, dispute and resolve, and referees:
   * code: 737 + 325 + 213 store lines and 281 handler lines;
   * tables: `card_cracks`, `pull_request_files` and `pull_request_reverts` (migration 0027);
   * configuration: `hotfixLabels`.
4. **Delivery gates and tracker status history** (ADRs 0051, 0057).
   * Code: `pkg/gate` and `pkg/flow`, plus `gate_transitions` and `status_transitions`.
   * Hot path: `observeGate` and `observeEpics` run on the tracker webhook, twice per assigned event (`server.go:231-232,269,285`), adding board reads.
   * Admission needs only assign, unassign and close.
   * Vikunja keeps no status history, so these rows cannot be recomputed.
5. **Deploy tracking** (ADR-0047). It is lead-time analytics that only the card and rarity reveal read:
   * `POST /api/v1/deploys`, `deploy-api.v1.schema.json`, and `SweepDeployChecks` through forge ancestry;
   * tables `deployments`, `pull_request_deployments` and `deployment_checks`.
6. **Pull request activity, CI runs and KPIs** (ADR-0058). Migration 0033 stores derived output (`pull_requests.kpis`, `kpis_computed_at`, `shape`) in a fact table. The CI state on the pushed head stays, because ADR-0040 and ADR-0070 use it.
7. **Rarity** (ADR-0056).
   * Where: `card_rarity` and the `pull_request_files` additions and deletions; configuration `sensitivePaths`, `attentionPaths` and `sizeExclude`.
   * Revealed values are frozen at release and cannot be recomputed.
8. **Epics as card sets** (ADR-0053).
   * What: `work_item_epics` and `RelationReader`, whose own context names the card contract.
   * Gap: first-seen times cannot be reconstructed.
9. **Consumer UI configuration.** `CardStyle{Skin, Theme}` (`config.go:78-81,128-138,170`) is documented as "Ploeg passes it through... attaches no meaning". It sits beside `DefaultCardSkin`, `cardShape` and the card `Bots` list. The loader uses `KnownFields(true)` (`config.go:279`), so a removed key must be accepted with a warning for one release.
10. **The pull request card comment** (ADR-0055).
    * Where: `card_comment.go`, `card_comments`, and `AttachToComment` in both forges.
    * When: `publishMergedCard` runs in the forge webhook path (`pull_request_facts.go:51`), and the sweeper runs every tick.
    * Also: `cardimage/comment.go:17` still matches any `<prefix>:run-card` marker.

## Wrong-way dependencies

* `pkg/shiftengine/review.go:160-168`: the review loop calls `RecordChangedPaths` and `Store.RefreshPullRequestKPIs`, and logs an error when the KPIs are not recomputed. Orchestration drives presentation.
* `pkg/store` imports `playkpi`, `rarity`, `flow` and `gate`, and computes formulas inside fact transactions.
* `pkg/config` imports `rarity` and `playkpi`; `pkg/httpapi/operator.go:21` imports `rarity`.
* The operator execution `demo` flag (`operator_execution.go:148-154`, `operator_delivery.go:346`) uses a consumer's word for a publication mode.
* Comments name a dashboard tool. "Grafana joins on…" appears in `litellm/client.go:21`, `forgebroker/forgejo.go:83`, `llmbroker/litellm.go:18` and `taskspec.v1.schema.json:34`.
* The OpenSpec design for the usage report (`openspec/changes/report-run-usage-on-pull-requests/design.md:194-198`) still names `PLOEG_REPORT_GRAFANA_URL` and a `VLOER_URL`. The implementation already uses generic URL templates (`main.go:276-278`).

## The opposite boundary

One fact Ploeg owns but under-exposes: live spend for a running Run reaches consumers only through the card (ADR-0049). It belongs on the operator Run resource, beside `/executions/{id}/spend`.

Settlement, delivery facts from the forge (ADR-0059) and CI on the pushed commit (ADR-0070) are Ploeg's own and stay.

## Data to export before dropping

None of these can be recomputed:
* `card_cracks` (human confirmations)
* `card_rarity.revealed`
* `card_comments` (forge comment ids, so Unfold can keep editing posted comments)
* `work_item_epics` (first-seen times)
* `status_transitions`, `gate_transitions`
* `deployments`

## Proposed gate

`internal/boundary` already enforces ADR-0069 for consumer names (`boundary_test.go:19`). Extend it with `TestPloegSpeaksOnlyItsDomain`.

**What it scans:**
* exported identifiers, through `go/ast`;
* route patterns;
* contract property and definition names;
* `yaml:` tags;
* new migration table and column names;
* chart values keys.

**Words it rejects** (whole words only): `card`, `rarity`, `grade`, `crack`, `skin`, `theme`, `palette`, `season`, `collection`, `kpi`, `dashboard`, `leaderboard`, `steward`, `referee`, `cosigner`, `grafana`.

**Words it allows:** `pack` (OKF knowledge pack), `roster` (team configuration) and `gate` (readiness and checks gates) stay.

**A second test** checks imports with `go list -deps`: `pkg/store`, `pkg/shiftengine` and `pkg/worker` may import only an allowlist of domain packages.

**Mutation test:** one forbidden identifier, one route and one schema property each must fail, and an in-domain "knowledge pack" must pass.

## Removal estimate

| Concern | Production lines |
| --- | --- |
| Forge activity, CI and KPIs | 2,900 |
| Card assembly, grade and list | 2,600 |
| Cracks | 1,600 |
| Card image and comment | 1,500 |
| Gates, status history, flow calendar | 1,500 |
| Rarity | 1,200 |
| Deploy tracking | 650 |
| Epics | 520 |
| Configuration and wiring | 500 |
| Memory graph adapter | 250 |
