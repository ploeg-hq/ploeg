## Context

`run-multi-agent-shifts` shipped the roster and the blackboard: several agents
work one item, and a reader's findings reach the pull request. What it cannot
do is act on them. A plan is a fixed list of Rounds, so a reviewer's verdict
has no way to change what happens next, and a human's review reaches nothing
at all — there is no forge webhook route (architecture.md §9.1).

The durable decision is [ADR-0017](../../../docs/adrs/0017-the-review-loop-is-verdict-driven-and-capped.md);
this document records only how it is built.

## Goals / Non-Goals

**Goals:** a reviewer's `request_changes` re-opens the plan's writer, bounded
by the verdict, then the pool, then the cap; the forge webhook endpoint exists, verified and
deduped, giving `ForgeProvider.ParseWebhook` its first caller.

**Non-Goals:** as in the proposal — no Follow-Up Work Items yet, no feedback
classifier, no reconciliation of the agent-verdict and human-review paths, and
no cluster network path (ops work, tracked in the runbook).

## Decisions

### D1 — The verdict rides the OutcomeReport, not a new endpoint

The report is already the one thing every adapter writes and every Run ends
with (ADR-0011). A second channel would need its own auth, its own idempotency
and its own failure mode. One optional enum field on `outcomereport.v1`,
persisted on `agent_runs` by migration 0010, and ignored from writing Roles.

### D2 — The loop re-opens the plan's own writer, positionally

"The plan's last writing Round" is found by scanning the configured Rounds
backwards. This keeps the verdict to one bit: it can re-run work the operator
already configured, and cannot author a Round, name a Role or raise a budget.
The positional rule is the cost — a plan whose writer is not where the loop
expects behaves confusingly — so `pkg/plan` refuses `maxFixRounds > 0` with no
writing Round at boot rather than at the moment it matters.

### D3 — The verdict decides whether to loop; pool then cap decide whether it may

Money first among the bounds on a wanted fix round, because it is the bound
that cannot be argued with and the one whose breach costs real spend. The cap
second. The verdict is read before both, because a Shift the reviewer approved
closes as approved: checking the cap first closed an approved final fix round
as `fix_round_cap_reached` and parked its pull request at `needs_human`
(backlog #115). Reading the verdict first spawns nothing, so it costs no money.

### D4 — The fix-round count is derived from the round counter

`fixRounds = (shifts.round - len(plan.Rounds) + 1) / 2`, since each fix round
is a writer plus a review. No column, matching how `reserved` is summed rather
than counted in ADR-0012: state that is derived cannot drift from what
happened, and it survives a restart mid-loop for free.

### D5 — The forge route acknowledges before it acts

Verify HMAC on the raw body, dedup on the delivery id, return 202. Forgejo's
`DELIVER_TIMEOUT` is 5 seconds and it does not retry usefully; anything slower
turns a working webhook into a disabled one. This change acts on nothing yet —
events are audited and dropped — so "before it acts" is cheap to honour now
and is the shape the follow-up work inherits.

### D6 — Dedup is a TTL'd table, not a cache

An in-memory set would forget across the restart that a redelivery is most
likely to follow. A small `forge_deliveries` table with a delivery id and a
timestamp, swept with the leases.

## Risks / Trade-offs

- [A reviewer that never approves spends the cap on taste] → cap defaults to
  2, findings are visible on the PR while it happens, and ADR-0017 names
  repeated cap-hits as a re-evaluation trigger.
- [Two paths will mean "keep going" — an agent verdict and a human review] →
  deliberately not reconciled until the forge route has carried real traffic;
  named as an ADR-0017 trigger rather than guessed at now.
- [A new inbound surface carrying text from outside the factory] → HMAC on the
  raw body before parsing, and the untrusted-input framing already used for
  agent findings applies to review bodies wherever they reach a prompt.

## Migration Plan

Migration 0010 is additive with a default. The loop is inert until a plan sets
`maxFixRounds`; the route is inert until a forge is configured AND the cluster
network path exists, which it does not yet. Rollback is a values edit.

## Open Questions

- Whether a human's `request_changes` should re-open the writer directly or
  create a Follow-Up Work Item. Deferred to the change that routes forge
  events, which needs backlog #107's branch→item lookup either way.
