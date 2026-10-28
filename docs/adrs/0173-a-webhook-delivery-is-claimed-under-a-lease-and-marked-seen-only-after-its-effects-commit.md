---
status: proposed
date: 2026-10-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A webhook delivery is claimed under a lease and marked seen only after its effects commit

## Context and Problem Statement

Threat-model finding F-05 ([issue #294](https://github.com/ploeg-hq/ploeg/issues/294)) found four faults in how ploegd deduplicates webhooks. The tracker path checked whether a delivery was recorded, acted, then recorded it, so two concurrent copies of one delivery both acted. The forge path recorded the delivery id before its effects ran, so a crash between the two lost the effects and the forge's redelivery was answered "seen". Vikunja, ClickUp and GitLab deliveries had no id at all and were never deduplicated, so a captured body replayed later re-queued paid work. `work_item_reviews` had no unique key, so a review delivered twice became a second fix round.

[ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md) proposes a durable inbox that fixes all of this and more, but it is not built and stays proposed. How does ploegd stop replays and duplicate effects now, without the inbox?

## Decision Drivers

* Deduplication records an effect, not a receipt: a delivery whose effects did not commit must act again when it is redelivered.
* Two concurrent copies of one delivery must never both act.
* A replayed body must be recognised even when the sender puts no delivery id on it.
* Two real changes must never be merged into one delivery.
* The change must fit the `forge_deliveries` table and the handlers as they are, so ADR-0060 can still replace them.

## Considered Options

* **Claim each delivery in one statement under a lease, complete it after the effects, derive a synthetic id from the signed payload for senders without one, and key each stored review on the forge's review identity**
* Run every effect of a delivery in the transaction that records it
* Hash the raw body as the delivery id for senders without one (ADR-0060's content key)
* Build the ADR-0060 inbox now

## Decision Outcome

Chosen option: "**Claim under a lease, complete after the effects**", because it closes every fault F-05 names with one migration and no new worker.

* `Store.ClaimDelivery` inserts or updates the `forge_deliveries` row in one statement. A handler gets `claimed` when the row is new or its earlier claim expired without completing, `in_flight` when another handler holds a live claim, and `completed` when the effects already committed. The lease is five minutes.
* The handler runs the effects, then calls `CompleteDelivery`. A tracker delivery whose effect fails calls `ReleaseDelivery`, answers 500 and acts again on the next delivery. A handler that crashes leaves a claim that expires, and the next delivery after the lease acts again. A copy that arrives while the claim is live is answered 409 with `Retry-After: 60`. Complete and release act only on the handler's own claim token, so a handler that wakes after its lease expired cannot undo the claim another handler took over.
* Vikunja and ClickUp put no delivery id on their webhooks, and GitLab before 17.4 sends no `Idempotency-Key`. Their providers return a synthetic id: a SHA-256 over the provider, the event kind, the object and when it changed, read from the payload after its signature or token was checked.
  * Vikunja: event name; task, project, assignee and related task; the payload's `time`, else the task's `updated`.
  * ClickUp: event name; task and the ids of its history items; the latest history item date.
  * GitLab: `Idempotency-Key` when sent, else object kind; project, object id, action or status; `updated_at`, else `finished_at`.
  * A payload without a change time gets no id and is not deduplicated, because an id without one would merge two real changes.
* `work_item_reviews.review_key` holds the forge's review id (GitHub's review id, a GitLab note id) or, when the forge sends none (Forgejo), a digest of reviewer, head and body. A unique index on Work Item, provider, repository, pull request and key makes `RecordChangesRequested` store a redelivered review once and report `duplicate` without queueing the Work Item again.
* Existing `forge_deliveries` rows are marked completed by the migration. Existing reviews keep an empty key and are not deduplicated against.

### Consequences

* Good, because a crash or a failed effect no longer loses a delivery, and concurrent duplicates act once.
* Good, because a captured Vikunja, ClickUp or GitLab body replayed within the 48-hour retention is recognised.
* Bad, because the synthetic id is built from parsed fields, so a sender that changes a field it does not mean as a change (a re-serialised timestamp) defeats it. ADR-0060's content key has the same weakness for any byte. The effects stay idempotent where they were.
* Bad, because a forge that redelivers within the five-minute lease after a crash gets 409 and must redeliver again later. GitHub, Forgejo and GitLab show a 409 as a failed delivery that can be redelivered.
* Bad, because effects that run outside the handler's own transaction (the forge path logs and carries on after an audit error, as before) are still at-least-once, not exactly-once. ADR-0060 is the fix for that.
* Neutral: delivery-id headers are still not covered by any signature, and no provider checks a timestamp window. A replay with a fresh `X-GitHub-Delivery` or `X-Forgejo-Delivery` header still gets a new id. The review key now stops such a replay from becoming a second fix round; T-FG-3 stays open for settlement from a Forgejo or GitLab payload.

### Confirmation

`go test ./pkg/store/ ./pkg/httpapi/ ./pkg/provider/...` runs in CI (`ci` / `verify`). It covers concurrent claims of one delivery (`TestClaimDelivery_ConcurrentDuplicatesClaimOnce`, `TestTrackerWebhook_ConcurrentDuplicateDeliveriesActOnce`, `TestForgeWebhook_ConcurrentDuplicateDeliveriesActOnce`), a crash between claim and effect (`TestClaimDelivery_ACrashBeforeCompletionLetsTheRedeliveryAct` and the two `ACrashBetweenClaimAndEffect` handler tests), a live claim answered 409, synthetic ids per provider (`delivery_id_test.go` in each provider), and a redelivered review (`TestRecordChangesRequested_ARedeliveredReviewIsNotASecondFixRound`). Each was mutation-tested in the pull request that introduced it.

## Pros and Cons of the Options

### Run every effect of a delivery in the transaction that records it

* Good, because the record and the effects commit together.
* Bad, because the effects call trackers and forges and span many store calls that each open their own transaction; holding one transaction across them would hold row locks across network calls.

### Hash the raw body as the delivery id

* Good, because it needs no parsing and never merges two different bodies.
* Bad, because issue #294 asks for an id from provider, event kind, object and change time, documented per provider, so an operator can tell from the fields why two deliveries were treated as one.

### Build the ADR-0060 inbox now

* Good, because it also gives retries, a dead-letter state and operator visibility.
* Bad, because it is a new worker, a new table and a change to every handler, scheduled with the outbox; F-05's replay part is fixed without it.

## Re-evaluation triggers

* ADR-0060's inbox is built: it replaces the claim on `forge_deliveries`.
* A provider signs its delivery id or adds a signed timestamp, so a replay window can be checked.
* Vikunja or ClickUp adds a delivery id header, or GitLab 17.3 and older leave support.
* A real delivery is dropped as a duplicate of a different change.
* A forge redelivers faster than the lease after a crash in production.

## More Information

* Threat model: [F-05 and T-PD-2](../security/threat-model.md#findings).
* Migration `0081_webhook_delivery_claims_and_review_keys.sql`; `pkg/store/deliveries.go`; `pkg/httpapi/webhook_delivery.go`; `provider.SyntheticDeliveryID` in `pkg/provider/delivery.go`.
