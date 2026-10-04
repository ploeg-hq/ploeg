# shift-orchestration Specification

## Purpose
How ploegd drives a Shift through its Team's plan: one live Shift per queued
Work Item, Rounds that advance only when all their Runs have finished, a failed
Run that re-opens its Round, closing on plan exhaustion or a terminal
Outcome, and parking the Work Item when the budget pool runs out. Archived from
the change `2026-07-29-run-multi-agent-shifts`.
## Requirements
### Requirement: A queued Work Item gets exactly one live Shift

When a Work Item enters `queued`, ploegd SHALL open a Shift for the Team that
owns it, carrying the branch, the budget pool and round zero. Two Teams SHALL
never hold a live Shift on the same Work Item (R1 in its Shift form).

#### Scenario: First assignment opens a Shift

- **WHEN** an assignment webhook queues a Work Item for team `bronze`
- **THEN** a Shift exists for that item with `round = 0` and `closed_at` null
- **AND** its branch is derived from the item, not from Team config

#### Scenario: A second Team cannot open a competing Shift

- **GIVEN** a live Shift on Work Item 42
- **WHEN** another Team attempts to open one on the same item
- **THEN** the attempt fails at the database, not in application code

#### Scenario: A plan-less Team behaves exactly as today

- **GIVEN** a Team with no configured plan
- **WHEN** its Work Item is queued
- **THEN** one Round with one writing Role is opened
- **AND** the observable behaviour is identical to the pre-Shift dispatch path

### Requirement: Rounds advance only when every Run in them has finished

ploegd SHALL open the next Round only when the current Round has no Run in
`pending` or `running`. Advancement SHALL be derived from Run state, never
reported by an agent (R2 — the pipeline must not depend on an agent behaving
well).

#### Scenario: A Round with one live reader does not advance

- **GIVEN** a Round of three readers, two finished and one still running
- **WHEN** the orchestrator evaluates the Shift
- **THEN** no new Round is opened

#### Scenario: A completed reader Round advances to the writer Round

- **GIVEN** a Round of three readers that have all reported
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the next Round in the plan opens with one writing Role
- **AND** the writer's Run receives the readers' findings

#### Scenario: A swept READING Run does not block its Round forever

- **GIVEN** a reading Run whose pod died without reporting
- **WHEN** `ExpireRuns` reclaims it
- **THEN** the same Round re-opens with that reading Role only, and the round
  counter does not advance
- **AND** once the Role's attempt budget is spent the Round completes and the
  Shift advances
- **AND** the Round's remaining findings still reach the next Round

### Requirement: A failed writing Run re-opens its Round rather than advancing the plan

A Round whose WRITING Run ended `failed` SHALL NOT advance the plan. The
orchestrator SHALL re-open that Round in place — without incrementing the round
counter, which doubles as the index into the Team's plan — until the Role
exhausts an attempt budget, after which the Shift SHALL close at `needs_human`
naming the repeated failure.

The budget SHALL be split by who failed (ADR-0021). An attempt whose
`failure_reason` is an infrastructure reason — the pod was killed, the node
went away, the gateway did not answer — SHALL count against
`MaxInfraFailures`; every other attempt SHALL count against `MaxRunAttempts`.
The two SHALL close the Shift with different reasons, because the reason is
what tells a person whether to look at the ticket or at the cluster. An
unrecognised `failure_reason` SHALL count against `MaxRunAttempts`, so a reason
nobody set cannot buy unlimited infrastructure retries.

`failed` is retryable by construction — it is the sweeper's verdict on a pod
that stopped renewing, or the worker's own report that its pod was taken away —
which is what distinguishes it from a `stuck` Outcome, where a human is needed
and no retry fixes it (R4). The attempt counts SHALL be derived from the Runs
in the Round, not held in a counter.

A writing Run's failure costs the work. Advancing over it means every later
Round reasons about a branch that was never written.

#### Scenario: The writer's pod dies

- **GIVEN** a Round whose only writing Run was reclaimed by `ExpireRuns`
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the same Round re-opens with that writing Role
- **AND** the round counter does not advance
- **AND** no later Round of the plan is opened

#### Scenario: The writer keeps dying

- **GIVEN** a writing Role that has failed `MaxRunAttempts` times in one Round
  for reasons the agent is answerable for
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the Shift closes at `needs_human`
- **AND** the close reason names the repeated failure rather than plan exhaustion

#### Scenario: The writer's pod keeps being killed

- **GIVEN** a writing Role whose Runs in one Round all ended `failed` for
  infrastructure reasons
- **WHEN** the orchestrator evaluates the Shift, fewer than `MaxInfraFailures`
  times
- **THEN** the Round re-opens with that writing Role
- **AND** the Role's `MaxRunAttempts` budget is undiminished, because nothing
  about the work has been tried

#### Scenario: The cluster keeps killing the writer

- **GIVEN** a writing Role whose Runs have failed `MaxInfraFailures` times in
  one Round for infrastructure reasons
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the Shift closes at `needs_human`
- **AND** the close reason names the infrastructure failure, distinctly from
  the reason used when the agent itself kept failing

### Requirement: A failed reading Run is retried, and a review that never came closes review_failed

A Round with a READING Role whose Runs all ended `failed` SHALL re-open in
place for that Role only, under the same two attempt budgets a failed writer
uses (ADR-0043). Readers of the same Round that succeeded SHALL NOT be re-run,
and the round counter SHALL NOT advance. A writing Run's failure in the Round
SHALL be handled first. The pool SHALL be checked when the retry is claimed,
so an unfundable retry opens no Run and parks the Shift as any unfundable Run
does.

When the Role's budgets are spent, the Round SHALL complete and the plan SHALL
advance. If the plan then ends and the last reading Round after the last
writing Round has a Role with no non-failed Outcome, the Shift SHALL close with
reason `review_failed` instead of `plan_exhausted`. With a writer's pull
request whose checks did not fail the Work Item SHALL still reach `awaiting_review`, and the tracker
comment SHALL say that no agent reviewed it and name the failure reason; it
SHALL NOT say the plan completed or that anything was approved.

#### Scenario: One of two reviewers fails

- **GIVEN** a review Round of two readers where one reported and the other's
  Run ended `failed`
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the Round re-opens with the failed reader only
- **AND** the reader that reported is not run again

#### Scenario: The reviewer never reviews

- **GIVEN** a writer that opened a pull request and a reviewer that failed
  `MaxRunAttempts` times
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the Shift closes `review_failed` and the Work Item reaches
  `awaiting_review`
- **AND** the tracker comment says the pull request was not reviewed by an
  agent and names the reviewer's failure reason

#### Scenario: A retried reviewer reviews

- **GIVEN** a reviewer that failed once and then reported on its retry
- **WHEN** the plan ends
- **THEN** the Shift closes as that review decides, never `review_failed`

### Requirement: A Shift closes on plan exhaustion or a terminal Outcome

A Shift SHALL close when its Team's plan has no further Round, or when any Run
reports an Outcome that ends the work. A closed Shift SHALL record why, so
"why did this item stop" is a query rather than a reconstruction, and SHALL
release the item's live-Shift slot so a later re-mandate can open a fresh one.

#### Scenario: The plan runs out

- **WHEN** the last Round of a Team's plan completes
- **THEN** the Shift closes with a recorded reason
- **AND** the Work Item reaches `awaiting_review` when a writer opened or updated a pull request whose checks did not fail, so a person reviews and merges it; otherwise it reaches `needs_human`

#### Scenario: A stuck Outcome freezes the plan

- **GIVEN** any Run reports `stuck` with a reason (R4)
- **THEN** the Shift closes, no further Round opens, and the item goes
  `needs_human` carrying that reason

### Requirement: A pull request is ready for review only when its checks passed on the pushed commit

After a writing Run opens or updates a pull request, the worker SHALL run the
configured checks on a fresh clone of the Run's branch at the pull request's
head, never in the agent's checkout, and SHALL report `incomplete` when the
clone is at another commit or the branch moves while the checks run
(ADR-0070). A Work Item SHALL NOT reach `awaiting_review` while the last
writing Run that opened or updated its pull request reported a `failed` or
`incomplete` verification. The pull request SHALL stay open. A configured plan
with fix rounds and budget left SHALL re-open its writing Round, whatever a
reviewer's verdict. Otherwise the Work Item SHALL reach `needs_human`: the
Shift SHALL close `checks_not_passed` when it would have been ready for review,
and keep the fix-round cap or budget reason when the fix loop ran out.

#### Scenario: The agent fixed the failure only in its checkout

- **GIVEN** a writer that pushed a commit failing a configured check and then
  fixed the file without committing it
- **WHEN** the worker verifies the Run
- **THEN** the verification is `failed` and names the pushed commit

#### Scenario: Failed checks with fix rounds left

- **GIVEN** a writer whose verification failed and a reviewer that approved
- **WHEN** the plan ends with fix rounds and budget left
- **THEN** the writing Round re-opens and the Work Item is not `awaiting_review`

#### Scenario: Failed checks with no fix round left

- **GIVEN** a writer whose verification failed or was incomplete
- **WHEN** the plan allows no fix round
- **THEN** the Shift closes `checks_not_passed` and the Work Item reaches
  `needs_human`

### Requirement: An exhausted budget pool parks the item rather than failing it

When `ClaimRole` raises `ErrBudgetExhausted`, ploegd SHALL move the Work Item
to `needs_human` with an R4 reason naming the spend. Retrying cannot fix
running out of money.

#### Scenario: The pool empties mid-Shift

- **GIVEN** a Shift whose remaining budget is below the viable floor
- **WHEN** the next Round would open
- **THEN** no Run is spawned, no attempt is burned, no credential is minted
- **AND** the item is `needs_human` with a reason naming the pool and the spend

