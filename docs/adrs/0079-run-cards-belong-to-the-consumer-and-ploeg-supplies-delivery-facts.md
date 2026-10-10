---
status: accepted
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: 0074
review-by: 2027-01-31
---

# Run cards belong to the operator consumer, and Ploeg supplies delivery facts

## Context and Problem Statement

Since August Ploeg has computed the Run card: a grade, a rarity tier frozen at release, flow figures, cracks with referees, epic sets, a card list by person and an SVG card comment on the pull request ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) to [ADR-0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md)). None of Ploeg's own decisions read any of it. [ADR-0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md) proposed moving the card out and also making the consumer collect the forge and tracker facts itself, with its own credentials and webhooks.

On 2026-10-10 the owner decided: "Ploeg no longer has any card logic, or it shouldn't. So this is purely an extra fun little domain on top of what Unfold is. And Unfold uses Ploeg to schedule agents." The card is the consumer's domain. Ploeg still talks to the forge and the tracker for its own work, so it keeps the plain delivery facts it already collects and hands them over.

What does Ploeg supply, how does the consumer take over the card without losing what only Ploeg holds, and in what order does the card code leave?

## Decision Drivers

* Ploeg names none of its consumers and does not compute a consumer's product ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* A card change must never need a Ploeg release.
* Ploeg already reads the forge and the tracker. A second collector with its own credentials, webhooks and rate limits duplicates that for no decision of Ploeg's.
* The running consumer must not break: the card endpoints keep answering until it has moved.
* Rows a person or a freeze created cannot be recomputed and must reach the consumer once.

## Considered Options

* Ploeg supplies the raw delivery facts it collects; the consumer computes the card
* The consumer collects the forge and tracker facts itself (ADR-0074)
* Keep the card in Ploeg

## Decision Outcome

Chosen option: "Ploeg supplies the raw delivery facts it collects; the consumer computes the card", because it removes every card idea from Ploeg without making the consumer rebuild the forge and tracker readers Ploeg already runs.

### What Ploeg keeps and supplies

Ploeg keeps collecting: Runs, Shifts and usage, live usage of a running Run, pull requests and their state, merge and close facts, reviews, conversation events, CI runs and jobs, changed files with line counts, labels, reverts, merge state, deploys, every tracker status and delivery gate move, the tracker's created time and estimate, and tracker parent membership. Each changed file of a merged pull request also keeps the raw indentation measurements Ploeg takes from the diff it never stores (migration `0043`), so the consumer can compute complexity without the diff.

The operator API exposes them, team-scoped, as values close to the stored columns:

* `GET /api/v1/operator/work-items/{id}/facts` answers one Work Item's `workItemFacts`.
* `GET /api/v1/operator/facts?member=&team=&since=&before=&limit=` lists them, newest activity first, paged with an opaque cursor; `member` matches any roster role.

Facts carry no grade, tier, score, KPI aggregate, status kind, working time or steward. The roster names logins with factual roles only: merger, reviewer, author, pusher, commenter and mover. Ploeg's own forge logins are listed as `botLogins`, not filtered.

### The keyed pull request comment

`PUT /api/v1/operator/work-items/{id}/pull-request-comments/{key}` keeps one comment per pull request and key, marked `<!-- ploeg:comment:<key> -->`, posted with Ploeg's forge credentials. It takes markdown up to 32 768 characters and an optional SVG up to 256 KiB, which Ploeg checks against an allowlist of static drawing elements (no script, event handler, `foreignObject`, entity declaration or external reference) and uploads as an attachment where the forge takes one. `adoptCommentId` takes over an existing Ploeg-marked comment, such as the card comment. `DELETE` removes the key's comment. Both need execute permission and an actor, and both are audited (`pull_request_comment.upserted`, `pull_request_comment.deleted`). Ploeg knows nothing of what the comment says.

### The one-time export

`GET /api/v1/operator/card-legacy-export?after=&limit=` pages, by Work Item, through every row a person or a freeze created: `card_cracks` in every state, frozen `card_rarity` rows, `card_comments` with the comment id and pull request, and the stored change shape of each merged pull request (`pull_requests.shape`), measured from a diff Ploeg did not keep. It is deprecated from the start.

### Order of removal

1. **This release.** Adds the facts, the keyed comment, the export and the per-file measurements. The card endpoints, sweeps and card comment keep working, and their schema descriptions say they are deprecated. `cards: {enabled: false}` in the configuration file stops the card comment, rarity and mend sweeps and the card comment posted on merge; the card routes keep answering. The default stays on.
2. **The consumer** computes cards from the facts, imports the export in every environment, takes over the card comment with `adoptCommentId`, and the operator sets `cards.enabled: false`.
3. **The next minor release** deletes the card code and tables:
   * **Tables and columns:** `card_cracks`, `card_rarity`, `card_comments`, and `pull_requests.kpis`, `pull_requests.kpis_computed_at` and `pull_requests.shape`, by a new migration.
   * **Packages:** `pkg/rarity`, `pkg/playkpi` (its CI job type moves to the store), `pkg/cardimage`, the flow figures, status kinds and calendars of `pkg/flow`, and the journey and bounce analysis of `pkg/gate`, which keeps only the per-board status-to-gate mapping that records gate moves.
   * **Store:** `card.go`, `card_comments.go`, `card_condition.go`, `card_flow.go`, `card_grade.go`, `card_list.go`, `card_rarity.go`, `cracks.go`, the mend half of `crack_facts.go`, the set half of `epics.go`, and the KPI and shape computation in `play_pipeline.go`.
   * **HTTP:** `operator_card.go`, `operator_cards.go`, `cracks.go`, `card_comment.go` and `card_rarity.go`, and the routes `GET work-items/{id}/card`, `GET cards`, `GET work-items/{id}/crack-candidates`, `GET` and `POST work-items/{id}/cracks`, `POST work-items/{id}/evolved`, `POST cracks/{crack}/confirm|dispute|resolve` and `GET card-legacy-export`.
   * **Sweeps:** the card comment, rarity and mend sweeps in `cmd/ploegd`.
   * **Configuration:** `cards`, a target's `cardStyle`, `rarity`, `cardShape` and `release`, a team's `cards` and `workingHours`, and a project's `statusKinds`, accepted with a warning for one release. Status moves are then recorded for every project with `gates:`.
   * **Contract:** every `card*`, `crack*` and `legacy*` definition of `operator-api.v1`, as a breaking change.

### Records

This record supersedes ADR-0074 for the question of who collects the forge and tracker facts. On ratification the card records are marked `rejected`, which the ledger allows for proposed records: ADR-0046, 0050, 0052, 0054, 0055, 0056 and 0061. ADR-0049 (live usage), 0051 (gate moves), 0053 (parent membership), 0057 (status moves) and 0058 (forge facts) stay for the facts they record, without the card figures. ADR-0045, 0047 and 0059 are unchanged.

### Consequences

* Good, because a card, a grade or a season changes without a Ploeg release, and Ploeg's code no longer holds a consumer's product.
* Good, because the consumer needs no forge or tracker credentials, webhooks or rate limits of its own.
* Good, because the export moves cracks, frozen rarity, the card comment and unmeasurable shapes once, before the tables go.
* Bad, because Ploeg still runs forge and tracker readers whose only reader is a consumer.
* Bad, because two releases and a coordinated import are needed, and a consumer that writes a keyed comment while `cards.enabled` is still true gets two comments on a pull request.
* Bad, because only pull requests measured after this release carry per-file indentation; older ones have only the exported shape.

### Confirmation

* `go test ./pkg/httpapi/` validates every facts, keyed comment and export response against `operator-api.v1.schema.json`, checks team scope and paging, checks that no derived card field appears in the facts, runs the keyed comment against Forgejo and GitLab fakes built on `net/http/httptest`, and proves `CardsDisabled` stops the card sweeps and publisher.
* `go test ./pkg/store/` proves per-file indentation is recorded at the merge and read back, and `go test ./pkg/playkpi/` proves the card's complexity agrees with the per-file facts until the card code goes.
* `go test ./pkg/svgsafe/` refuses scripts, handlers, `foreignObject`, entities and external references.
* `go test ./cmd/ploegd/ ./pkg/config/` proves `cards.enabled: false` stops the sweeps and that the key defaults to on.
* CI runs all of them with `mise exec -- go test ./...`.

## Pros and Cons of the Options

### The consumer collects the forge and tracker facts itself (ADR-0074)

* Good, because Ploeg would drop its card-only readers and the tracker webhook's extra board reads.
* Bad, because the consumer needs its own forge and tracker identities, webhooks and rate limits for facts Ploeg already reads.
* Bad, because a collector outage loses tracker status moves and deploys seen once.

### Keep the card in Ploeg

* Good, because nothing moves.
* Bad, because Ploeg keeps a consumer's product, and every card change needs a Ploeg release.

## Re-evaluation triggers

* A Ploeg decision (admission, routing, budget or review) starts to need a figure the consumer computes. It returns as a Ploeg fact with its own record.
* A second consumer needs facts in a shape the facts endpoints cannot give.
* The facts list exceeds its 16 MiB response bound at its 25-item page in practice.
* The consumer has imported the export in every environment. That starts the second release.

## More Information

* 2026-10-10: decided by the owner; part 1 of 2.
* [ADR-0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md): the proposal this record replaces.
* [ADR-0069](0069-ploeg-names-none-of-its-consumers.md): why the contract says "operator consumer".
* Contract: `docs/contracts/operator-api.v1.schema.json`, definitions `workItemFacts`, `pullRequestCommentRequest` and `legacyExportResponse`.
* 2026-10-10 — Ratified by [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md), which carries out step 3 now that the consumer has imported the export on every live environment.
