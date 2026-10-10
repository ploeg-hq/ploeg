---
status: proposed
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run card's flow figures come from every recorded tracker status and a team calendar

## Context and Problem Statement

On 2026-10-02 the owner asked for the important KPIs on the Run card: how long the ticket sat in every status, how long it took to get merged, how long CI took, and more like it. Forge-side figures (the pull request timeline, first feedback, CI timings, commits, complexity) are a separate record. This one covers the tracker flow, Ploeg's own queue and agent time, and delivery timing.

Ploeg already keeps part of what is needed. `gate_transitions` ([ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md)) keeps only moves into another delivery gate. A ticket that goes from Refinement to Ready to Doing, all unmapped or all in development, leaves no trace, so time in those statuses is lost. `work_items` knows when Ploeg first saw a ticket but not when the tracker created it. Runs, merges, deploys ([ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)) and mended cracks ([ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md)) are stored with their times.

The questions are where every status move is kept, which statuses count as work and which as waiting, how working time is counted, and how flow figures stay facts about the card instead of a score of a person.

## Decision Drivers

* Ploeg sends facts and deterministic derived values only ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)). An unknown figure is null, never 0 ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)).
* Card facts, not people. Waiting and blocked time say how work flows through a team. They are not the steward's doing, and no flow figure may rank a person or feed the grade ([ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)) or the rarity ([ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md)).
* The bounce ledger of ADR-0051 keeps its meaning: one row is one gate change.
* The card is assembled on every read, and the card list assembles up to 50 cards per request. Flow must stay bounded.
* No new webhook registration and no new poll, as in ADR-0051.
* `operator-api.v1` changes only additively.

## Considered Options

* **A new `status_transitions` table, written beside `gate_transitions` from the same board read; kinds and working time computed on read**
* Make `gate_transitions.gate` nullable and keep every status there
* Read ClickUp's Time in Status endpoint and Vikunja's task history when a card is read
* Store a computed flow snapshot per card and update it on every event

## Decision Outcome

**Narrowed on 2026-10-10 by [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md).** Ploeg keeps points 2 and 3, and the recording of point 1 for every board with `gates:` only: `statusKinds:` no longer makes a board record, and the status recorded is the one that decides the gate, else the only status the ticket is in. They reach the consumer as `statusTransitions`, `workItem.trackerCreatedAt` and `workItem.estimateSeconds` in the delivery facts. Points 4 to 9 (status kinds, working time and team calendars, the flow figures, their schema and `pkg/flow`) are gone, and `statusKinds` and `workingHours` are retired keys. The text below is the decision as first recorded.

Chosen option: "**a new `status_transitions` table, with kinds and working time computed on read**", because it keeps the gate ledger's invariant, needs no tracker read when a card is read, and lets a team correct its status kinds or calendar without rewriting history.

1. **Recording.** A board records its status moves when it has `gates:` or `statusKinds:` under `trackers.<tracker>.projects`. On the tracker updates and assignments that ADR-0051 already handles, the single board read that serves gates also stores a row in `status_transitions` (migration 0032) whenever the ticket's status differs from the last one recorded, compared trimmed and without case. The status is the one that decides the gate; else a status the board lists under `statusKinds`; else the only status the ticket is in. A ticket in two different unmapped buckets records nothing. Each row keeps the status, its gate at that moment or null, and the time. No actor is kept.
2. **Whose clock.** The time is the tracker's: Vikunja's `task.updated`, ClickUp's history `date`. When the tracker gives none, gives one before the last recorded move, or gives one more than five minutes ahead of Ploeg, the row is timed when Ploeg received it and marked `observed`. The card's status entry says `observed: true` when any entry into it was timed that way.
3. **Tracker facts.** Ploeg keeps the tracker's creation time (`work_items.tracker_created_at`: Vikunja `created`, ClickUp `date_created`) and ClickUp's `time_estimate` as `estimate_seconds`, both read at ingest and on every board read. Vikunja has no estimate, so it stays null.
4. **Status kinds.** Every status is `active`, `waiting`, `blocked` or `done`. A board's `statusKinds:` lists statuses per kind and wins. Otherwise, compared without case: a name containing "blocked", "on hold", "on-hold" or "impeded" is blocked; the done gate, or a name that is "done", "closed", "complete", "completed", "released" or "resolved", is done; a name containing "ready", "waiting", "awaiting", "pending", "to review", "to test", "to do", "todo", "backlog", "queue" or "icebox", or that is "new" or "open", is waiting; the development and test gates are active; a status in no gate whose name contains "in progress", "doing", "progress", "develop", "review", "testing", "in test", "qa", "build" or "working" is active; everything else, the acceptance gate included, is waiting. Kinds are computed when the card is read, so a changed `statusKinds:` applies to old moves too. Gates are stored with the move, as ADR-0051 stores them.
5. **Working time.** Every human-timeline duration also has working seconds: the seconds inside a working day's hours, in the team's zone, skipping configured holidays. The default is Monday to Friday, 09:00 to 17:00, Europe/Amsterdam, no holidays. `teams.<team>.workingHours` sets `timezone`, `days`, `start`, `end` and `holidays`. Each day's hours are its own wall-clock hours, so a daylight saving change inside them counts the real elapsed time. Agent time has no working seconds: agents keep no office hours.
6. **The figures.** `flow` (`cardFlow`) holds:
   * `statuses`: per status in first-entered order, its gate, kind, visits, seconds, working seconds, `observed` and `current`, up to now for the current one. At most 50 statuses are listed and the newest 500 moves are read; `truncated` says when more existed. `gates` and `kinds` sum the same time per gate and per kind. `statusesSince` is the first recorded move.
   * `leadTime`: from the tracker's creation, else Ploeg's first sight (`start: first_seen`), to the release; while the card can still be delivered, to now with `running: true`. A withdrawn card, or one whose pull requests all closed, has none.
   * `cycleTime`: from the first active status or the first Run, whichever came first, to the release, else to the merge of the latest merged play, else to now. `timeToStart` runs from the lead time's start to the cycle's start, or to now before work started.
   * `efficiency`: active seconds over active, waiting and blocked seconds inside the cycle, to three decimals. `blockedSeconds` and `reopens` (a move from a done status into another kind) cover all recorded moves.
   * `queueSeconds`: from the first `work_item.queued` or `work_item.approved` audit row to the first Run. `agentSeconds`: every started Run's duration, a running one up to now. `runs`. `firstRunToFirstPlaySeconds`: from the first Run to the card's first `pr_opened` event, the same opening time the plays use.
   * `mergeTo` and `mergeToWorking`: per environment, from the latest merged play's merge to its first deploy there. `environmentsReached` lists them in deploy order. `timeToProduction` is the release environment's entry ([ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md): `release.environment`, else `production`), and null while no deploy reached it, including a release counted from the merge.
   * `restores`: per mended confirmed crack, confirmation to mend, 0 when the mend came first; `meanRestoreSeconds` is their mean to one decimal.
   * `estimateSeconds`, `calendar` (the calendar the working seconds used) and `notCollected`: `statuses` when the board records none, `estimate` when the tracker has none, `holidays` when the calendar lists none.
7. **Card facts, not people.** Flow is a property of the card. It is never an input to the grade or the rarity, and their formulas are unchanged. It is served only where the card is served, under the same team scope and the same visibility rules (binders private, team pages for the team, clients team aggregates only), and nothing sums it per person.
8. **Bounded on read.** `OperatorCard` reads flow only when the caller passes `CardOptions.Flow`: one indexed query for the newest 501 moves (`status_transitions_by_work_item`), one indexed lookup of the first admission audit row (`audit_log_by_item`) and four columns of the row it already reads. `pkg/flow.Compute` derives everything in memory. Its benchmark of a 500-move card spread over two years takes about 0.4 ms on the development machine; an ordinary card takes microseconds. The crack and rarity sweeps and the card comment do not ask for flow.
9. **Schema.** `card.flow` is optional, `oneOf` `cardFlow` or null, with `cardFlowStatus`, `cardSpan` and `cardDuration` in `operator-api.v1.schema.json`. The code is `pkg/flow`, `pkg/store/statuses.go` and `pkg/store/card_flow.go`.

### Consequences

* Good, because time in every status, including the unmapped ones, is on record from the first webhook after a board is configured, at no extra tracker read on boards that already map gates.
* Good, because a team can fix a status kind or its calendar later and every card reads the corrected figures.
* Good, because each figure names where its ends came from (`start`, `end`, `observed`), so a reader can tell a tracker time from Ploeg's.
* Bad, because a webhook Ploeg never receives is a move it never records; the next one catches the status up and the time in between lands in the previous status. ADR-0051 has the same gap.
* Bad, because the first recorded status starts when Ploeg first read the ticket, not when the ticket entered it. Time before that is not counted, and `statusesSince` says where the record begins.
* Bad, because a board that maps neither gates nor kinds records nothing; those cards list `statuses` in `notCollected`.
* Bad, because the default kinds are a first guess from common board names. A board with unusual names should set `statusKinds:`.
* Bad, because computing on read means two reads a minute apart show different running spans, and old figures are not kept.
* Neutral: a board that only sets `statusKinds:` now costs one board read per update webhook of a ticket with a Work Item.

### Confirmation

Since 2026-10-10 the store and HTTP tests that ADR-0080 names check what this record still decides. The card tests listed below were removed with the card.

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/flow`: default kinds and board overrides, refused kind lists; the calendar over a working day, nights, weekends, a full week, both Europe/Amsterdam daylight saving weekends, a daylight saving change inside working hours, holidays and extra days, refused hours; a released card's statuses, gate and kind totals, lead, cycle and start time, efficiency, reopens, queue, agent and first-play time, `mergeTo`, time to production, restores and `notCollected`; an open card running to now; a merged card without release ending its cycle at the merge; a closed card without lead or cycle time; an unstarted card waiting to start; the 50-status cap; unknowns encoded as null; a benchmark of a long card.
* `pkg/store`: migration 0032; only a changed status is stored, and a missing, earlier or future tracker time is observed; tracker creation time and estimate at ingest and on a board read; a card's flow from stored statuses, Runs, a merged play, deploys and a mended crack, with a board's `statusKinds`; no flow without `CardOptions.Flow`, and the same grade and rarity with or without it; the newest 500 moves read when there are more.
* `pkg/httpapi`: Vikunja webhooks record unmapped statuses, keep a repeat once, mark untrusted times observed, skip two unmapped buckets and another board, keep the tracker creation time, and the card and card list validate against `operator-api.v1` with flow; a board with only `statusKinds` records statuses and one with neither is never read; ClickUp webhooks record statuses with ClickUp's times and the card carries `estimateSeconds` and a lead time from `date_created`.
* `pkg/config`: `statusKinds` and `workingHours` load, resolve per board and per team, and refuse unknown kinds, duplicate or padded statuses, conflicting lists, bad zones, days, hours and holidays.
* `pkg/provider/vikunja`, `pkg/provider/clickup`: creation times and ClickUp estimates (number, string, null, nonsense) are read.

There is no poll path: like gates, status moves come only from delivered webhooks.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Make `gate_transitions.gate` nullable

* Good, because one table holds every move.
* Bad, because every reader of `gate_transitions` (the gate position, bounces, the roster's `qa` and `acceptor`, the card list's member query) assumes one row is one gate change, and each would need a filter that is easy to forget.
* Bad, because a status change inside one gate would need a row that is not a gate move, which ADR-0051's tests forbid.

### Read the trackers' own history when a card is read

* Good, because ClickUp's Time in Status endpoint knows moves Ploeg never received.
* Bad, because it needs a ClickApp per workspace and Vikunja has no equivalent, so two boards would answer differently.
* Bad, because a card read would cost tracker calls, which ADR-0046 rules out, and the card list would cost 50 of them.

### Store a computed flow snapshot

* Good, because a read would be one row.
* Bad, because running spans change every second and a changed status kind or calendar would need every snapshot rebuilt.

## More Information

* The owner's request of 2026-10-02; forge-side KPIs (pull request timeline, first feedback, CI timings, commits, complexity) are decided separately.
* [Run cards](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/concepts/run-cards.md) explains the figures; [configure status kinds and working hours](https://github.com/ploeg-hq/ploeg/blob/2d9595371064e7528fbda4344b6f116215a1b789/docs/how-to/configure-status-kinds-and-working-hours.md) is the operator's guide.
* The [works council and DPIA pack](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/reference/run-cards-works-council-pack.md) lists what flow stores.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. The facts this record keeps stay and are exposed raw through the delivery facts endpoints; the card figures built on them are removed in the next minor release.
* 2026-10-10 — Narrowed by [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md) to the facts it records.

## Re-evaluation triggers

* A card shows a status stay that a lost webhook explains: add a poll for open tickets on recording boards, for gates and statuses together.
* Teams set `statusKinds:` on most boards, or override the same default name: change the defaults under a new record.
* A card list request with flow passes 1 s at p95, or a Work Item passes 500 recorded status moves.
* A tracker exposes status history Ploeg can read without per-workspace setup.
* Someone asks to total flow figures per person, or to use them in a review: that is a works council matter before it is a code change.
