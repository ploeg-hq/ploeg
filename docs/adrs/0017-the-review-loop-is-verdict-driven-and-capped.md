---
status: proposed
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The review loop is verdict-driven and capped

## Context and Problem Statement

A Team's plan is an ordered list of Rounds, and ADR-0010 leaves it there: the
plan runs to exhaustion and the Shift closes. That is enough to put several
specialists on one item, and not enough to *finish* one. A reviewer that finds
a real defect has nowhere to send it — the plan's next entry opens regardless
of what the reviewer concluded, and if the reviewer was last, the Shift closes
with the defect recorded and unfixed.

Every plan shape available under a fixed list is wrong in one direction. A
plan ending `[…, build, review]` never acts on the review. A plan ending
`[…, build, review, build]` burns a writing Run whether or not anything needs
changing, and still cannot handle the second round of feedback. Padding the
plan with more fix rounds multiplies the waste without ever being enough.

The question this record answers: what makes a Round open, and what stops the
loop.

## Decision Drivers

* R2 — advancement must be derived from Run state, never taken on an agent's
  word about what should happen next.
* An agent must not be able to keep itself running, or to spend the pool by
  asserting it is not finished.
* Money is bounded before it is spent (ADR-0012), not apologised for after.
* The stopping condition has to be legible to whoever reads the audit trail
  six weeks later.

## Considered Options

* **A reviewing Role returns a verdict; a request for changes re-opens the
  plan's writer, capped**
* Fixed plans only — pad with fix rounds and accept the waste
* Let the reviewer nominate the next Round (an agent-authored plan)
* Diff-based looping — re-run the reviewer until it reports no findings

## Decision Outcome

Chosen option: "**a reviewing Role returns a verdict; a request for changes
re-opens the plan's writer, capped**".

A reading Role may return `verdict: approve | request_changes` in its existing
`OutcomeReport`. When a plan's final Round completes and any reader in it
asked for changes, the engine re-opens the plan's last writing Round with the
findings attached, then the review Round after it. Each such pair is one *fix
round*.

Three bounds stop it. The verdict decides whether a fix round is wanted, and
the pool and the cap decide, in that order, whether a wanted one may open:

1. **The verdict.** `approve` closes the Shift as approved; no verdict at all
   closes it as an exhausted plan. Either way no fix round is wanted, so the
   pool and the cap are not consulted and cannot name the close.
2. **The pool.** If the remaining budget cannot fund another Round, the Shift
   parks at `needs_human` naming the spend (ADR-0012). Money is checked before
   the cap, and before any Run is spawned.
3. **The cap.** `maxFixRounds` (default 2) bounds how many times the loop may
   run for one Shift.

The count is **derived** from `shifts.round` against the plan's length, not
kept in a column — the same discipline `reserved` follows in ADR-0012, and for
the same reason: a counter can disagree with what actually happened.

What the verdict is NOT: it cannot name the next Role, change the plan, raise
the cap, or extend the budget. It is one bit that may re-run *the plan's own
writer*, and every other lever stays with configuration. An agent that lies
about needing changes wastes at most `maxFixRounds` writer Runs against a
pool that was already bounded — the same exposure as a writer that loops on
its own, which the per-Run cap already handles.

The verdict is honoured only from a READING Role. A writer approving its own
work would be the loop grading itself.

### Consequences

* Good, because the common case gets cheaper: a clean review ends the Shift
  instead of burning the padded fix round every plan would otherwise carry.
* Good, because the loop's stopping condition is three explicit bounds rather
  than a plan author's guess, and each is visible in `shifts.close_reason`.
* Good, because it needs no new orchestration surface — one additive field on
  a contract that already exists, and rounds that the engine already knows how
  to open.
* Good, because an agent gains no new authority: it answers a closed question
  and cannot author work.
* Bad, because a Shift's total cost is no longer a function of its plan alone;
  reading `plan` no longer tells you the maximum spend without also reading
  `maxFixRounds` and the pool. Accepted: the pool is the real ceiling and is
  enforced before every Round.
* Bad, because a reviewer with poor judgement can spend the cap on
  disagreements about taste. Mitigated by the cap being small by default, and
  by findings being visible on the pull request while it happens.
* Bad, because "the plan's last writing Round" is a positional rule: a plan
  whose writer is not where the loop expects gets a confusing re-run.
  Validation rejects a plan with no writing Round when `maxFixRounds > 0`.

### Confirmation

`go test ./pkg/shiftengine/` covers the loop's bounds directly: a
request-changes verdict re-opens the writer, an approve closes, the cap stops
a reviewer that never approves, an exhausted pool parks before the cap is
reached, and a verdict from a WRITING Role is ignored. `go test ./pkg/plan/`
rejects a plan that configures fix rounds with no writer to re-run. The
contract field is pinned to its schema by `pkg/harness/contract_test.go`, and
`go test ./internal/ledger/` gates this record itself.

## Pros and Cons of the Options

### Fixed plans only

* Good, because the maximum cost of a Shift is readable from its plan alone.
* Bad, because every plan pays for a fix round it usually does not need, and
  still cannot handle a second round of feedback.
* Bad, because a found defect goes unfixed with the evidence sitting in the
  audit trail — the failure this record exists to remove.

### Let the reviewer nominate the next Round

* Good, because it handles cases nobody planned for.
* Bad, because it hands an agent authorship of the work plan, and R2 exists
  precisely so the pipeline does not depend on an agent behaving well.
* Bad, because it makes cost unbounded in a way no configuration review can
  catch.

### Diff-based looping until the reviewer reports no findings

* Good, because it needs no new field at all.
* Bad, because "no findings" is not a decision — a reviewer that always finds
  something stylistic never terminates, and one that finds nothing on a broken
  branch terminates wrongly.
* Bad, because it reads intent out of prose, which is the ambiguity the
  verdict enum removes for one byte.

## Re-evaluation triggers

* A real Shift hits `maxFixRounds` more than once in a month — the cap is
  wrong, or the reviewer and writer disagree in a way more rounds will not
  settle.
* Fix rounds routinely park on budget before reaching the cap: the pool, not
  the cap, is the operative bound, and the cap is then decoration.
* Forge review webhooks land (the `close-the-review-loop` change): a HUMAN's
  request for changes arrives by a different route, and the two paths should
  be reconciled rather than left to diverge.
* Any proposal to let a verdict carry more than the enum — a target Role, a
  budget, a plan — which would be this decision's central boundary moving.

## More Information

* [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) — Rounds, and
  why one is readers or a single writer.
* [ADR-0011](0011-the-pull-request-is-the-blackboard.md) — findings ride the
  OutcomeReport; the verdict rides the same envelope.
* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md) — the pool that
  bounds the loop before the cap does.
* `openspec/changes/close-the-review-loop/` — the change that implements this.
