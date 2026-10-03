---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Delivery gates are mapped per board from tracker statuses

## Context and Problem Statement

A pull request that merges is not yet delivered. On the boards Unfold serves, a ticket moves through test and acceptance after development, and it can be sent back: a tester finds a defect, a client changes the requirement, a test environment is down. The owner's card contract for Unfold (P2b) wants that path on the Run card, as gates, bounces with a reason, a right-first-time count, and an `evolved` mark when a requirement changed after acceptance. Defect bounces also lower the grade's delivery subgrade ([ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)). Every board names its columns differently: Vikunja has buckets per Kanban view, ClickUp has per-List custom statuses. Ploeg already receives Vikunja `task.updated` deliveries on the webhook it registers, and ClickUp `taskStatusUpdated` deliveries on its ClickUp webhook route. Neither was used for anything but withdrawal.

## Decision Drivers

* The tracker is the source of truth for where a ticket stands. Ploeg records what it learned and when, and never writes a gate back.
* Each board's own column names map to gates in configuration, checked at boot like routing ([ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)).
* A bounce reason a human didn't give stays `unknown`, never a guess.
* No new hoster configuration beyond the mapping, and no new webhook registration.

## Considered Options

* **Map statuses per tracker project in ploegd's configuration; record a gate move when a delivered webhook shows the ticket in another gate; take the bounce reason from a comment or label prefix**
* Poll every routed ticket's status in the sweep
* Infer gates from Ploeg's own lifecycle (queued, in review, merged)

## Decision Outcome

Chosen option: "**map statuses per project, record moves from delivered webhooks, take reasons from a `bounce:` prefix**".

1. **Mapping.** A project under `trackers.<tracker>.projects` may carry `gates:` with the lists `development`, `test`, `acceptance` and `done`, each naming statuses (ClickUp) or bucket titles (Vikunja). Names compare trimmed and case-insensitively. ploegd refuses to start when a gate key is unknown, a status is empty or padded, a status is in two gates, `gates:` maps nothing, or one board is mapped differently in two entries. Named projects resolve to ids at boot, as routing does.
2. **Ingestion.** On a tracker update (Vikunja `task.updated`, including done; ClickUp `taskUpdated`, `taskStatusUpdated` or `taskPriorityUpdated`) or an assignment, for a ticket that has a Work Item on a mapped board, Ploeg reads the ticket (`GET /tasks/{id}?expand=buckets` in Vikunja, `GET /task/{id}` in ClickUp). When its statuses resolve to exactly one gate and that gate differs from the last one recorded, Ploeg stores a row in `gate_transitions` (migration 0026) with the status, the tracker user who made the change (Vikunja's `doer`, ClickUp's history `user`) and the change time from the payload, or the receive time when the payload has none or it predates the last move. Statuses that map to no gate, or to two gates, record nothing. A read failure is logged and never fails the webhook.
3. **Bounce.** A bounce is a move to an earlier gate in the order development, test, acceptance, done. Its reason is the newest comment starting with `bounce:<reason>` written since the gate it left was entered; otherwise the reason of the ticket's single `bounce:<reason>` label (or ClickUp tag); otherwise `unknown`. Reasons are `defect`, `requirement`, `misunderstood` and `environment`. Two different `bounce:` labels give `unknown`. The reason is read once, when the move is recorded.
4. **Card.** `gates` holds `current`, `history` (each gate with `enteredAt` and, except the current one, `leftAt`), `bounces` (`from`, `to`, `at`, `reason`, `actor`) and `rightFirstTime`: for every gate after development that the Work Item entered, the count of defect and unknown bounces that left it. `gates` is null when no move was recorded. `evolved: true` appears when a `requirement` bounce left acceptance or done. The roster gains `qa`, the tracker user who moved the ticket out of test, and `acceptor`, the one who moved it out of acceptance. Schema: `cardGates` and `card.evolved` in `operator-api.v1.schema.json`.
5. **Grade.** Only `defect` and `unknown` bounces count against delivery.

### Consequences

* Good, because a board's own vocabulary drives gates, and a typo is a boot failure instead of a silent gap.
* Good, because the webhooks Ploeg already receives carry the moves, so nothing new is registered and nothing polls.
* Bad, because a webhook Ploeg never receives is a move it never records. The next delivered move catches the gate up, but the stay in between is lost. Vikunja's coverage check (`/readyz`) shows a project whose webhook is missing; ClickUp's webhook is registered by hand.
* Bad, because a reason written after the move, or a label added later, is not picked up.
* Bad, because roster names from the tracker are tracker usernames and roster names from the forge are forge logins. One person with two different names shows as two people.
* Bad, because a ticket in two Kanban views whose buckets map to different gates records nothing until they agree.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/gate`: reason parsing (prefix, HTML, case, unknown words); mapping validation and resolution, including ambiguity; the journey: history, bounces, unknown reasons, right-first-time and `evolved`.
* `pkg/config`: invalid mappings fail at load; named and pinned projects resolve to boards.
* `pkg/provider/vikunja` and `pkg/provider/clickup`: the actor and time from webhooks; board status and comment reads against `httptest` fakes.
* `pkg/store`: only moves into another gate are stored, a reason only on a bounce; the card's gates, bounces, roster roles and grade inputs.
* `pkg/httpapi`: webhooks against a fake Vikunja record moves and reasons (comment before label, two labels unknown, unmapped status or board ignored, no board read without a Work Item or a mapping), and the card validates against `operator-api.v1`.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Poll every routed ticket's status in the sweep

* Good, because a missed webhook would be caught up.
* Bad, because it costs one tracker read per open ticket per sweep for a fact the webhooks already deliver, and still misses a move that is undone between two sweeps.

### Infer gates from Ploeg's own lifecycle

* Bad, because test and acceptance happen outside Ploeg. Only the tracker knows them.

## More Information

* The owner's card contract addendum (P2b–P4) names the gates, the reasons and `evolved`.
* The Vikunja read assumes a version whose single-task read honours `expand=buckets` (0.24 or later). It was not checked against a live instance.
* [Map tracker statuses to gates](../how-to/map-tracker-statuses-to-gates.md) is the operator's guide.
* [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md): the grade that counts bounces.

## Re-evaluation triggers

* A card shows a missing gate stay that a lost webhook explains: add a poll for mapped, open tickets.
* Ploeg starts registering ClickUp webhooks itself, or Vikunja adds a dedicated bucket-move event.
* Tracker assignees or user identities get linked to forge logins, so the roster can merge one person's names.
