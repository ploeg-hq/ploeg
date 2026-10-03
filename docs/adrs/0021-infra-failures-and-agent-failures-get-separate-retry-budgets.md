---
status: proposed
date: 2026-08-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Infrastructure failures and agent failures get separate retry budgets

## Context and Problem Statement

[ADR-0019](0019-a-failed-writing-run-reopens-its-round.md) re-opens a failed
writing Round and parks the Shift after `store.MaxRunAttempts` attempts. Its
first re-evaluation trigger reads: *"A writing Run reaches `MaxRunAttempts` in
production more than once in a month — the failure is not transient and
retrying is the wrong response."*

That trigger fired. Work item 98 reached the cap on 2026-08-13 and parked
`needs_human` with close reason `writing_run_failed_repeatedly`; the same
ticket had reached it on 2026-08-11 and 2026-08-12 as well.

The re-evaluation reaches the opposite conclusion to the one the trigger
anticipated. The runs were not failing on the work. LiteLLM's spend ledger
records runs 112, 113 and 114 making 19, 17 and 1 completed model calls, with
context growing from 18k to 53k tokens — each pod killed within seconds of a
call that had just succeeded, for a total settled spend of $0.0237 against an
$8.00 Shift pool. All three were reclaimed by the sweeper as
`failed`/`lease_lost`. The agent never got a verdict in; the cluster took the
pod away three times.

So the cap was reached by a transient failure that the accounting could not
distinguish from a considered one. `retryFailedWriter` counts every `failed`
Run in the Round alike, and — as the test that pins it says — the sweeper is
in practice the only producer of `failed`. The budget nominally reserved for
"how many times may this agent be wrong about this work" was being spent
entirely on infrastructure, and the close reason then sent a person to read
agent logs for a problem that was never in the agent.

The question this record answers: when a Round's writer keeps failing, does it
matter *who* failed — and if so, what does the answer change.

## Decision Drivers

* R2 — crash-safety is mechanical. A node eviction should not need a person.
  ADR-0019 names this driver too; splitting the budget is what makes the
  outcome match it when evictions arrive in threes.
* A close reason is what an operator greps six weeks later. It should say which
  way to look: at the ticket, or at the cluster.
* The estate already answers this question, in this codebase, for this event.
  `store.ExpireLeases` on the pre-Shift path refunds the attempt, counts
  `infra_failures` apart, and caps them at `store.MaxInfraFailures`. The Shift
  path inherited none of it.
* ADR-0012's discipline: derive counts from what happened. `failure_reason` is
  already on the `agent_runs` row, so the split needs no new counter.

## Considered Options

* **Count infra-caused attempts against a separate, larger cap**
* Leave the budget shared and only change the close reason's wording
* Refund the attempt outright, as `ExpireLeases` does, and keep one cap
* Raise `MaxRunAttempts` for everyone

## Decision Outcome

Chosen option: "**count infra-caused attempts against a separate, larger
cap**", because it is the only option that both stops a cluster problem from
consuming the ticket's patience and keeps a genuinely failing agent bounded at
the small number ADR-0019 chose for it.

`work.FailureReason.IsInfra` classifies the enum: `infra_node`, `infra_llm` and
`lease_lost` are the agent's non-doing; `agent_error` and `budget` are its own
verdict. An unset or unrecognised reason is charged to the agent deliberately —
a reason nobody set must not buy unlimited infrastructure retries.

`store.FailedRunsInRound` returns `InfraAttempts` alongside `Attempts`,
partitioned in SQL by `work.InfraFailureReasons()` so the query and the engine
cannot drift apart. `FailedRun.AgentAttempts()` is the remainder.

`shiftengine.retryFailedWriter` then applies two bounds to the same Round:

1. `InfraAttempts >= store.MaxInfraFailures` closes the Shift with the new
   close reason **`writing_run_killed_repeatedly`**, whose message points at
   evictions, node pressure and image pulls rather than at the ticket.
2. `AgentAttempts() >= store.MaxRunAttempts` closes it with
   `writing_run_failed_repeatedly` exactly as before.

`store.MaxInfraFailures` is reused rather than invented: it is the number this
repository already chose for "how many infrastructure failures before we give
up", and using a second number would leave the two paths disagreeing about the
same question.

Readers are untouched, and so is everything else ADR-0019 decided — the retry
still re-opens in place, the counter still does not advance, and the count is
still derived from `agent_runs` rows.

### Consequences

* Good, because the failure the factory is most likely to meet no longer
  consumes the budget reserved for the work. Item 98's three kills would have
  left all three agent attempts intact.
* Good, because the close reason now carries the diagnosis. The 2026-08-13
  investigation began by reading agent logs because
  `writing_run_failed_repeatedly` said the run kept dying, and the runs were
  fine.
* Good, because it needs no schema change and no new counter: `failure_reason`
  was already on the row, unread by this path.
* Good, because the Shift path now answers the infra question the same way
  `ExpireLeases` always has, instead of two paths disagreeing.
* Bad, because a Round can now spend up to `MaxInfraFailures` pods on a cluster
  that is broken, where it previously stopped at three. Accepted: each such run
  now reports in seconds rather than stranding a Lease for a full TTL, and
  parking a healthy ticket was the worse failure.
* Bad, because there is still **no backoff** between infra retries on this
  path, where `ExpireLeases` has 1/5/15/60-minute steps. A cluster that kills
  pods instantly will burn the infra budget quickly. Deliberately out of scope:
  gating a reopened Run on a time would change `store.ClaimRole` and the KEDA
  trigger predicate in the chart, which must stay byte-identical to each other,
  and that is a schema-and-deploy change rather than an engine one.
* Bad, because `IsInfra` is a second classification of `failure_reason`
  alongside `Valid`, and a new reason must be added to both. Mitigated by
  `TestInfraFailureReasons_MatchesIsInfra`, which fails if the SQL list and the
  predicate disagree.

### Confirmation

`go test ./pkg/shiftengine/` —
`TestFailedWriter_InfraKillsDoNotSpendTheAgentBudget` (three sweeper-expired
Runs leave the Shift open and the writer claimable a fourth time),
`TestFailedWriter_ParksAtTheInfraCapAndSaysSo` (the infra cap closes the Shift
with `writing_run_killed_repeatedly`), and
`TestFailedWriter_ParksTheShiftAtTheAttemptCap` (an agent-attributed failure
still parks at `MaxRunAttempts` with `writing_run_failed_repeatedly`). The
first two fail against the shared-budget engine.

`go test ./pkg/work/` — `TestFailureReason_IsInfra` and
`TestInfraFailureReasons_MatchesIsInfra` pin the classification and the
Go/SQL agreement the split rests on.

## Pros and Cons of the Options

### Leave the budget shared and only change the close reason's wording

* Good, because it is a one-line change and cannot alter retry behaviour.
* Bad, because it fixes only the diagnosis and not the outcome: a healthy
  ticket still parks after three evictions, and a person is still called for a
  machine's doing. The message would say "look at the cluster" while the item
  sat `needs_human` waiting for a human it did not need.

### Refund the attempt outright, as `ExpireLeases` does, and keep one cap

* Good, because it mirrors the pre-Shift path most literally.
* Bad, because ADR-0019 derives the attempt count from the `agent_runs` rows
  rather than storing it, and there is no counter to decrement. Refunding would
  mean deleting or re-labelling a row that records something that really
  happened — the opposite of ADR-0012's discipline.
* Bad, because an unbounded refund makes infra retries infinite; the cap would
  have to come back in some other form anyway.

### Raise `MaxRunAttempts` for everyone

* Good, because it is the smallest possible edit.
* Bad, because it buys the infra case its retries by also letting a genuinely
  failing agent run more times, spending pool and wall-clock on work that is
  not going to succeed. ADR-0019 chose three for the agent on purpose.
* Bad, because the close reason still would not say which failure happened.

## Re-evaluation triggers

* A Round reaches `MaxInfraFailures` in production at all — the cluster is
  failing persistently rather than transiently, and backoff (below) becomes
  urgent rather than deferred.
* Backoff lands for reopened Runs (a `not_before` on `agent_runs`, honoured by
  `ClaimRole` and the KEDA predicate together), which would let the infra cap
  rise or the interval carry the bound instead.
* `failed` gains a producer other than the sweeper and the worker's own infra
  paths — the assumption that the split is mostly sweeper-versus-agent would
  need re-reading.
* Backlog #109 lands (settling spend for swept runs): infra retries would then
  draw on the pool, and a larger infra budget starts costing money rather than
  only pods.

## More Information

* Evidence: the 2026-08-13 investigation of Shift 73 / work item 98 — three
  runs reclaimed `lease_lost`, LiteLLM spend ledger showing completed model
  calls right up to each kill, and container memory peaking at 458Mi against a
  768Mi limit (not an OOM).
* [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md) — the record this
  refines; its first re-evaluation trigger is what opened this one.
* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md) — the
  derive-never-count discipline the split obeys.
* [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) — a Round is
  readers or one writer, which is what makes "the Round's writer" well defined.
* `store.ExpireLeases` / `store.MaxInfraFailures` — the pre-Shift path whose
  answer to this question the Shift path now shares.
