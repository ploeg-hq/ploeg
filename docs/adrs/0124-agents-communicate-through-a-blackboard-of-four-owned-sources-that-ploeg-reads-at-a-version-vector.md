---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Agents communicate through a blackboard of four owned sources that Ploeg reads at a version vector

## Context and Problem Statement

[ADR-0011](0011-the-pull-request-is-the-blackboard.md) made the pull request the blackboard for findings and Ploeg the courier. Since then a Run's view has grown well past the pull request: Work Item Notes and reviewed knowledge in the Tenant's graphs ([ADR-0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md), [ADR-0088](0088-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md)), operator notes, restarts, review requests and the tracker task itself. Each reaches a Run through its own path, read at no recorded moment, under one shared byte budget, and nothing tells a Run what changed after its claim. The owner asked on 2026-10-18 how agents should communicate and delegated the decisions. How do agents share what they know, how fresh is a Run's view, and how can a person replay what a Run was told?

## Decision Drivers

* Agents never talk to each other: no agent-to-agent channel and no A2A inside a Team, as ADR-0011 already found for findings.
* One owner per fact, so two copies cannot disagree and a reader always knows where to look.
* A Run's briefing is explainable and replayable from what was recorded, not from today's state.
* Freshness costs model spend, so a Run catches up at the moments it matters, not continuously.
* Nothing in the narrowest, freshest knowledge may be crowded out by wide, old knowledge.
* Every harness must keep working, including ones that take a single prompt turn.

## Considered Options

* A blackboard of four owned sources behind Ploeg, read at a version vector, with catch-up at checkpoints
* A shared message bus agents publish to and subscribe from
* Keep ADR-0011's courier and add sources one path at a time

## Decision Outcome

Chosen option: "**a blackboard of four owned sources behind Ploeg, read at a version vector, with catch-up at checkpoints**", because it extends ADR-0011's pattern to every source a Run reads, keeps every fact with one owner, and makes each briefing reproducible. Decided by the owner's delegation on 2026-10-18.

What is decided:

* **Four owners, one fact each.** The forge owns code, diffs, checks and review comments (version: commit sha). Ploeg owns Work Item state, Shifts, Runs, findings, verdicts, questions and budgets (version: its audit sequence). Omnigraph owns knowledge and memory at the levels Work Item, repository, Client and Tenant (version: graph commit). The tracker owns intent and acceptance criteria (version: the Work Item's revision, its `updated_at`). Everyone else holds pointers, never copies. Live state such as "the pull request is red", "the branch exists" or "the budget is spent" is never stored as a fact; at most as a pointer with when it was observed.
* **Ploeg is the front desk.** ploegd assembles each Run's view of the board and is the only writer to Omnigraph; a worker pod never holds a graph token, as already holds for Work Item Notes.
* **Append and supersede.** A graph write carries the commit it was based on, and a newer note supersedes an older one with an explicit edge instead of editing it, so history stays readable and every briefing can be read back. [ADR-0122](0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md) owns the notes subsystem that does this.
* **Each level has its own speed.** Work Item Notes are written directly (seconds); repository notes come from approved Runs; Client and Tenant knowledge only through review. ADR-0088 and ADR-0122 decide the promotion rules.
* **The version vector.** At the claim, ploegd records on the Run the highest audit event id and the Work Item's tracker revision, and returns them as the claim's `board`. Once the worker has composed the prompt, it sends a `briefed` checkpoint with the commit its clone stands on, the notes and knowledge graph commits it read, the keys of the Work Item Notes and the paths of the concepts the briefing showed, and each section's budget, use and digest. ploegd writes both once (migration `0063_run_board_versions.sql`).
* **A fixed budget per section, narrowest and freshest first, every item cited.** The findings section (earlier Rounds' findings, reviews, the restart, people's notes and board changes left for the Round) has 8,000 bytes and lists the newest Round first, each item headed by who wrote it and in which Round. The knowledge pack splits its budget (24,000 bytes by default) into fixed sections: three eighths each to the Work Item (its `work-item` notes, newest first, and concepts from its context) and the repository (its `repository` notes, its own bundle and the graph's repository concepts), an eighth each to the Client and the Tenant (configured bundles count as Tenant). An unused section does not lend to another. Each index entry cites its note key, graph commit or source. Each note is selected at the level it is read at (ADR-0122's `work-item` or `repository`; the levels are spelled as in the notes contract everywhere) and rendered with its provenance, untrusted text quoted as data. The findings by id and the questions of [ADR-0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md) are sections of the board view, `review_findings` (8,000 bytes) and `questions` (4,000 bytes), rendered once, by the board view. The active work of [ADR-0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md) (other live Shifts on the same repository and the paths they touch, overlaps first) is the `active_work` section, 2,000 bytes, which ploegd renders at the claim and the claim carries as `activeWork`.
* **Catch-up at checkpoints.** The worker keeps a board file (`PLOEG_BOARD_FILE`) current every two minutes through `POST /api/v1/runs/{token}/board`, and the prompt tells the agent to read it right before it pushes and right before it writes its findings or verdict. When the agent's turn ends, the worker asks once more (`before_finish`): an ACP agent gets the difference as one further prompt turn; for every other harness the difference is recorded on the Run and the next Round's briefing carries it as "board changes during round N". ploegd computes the difference per source from the Run's recorded versions, never from the request: Ploeg's own events for the Work Item (without the Run's own progress, outcomes or gateway bookkeeping), unconsumed people's instructions, notes on its root Work Item or repository that notes.ForRun lets it read, written by people, Ploeg or Runs outside its own Round, a person's answers to its Work Item's questions, contradictions flagged on notes it was briefed with, the tracker revision with the task's current title and description, the knowledge graph commit, and the branch head when it holds commits the Run did not make. At most twelve changes, newest first, each cited; text others wrote is quoted as data. Urgent changes, such as a withdrawn Work Item, keep using the existing steer channel.
* **Runs of one Round still never observe each other** ([ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md)): a catch-up leaves out outcomes and notes from its own Round.
* **Usefulness is measured.** `ploeg_runs_briefed_total{team,role,notes}` and `ploeg_run_briefed_notes_total{team,role}` count finished Runs that recorded a briefing, and [briefings](../ops/briefings.md) gives the SQL that relates each note to the Rounds and cost of the Shifts it briefed.
* **Replay.** `GET /api/v1/operator/runs/{id}/briefing` rebuilds the findings section from the append-only records the claim read and compares its digest with the one the worker reported, rebuilds the review findings, questions and active work sections from what Ploeg holds now (active work is live state, so it matches only while nothing changed), reads the briefed notes back by key with their provenance, lists the concepts as pointers at the recorded graph commit, and serves every catch-up the Run was given.

### Consequences

* Good, because every fact has one owner, and a Run's briefing names the version of each source it was read at.
* Good, because a Run that works for an hour is told what changed before it pushes, judges or finishes, at the cost of at most one extra prompt turn and only when something changed.
* Good, because a Work Item's own notes can no longer be squeezed out by wide knowledge; each level keeps its budget.
* Good, because an operator can replay what a Run was told and see whether it matches, without trusting the worker.
* Bad, because the audit sequence is read as the highest id, and an id allocated before a concurrent commit can land below it; such an event is missed by the catch-up, never by the next claim.
* Bad, because the before-push and before-verdict checkpoints depend on the agent reading the board file when the prompt says to; only before-finish is enforced, and only for ACP.
* Bad, because fixed sections can leave budget unused while another level omits items.
* Bad, because concepts replay as pointers: a graph that moved on cannot be read at an older commit through the stored queries.

### Confirmation

* `go test ./pkg/httpapi/ -run TestBoard` claims Runs, records a briefing, changes each source, checks that a catch-up names each change and nothing from the Run's own Round, that a before_finish left for the next Round reaches its briefing, and that the operator replay matches the worker's digest and validates against `docs/contracts/operator-api.v1.schema.json`.
* `go test ./pkg/harness/ -run TestBoardBodies` validates the claim's `board`, the `briefed` checkpoint and the board bodies against `docs/contracts/checkpoint.v1.schema.json` and `docs/contracts/run-api.v1.schema.json`.
* `go test ./pkg/harness/adapters/acp/ -run TestRun_CatchesUpWithTheBoard` sends one further turn when the board changed and none when it did not.
* `go test ./pkg/worker/ -run 'BoardWatch|BriefedCheckpoint'` and `go test ./pkg/knowledge/` check the board file, the before_finish delivery per harness, the briefed checkpoint and the per-level budgets.

## Pros and Cons of the Options

### A shared message bus

* Good, because agents would see changes the moment they happen.
* Bad, because it is agent-to-agent communication by another name: messages become a second copy of facts the forge, Ploeg, the graph and the tracker already own.
* Bad, because every harness would need a subscriber, and a single-turn harness cannot take messages at all.

### Keep ADR-0011's courier and add sources one at a time

* Good, because nothing new is decided.
* Bad, because briefings stay unreplayable and Runs stay blind to what changed after their claim.

## Re-evaluation triggers

* A harness other than ACP gains a way to take a further prompt turn or a mid-run message.
* Omnigraph's stored queries can read at a given commit, so concepts can replay exactly.
* The usefulness query shows Work Item Notes make no difference to Rounds or cost over 50 Shifts, or one level's section omits items in more than a quarter of briefings.
* More than one catch-up in twenty leads to a further turn that changes nothing.

## More Information

* Extends [ADR-0011](0011-the-pull-request-is-the-blackboard.md); [ADR-0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md) and [ADR-0088](0088-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md) stay in force.
* The streams that close the remaining gaps, merged before this record: ADR-0121 findings, answers and questions; ADR-0122 knowledge provenance, trust, promotion, review outcomes and deletion; ADR-0123 premise check, active work and dependencies.
* Choices a person should re-check: the two-minute refresh; the split of the knowledge budget (three eighths, three eighths, an eighth, an eighth) and that unused budget does not move; twelve changes per catch-up; configured knowledge directories counting as Tenant; reading the audit sequence as the highest id; reading notes changes from Ploeg's own note outbox rather than the graph; that a catch-up turn is only offered to ACP and only when the turn ended on its own.
* Migration `pkg/store/migrations/0063_run_board_versions.sql`.
* 2026-10-18: accepted by the owner's delegation.
