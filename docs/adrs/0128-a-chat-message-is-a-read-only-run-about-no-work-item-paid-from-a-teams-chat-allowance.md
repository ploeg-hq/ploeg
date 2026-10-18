---
status: proposed
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A chat message is a read-only Run about no Work Item, paid from a Team's Chat Allowance

## Context and Problem Statement

An operator consumer wants a person to chat with their agent work: the person types a message, and the consumer answers with a model that may call the consumer's own read and act tools, often several times for one message. Every model call a deployment makes for its work is authorized, capped and metered by Ploeg ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)).

The Ask ([ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)) already authorizes one model call outside any Shift: a read-only Run with the Role `ask`, held against a Team's monthly Ask Allowance, which returns a capped key to the consumer. But an Ask is about one existing Work Item, its Run belongs to that Work Item, and its spend shows in that Work Item's facts. A chat message is about no particular Work Item, and `agent_runs.work_item_id` is required.

How does Ploeg authorize, cap and settle a model call that belongs to a Team but to no Work Item, and who pays for it?

## Decision Drivers

* Ploeg never runs a model and never holds a prompt; it authorizes, mints a capped credential and settles (ADR-0008, ADR-0082).
* Chatting must not spend any Work Item's Shift Budget, appear in any Work Item's delivery facts, or use up the Asks a Team has left.
* Every Hold is released by state, and concurrent admissions cannot overspend ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)).
* A consumer must not have to ask the owner to configure another policy before chats work: the owner chose the Team's Ask model for chats.
* Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* A chat Run with no Work Item, paid from a separate Chat Allowance and metered under the Team's ask policy
* Admit each chat message as an Ask on a placeholder Work Item per Team
* Pay chat messages from the Team's Ask Allowance
* A new `(team, chat)` inference policy for chat Runs

## Decision Outcome

Chosen option: "A chat Run with no Work Item, paid from a separate Chat Allowance and metered under the Team's ask policy", because it reuses the Ask's admission, key and settlement unchanged, keeps chat spend out of every Work Item, and needs no configuration a Team with Asks does not already have.

**A chat message is a Run with no Work Item.** It is an `agent_runs` row with the Role `chat`, `writes` false, `work_item_id` null, and no Shift, no Round and no Lease. Migration `0060_chats.sql` drops the column's `NOT NULL` and adds a check that only a non-writing `chat` Run without a Shift may leave it empty, so every other Run still belongs to a Work Item. No worker claims a chat Run and its run token never leaves Ploeg. It runs from admission until the consumer finishes it or its deadline passes, and the deadline is the key's TTL. A crew Role cannot be named `chat`. A `chats` row beside the Run keeps its consumer, `chatId`, actor, acting user (`askedBy`), Team, allowance and the SHA-256 of the message, never the text.

**The Chat Allowance is the Ask Allowance's ledger with another purpose.** `inference_allowances` gains rows with the purpose `chat`, the scope kind `team` and the period of one UTC calendar month, as ADR-0082 designed it to. A period's row is created by its first chat message with `PLOEG_CHAT_ALLOWANCE_USD` (default 2.00 USD) and its limit stays fixed for the period. Admission locks the row and admits only when `limit − settled − held` covers the per-chat Budget, exactly as an Ask does; a refusal answers 402 with the allowance and its reset time. The per-chat Budget is `PLOEG_CHAT_BUDGET_USD` (default 0.05 USD, larger than the per-Ask Budget because one message may take several tool-calling model calls).

**The model is the Team's Ask model.** A chat Run is metered under the Team's `(team, ask)` managed inference policy: its models, TTL and budget cap. The per-chat Budget is lowered to that policy's `budgetUsd` when the policy's is smaller, so a Team that wants the full 0.05 USD per chat message raises its ask policy's `budgetUsd`; its Asks keep their own, smaller `PLOEG_ASK_BUDGET_USD`. A Team without a `(team, ask)` policy has no chats: admission answers 409 `inference_policy` before recording anything. There is no `(team, chat)` policy, and one in configuration is never read for a chat Run.

**The routes are the Ask's, under the Team.** `POST /api/v1/operator/teams/{team}/chats` admits a chat message for a Team in the consumer's scope (404 outside it), with execute permission and exactly one `X-Ploeg-Actor`; `GET .../chats/{chatId}` reads it and `POST .../chats/{chatId}/finish` finishes it and blocks its key, idempotently. A replayed `chatId` with the same message and actor returns the same chat message and carries a key only if no mint had begun; anything else under that `chatId` answers 409 `chat_conflict`. Ploeg never stores a key and never mints a second one. `GET /api/v1/operator/allowances` takes `purpose=chat` for the Chat Allowance; without it, or with `purpose=ask`, it answers the Ask Allowance as before.

**Chat spend is the Team's, never a Work Item's.** A chat Run is absent from every Work Item's detail, delivery facts and Runs, and from the Run list, which is scoped by Work Item. The team summary's pending, running and finished Runs, settled spend and Hold count it under the Run's own Team; its settlement audit event, which has no Work Item, carries the Team and the Run. Team capacity, which limits Runs a worker claims, leaves chat Runs out as it does Asks. The unsettled-accounts read lists a stuck chat Run with `workItemId` null.

### Consequences

* Good, because a chat message costs a known, capped amount Ploeg authorized, without touching any Work Item, Shift or Lease, or the Team's Asks.
* Good, because admission, key issuance, blocking, the deadline sweep and settlement are the Ask's own code paths, so a crashed consumer cannot strand money.
* Bad, because a Run without a Work Item is a new shape: every query that reads `agent_runs.work_item_id` must accept null, and the unsettled-accounts contract's `workItemId` became nullable.
* Bad, because chats and Asks share one policy: changing the Team's ask models or TTL changes both, and the policy's `budgetUsd` caps both.
* Bad, because the consumer holds a live key for up to its TTL and may make many calls with it; the per-chat Budget caps what that can cost.

### Confirmation

* `go test ./pkg/store/` proves an admitted chat message creates a Run with no Work Item, Shift or Lease, that the schema refuses any other Run without a Work Item, that a replayed `chatId` returns the same chat and a reused one conflicts, that the Chat Allowance refuses what it cannot cover while the Ask Allowance is untouched, that the deadline sweep expires a chat Run, that settlement moves its Hold to settled spend in the team summary of its own Team only, that team capacity and a Work Item's facts leave it out, and that the unsettled-accounts read lists it with no Work Item.
* `go test ./pkg/httpapi/` validates every chat and allowance response against `operator-api.v1.schema.json`, checks execute permission, the actor, team scope (404 outside it), input (400 `invalid_chat`), 402 on an exhausted allowance, 409 without a `(team, ask)` policy even when a `(team, chat)` policy exists, 503 without managed inference, that the ask policy's model, TTL and smaller budget shape the key, that a replay mints no second key, that finishing blocks the key once, and that `purpose=chat` reads the Chat Allowance, against a LiteLLM fake built on `net/http/httptest`.
* `go test ./pkg/plan/` refuses a crew Role named `chat`.
* CI runs all three with `mise exec -- go test ./...`.

## Pros and Cons of the Options

### Admit each chat message as an Ask on a placeholder Work Item per Team

* Good, because it needs no migration and no new routes.
* Bad, because a placeholder Work Item is invented state that every Work Item list, summary and tracker sync would have to hide, and its facts would report chat spend as an Ask about work that does not exist.

### Pay chat messages from the Team's Ask Allowance

* Good, because it needs no second allowance.
* Bad, because a long chat would use up the Asks a Team has left for the month, and the owner wants the two capped apart.

### A new `(team, chat)` inference policy for chat Runs

* Good, because chats could use another model, TTL or cap than Asks.
* Bad, because every deployment would need new configuration before chats work, and the owner chose the Ask model for chats.

## Re-evaluation triggers

* A Team wants chats on another model, TTL or budget cap than its Asks.
* A consumer needs a chat scope other than a Team, such as a person, a Client or a Tenant, or a period other than a calendar month.
* A chat message regularly needs more than the per-chat Budget, or settlement lags so far behind that held chat messages refuse admissions the allowance could afford.
* A consumer needs Ploeg to keep the conversation, or to link a chat message to the Work Items it read or acted on.

## More Information

* 2026-10-18 — The owner decided that a chat message is paid from a separate Chat Allowance (purpose `chat`, scope `team`, one UTC calendar month), that it uses the Team's ask policy, and that Ploeg keeps only the message's SHA-256.
* Contract: `docs/contracts/operator-api.v1.schema.json`, definitions `chatAdmitRequest`, `chatAdmitResponse`, `chatResponse`, `chat`, `chatCredential` and `chatAllowance`, and the widened `allowanceResponse`, `allowanceRefusalResponse` and `unsettledAccount`.
* Related: [ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md), [ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md), [ADR-0093](0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md).
