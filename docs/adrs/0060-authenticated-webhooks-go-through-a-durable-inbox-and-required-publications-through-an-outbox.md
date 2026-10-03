---
status: proposed
date: 2026-10-03
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Authenticated webhooks go through a durable inbox, and required publications through an outbox

## Context and Problem Statement

Ploeg exchanges facts with two kinds of outside system. Forges (Forgejo, GitLab) and trackers (Vikunja, ClickUp) send webhooks in. Ploeg writes comments and statuses back out. In both directions the store commits one thing and the effect on the other side happens after it, in the same request or goroutine, with nothing that remembers an unfinished effect. The 2026-10-02 code-quality review raised this as F01 and F15 ([research record](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/research/2026-10-02-code-quality-review.md)). On `development` today:

**Inbound (VIK-1727).**

* PR #141 moved signature verification in front of the dedup write. `handleForgeWebhook` (`pkg/httpapi/server.go`) now calls `ParseWebhook`, then `Store.SeenDelivery` (`pkg/store/shift.go`). `SeenDelivery` inserts into `forge_deliveries` (migration `0011`) and reports a conflict as "seen". The insert commits before any effect runs.
* The effects run after it, one event at a time: `AuditForgeEvent` (an error skips the rest of that event with `continue`), `recordPullRequestFacts`, `Reviews.HandleForgeEvent` for merges and closes, and `actOnForgeEvent` (`pkg/httpapi/forge_followup.go`), which creates repair follow-ups and records requested changes. Every one of them logs and carries on, and the handler answers 202.
* So a crash, a deploy or an error after the insert loses the effects. An honest redelivery of the same delivery id is then answered "redelivered; ignoring". Only `ReviewWatch.Reconcile` (`pkg/shiftengine/review.go`) has a second path, and it re-reads only the pull requests of `awaiting_review` Work Items, to settle merges and closes and refresh their facts. A lost "changes requested" review, a lost failed-check follow-up and a lost audit row are not recovered.
* The handler reads only `X-Forgejo-Delivery` and `X-Gitea-Delivery`. GitLab sends neither, so a GitLab delivery reaches `SeenDelivery` with an empty id, which is treated as fresh. GitLab has no dedup.
* `handleTrackerWebhook` has no dedup at all. A Vikunja or ClickUp delivery that is sent twice ingests twice. When one of several events fails it answers 500 after the earlier events of the same delivery were already applied, so a retry applies them again.
* Several effects are not idempotent on their own: `RecordChangesRequested` inserts into `work_item_reviews` with no unique key, which is how one review becomes two fix rounds; `gate_transitions` and `status_transitions` append rows with no natural key.

**Outbound (VIK-1728).**

* `publishRound` (`pkg/shiftengine/publish.go`) posts review findings on the pull request. It runs in `evaluate` (`pkg/shiftengine/engine.go`) before the `OpenRound` compare-and-swap. Two evaluators that see the same complete Round both post, and the loser of the CAS has already published. Its own comment says so and accepts it.
* `close` publishes after `CloseShiftAndSettle` commits: `notifyTracker` (a tracker comment and `SetStatus`), `publishBudgetExhausted` and `publishUsageReport`. Each failure is logged and skipped. The Shift is closed, so no evaluator visits it again, and nothing retries a notice that did not arrive.
* The usage report (`<!-- ploeg:usage-report -->`) and the Run card comment (`<!-- unfold:run-card -->`, [ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md)) already find their own comment by a hidden marker and edit it. Findings, the budget notice and tracker comments have no marker.

A forge or tracker outage therefore leaves a reviewer without findings, a board without the comment asking a person to merge, and a Work Item whose budget stopped with no explanation on its pull request. The record in Ploeg is right; the people reading the forge and the board are not told.

How does Ploeg make sure every authenticated delivery has its effects exactly once, and every publication a person depends on arrives exactly once, across crashes, outages and concurrent evaluators?

## Decision Drivers

* An authentic delivery whose effects did not run must not be answered "already seen". Dedup records an effect, not a receipt.
* A person must not wait on a notice that will never arrive, and must not get the same finding twice.
* State changes in Ploeg must not wait on a forge or a tracker ([ADR-0011](0011-the-pull-request-is-the-blackboard.md): Ploeg is the transport; the blackboard spec: write-back failure never blocks the transition).
* PostgreSQL is Ploeg's only stateful dependency. Claims, Leases and Shifts already use row locks and compare-and-swap in it. The review advises against a message broker.
* Providers differ: Forgejo sends a delivery id, GitLab sends one Ploeg does not read yet, Vikunja and ClickUp send none.
* Operators must see stuck work from the outside, through `/metrics` ([alerts](../ops/alerts.md)) and the operator API, without reading logs.

## Considered Options

* **Inbox and outbox tables in PostgreSQL, drained by workers inside ploegd**
* Status quo: dedup on receipt, best-effort publication, reconciliation for review settlement
* A message broker (NATS JetStream, RabbitMQ or similar) between the webhook handlers, the engine and the publishers

## Decision Outcome

Chosen option: "**Inbox and outbox tables in PostgreSQL**", because it fixes both directions with the database Ploeg already runs, puts the effect record in the same transaction as the effect, and needs no new deployable. A broker would still need an outbox to get a message out of a PostgreSQL transaction, and an inbox to dedup what it redelivers.

### 1. The inbox

A new table, `webhook_inbox`, in the next migration:

| Column | Meaning |
| --- | --- |
| `id` | `BIGSERIAL` primary key |
| `provider` | The configured provider name from the route (`forgejo`, `gitlab`, `vikunja`, `clickup`, or a named instance) |
| `delivery_key` | The dedup key, see *Delivery keys* |
| `key_source` | `header` or `content` |
| `payload_sha256` | SHA-256 of the raw body as received |
| `payload` | The raw body (`BYTEA`, already bounded to 1 MiB by the parsers), plus the headers the parser reads, as JSON |
| `status` | `pending`, `processing`, `done`, `dead` or `conflict` |
| `attempts` | Processing attempts started |
| `last_error` | The last processing error, truncated |
| `received_at`, `next_attempt_at`, `claimed_until`, `processed_at` | Timestamps for the worker and for operators |

`UNIQUE (provider, delivery_key)`, and a partial index on `(next_attempt_at)` where `status` is `pending` or `processing`.

**Delivery keys.** A key is taken only from a request that has already authenticated.

* Forgejo: `X-Forgejo-Delivery`, else `X-Gitea-Delivery`. The header is not covered by the body HMAC; it is bound to a request whose body is. A replayed signed body with a new header gets a new row and does nothing twice, because the effects are idempotent (section 2).
* GitLab: the delivery header GitLab keeps the same across its retries (`Idempotency-Key` on versions that send it, else `X-Gitlab-Event-UUID`). The implementation confirms which one the deployed GitLab sends before relying on it.
* Vikunja and ClickUp send no delivery id. The key is the hex SHA-256 of the authenticated raw body, with `key_source = content`. This was chosen over a key assembled from parsed fields (task id, event name, ClickUp history item ids) because it needs no parsing before the row is stored, it never merges two different events (their bodies differ at least in a timestamp or a history item), and it works the same for any future provider without an id. The cost: a provider that re-serializes the body on retry produces a new key, so dedup by key is a first line and idempotent effects are the guarantee. For a content key, "same key, different payload" cannot happen.
* An authenticated request with no key at all (a Forgejo without the header) gets a content key, never an empty one.

**Receive.** Each webhook handler does three things and nothing else:

1. Verify. `ParseWebhook` is split so that verification of the bounded raw body happens first and returns the body. An unauthenticated request writes nothing and gets 400, as today.
2. Insert. `INSERT … ON CONFLICT (provider, delivery_key) DO NOTHING RETURNING id`. If the row exists with the same `payload_sha256`, it is a redelivery: answer 202 and do nothing, whatever its status, because the worker owns it from here. If the row exists with a different hash, the delivery is a conflict: store it in `webhook_inbox_conflicts` (provider, key, both hashes, the new payload, `received_at`), keep the original row untouched, log at error and answer 409, so the forge's own delivery log shows it.
3. Answer 202 after the insert commits. A failed insert answers 500 so the provider may retry.

The handler no longer parses events, audits, ingests or touches a Shift. The old `forge_deliveries` table and `SeenDelivery` are removed once the inbox is live; their rows expire within the 48 hours they are already kept.

**Process.** A worker loop inside ploegd, next to the sweep in `cmd/ploegd/sweep.go`:

* Claim: `UPDATE webhook_inbox SET status = 'processing', attempts = attempts + 1, claimed_until = now() + interval '2 minutes' WHERE id IN (SELECT id FROM webhook_inbox WHERE status IN ('pending', 'processing') AND next_attempt_at <= now() AND (claimed_until IS NULL OR claimed_until < now()) ORDER BY id LIMIT 20 FOR UPDATE SKIP LOCKED) RETURNING …`. The claim commits before processing, so no transaction stays open across a forge call (`mirror` reads the tracker during ingest). A worker that dies leaves `claimed_until` to expire, and another replica takes the row.
* Order: rows of one provider and one subject (pull request or tracker item) are processed in `id` order. A claim skips a row while an older unfinished row for the same subject exists. Without this, a retried "assigned" could land after a later "unassigned".
* Effects: the worker parses the stored body with the provider's parser (no second verification) and runs the same effects as today, each one idempotent per section 2. Where an effect and its bookkeeping both live in PostgreSQL, the row moves to `done` in the same transaction as the last effect.
* Retry: an error sets `status = 'pending'`, `last_error`, and `next_attempt_at = now() + min(2^attempts × 5 s, 15 min)` with ±20 % jitter. An unknown Work Item, an unrouted repository or an event Ploeg does not act on is `done`, not an error: the event was handled by deciding to do nothing.
* Dead letter: after 10 attempts (about two hours at the cap) the row becomes `dead` and keeps its `last_error`. An operator can set a `dead` row back to `pending` through the operator API; nothing else revives it.

### 2. Idempotent effects per event kind

Dedup by key does not cover a redelivery with a new key (Forgejo's "redeliver" button may mint one) or a crash between two effects of one row. Each effect therefore carries its own natural key, and a repeat is a no-op:

| Event | Effect | Idempotency |
| --- | --- | --- |
| any forge event | `AuditForgeEvent` | Keyed by `(inbox_id, event_index)`; `ON CONFLICT DO NOTHING` |
| `pr_opened`, `pr_synchronized`, `pr_merged`, `pr_closed`, reviews | `recordPullRequestFacts` | Already an upsert on `(forge, owner, repo, number)`; review rows gain a unique key on the forge's review id |
| `pr_merged`, `pr_closed` | `Reviews.HandleForgeEvent` | Already a state compare-and-swap; a settled item does not settle again |
| `review_submitted` with changes requested | `RecordChangesRequested` | New unique key `(provider, repo, pr, review id)` on `work_item_reviews`, or `(provider, repo, pr, reviewer, head_sha, body hash)` where the forge sends no review id |
| `check_failed` | `CreateRepairFollowUp` | Already refuses a second open repair (`FollowUpDuplicate`); keyed additionally by `(source, branch, head_sha)` so a closed repair is not reopened by a repeat of the same failure |
| tracker `assigned` | `IngestAssigned`, `EnsureShift` | Already an upsert and an "ensure" |
| tracker `unassigned`, `closed` | withdrawal | Already a state compare-and-swap |
| tracker `updated`, `closed` | `observeGate`, `observeEpics` | `gate_transitions` and `status_transitions` gain a unique key on `(work_item_id, status, observed_at)` from the tracker's own change time |
| routing refusal | `RefuseRoute` audit row plus tracker comment | The audit row is keyed by `inbox_id`; the comment becomes an outbox intent (section 3) |

### 3. The outbox

A new table, `publication_outbox`:

| Column | Meaning |
| --- | --- |
| `id` | `BIGSERIAL` primary key |
| `logical_id` | A deterministic id, unique: the same publication always gets the same id, see below |
| `kind` | `review_findings`, `budget_notice`, `tracker_close_comment`, `tracker_status`, `route_refusal_comment`, `usage_report`, `run_card` |
| `class` | `required` or `cosmetic` (section 4) |
| `target` | JSON: forge or tracker provider, repository and pull request number, or tracker external id |
| `work_item_id`, `shift_id`, `run_id` | For operator views; nullable |
| `body` | The rendered body, marker included, rendered when the intent is written |
| `status` | `pending`, `sending`, `sent`, `dead` or `superseded` |
| `attempts`, `last_error`, `next_attempt_at`, `claimed_until` | As in the inbox |
| `remote_id` | The comment id the forge or tracker returned, or found by marker |
| `created_at`, `sent_at` | Timestamps |

**Written with the lifecycle change.** The intent is inserted in the same transaction as the state change that makes it due, with `ON CONFLICT (logical_id) DO NOTHING`:

| Publication | Transaction | Logical id |
| --- | --- | --- |
| Review findings of one reading Run | The one that stores the Run's outcome (`handleOutcome` → store) | `findings:run:<run_id>` |
| Budget-stop explanation | `CloseShiftAndSettle`, when the close reason is budget exhaustion | `budget:shift:<shift_id>` |
| Tracker comment at a terminal close | `CloseShiftAndSettle`, when the settle is terminal and the item has a tracker | `tracker-close:shift:<shift_id>` |
| Tracker status at a terminal close | Same | `tracker-status:shift:<shift_id>` |
| Routing refusal comment | `RefuseRoute` | `route-refusal:<provider>:<external_id>:<inbox_id>` |
| Usage report | Any change that moves its figures | `usage:shift:<shift_id>`; a newer intent marks older pending ones `superseded` |
| Run card comment | As in [ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md) | `card:work-item:<work_item_id>`; same superseding |

Findings are written per Run, not per Round, so the evaluator race disappears: two evaluators no longer publish anything, they only advance the Round. A findings intent becomes eligible when its Round is complete or its Shift is closed, which keeps today's rule that findings reach the pull request when their Round finishes. The cancellation path (`close` without notifying) writes no tracker intents, as today.

**Delivery worker.** A loop inside ploegd, claimed like the inbox (`FOR UPDATE SKIP LOCKED`, a lease in `claimed_until`, no transaction across the network call). Per intent:

1. If `attempts > 0`, an earlier attempt may have succeeded remotely and crashed before its acknowledgement. The worker lists the target's comments (`ForgeProvider.Comments`, `BoardReader.BoardComments`) and looks for the intent's marker. Found: record `remote_id`, set `sent`, stop. If the list fails, the worker does not post blind; it schedules a retry. This is the rule `publishUsageReport` already follows.
2. Otherwise set `status = 'sending'`, `attempts = attempts + 1` and commit, then send.
3. Success: `status = 'sent'`, `remote_id`, `sent_at`. Failure: back to `pending` with backoff as in the inbox. A 4xx that cannot succeed by waiting (pull request deleted, item gone, 404 or 410) goes straight to `dead` with the reason.
4. `usage_report` and `run_card` edit the marked comment when it exists, as today, and post when it does not.
5. `tracker_status` needs no marker: `SetStatus` to the same state is idempotent.

**The marker.** Every forge comment body starts with `<!-- ploeg:intent <logical_id> -->`. Forgejo and GitLab render Markdown and hide the comment. On trackers the marker depends on what the tracker keeps: Vikunja comments are HTML and may be sanitized, ClickUp comments are plain text where an HTML comment shows as text. The implementation tests each tracker. Where a hidden marker does not survive, the comment ends with a short visible line `ref: <logical_id>`.

**Dead letter.** After 12 attempts (about three hours at a 15-minute cap) a `required` intent becomes `dead`. A `cosmetic` intent becomes `dead` after 5. An operator can reset a `dead` intent to `pending`.

### 4. Required evidence and cosmetic publications

A `required` publication is one a person acts on or must be told; it is retried to `dead` and shows in the alerts. A `cosmetic` one is replaced by its next version and only counts in metrics.

| Kind | Proposed class | Why |
| --- | --- | --- |
| `review_findings` | required | The review a human is waiting for ([ADR-0011](0011-the-pull-request-is-the-blackboard.md)) |
| `budget_notice` | required | The only explanation on the pull request of why work stopped |
| `tracker_close_comment` | required | The board's notice that a person must merge or step in |
| `route_refusal_comment` | required | Tells the person who assigned the item why nothing started |
| `tracker_status` | required | Moves the item on the board, where the provider maps a state; a no-op provider marks it `sent` at once |
| `usage_report` | cosmetic | A view of stored figures; the next refresh replaces it |
| `run_card` | cosmetic | A view of the card ([ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md)); the sweep re-renders it |

This classification is an owner question (see below).

### 5. Operator visibility

* `/metrics` gains gauges computed from SQL like the others ([alerts](../ops/alerts.md)): `ploeg_webhook_inbox_pending{provider}`, `ploeg_webhook_inbox_oldest_pending_seconds{provider}`, `ploeg_webhook_inbox_dead{provider}`, `ploeg_webhook_inbox_conflicts{provider}`, `ploeg_publication_outbox_pending{kind,class}`, `ploeg_publication_outbox_oldest_pending_seconds{class}` and `ploeg_publication_outbox_dead{kind,class}`. The chart's `PrometheusRule` gains alerts on dead or conflicting inbox rows, on dead required intents, and on a required intent pending for more than 30 minutes.
* The operator API (`operator-api.v1`) gains, additively: `GET /api/v1/operator/summary` reports `inbox: {pending, dead, conflicts, oldestPendingAt}` and `publications: {pending, dead, oldestPendingAt}`; `GET /api/v1/operator/work-items/{id}` lists that item's undelivered publications with kind, class, status, attempts and last error; `GET /api/v1/operator/inbox?status=dead|conflict` lists rows without payloads; `POST /api/v1/operator/inbox/{id}/retry` and `POST /api/v1/operator/publications/{id}/retry` reset a `dead` row. Vloer shows them when its operator views are next touched; that is not part of this decision.

### 6. Retention

* Inbox `done` rows keep their payload for 7 days, then the payload is cleared; the row (key, hash, timestamps) is deleted after 30 days. 30 days is longer than any provider's retry window and covers a manual redelivery from a forge's history.
* Inbox `dead` and `conflict` rows, and `webhook_inbox_conflicts`, are kept until an operator retries them, and deleted after 90 days.
* Outbox `sent` and `superseded` rows are deleted after 30 days; `dead` rows after 90 days.
* The deletions run in the existing sweep loop, as `SweepDeliveries` does today.

### Consequences

* Good, because a delivery is "seen" only when its effects ran, so a crash or an error between receipt and effect is retried instead of lost.
* Good, because GitLab, Vikunja and ClickUp deliveries get dedup for the first time, and every effect becomes safe to repeat, which also covers a redelivery under a new id.
* Good, because a forge or tracker outage leaves pending intents that publish after recovery, and a crash between a remote success and the local acknowledgement does not post twice.
* Good, because concurrent evaluators no longer publish findings at all, so the duplicate the code comment accepted is gone.
* Good, because stuck work in either direction shows in `/metrics`, in alerts and in the operator API.
* Bad, because webhook effects are now asynchronous. Assignment to a queued Work Item takes one worker tick longer, and tests that assert an effect right after the 202 must wait for the worker.
* Bad, because the change touches the four parsers, both webhook handlers, the follow-up, gate and status writers, `CloseShiftAndSettle`, the outcome store, every publisher and the operator contract, with new unique keys on tables that may already hold duplicates. The migration must de-duplicate before it adds them.
* Bad, because the inbox stores raw webhook bodies for up to 7 days. They hold tracker titles and descriptions and review text. They are already in the forge and the tracker, but they are a second copy in Ploeg's database.
* Neutral, because the tracker marker may have to be visible on some trackers.

### Confirmation

When this is implemented, `go test ./...` in `apps/ploeg`, run by `.forgejo/workflows/on_pull_request.yml`, covers it with these tests against the embedded PostgreSQL and `httptest` fakes. Each fails on today's code:

* **Crash after receipt recovers.** A signed Forgejo delivery is inserted and answered 202; the handler's process is cancelled before any effect runs. A fresh worker claims the row after `claimed_until` passes, runs the effects, and the audit row, the pull request facts and the requested-changes row exist once. The same test for Vikunja `assigned` queues the Work Item once.
* **Duplicate authentic delivery has one effect.** The same signed delivery sent twice gives one inbox row and one `work_item_reviews` row. The same review sent again with a different `X-Forgejo-Delivery` gives two inbox rows and still one review row. A Vikunja body sent twice gives one row with a content key. A GitLab delivery with its delivery header sent twice gives one row.
* **Same key, different payload is a conflict.** A second signed body under an existing Forgejo delivery id answers 409, leaves the first row and its effects unchanged, writes a conflict row and raises `ploeg_webhook_inbox_conflicts`.
* **An unauthenticated request writes nothing.** A bad signature inserts no inbox row, and a valid delivery with the same id afterwards is processed (F01's acceptance).
* **Retry and dead letter.** An effect that fails three times is retried with growing `next_attempt_at`, then succeeds; one that always fails is `dead` after the cap, appears in the operator API and the gauge, and runs after an operator retry.
* **Forge outage leaves a pending intent that publishes after recovery.** With the fake forge answering 503, a reading Run's outcome and a budget close produce `pending` findings and budget intents and the Shift closes as before; the fake recovers; the worker posts each once with its marker, and the intents are `sent`.
* **Crash between remote success and local ack does not duplicate.** The fake forge accepts the comment and the worker is stopped before it records `sent`. The next attempt lists the comments, finds the marker, records `remote_id` and posts nothing. The same with a failing comment list posts nothing and retries.
* **Concurrent evaluators publish once.** Two `evaluate` calls on the same complete Round produce one findings comment per reading Run.
* **A close in one transaction.** `CloseShiftAndSettle` with a budget close reason commits the settle and the budget and tracker intents together; a forced error rolls back all of them.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Status quo: dedup on receipt, best-effort publication, reconciliation for review settlement

* Good, because nothing changes and webhook effects stay synchronous and easy to follow in a log.
* Good, because `Reconcile` already repairs the most expensive loss, a merge or close that did not settle.
* Bad, because every other effect lost after the dedup insert is lost for good, and the redelivery that would repair it is ignored.
* Bad, because three of four providers have no dedup, and duplicated reviews become duplicate fix Rounds.
* Bad, because a closed Shift's notices are never retried, so an outage at the wrong minute leaves a person uninformed with no trace but a log line.
* Bad, because widening reconciliation to every effect means polling every provider for everything, which the forges' and trackers' rate limits and APIs do not allow.

### A message broker (NATS JetStream, RabbitMQ or similar)

* Good, because brokers bring retries, dead-letter queues and consumer groups.
* Bad, because publishing to a broker after a PostgreSQL commit has the same dual-write gap this record closes; it needs an outbox table anyway.
* Bad, because brokers deliver at least once, so consumers still need the inbox's dedup and the idempotent effects.
* Bad, because it is a new stateful service to deploy, back up and monitor in a homelab-scale installation and later in every self-hosted one, against the review's explicit advice.
* Neutral, because the outbox table can feed a broker later if Ploeg ever needs fan-out to other services.

## Re-evaluation triggers

* Inbox or outbox throughput passes 50 rows a second for an hour, or the claim query passes 100 ms at p95: partition, or reconsider a broker.
* A provider sends a delivery id Ploeg can verify cryptographically (a signed id or a signed timestamp): use it as the key and drop the content key for that provider.
* A tracker refuses both a hidden and a visible marker, or edits it out: tracker publications need another dedup, such as storing the remote id before a retry is possible.
* A second Ploeg deployment shares a forge or tracker with this one: logical ids need a deployment prefix.
* More than five `dead` required intents in a month: revisit the caps and the classification.

## More Information

* Technical story: VIK-1727 (inbox) and VIK-1728 (outbox), from the 2026-10-02 code-quality review findings F01 and F15 ([research record](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/research/2026-10-02-code-quality-review.md)).
* 2026-10-03: proposed.
* Builds on PR #141, which moved signature verification in front of the dedup write (VIK-1715).
* Supersedes nothing. Replaces the mechanism of migration `0011` (`forge_deliveries`) and the at-least-once rule in `publishRound`'s documentation.
* Related: [ADR-0011](0011-the-pull-request-is-the-blackboard.md), [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md), [ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) and [ADR-0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md) (both name a lost webhook as a trigger), [ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md) (the marker precedent).

### Owner questions

1. **Classification.** Is the required/cosmetic split in section 4 right? In particular: is the tracker status write required, given most providers ignore it today, and should the usage report be required while it is the only cost evidence on the pull request?
2. **Content keys.** Vikunja and ClickUp are keyed by the SHA-256 of the raw body. Accept that, or key ClickUp by its history item ids so a re-serialized retry still dedups and a conflict can be detected?
3. **Conflict answer.** A reused delivery id with a different payload answers 409 so it shows in the forge's delivery log. Prefer 202 with the conflict recorded only in Ploeg?
4. **Retention of payloads.** Raw bodies are kept 7 days and key rows 30 days. Shorter, given the bodies carry tracker and review text?
5. **Visible tracker marker.** Where a tracker strips hidden markers, is a visible `ref: …` line on Ploeg's tracker comments acceptable?
6. **Caps.** 10 inbox attempts and 12 required-publication attempts, with a 15-minute backoff cap, give up after two to three hours. Should a required publication instead retry for a day, since a forge outage over a weekend is the case this is for?
