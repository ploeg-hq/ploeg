---
status: proposed
date: 2026-10-06
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg exposes its execution facts, and consumers collect everything else

## Context and Problem Statement

Since August, Ploeg has grown the Run card, along with every pipeline that feeds it:

* **The card itself:** assembly, a grade, a rarity tier frozen at release, an SVG card comment on the pull request, and a card list searched by person.
* **Cracks:** a confirm, dispute and resolve workflow with referees.
* **Tracker readers:** status history, delivery gates, a team working calendar, and epics as card sets.
* **Forge readers:** pull request activity, CI runs, changed files, reverts and change-shape KPIs.
* **A deploy endpoint.**

The [boundary audit](../research/2026-10-05-ploeg-boundary-audit.md) measured all of this at about 13,000 production and 10,000 test lines, and 3,205 of the 5,033 lines of `operator-api.v1.schema.json`. Ploeg's own decisions read none of it.

The dependencies also point the wrong way:
* the review loop recomputes card KPIs (`pkg/shiftengine/review.go:160-168`);
* `pkg/store` computes rarity and flow formulas inside fact transactions;
* the tracker webhook path does extra board reads on every assignment.

The owner's direction is that Ploeg should expose only the minimal facts about its own work, plus its own metrics, and know nothing about the card. A separate Unfold process should gather the card data, outside Ploeg's critical path.

What does Ploeg expose, and what does it stop collecting?

## Decision Drivers

* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). A card, a grade, a rarity tier and a season are a consumer's product.
* **A consumer's collection must not share Ploeg's critical path.** Webhook handlers, the review loop and the store's transactions serve admission and execution only.
* **Ploeg knows no vendors** ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)). So it cannot be the natural collector of vendor facts it never uses.
* **Dependencies point from the consumer to Ploeg, never back.**

## Considered Options

* Ploeg exposes its execution facts, events and metrics; a consumer collects everything else
* Ploeg keeps collecting every card input and exposes it raw (the 2026-10-06 morning revision of this record)
* Keep the card in Ploeg

## Decision Outcome

Chosen option: "**Ploeg exposes its execution facts, events and metrics; a consumer collects everything else**".

### What Ploeg exposes

* **Resources.**
  * **Work Item:** `id, externalId, url, team, title, state, createdAt, updatedAt, target, latestShift`.
  * **Shift:** `id, workItemId, branch, round, budgetUsd, spentUsd, reservedUsd, openedAt, closedAt, closeReason, pullRequests[]{destination, repo, number, url, headSha, openedAt, openedByRunId}`.
  * **Run:** `id, shiftId, role, round, writes, state, startedAt, finishedAt, outcome, verdict, failureReason, usage, authorizedUsd, settled cost, traceAlias`, plus live spend while the Run is running.
  * **The pull request facts Ploeg decides on:** merged, merged-by, head SHA, checks state, merge state, review verdicts.
* **A versioned event stream.** `GET /api/v1/operator/events` becomes a contract:
  * actions form an enum: `shift.opened`, `run.started`, `run.finished`, `run.settled`, `pull_request.opened`, `pull_request.merged`, `shift.closed` and the existing operator actions;
  * cursor semantics are documented;
  * retention is at least 30 days, so a consumer that falls behind resumes from its cursor;
  * an optional CloudEvents mapping is defined.

  There are no outbound webhooks: if Ploeg pushes, it carries its consumers' delivery problems.
* **Its own metrics.** These are the Run-health metrics of [ADR-0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md).

### What Ploeg stops collecting and computing

These move to the consumer:
* card assembly, the card and cards routes, grade, rarity and the card list by person;
* the card image and the card comment;
* the crack workflow;
* tracker status history, delivery gates, the working calendar, and epics as sets;
* forge pull request activity, CI run history, changed files, reverts and change-shape KPIs;
* the deploy endpoint;
* the card-only provider capabilities: activity, CI-history, diff and change readers, the comment attacher, the board, ancestry and relation readers;
* consumer UI configuration (`CardStyle`, `DefaultCardSkin`, `cardShape`, `Referees`).

Unfold's collector service (Unfold ADR-0029) takes these over with its own forge and tracker identities. Ploeg never calls it.

### Order

1. **Ploeg release N** adds the Shift's `pullRequests[]`, the Run's live spend, the action enum, the new events and the retention promise. It also adds a one-off read-only export of what cannot be re-read:
   * crack confirmations;
   * revealed rarity;
   * posted card comment ids;
   * epic first-seen times;
   * status and gate history;
   * deploys;
   * tracker creation times and estimates.

   The card routes stay.
2. **The collector** is built, ingests live while Ploeg still collects, imports the export, and is compared card by card for two weeks.
3. **Unfold** switches its card source to the collector.
4. **Ploeg release N+1** removes everything listed above:
   * the KPI call in the review loop and the board reads in the tracker webhook path;
   * the derived and feed tables, dropped by the next migration;
   * the card definitions in `operator-api.v1` and their Go types, as a breaking change.

   It accepts removed configuration keys with a warning for one release (`pkg/config/config.go:279`).

### Records index

On ratification, the Records index marks these records `rejected`. The ledger allows this for records that are still proposed.
* **The card's:** ADR-0046, 0050, 0054, 0055, 0056 and 0061.
* **Its feeds':** ADR-0047, 0051, 0052, 0053, 0057 and 0058.

ADR-0049's fact, live spend, stays on the Run resource. ADR-0045 and ADR-0059 stay, narrowed to the facts Ploeg decides on. ADR-0069 is unchanged; this record extends it from names to concepts.

### Consequences

* **Good:**
  * About 13,000 production lines, 10,000 test lines and 3,200 schema lines leave Ploeg, along with the card-only vendor capabilities.
  * Ploeg's webhook handlers, review loop and store transactions serve execution only, so a collection bug can no longer delay a claim or a review.
  * A card change never needs a Ploeg release.
* **Bad:**
  * The consumer needs its own forge and tracker identities, webhooks and rate limits.
  * An outage of the collector loses what cannot be re-read: tracker status transitions, and deploys seen once.
  * Two Ploeg releases, an export and a coordinated contract version are needed.

### Confirmation

* **Vocabulary test.** `internal/boundary` gains `TestPloegSpeaksOnlyItsDomain`.
  * It scans:
    * exported identifiers;
    * route patterns;
    * contract property and definition names;
    * `yaml:` tags;
    * new migration names;
    * chart values keys.
  * It fails on whole-word `card`, `rarity`, `grade`, `crack`, `skin`, `theme`, `season`, `collection`, `kpi`, `steward`, `referee` or `cosigner`.
  * Records and existing migrations are exempt.
  * It is shown to work by an injected identifier, route and property, each of which must fail, and an in-domain "knowledge pack", which must pass.
* **Import test.** A test runs `go list -deps` on `pkg/store`, `pkg/shiftengine`, `pkg/httpapi` and `pkg/worker` and fails on any import of `pkg/rarity`, `pkg/playkpi`, `pkg/flow`, `pkg/gate` or `pkg/cardimage`, until those packages are deleted.
* **Events.** A schema test pins the action enum in `operator-api.v1`. A store test proves every listed action is written by the transition that names it.
* **CI.** `mise exec -- go test ./...` runs all of them.

## Pros and Cons of the Options

### Ploeg keeps collecting every card input and exposes it raw

* Good, because the consumer needs no forge or tracker credentials.
* Bad, because Ploeg keeps vendor readers and webhook work its own decisions do not use, on its critical path, which contradicts ADR-0077.

### Keep the card in Ploeg

* Good, because nothing moves.
* Bad, because Ploeg keeps a consumer's product, and every card change needs a Ploeg release.

## Re-evaluation triggers

* A Ploeg decision (admission, routing, budget or review) starts to need one of the dropped feeds. It returns as an execution fact with its own record.
* Two consumers need the same collected facts. A shared collector, owned outside Ploeg, is considered.
* The collector loses tracker status history in an outage twice in a quarter. Ploeg relaying status events through its Work Source contract is reconsidered.

## More Information

* Evidence:
  * [KEDA, plug-and-play integrations, and card collection](../research/2026-10-06-keda-integrations-and-card-collection.md), §3;
  * the [boundary audit](../research/2026-10-05-ploeg-boundary-audit.md).
* 2026-10-05: proposed as "consumers read the forge and tracker themselves".
* 2026-10-06: revised in the morning to "Ploeg keeps collecting and exposes raw facts", then rewritten after the owner chose minimal exposure and a separate collector.
