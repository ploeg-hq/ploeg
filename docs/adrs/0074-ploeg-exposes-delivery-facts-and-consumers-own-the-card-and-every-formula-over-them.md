---
status: proposed
date: 2026-10-05
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg publishes execution facts, and consumers own presentation and delivery analytics

## Context and Problem Statement

Ploeg's business is to admit, budget and execute agent work. Since August it has also grown a Run card and the pipelines that feed it:

* **The card itself:** assembly, a grade, a rarity tier frozen at release, an SVG image posted as a pull request comment, and a card list searched by person.
* **Cracks:** a confirm, dispute and resolve workflow with referees.
* **Feed pipelines:**
  * delivery gates and status history derived from tracker statuses;
  * a team working-time calendar;
  * deploy tracking;
  * epics as card sets;
  * harvested pull request activity, CI runs and change-shape KPIs.

The [boundary audit](../research/2026-10-05-ploeg-boundary-audit.md) measured this at about 13,000 production and 10,000 test lines. That is about 45% of `pkg/store`, and 3,205 of the 5,033 lines of `operator-api.v1.schema.json`.

The core never reads any of it. `pkg/shiftengine`, `pkg/worker` and `pkg/harness` import none of `rarity`, `playkpi`, `flow`, `gate` or `cardimage`. The dependencies point the wrong way:

* the review loop recomputes card KPIs (`pkg/shiftengine/review.go:160-168`);
* `pkg/store` computes rarity and flow formulas inside fact transactions;
* `pkg/config` carries a consumer's card skin and theme, "passed through, no meaning";
* a webhook handler renders and posts a card image.

The owner's view: the concept of a card should be unfamiliar to Ploeg. Where is the line between what Ploeg records and what its consumers make of it?

## Decision Drivers

* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). A card, a skin, a rarity tier and a season are a consumer's product, not execution.
* **Dependencies point from the consumer to Ploeg, never back.** The execution core must not call presentation or analytics code.
* **Ploeg keeps the facts only it can observe** and that its own decisions use: authorized and settled spend, Run outcomes, what the forge says about the pull request Ploeg opened ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md), [ADR-0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md)).
* **Facts others can observe directly are theirs to read.** A tracker's status history, a forge's reverts and CI jobs, and a pipeline's deploys can be read by a consumer itself.
* **Data that cannot be recomputed is exported before anything is dropped.**

## Considered Options

* Ploeg publishes execution facts and events, and consumers own every card, KPI and delivery analytic
* Keep the card in Ploeg, and fix only the wrong-way calls
* Move the card into a separate Ploeg-owned service beside ploegd

## Decision Outcome

Chosen option: "**Ploeg publishes execution facts and events, and consumers own every card, KPI and delivery analytic**".

### What Ploeg keeps

* Work Items, Shifts, Rounds, Runs, Leases and Outcomes, with the operator resources that already expose them.
* Usage per Run and the Shift's authorized, reserved and settled pool ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [ADR-0045](0045-keep-run-usage-and-merge-facts.md)). Live spend for a running Run moves from the card to the operator Run resource, beside `/executions/{id}/spend`. Today it reaches consumers only through the card ([ADR-0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md)), which is the one place Ploeg under-exposes its own facts.
* The pull request facts its own decisions use: state, merge, merged-by, reviews and changes-requested, CI state on the pushed head ([ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md), [ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md), [ADR-0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md)).
* The usage report on the pull request (ADR-0060's outbox carries it). It renders facts, not a card, and its links are generic URL templates.
* Audit events on `GET /api/v1/operator/events`. These are the feed a consumer builds from: `run.finished` with usage, `pull_request.merged`, `shift.closed`.

### What leaves Ploeg, and who owns it

| Concern | Today in Ploeg | Owner |
| --- | --- | --- |
| Card assembly, grade, card list by person | `pkg/store/card*.go`, `/work-items/{id}/card`, `/cards` ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md), [0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md), [0054](0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md), [0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md)) | Unfold |
| Rarity | `pkg/rarity`, `card_rarity` ([ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md)) | Unfold |
| Card image and pull request card comment | `pkg/cardimage`, `card_comments`, `AttachToComment` ([ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md)) | Unfold, posting with its own forge identity |
| Cracks, referees, cosigners | `pkg/store/cracks.go`, crack routes ([ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md)) | Unfold, reading reverts and changed files from the forge |
| Delivery gates, status history, working-time calendar | `pkg/gate`, `pkg/flow`, `gate_transitions`, `status_transitions` ([ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md), [0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md)) | Unfold, or the tracker's own reports |
| Deploy tracking | `POST /api/v1/deploys`, `deployments` ([ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)) | The pipeline sends deploys to Unfold or to observability |
| Pull request activity, CI runs, change shape, KPIs | `pkg/playkpi`, `play_pipeline`, migration 0033 ([ADR-0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md)) | Unfold, reading the forge |
| Epics as card sets | `pkg/store/epics.go`, `work_item_epics` ([ADR-0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md)) | Unfold, reading tracker relations |
| Consumer UI configuration | `CardStyle`, `DefaultCardSkin`, `cardShape`, card `Bots` and `Referees` in `pkg/config` | The consumer's own configuration |
| A third-party memory graph adapter | `pkg/knowledge/omnigraph.go`, `ploeg-okf to-omnigraph` / `from-omnigraph` | That graph's own tooling; Ploeg keeps OKF packs ([ADR-0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md)) |

Three smaller fixes come with it:
* The operator execution `demo` flag becomes a neutral publication mode.
* Comments naming a dashboard tool are reworded to name the published correlation key.
* The legacy `[a-z0-9-]+:run-card` marker match is removed.

### Order

1. **Unfold first.** Unfold assembles its own card from Ploeg's operator events and resources, plus its own tracker and forge reads, and pins a Ploeg release that still serves the card routes.
2. **Release N.** Ploeg stops writing card-only data and marks the card, crack, gate, epic and deploy routes deprecated. It accepts the removed configuration keys with a warning (the loader rejects unknown keys, `pkg/config/config.go:279`). A read-only export command writes the data that cannot be recomputed:
   * crack confirmations;
   * revealed rarity;
   * the forge ids of posted card comments;
   * epic first-seen times;
   * status and gate history;
   * deploys.
3. **Release N+1.** The next migration drops the card-only tables and columns, the card definitions leave `operator-api.v1` with their Go types in the same change, and the configuration keys are removed. The contract change is breaking and is versioned as such.

On ratification, the card records listed above (0046, 0049–0058, 0061) are marked `rejected` in the Records index and their files keep their text, as the ledger allows for proposed records. [ADR-0045](0045-keep-run-usage-and-merge-facts.md) and [ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) remain, narrowed to the facts listed under "What Ploeg keeps". [ADR-0069](0069-ploeg-names-none-of-its-consumers.md) is unchanged; this record extends it from names to concepts.

### Consequences

* **Good:**
  * About 13,000 production and 10,000 test lines, 3,200 schema lines and 15 tables or column groups leave Ploeg.
  * `pkg/store` shrinks by about 45%.
  * The tracker webhook path stops calling extra board readers on every assignment.
  * The execution core imports no presentation or analytics code.
* **Good:** each concern lives with the system that uses it, so a card or KPI change no longer needs a Ploeg release, migration and contract version.
* **Good:** the duplicated rarity and calendar formulas in Unfold become the only copies, so there is nothing to keep in step.
* **Bad:**
  * Unfold must read the forge and tracker for facts Ploeg collected for it, with its own credentials and rate limits.
  * Unfold also takes on the card comment, which needs a forge identity of its own.
* **Bad:**
  * Two releases and an export are needed, and the breaking contract change must be coordinated with Unfold's pin.
  * Status and gate history that Vikunja does not keep exists only in the export.
* **Bad:** delivery analytics are no longer in the same database as spend. A consumer that wants cost per deployed change joins two sources.

### Confirmation

* **Vocabulary test.** `internal/boundary` gains `TestPloegSpeaksOnlyItsDomain`.
  * It scans:
    * exported Go identifiers in `pkg/` and `cmd/`;
    * route patterns;
    * property and definition names in `docs/contracts/*.json`;
    * `yaml:` tags in `pkg/config`;
    * table and column names in migrations after the current last one;
    * `ops/helm/ploeg/values.yaml` keys.
  * It fails on whole-word `card`, `rarity`, `grade`, `crack`, `skin`, `theme`, `palette`, `season`, `collection`, `kpi`, `dashboard`, `leaderboard`, `steward`, `referee`, `cosigner` or `grafana`.
  * Records (ADRs, research and existing migrations) are exempt.
  * It is shown to work by injecting a forbidden identifier, route and schema property, each of which must fail. An in-domain case, such as "knowledge pack", must pass.
* **Import test.** A test runs `go list -deps` on `pkg/store`, `pkg/shiftengine` and `pkg/worker` and fails on any import outside an allowlist of domain packages.
* **Schema check.** After Release N+1, `operator-api.v1.schema.json` has no card or crack definitions, and the existing schema tests still pass.
* **CI.** `mise exec -- go test ./...` runs all three.

## Pros and Cons of the Options

### Keep the card in Ploeg, and fix only the wrong-way calls

* Good, because it is the smallest change, and Unfold keeps a single source for its card.
* Bad, because Ploeg keeps owning a product's vocabulary and formulas. Every card change still needs a Ploeg release.
* Bad, because the pipelines that exist only for the card stay in the execution store.

### Move the card into a separate Ploeg-owned service beside ploegd

* Good, because the execution core would be clean, and the card would be computed in one place.
* Bad, because Ploeg would still own a consumer's product, now as a second deployable.

## Re-evaluation triggers

* A second consumer needs the same card. A shared library or service may then pay, owned outside Ploeg.
* A Ploeg decision (admission, routing, budget or review) starts to need a fact this record moves out. That fact returns as an execution fact with its own record.
* Unfold cannot read a needed fact from the tracker or forge because of rate limits or missing credentials. Ploeg may then relay that fact as an event, not as a card.

## More Information

* Evidence: the [boundary audit](../research/2026-10-05-ploeg-boundary-audit.md), and the [substrate, language and Run bottlenecks](../research/2026-10-05-substrate-language-and-run-bottlenecks.md) research, §5 and §6.
* This number first held a proposal to publish formula conformance vectors for Unfold's copies. That proposal was withdrawn before merge, because Ploeg will own no formula a consumer copies.
* 2026-10-05: proposed after the owner said the concept of a card should be unfamiliar to Ploeg, and asked which other dependencies point the wrong way.
