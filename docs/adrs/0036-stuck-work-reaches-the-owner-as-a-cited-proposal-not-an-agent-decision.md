---
status: proposed
date: 2026-09-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Stuck work reaches the owner as a cited proposal, and no agent applies a decision

## Context and Problem Statement

On 2026-09-28 the owner approved designing an escalation ladder, so that fewer
stuck Work Items reach a person, and allowed the answer to be that parts of it
should not be built.

The motivating case is VIK-573 (webgrip/glide pull request #3). The Work Item
asked for a kind end-to-end test and told the writer to "extend the existing
kind e2e (backlog #89)". That e2e did not exist, and kind cannot run on the
remote-Docker CI runner. This is what happened:

| Round | Role | What it did |
| --- | --- | --- |
| 1 | builder | Delivered the documentation criteria (AC3 to AC5), wrote in the pull request body that AC1 and AC2 were "not delivered", moved them to backlog #127 and reported `pr_opened` |
| 2 | reviewer | `request_changes`: the headline deliverable is missing, and the premise is stale "and was not escalated". Its instruction was to deliver the e2e or report `stuck` |
| 3 | builder | Reported `stuck` with the runner evidence, and the Shift closed at `needs_human` |
| owner | person | Chose option (b), re-scoping AC1 and AC2 to the real-cluster qualification (VIK-1264, VIK-1268), because a kind-capable runner needs privileged Docker and homelab-cluster ADR-0053 rules that out |

Read closely, the loop worked. The reviewer was right: round 1 reported
`pr_opened` with two criteria missing. Round 3's `stuck` was the correct
terminal. What cost time was elsewhere:

1. The premise was false before any Run started, and a writing Round was
   spent finding that out.
2. The writer's delivery contract has no rule for a partly infeasible Work
   Item. It says only "If the Work Item cannot be completed, explain why on
   stderr and exit non-zero" (`pkg/worker/task.go`), so round 1 reported
   `pr_opened` with a gap.
3. The tracker comment for the stuck Shift read "Ploeg finished this item and
   opened a pull request … Please review and merge" (VIK-573 comment 874).
   `trackerMessage` in `pkg/shiftengine/publish.go` picks its headline by
   whether a pull request link exists before it checks for `stuck`. The owner
   was asked to merge an item the agent had just said it could not finish.
4. The owner assembled the decision alone: reading the pull request, recalling
   ADR-0053 in another repository, and writing option (b). The decision could
   be derived from a recorded one, but no process derived it.

Volume matters too. The 2026-09-27 loop baseline counts 2 `stuck` outcomes in
56 unattended Runs, and 13 Shifts in seven weeks. Anything built here is paid
for in owner review of the mechanism itself, so its cost has to be weighed
against a handful of escalations a month.

Which tiers of the proposed ladder should Ploeg build, and with what authority?

## Decision Drivers

* R2 and ADR-0017: an agent reports and ploegd decides. An agent's output may
  change what runs next only through a closed, bounded field.
* The owner's minutes are the scarcest resource. The ladder must cut owner
  work per escalation, not only the number of escalations.
* A wrong decision applied silently costs more than a correct one the owner
  confirms in one action. At a few escalations a month, the saving from
  autonomy is minutes, and the downside is a wrong re-scope, a wrong closure
  or a wrong security call.
* Recorded decisions must be findable by a machine, and their authenticity must
  be checkable. Today Ploeg writes its tracker comments as the owner's own
  Vikunja account, so a comment's author does not prove that the owner wrote
  it (comments 874 and 892 on VIK-573 have the same author).
* Most of Ploeg's ADRs are `proposed` ("do not design against it yet"). Only
  0001 to 0014, 0020 and 0022, Unfold's 0001 to 0004 and the homelab records
  marked accepted are in force. Anything bounded by recorded decisions is
  bounded by a small corpus.
* Every tier needs a budget and a way to stop, and must not create a new way
  for an agent to keep work running.

## Considered Options

* **Ladder A**: T0 premise check (opt-in), T1 as a writer contract fix and a
  message fix instead of new reviewer semantics, T2 as a proposal-only lead
  whose output the owner accepts in one action, and T3 as the owner for
  everything else
* Ladder B: the full proposal, including a T2 lead that applies decisions
  within a mandate without the owner
* Ladder C: build nothing new; fix the writer contract and the tracker message
  only

## Decision Outcome

Chosen option: "Ladder A". It removes the costs VIK-573 actually incurred,
stale premises, `pr_opened` hiding a gap, a misleading headline and a decision
the owner assembled alone, and grants no agent new authority. Everything about
the lead is reversible: if its briefs do not save owner minutes, it is switched
off per Team and nothing else changes.

### Owner decisions (2026-09-28)

The owner answered the questions this record left open. The record stays
`proposed` until it is merged and ratified; these answers are the terms it
will be ratified on.

1. **The lead stays proposal-only** until E1 reaches at least 90 % over at
   least 20 briefs with E3 at zero (no wrong decision). Only then may a new
   record reopen autonomy, as the first re-evaluation trigger says.
2. **Decision sources.** The first slice lists Unfold's own accepted ADRs in
   `escalation.decisionSources`. homelab-cluster's accepted ADRs are added in
   the second slice, once ploegd fetches listed sources into the briefing.
3. **T0 defaults.** The premise check is on by default for Team `bronze`, and
   opt-in for every other Team.
4. **Budgets.** Pre-flight US$0.10 per Run, the lead US$1.00 per brief, and
   `maxBriefs` 1 per stuck Work Item.
5. **Authenticated owner decisions.** Unfold (ploegd) gets its own Vikunja user,
   and the owner mints its token. Once Ploeg's tracker comments are written as
   that user, a comment headed `**Owner decision YYYY-MM-DD:**` whose author
   is the owner's account is authentic, and may become a ground. The same user
   gives the outcome observation loop (homelab-cluster
   `rfc-outcome-observation-loop.md`, decision 6) an attributable Unfold writer.
   The work is tracked under epic VIK-1276.

### The ladder

| Tier | Built as | Who acts | Authority | Budget | Stops because |
| --- | --- | --- | --- | --- | --- |
| T0 premise check | A reading Role with `preflight: true`, placed in a Round before the writer | agent (cheap model) | Returns `no_change_needed` (proceed) or `stuck` with the failed premise. A stuck Run already freezes the plan (`shiftengine.evaluate`, R4), so the engine does not change | the Role's per-Run `cap`, recommended US$0.10 | one Run per Shift, like any Round |
| T1 writer and message | Delivery contract text, plus the tracker headline | agent / ploegd | None. A writer that cannot meet an acceptance criterion delivers the rest, keeps the pull request, and reports `stuck` naming the unmet criteria. A stuck close's headline says "stuck", whether or not a link exists | none | not a loop |
| T2 lead (proposal only) | An escalation Follow-Up, the same shape as the repair Follow-Ups of `forgeFollowUps`: when a Work Item settles `needs_human`, ploegd queues one reading Run for the Team named in `teams.<name>.escalation.team` | agent (strong model) | Writes an escalation brief. It cannot push, requeue, approve, close, create Work Items or change a budget | the Follow-Up's Shift pool, recommended US$1.00, capped by `createdWork.poolUsd` for its tree | `maxBriefs` per Work Item (default 1), derived from the audit log; never for a Work Item whose last brief the owner declined; never for a Work Item that is itself an escalation Follow-Up |
| T3 owner | Vloer's needs_human lane and the operator API | person | Every decision. Accepting a brief is one action, and editing it is always possible | owner minutes (K6) | not a loop |

T1 means T1 in the proposal: the reviewer is not given a third verdict meaning
"infeasible and properly escalated". In VIK-573 the reviewer's
`request_changes` was correct, and ADR-0017's re-evaluation triggers name "a
verdict carrying more than the enum" as the boundary it guards. The defect was
on the writer's side, and it is fixed there.

T0 is per Team because every Work Item pays for it, while the waste it
prevents shows up in only a few. It pays off for a Team whose Work Items come
from people who write "extend the existing X" without checking that X exists.
Team `bronze`, where VIK-573 ran, is that Team, so T0 is on by default there
and opt-in for the others (owner decision 3).

### The escalation brief

The lead returns `no_change_needed` with `findings` in a fixed shape. ploegd
parses the shape and rejects a brief that does not match:

* **Question**: the one decision the owner has to make, in one sentence.
* **Options**: at most three. Each gives what happens, its cost, and its effect
  on the Work Item's acceptance criteria.
* **Recommendation**: one option, and its action from a closed set:
  `requeue_with_amendment` (with the amendment text), `split` (with
  `createdWorkItems`, which are held as `proposed` under ADR-0031),
  `withdraw` (with a reason), or `owner_decides`.
* **Grounds**: each recorded decision the recommendation rests on, given as a
  repository, a path, the commit SHA and a verbatim quote.
* **Class**: `within_record` or `new_decision`, and for `new_decision` one or
  more of `money`, `security`, `policy`, `contradicts_record` or `outward`.

ploegd verifies the grounds itself, not through the agent. The cited file must
exist at that SHA on the repository's trunk. Its front matter must say
`status: accepted`, or the ground must be an owner decision record (below).
The quote must appear in the file. A ground that fails is dropped and marked
on the brief. A recommendation with no verified ground is re-classed
`owner_decides`. The lead can suggest where the answer lies, but it cannot
widen its own mandate by citing something that is not in force.

The brief is posted on the pull request, is posted to the tracker under the
stuck headline, and appears in Vloer's `needs_human` lane with **Accept**,
**Edit and accept** and **Decline**.

### Recorded decisions: what a machine may treat as one

1. **An accepted ADR on a trunk commit**, in a repository listed in
   `escalation.decisionSources`. It is found through that repository's ADR
   index (`apps/ploeg/docs/adrs/README.md`, `docs/adr/index.md`, homelab
   `docs/techdocs/docs/adr/`). A `proposed` record is evidence but never a
   ground. The ADR indexes are already machine-readable tables, and
   `internal/ledger` already enforces their status mirror.
2. **An owner decision record in Ploeg.** Accepting a brief, or a new operator
   call (`POST /api/v1/operator/work-items/{id}/decisions`), stores the text,
   the Work Item, the operator identity and the time in an append-only table,
   and audits it. ploegd mirrors it to the tracker as a comment that starts
   with `**Owner decision YYYY-MM-DD:**`. The comment is a copy for people.
   The row is the record.

Tracker comments are not grounds, whatever they say, until Ploeg writes to the
tracker as a different user from the owner. Today anything that can reach the
tracker as Ploeg can post text that looks like an owner decision.

### Authority boundaries

* Only ploegd holds state transitions. The lead's brief is data. Accepting it
  runs the recommended action through the operator API with the owner's
  operator identity, and is audited as the owner's act.
* Whatever its grounds, a brief whose action would raise a budget, grant
  credentials or network reach, change a policy or a Kyverno or PSA rule,
  contradict an accepted record, merge, publish, or post outside the forge and
  tracker is class `new_decision`. ploegd enforces this class for `split` and
  `requeue_with_amendment` by scanning the amendment for the Work Item fields
  it would change (Team, budget, Work Target); a lead cannot declare them
  `within_record`.
* Accepting `requeue_with_amendment` counts as a fresh human mandate, as a
  tracker reassignment does in `store.go` today: attempts reset. That is safe
  only because the owner performs it. No agent path reaches it.

### Loop prevention

* The lead has no path that requeues its source. The only exit from
  `needs_human` stays a person.
* The number of briefs is derived from the audit log, like `fixRoundsRun`, and
  capped by `maxBriefs`.
* An escalation Follow-Up that itself ends `stuck` or `failed` produces no
  second brief. The source stays `needs_human` with the Follow-Up's reason
  attached.
* A declined brief blocks later briefs for that Work Item until the owner
  requeues or edits it.

### Reward-hacking risks and the counter to each

| Risk | Counter |
| --- | --- |
| The lead cites something that is not in force to look within record | ploegd verifies path, SHA, `status: accepted` and the quote. Unverified grounds re-class the brief as `owner_decides` |
| The lead cites a real record but draws an inference it does not support. ADR-0053 governs the agent plane, and applying it to a CI runner is an inference | Proposal only. The inference is visible on the brief, and the owner judges it. This is the main reason T2 does not apply decisions |
| A writer adds or edits an ADR so that a future brief is within record | Grounds need `status: accepted` on trunk, and only the owner merges. A writer's pull request is never a ground |
| The lead games acceptance by recommending `owner_decides` or the safest option every time | Metrics count `owner_decides` separately, and a brief that always punts is visible as a low useful-brief rate |
| Text in the Work Item or pull request tells the lead what to decide | The ADR-0030 ranking applies: repository and Work Item text rank below the delivery contract, and the brief's shape and verification are ploegd's |

### Metrics

Each measure is paired, so that improving one at the expense of another shows.

| Measure | Definition | Source | Pair |
| --- | --- | --- | --- |
| E1 accepted-as-is rate | Briefs accepted without edit ÷ briefs produced | new audit rows | E3 |
| E2 owner minutes per escalation | Active seconds on a `needs_human` item in Vloer | KPI D2 (VIK-1272) | E1 |
| E3 wrong-decision rate | Accepted briefs whose Work Item is reopened, re-scoped again or reverted within 14 days ÷ accepted briefs | audit log and forge | E1 |
| E4 cost per brief | Settled spend of escalation Follow-Ups | `run_llm_accounts` | E2 |
| P1 premise bounce rate and false-bounce rate | T0 `stuck` ÷ T0 Runs, and bounces the owner requeued unchanged ÷ bounces | audit log | writer Rounds saved |

"needs_human resolved without the owner" is not a measure, because under this
decision it is zero by construction. E1 is what it would become once the owner
trusts the briefs.

### Consequences

* Good, because each VIK-573 cost has an owner: T0 catches the stale premise
  before a writer Round, T1 ends the `pr_opened`-with-a-gap round and the
  "please merge" headline on a stuck item, and T2 hands the owner option (b)
  already drafted, with ADR-0035 and ADR-0053 quoted.
* Good, because no agent gains a state transition. ADR-0017's boundary, ADR-0031's
  approval gate and R2 hold unchanged.
* Good, because T2 reuses the Follow-Up shape, the created-work budget pool,
  the operator API and Vloer's approve pattern instead of adding an
  orchestration surface.
* Bad, because every escalation still costs the owner one action. Accepted: at
  a few escalations a month, one action is small, and it is where a wrong
  decision is caught.
* Bad, because the first slice reads only the target repository. VIK-573's
  ground, homelab ADR-0053, lives in another repository and reading Runs get no
  forge token (ADR-0013). The lead would have found it only through ADR-0035's
  reference, and ADR-0035 is `proposed`. Cross-repository grounds need ploegd
  to fetch the listed decision sources into the briefing. That is the second
  slice.
* Bad, because the ground check proves a quote exists, not that it supports
  the conclusion.

### Confirmation

This record is proposed, and nothing has been built yet. When the slices land,
`go test ./...` in `.forgejo/workflows/on_pull_request.yml` is to cover:

* `pkg/worker`: golden files for the writer contract's partial-infeasibility
  rule and for the `preflight` and lead prompts.
* `pkg/shiftengine`: `trackerMessage` gives a stuck close a stuck headline
  whether or not a link exists (a regression test that fails on the current
  code), and a `preflight` Role's `stuck` freezes the plan before the writer's
  Round.
* The escalation Follow-Up package: at most `maxBriefs`, derived from the
  audit log; no brief for an escalation Follow-Up; no brief after a decline;
  ground verification rejects a wrong SHA, a `proposed` status and a missing
  quote; a `new_decision` field change cannot be classed `within_record`.
* `pkg/httpapi`: accepting a brief needs execute permission for the Team, runs
  only the brief's closed action, and writes the owner decision row.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Ladder B: a lead that applies decisions within a mandate

* Good, because at its best the owner never sees an escalation.
* Bad, because the mandate corpus is thin. Most Ploeg records are `proposed`,
  and tracker comments cannot be authenticated. A mandate that is technically
  enforceable today would be too narrow to decide VIK-573.
* Bad, because its hardest error, a real citation carrying an inference it does
  not support, cannot be caught mechanically. At a few escalations a month the
  owner minutes saved do not buy that risk.
* Bad, because an agent that can requeue its own source reopens the loop
  ADR-0017 closed. Preventing that takes more machinery than ladder A needs in
  total.

### Ladder C: the writer and message fixes only

* Good, because it is two small changes with regression tests.
* Bad, because the owner still assembles every decision from the pull request
  alone, which was most of VIK-573's cost. It is ladder A's first slice, not an
  alternative to it.

## Re-evaluation triggers

* E1 is at least 90 % over at least 20 briefs, with E3 at zero. Reopen whether
  briefs of class `within_record` whose only action is `withdraw` or `split`
  may apply without the owner, in a new record that supersedes this one.
* E1 stays below 50 % after 10 briefs, or E2 does not fall. Switch the lead off
  and record the rejection.
* Ploeg writes to the tracker as its own user. Tracker comments headed
  `**Owner decision YYYY-MM-DD:**` from the owner's account may then become
  grounds.
* More than 10 `needs_human` settles in a month. The volume argument changes.
* T0's false-bounce rate exceeds 20 %, or T0 bounces nothing in 30 Work Items.
  Retire it for that Team.

## More Information

* Case: webgrip/glide pull request #3, VIK-573 comments 874 and 892.
* Evidence: `docs/research/2026-09-27-loop-baseline.md` (volumes),
  `docs/reference/kpis.md` (K4, K6, D2).
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) for the
  verdict boundary, [ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)
  for instruction ranking,
  [ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md) for
  created work and its approval gate,
  [ADR-0013](0013-push-rights-are-minted-per-run.md) for readers getting no
  forge token, and
  [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md)
  for its reference to homelab-cluster ADR-0053.
* Board: epic VIK-1276 in Vikunja project Ploeg.
