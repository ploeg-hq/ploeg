---
status: proposed
date: 2026-09-23
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-11-30
---

# Runs create Work Items that wait for approval, within per-Team limits

## Context and Problem Statement

Unfold ADR-0003 says work can create work: a Run may split a Work Item, make
one Ready, or record work it discovered (Product R12). Ploeg had the
`follow_up_created` Outcome and the `follow_up` origin but no way for a Run to
create anything. The same ADR warns that agents creating work can flood the
queue, and requires limits on depth, count and budget before Runs create Work
Items unattended.

Two owner questions are still open: whether created Work Items are written
back to the tracker, and whether a person approves them before dispatch. How
does a Run create Work Items now, without deciding either question by
accident?

## Decision Drivers

* R12: a created Work Item names its source and says whether it is Ready.
* The two open owner questions must stay open. The safe answer to each must be
  the default, and the other answer must be one configuration change away.
* R2 and ADR-0018: an agent reports, and ploegd decides. An agent must not
  dispatch work, pick a budget or reach a Team that does not exist.
* No proposal disappears. An entry that is refused is refused with a reason
  someone can read.

## Considered Options

* **Created Work Items live in Ploeg, wait as `proposed` until a person
  approves them, and are bounded per Team by count, depth, open items and a
  budget pool**
* Create them in the tracker and let assignment dispatch them
* Dispatch them directly, relying on the Shift budget alone

## Decision Outcome

Chosen option: the first, because it answers both open questions with their
safe value and needs only configuration to change either answer.

**The contract.** `outcomereport.v1` gains an optional `createdWorkItems`
array (at most 50). Each entry has `title`, `description`, `ready`, `kind`
(`split`, `clarify` or `discovered`) and an optional `team`. A report without
it is unchanged. It travels through the outcome drop box, and like findings it
always survives the adapter's merge (ADR-0018). ploegd rejects a malformed
entry at the API boundary. The worker drops malformed entries before posting
and notes the discard in the Run's summary, so one bad entry cannot cost the
Run its outcome.

**Storage.** On a valid outcome, ploegd stores each accepted entry in the same
transaction as the outcome. The new Work Item has origin `follow_up`, provider
`ploeg` (no tracker holds it, so nothing is written back), the source Work
Item and Run, its root Work Item, a depth of the source's depth plus one, the
source's Work Target and priority, and an allotted budget. Each accepted entry
is audited on both Work Items, and each rejected entry is audited on the
source with its reason. A created Work Item shares `source_work_item_id` with
repair Follow-Ups but is told apart by its source Run: forge events on its
branch belong to it, not to its source, and it does not count toward a
source's repair cap.

**Limits.** Every Team has these limits. `teams.<name>.createdWork` in the
config file overrides them.

| Setting | Default | Meaning |
| --- | --- | --- |
| `autoDispatch` | `false` | Queue accepted Work Items directly instead of holding them as `proposed` |
| `maxCreatedPerRun` | 5 | Work Items one Run may create |
| `maxDepth` | 2 | How deep created work may nest below a tracker or Vloer Work Item |
| `maxOpen` | 20 | Created Work Items from this Team's Runs that may be proposed, queued or leased at once |
| `itemBudgetUsd` | 2.00 | Shift pool allotted to each created Work Item; its Shift pool is the smaller of this and the plan's pool, and is never unmetered |
| `poolUsd` | 10.00 | Budget that all created Work Items under one root Work Item may be allotted together |
| `refinementTeam` or `refinementRole` | none | Where Work Items that are not Ready go; a Role resolves to the one Team whose plan runs it |

A failed source Run creates nothing, because its retry may propose the same
work again. A requested Team that ploegd does not know is rejected. Rejecting
a proposed Work Item releases its allotment back to the pool.

**The approval gate.** An accepted Work Item is `proposed` unless its Team
sets `autoDispatch: true`. Work that is not Ready goes to the refinement
target if one is set. With `autoDispatch` it is queued only when a refinement
target receives it; without one it stays proposed, because no unattended Run
can make it Ready. `POST /api/v1/operator/work-items/{id}/approve` queues a
proposed Work Item, and `/reject` closes it as `done` with a required reason.
Both need an operator consumer with execute permission for the item's Team.

**The planner.** A plan Role with `planner: true` (never a writer) receives
`planner` on its claim. The worker then composes
`worker.ComposePlannerPrompt`: read the Work Item and the code, write nothing,
and return `follow_up_created` with `createdWorkItems`, or `no_change_needed`
when the Work Item is already Ready.

### Consequences

* Good, because nothing a Run creates is dispatched until a person approves it
  by default. The approval step is how the owner learns whether it is needed.
* Good, because each limit bounds a different way of flooding the queue: one
  Run (count), a chain of Runs (depth), many Runs at once (open items) and
  cost (the pool).
* Good, because created work uses the existing Shift, budget and review path
  once queued, with a metered pool even for an otherwise unmetered Team.
* Bad, because created Work Items are invisible on the board. A person finds
  them through Vloer or the operator API until the write-back question is
  answered.
* Bad, because the pool is allotted, not spent: a created Work Item that
  finishes cheaply still holds its allotment against its tree. This
  over-counts in the safe direction.
* Bad, because `maxOpen` counts per source Team under an advisory lock, but
  the pool check for one tree is only serialized per source Team. Two Teams
  creating work in the same tree at the same moment could overdraw the pool
  by one allotment.

### Confirmation

`go test ./...` in `.forgejo/workflows/on_pull_request.yml` runs:

* `pkg/followup` — count, depth, open-item and pool limits, failed sources,
  unknown Teams, and the approval and refinement routing table.
* `pkg/httpapi/created_work_test.go` — against Postgres: storage with source,
  depth and allotment; recorded rejections; depth and flood limits; the pool
  and the capped Shift pool; not-Ready routing; boundary validation; approve
  and reject with scope and permission checks; the planner claim flag.
* `pkg/harness/created_test.go` — the schema accepts the Go shape, rejects
  malformed entries, and the drop box merge keeps created work.
* `pkg/worker/prompt_planner_test.go` — the planner prompt golden file, and a
  check that its example is a valid outcome.
* `pkg/config/created_work_test.go` — defaults, overrides and unsafe
  configuration.

## Pros and Cons of the Options

### Create them in the tracker and let assignment dispatch them

* Good, because the board shows all work and a person approves by assigning.
* Bad, because it answers the open write-back question with yes, and it needs
  create support in every tracker provider, which none has.

### Dispatch them directly, relying on the Shift budget alone

* Good, because it is the least code.
* Bad, because it answers the open approval question with no, and the Shift
  pool bounds one Work Item, not the number or depth of them.

## Re-evaluation triggers

* The owner decides that created Work Items are written back to the tracker.
  Provider `ploeg` then becomes a create-on-tracker step, and this record is
  superseded.
* The owner decides whether a person approves created work before dispatch.
  If not, `autoDispatch` becomes the default and the gate becomes an
  exception.
* Repair Follow-Ups from failed checks (`forgeFollowUps`) keep their own
  per-pull-request cap and are not counted here. If they start creating work
  in volume, one set of limits for both should replace the two.
* A tree overdraws its pool, or a Team's open created Work Items reach
  `maxOpen` in normal use.

## More Information

* Unfold [ADR-0003](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/adr/adr-0003-the-unit-of-work-is-the-work-item.md)
  and [Product R12](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/domain/rules.md#r12).
* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md) for the pool
  that caps a created Work Item's Shift, and
  [ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md) for
  the drop box.
* Open owner questions, asked on VIK-1122: (1) Are Work Items a Run creates
  written back to the tracker? (2) Does a person approve them before they are
  dispatched?
* Backlog items #41 to #44, #47 and #49.
