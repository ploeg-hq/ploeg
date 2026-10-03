---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Budgets are two-level: a Shift pool, authorized and settled per Run

## Context and Problem Statement

Today every Run mints its own LiteLLM key with its own budget
([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)). With one Run
per item that is also the item's ceiling. With a Shift of five personas plus
retries it is not: five personas at $2 is $10, and nothing bounds it.

Two limits are wanted and they are not the same kind of number. A per-agent cap
stops one agent looping away a fortune. A per-ticket cap stops the *item* from
costing more than it is worth. They must not simply sum, because unspent
allowance should remain available to whoever needs it.

Runs in a Shift may start **simultaneously** (ADR-0010), which is what makes
this non-trivial: five concurrent Runs each checking "is there room?" all see
the full pool and all proceed.

## Decision Drivers

* **The two limits protect against different failures** — a runaway agent versus
  a runaway ticket — so they need different enforcement points.
* **Unspent allowance must not be stranded.** A cheap philosopher should leave
  more for the writer without anyone rebalancing anything.
* **Concurrency is the hard part.** Any check-then-act across separate pods
  races.
* **Backlog #44 already concluded the gate belongs before dispatch**: a run that
  cannot afford to start should never spawn, never mint a key, never burn an
  attempt.
* **R2 — crash-safety must never depend on an agent behaving well.** A Run that
  dies must not strand the money it was holding.

## Considered Options

* Per-Run budgets only (today)
* Per-Run budgets plus a post-hoc alert when a Shift's total is exceeded
* Divide the Shift pool into fixed per-role allocations
* **A Shift pool, authorized per Run and settled on report**

## Decision Outcome

Chosen option: **the Shift holds a pool; each Run takes an authorization against
it and settles on report** — the shape of a card payment: a hold for the
estimated amount, replaced by the real one.

The per-Run key is minted for `min(roleCap, poolRemaining)`, so an agent can
never overrun the ticket ceiling mid-run — LiteLLM cuts it off exactly at the
pool boundary.

| Limit | Protects against | Enforced by |
| --- | --- | --- |
| Per-Run cap | one agent looping | LiteLLM — the minted key stops working |
| Shift pool | the item costing more than it is worth | Ploeg, **before** spawning |

**A hold lives on the Run that holds it, not in a counter on the Shift.**
`reserved` is summed over running Runs:

```sql
-- authorize, inside the claim transaction
SELECT budget, spent FROM shifts WHERE id = $1 AND closed_at IS NULL FOR UPDATE;
SELECT COALESCE(SUM(authorized), 0) FROM agent_runs
 WHERE shift_id = $1 AND state = 'running';
-- authorized := min(roleCap, budget - spent - reserved); refuse below the floor
UPDATE agent_runs SET state = 'running', authorized = $2 WHERE id = $1;
```

Locking the Shift row is what serialises concurrent claims, so five readers
starting together cannot each see the full pool. It is the same discipline the
lease already relies on — one row, one winner — and introduces no coordinator.

Settlement records what was actually spent, and that is all:

```sql
UPDATE shifts SET spent = spent + $2 WHERE id = $1
```

**No release statement exists, because none is needed.** A Run that stops
running stops appearing in the sum, so the hold releases itself the moment the
Run reaches any terminal state — reported, swept, or expired. A missed release
is impossible rather than merely unlikely, and unspent allowance returns to the
pool with nobody having to remember to put it back.

This replaces a `shifts.reserved` counter that an earlier draft of this record
specified. A counter can disagree with what is actually in flight; a sum
derived from the Runs cannot. It is also one column fewer.

`remaining = budget - spent - reserved` still holds; `reserved` is simply
computed rather than stored.

Two rules complete it:

* **Exhaustion is `needs_human`, never `failed`.** Retrying cannot fix running
  out of money — only a person can, by topping up or closing the ticket. R4
  requires the reason to be carried: *"exhausted its $10 pool after 7 runs"*.
* **A floor, not just a ceiling.** Below a minimum viable remainder, do not
  spawn: a Run against $0.04 dies having achieved nothing while burning an
  attempt. This is backlog #44's pre-dispatch gate and #60's "gate outcome, not
  a dispatched-then-failed run".

Crash safety reuses existing machinery: a dead Run's authorization is released
by the **lease sweeper**, in the same transaction that expires the lease. No new
timer, no second reaper.

### Consequences

* Good, because borrowing falls out for free. Nothing is pre-allocated, so an
  unspent hold returns to the pool at settlement and the next Run sees it.
* Good, because the pool is what actually bounds retries: `MaxAttempts` times a
  per-Run cap is otherwise unbounded in practice.
* Good, because it is correct under simultaneity without any cross-pod
  coordination.
* Good, because spend attribution is unchanged — per-Run LiteLLM keys and their
  `key_alias` stay exactly as ADR-0008 has them, so `homelab-cluster`'s Grafana
  dashboards keep joining.
* Bad, because the Shift row becomes a contention point: every spawn and every
  report updates it. At a handful of Runs per item this is nothing, and it is
  the same shape as the existing lease row.
* Bad, because an authorization is an estimate, so a Shift can look more
  committed than it is until settlement. Accepted — the error is always
  conservative, never an overspend.
* Bad, because per-role caps are one more thing to configure. Accepted: without
  them a single agent can consume the whole pool on its first run.

### Confirmation

All in `pkg/store/shift_test.go`, gated by `go test ./pkg/store/` in
`.forgejo/workflows/on_pull_request.yml`:

* `TestAuthorizeIsAtomicUnderConcurrency` — five goroutines authorize against a
  pool that funds two; exactly two succeed, three get `ErrBudgetExhausted`, and
  `spent + reserved` never exceeds `budget`.
* `TestSettlementReleasesTheHoldAndRecordsSpend` — reserved returns to zero and
  unspent allowance returns to the pool.
* `TestSweptRunCannotReport` — sweep a Run mid-flight and assert both that it
  cannot report afterwards and that `reserved` is zero. The R2 proof for money.
* `TestAuthorizationIsCappedByPoolRemaining` — the `min(roleCap, remaining)`
  rule that prevents a mid-run overrun.
* `TestExhaustedPoolRefusesToSpawn` — nothing is spawned below the floor, and
  the slot survives so it is still claimable once topped up.
* `TestZeroBudgetMeansUnmetered` — a zero budget is "not metered", the shape
  every team has today; reading it as "exhausted" would stop all work the day
  Shifts are enabled.

Still owed by the orchestration change, not by this one: the `needs_human`
transition with its R4 reason when a pool is exhausted. `ErrBudgetExhausted` is
raised at the gate today; nothing yet turns it into an item state.

## Pros and Cons of the Options

### Per-Run budgets only (today)

* Good, because it is simple and already works.
* Bad, because nothing bounds an item. A Shift multiplies per-item spend by its
  roster and again by its retries.

### Per-Run budgets plus a post-hoc alert

* Good, because it is nearly free to build.
* Bad, because it detects overspend rather than preventing it. The money is gone
  by the time anyone is told.

### Fixed per-role allocations dividing the pool

* Good, because it is trivially safe under concurrency — nobody shares anything.
* Bad, because it strands money: the philosopher's unused $0.40 is unavailable
  to the writer that needs it, which is precisely the behaviour to avoid.
* Bad, because the allocations must sum to the pool, so adding a persona means
  re-cutting every share.

### A Shift pool, authorized and settled

* Good, because it gives both limits, correct concurrency, and free borrowing
  with one conditional `UPDATE`.
* Bad, because reservations must be released on crash — mitigated by tying them
  to the lease sweeper that already exists for exactly this reason.

## Re-evaluation triggers

* **Authorization contention shows up in latency** — if Shift-row updates ever
  serialise meaningfully, the pool needs sharding or an append-only ledger.
* **Per-agent keys become per-persona rather than per-run** — that changes the
  `ploeg-<12hex>` alias format `homelab-cluster` dashboards join on, and must be
  coordinated (ADR-0008).
* **LiteLLM gains native hierarchical budgets** — a parent budget with child keys
  would move this enforcement out of Ploeg entirely, which would be better.
* **Estimates prove systematically wrong** — if held amounts rarely resemble
  actual spend, the authorization figure needs to come from history rather than
  from the role cap.

## More Information

* Owner of the pool: [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md).
* Credential and metering seam this builds on:
  [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md).
* Backlog #44 (budget plumbing and the pre-claim gate), #60 (gate outcomes
  versus dispatched-then-failed runs).
* Domain rules cited: R2 (crash-safety), R4 (stuck carries a reason), R5 (retry
  budget and the stale circuit-breaker).
