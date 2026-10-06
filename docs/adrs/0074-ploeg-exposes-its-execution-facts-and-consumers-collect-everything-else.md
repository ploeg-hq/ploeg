---
status: proposed
date: 2026-10-06
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg exposes delivery facts, and consumers own the card and every formula over them

## Context and Problem Statement

Ploeg's business is to admit, budget and execute agent work. Since August it has also grown the Run card:

* **The card itself:** assembly, a grade, a rarity tier frozen at release, an SVG image posted as a pull request comment, and a card list searched by person.
* **Cracks:** a confirm, dispute and resolve workflow with referees.
* **Feed pipelines:**
  * delivery gates derived from tracker statuses;
  * a team working-time calendar;
  * change-shape KPIs computed from pull request activity.

The [boundary audit](../research/2026-10-05-ploeg-boundary-audit.md) measured all of this at about 13,000 production and 10,000 test lines, and 3,205 of the 5,033 lines of `operator-api.v1.schema.json`.

The dependencies point the wrong way:
* the review loop recomputes card KPIs (`pkg/shiftengine/review.go:160-168`);
* `pkg/store` computes rarity and flow formulas inside fact transactions;
* `pkg/config` carries a consumer's card skin and theme;
* a webhook handler renders and posts a card image.

The owner's direction has two parts:
* The concept of a card should be unfamiliar to Ploeg.
* Ploeg should still expose every fact the card needs. Ploeg already receives the forge and tracker webhooks and holds the forge and tracker credentials, so it is the natural collector.

Where is the line between what Ploeg records and what a consumer makes of it?

## Decision Drivers

* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). A card, a skin, a grade, a rarity tier and a season are a consumer's product.
* **Dependencies point from the consumer to Ploeg, never back.** The execution core must not call presentation or scoring code.
* **One collector.** A consumer should not need its own forge and tracker credentials, rate limits and webhooks to learn what Ploeg already observes.
* **Facts are kept raw, and formulas live with the product.** A formula change must not need a Ploeg release, a migration or a contract version.
* **A fact that cannot be observed again later is recorded when it happens.** Vikunja keeps no status history, and a deploy is seen once.

## Considered Options

* Ploeg records and exposes raw delivery facts under neutral names; consumers own the card, every formula and every presentation
* Ploeg drops the card and its feeds; consumers read the forge and tracker themselves
* Keep the card in Ploeg, and fix only the wrong-way calls

## Decision Outcome

Chosen option: "**Ploeg records and exposes raw delivery facts under neutral names; consumers own the card, every formula and every presentation**".

### What Ploeg records and exposes

These are facts, under neutral names, through the operator API and its events:

| Fact | Today | After |
| --- | --- | --- |
| Shifts, Rounds, Runs, Outcomes, usage, authorized and settled spend | Operator resources | Unchanged; live spend for a running Run moves onto the Run resource ([ADR-0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md)'s fact, without the card) |
| Pull request state, merge, merged-by, reviews, CI state on the pushed head | Pull request facts ([ADR-0045](0045-keep-run-usage-and-merge-facts.md), [ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)) | Unchanged |
| Pull request activity, CI runs and jobs, changed files, reverts | Migration 0033 and `pull_request_files`/`pull_request_reverts` ([ADR-0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md), [ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md)) | Kept as raw rows; the derived `kpis` and `shape` columns leave |
| Tracker status transitions | `status_transitions` ([ADR-0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md)) | Kept; gate mapping and working-time arithmetic leave |
| Deploys of merged changes | `POST /api/v1/deploys` ([ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)) | Kept |
| Tracker relations between Work Items | `work_item_epics` ([ADR-0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md)) | Kept as relations, with first-seen times; "set" semantics leave |

**A facts endpoint.** `GET /api/v1/operator/work-items/{id}/facts` returns all of the above for one Work Item. A list form returns them for many items with a cursor. Both are versioned in `operator-api.v1`. New facts are announced as audit events, so a consumer can follow them without polling every item.

**A generic consumer publication.** Ploeg publishes a pull request note supplied by an authorized consumer through its outbox ([ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)): markdown, an optional image and a consumer-chosen marker that Ploeg finds and edits in place. Ploeg does not know what the note means. This replaces the card comment ([ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md)) without giving a consumer forge write credentials.

### What leaves Ploeg

These become the consumer's:

| Concern | Today in Ploeg |
| --- | --- |
| Card assembly, the card routes, the card list by person | `pkg/store/card*.go`, `/work-items/{id}/card`, `/cards` ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md), [0054](0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md)) |
| Grade | `pkg/store/card_grade.go` ([ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md), [0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md)) |
| Rarity | `pkg/rarity`, `card_rarity` ([ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md)) |
| Change-shape KPIs | `pkg/playkpi` |
| Delivery gates and the working-time calendar | `pkg/gate`, `pkg/flow` ([ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md)) |
| Card image and rendering | `pkg/cardimage` |
| The crack workflow: confirm, dispute, resolve, referees, cosigners | `pkg/store/cracks.go`, crack routes. The revert and changed-file facts it reads stay |
| Consumer UI configuration | `CardStyle`, `DefaultCardSkin`, `cardShape`, card `Referees` in `pkg/config` |

The execution core stops calling any of it:
* The review loop records pull request facts and stops recomputing KPIs.
* `pkg/store` stops importing `rarity`, `playkpi`, `flow` and `gate`.

Three smaller fixes come with it:
* The operator execution `demo` flag becomes a neutral publication mode.
* Comments naming a dashboard tool are reworded to name the published correlation key.
* The legacy `[a-z0-9-]+:run-card` marker match is removed.

### Order

1. **Ploeg adds the facts endpoint, its events and the generic publication, beside the card routes.**
2. **The consumer builds its card from the facts endpoint,** imports a one-off export of the data Ploeg derived and cannot re-derive, and switches its card comment to the generic publication. The export covers:
   * crack confirmations;
   * revealed rarity;
   * the forge ids of posted card comments.
3. **Ploeg marks the card, grade, rarity and crack routes deprecated for one release.** It accepts the removed configuration keys with a warning (the loader rejects unknown keys, `pkg/config/config.go:279`).
4. **The next release removes them.**
   * The next migration drops the derived tables and columns: `card_rarity`, `card_comments`, `card_cracks`, `kpis`, `kpis_computed_at`, `shape` and `gate_transitions`.
   * The card definitions leave `operator-api.v1` with their Go types.
   * The contract change is breaking and is versioned as such.

On ratification, the Records index marks two groups of records:
* **`rejected`:** ADR-0046, 0050, 0054, 0055, 0056 and 0061, which made the card Ploeg's.
* **Kept, narrowed to the facts above:** ADR-0047, 0049, 0051, 0052, 0053, 0057 and 0058.

[ADR-0069](0069-ploeg-names-none-of-its-consumers.md) is unchanged; this record extends it from names to concepts.

### Consequences

* Good, because about 9,000 production lines of formulas, rendering and workflow leave Ploeg. The collectors stay, so the consumer gains every fact without new credentials.
* Good, because a card, grade or rarity change no longer needs a Ploeg release, migration or contract version.
* Good, because the execution core imports no presentation or scoring code, and the review loop no longer fails over a KPI.
* Good, because the duplicated rarity and calendar formulas in the consumer become the only copies.
* Bad, because Ploeg keeps ingest pipelines that its own decisions do not use. Their cost (forge reads per pull request, rows per status change) stays in Ploeg, bounded by the Teams it serves.
* Bad, because the change takes two releases, an export and a coordinated contract version.
* Bad, because the generic publication is a new consumer-facing write path. It is limited to pull requests Ploeg opened, one note per consumer marker, through the outbox, and is audited.

### Confirmation

* **Vocabulary test.** `internal/boundary` gains `TestPloegSpeaksOnlyItsDomain`.
  * It scans:
    * exported Go identifiers in `pkg/` and `cmd/`;
    * route patterns;
    * property and definition names in `docs/contracts/*.json`;
    * `yaml:` tags in `pkg/config`;
    * table and column names in migrations added after this record;
    * `ops/helm/ploeg/values.yaml` keys.
  * It fails on whole-word `card`, `rarity`, `grade`, `crack`, `skin`, `theme`, `palette`, `season`, `collection`, `kpi`, `leaderboard`, `steward`, `referee` or `cosigner`.
  * Records and existing migrations are exempt.
  * It is shown to work by injecting a forbidden identifier, route and schema property, each of which must fail; an in-domain "knowledge pack" must pass.
* **Import test.** A test runs `go list -deps` on `pkg/store`, `pkg/shiftengine` and `pkg/worker` and fails on any import of `pkg/rarity`, `pkg/playkpi`, `pkg/flow`, `pkg/gate` or `pkg/cardimage`, until those packages are deleted.
* **Facts endpoint.** A schema test validates `/work-items/{id}/facts` against `operator-api.v1`. A qualification test proves that every input the consumer's card reads today is present in it.
* **CI.** `mise exec -- go test ./...` runs all of them.

## Pros and Cons of the Options

### Ploeg drops the card and its feeds; consumers read the forge and tracker themselves

* Good, because Ploeg shrinks the most, about 13,000 lines.
* Bad, because each consumer needs its own forge and tracker credentials, webhooks and rate limits.
* Bad, because the tracker status history and the deploys Ploeg already records would have to be collected again by the consumer.

### Keep the card in Ploeg, and fix only the wrong-way calls

* Good, because it is the smallest change.
* Bad, because Ploeg keeps owning a product's vocabulary and formulas, and every card change needs a Ploeg release.

## Re-evaluation triggers

* A second consumer needs the same card. A shared library may then pay, owned outside Ploeg.
* A fact's ingest cost passes 10% of ploegd's forge API calls or database rows. That fact moves to its consumer.
* A Ploeg decision (admission, routing, budget or review) starts to need a derived figure. That figure returns as an execution fact with its own record.

## More Information

* Evidence: the [boundary audit](../research/2026-10-05-ploeg-boundary-audit.md).
* 2026-10-05: first proposed as "consumers read the forge and tracker themselves".
* 2026-10-06: revised after the owner asked that Ploeg keep exposing every fact the card needs. The collectors stay; the formulas, the rendering and the workflow leave.
