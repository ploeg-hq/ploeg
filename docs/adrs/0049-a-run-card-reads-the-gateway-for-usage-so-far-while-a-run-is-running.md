---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run card reads the gateway for usage so far while a Run is running

## Context and Problem Statement

On 2026-10-01 the owner looked at a Work Item whose builder had run for 31 minutes and asked what was going on: "Been running for a while but no reports". The card read "Not reported" for cost, tokens and run time. The Run was healthy. It had made 86 model calls, spent US$ 0,27 of US$ 2,00 and changed nine files. The figures were missing because [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) builds a card from stored facts only. A Run stores its usage when it finishes, and its run time counts only once it has a `finished_at`. The gateway, LiteLLM, records every call as it happens. So while a Run is running, the card's figures stay empty, which looks the same as a broken Run.

## Decision Drivers

* A running Run must not look broken. A card shows what has been spent so far.
* Unknown stays unknown ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)). If the gateway can't be read, the figure is absent, never zero.
* Reading a card never moves a budget, a hold or a settlement ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)).
* `operator-api.v1` changes only additively.
* No new hoster configuration. The owner decided on 2026-10-01 to keep it to a minimum.

## Considered Options

* **The card endpoint reads the gateway's spend logs for each running Run and returns a separate `live` block**
* The worker reports usage to Ploeg periodically while it runs
* Vloer reads the gateway itself
* Fold the gateway reading into `totals`

## Decision Outcome

Chosen option: "**the card endpoint reads the gateway for each running Run, into a separate `live` block**". The gateway already holds the figures, and Ploeg already reads the same spend logs to settle a Run. The stored totals stay the record.

1. **When.** `GET /api/v1/operator/work-items/{id}/card` sets `live` while at least one of the Work Item's Runs is `running`, and `null` otherwise.
2. **What.** `live` has `runningRuns`, `observedAt`, `runSeconds`, `costUsd`, `inputTokens`, `outputTokens` and `usageComplete`. Each figure is what the finished Runs recorded plus the gateway's total so far for each running Run. `runSeconds` counts a running Run up to `observedAt`. The schema is the `cardLive` definition in `operator-api.v1.schema.json`.
3. **Source.** `LLMControl.Live` reads the Run's keys through the same `SettledSpendForRun` spend-log read that settlement uses, with the account's recorded key identity. It writes nothing: no `observed_spend`, no audit row, no ledger change.
4. **Failure.** All gateway reads for one card share a three-second deadline. If any running Run can't be read (no managed inference, no account yet, gateway down or slow), `live` keeps `runningRuns` and `runSeconds` and leaves out cost and tokens, with `usageComplete: false`.
5. **Vloer.** While `live` is set, the card shows its cost, tokens and run time as "so far". A figure missing from `live` reads "Not reported yet". `totals` is unchanged and is still what a finished card shows.

### Consequences

* Good, because a running Run shows its real spend and progress. A stalled Run now looks different from a busy one.
* Good, because `totals` and settlement are untouched. A reading can be wrong or late without changing what Unfold charges.
* Bad, because each card read during a Run costs one key list and one spend-log read per running Run. Vloer refreshes a card while live updates are on. The deadline caps the delay to three seconds, but a long Run's spend log grows with every call.
* Bad, because the reading is provisional. LiteLLM writes spend logs asynchronously, so the last few calls can be missing, and the settled figure can still differ slightly.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store`: `live` adds the gateway reading of a running Run to a finished Run's figures and reads only running Runs; without a reader, or after a failed read, it reports run time only; a Work Item with no running Run has no `live`; `totals` is unchanged.
* `pkg/httpapi`: the card endpoint's `live` validates against `operator-api.v1` and carries the fake gateway's spend and tokens for a running Run; reading the card leaves the account `issued` with no observed spend; without managed inference, cost and tokens are absent.

In `apps/vloer`, `npm test` covers the proxy passing `live` through validated, and the card's "so far" and "Not reported yet" figures.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### The worker reports usage periodically

* Good, because a card read would cost no gateway call.
* Bad, because it adds a write path and a schedule to every Run, plus a new failure mode, to copy figures the gateway already has.

### Vloer reads the gateway itself

* Bad, because Vloer must never hold the LiteLLM master key (`apps/vloer/AGENTS.md`).

### Fold the reading into `totals`

* Good, because Vloer would change less.
* Bad, because `totals` would mix settled facts and a provisional reading, and a finished card could no longer be told apart from a running one.

## More Information

* [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md): the card `live` is added to.
* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md): settlement, which this reading leaves alone.
* The settlement read this reuses is `LLMControl.Settle` in `pkg/httpapi/llm_control.go`.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. The facts this record keeps stay and are exposed raw through the delivery facts endpoints; the card figures built on them are removed in the next minor release.

## Re-evaluation triggers

* A card read during a Run passes 1 s at p95, or the gateway's spend-log reads show up in its own latency.
* LiteLLM offers a per-key usage total that includes tokens, so one cheap read could replace the spend-log scan.
* The worker starts reporting usage to Ploeg during a Run for another reason.
