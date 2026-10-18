---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Findings carry ids, writers answer them, and a Run asks instead of guessing

## Context and Problem Statement

A reviewer's findings are one block of markdown ([ADR-0011](0011-the-pull-request-is-the-blackboard.md)). The fix Round's writer gets the block in its briefing and nothing records what it did with each point, so the next reviewer reads the whole diff again and cannot tell a finding the writer fixed from one it ignored. A Run that needs a decision only a person can make either guesses or reports `stuck` with prose, and nothing brings the person's answer back to the next Run. A shadow reviewer ([ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md)) is counted in a metric, but a person cannot see on one Work Item where it disagreed with the acting reviewer.

How do agents in one Shift answer each other point by point, and ask a person, without talking to each other directly?

## Decision Drivers

* Agents never talk to each other directly: every exchange goes through what Ploeg stores and briefs (the blackboard).
* One owner per fact: Ploeg owns findings, answers, questions and verdicts; ids are Ploeg's, not the agent's.
* Additive and compatible: an older worker or outcome without the new fields keeps today's behaviour exactly.
* A shadow still never acts.
* No paid model spend to prove it: every test uses fakes and the embedded database.

## Considered Options

* **Structured findings, answers and questions on the outcome report, with ids Ploeg assigns**
* Ask agents to number findings in the prose and parse the numbers
* Let agents reply to each other on the pull request thread

## Decision Outcome

Chosen option: "**structured findings, answers and questions on the outcome report, with ids Ploeg assigns**", because it keeps the exchange on Ploeg's contract, where it is validated, stored, briefed and shown, and leaves the prose path untouched for older workers.

This was decided by the owner's delegation on 2026-10-18 (the blackboard design brief, ticket VIK-2031).

What is decided:

* **Contract** ([outcome report](../contracts/outcomereport.v1.schema.json), all optional):
  * `findingItems[]` from a reading Run: `{id?, severity?, title, body?, path?, line?}`, severity `blocking | should_fix | nit`. ploegd assigns a new finding the id `f<runId>-<n>`; an entry whose `id` names a finding an acting reader of the Work Item raised earlier raises that finding again.
  * `findingAnswers[]` from a writing Run: `{findingId, status, reason?}`, status `fixed | disagree | out_of_scope | deferred`, a reason required for `disagree`.
  * `questions[]` from any Run: `{to, text}`, `to` is `person | reviewer | next_run`; ploegd assigns `q<runId>-<n>`.
  * `questionAnswers[]`: `{questionId, answer}`, for a question to a reviewer or the next Run.
  * ploegd refuses a report whose entries break the schema (400); the worker drops a malformed entry before sending, so an agent's mistake costs that entry only.
* **What ploegd keeps** (`agent_runs.finding_items`, `finding_answers`, `questions`, `question_answers`, migration 0060): findings from readers only, answers to findings from writers only and only for a known finding, questions and answers from Runs that are not shadows, answers only to known questions to a reviewer or the next Run. Everything dropped is counted in the outcome's audit event under `blackboard.dropped`. A shadow's findings are stored, with ids, for comparison and are never briefed, answered or re-raised.
* **Briefing** (claim response and Task Spec `reviewFindings`, `questions`):
  * the findings of the Shift's last review Round before the Run, each with the writer's answer given after it was last raised, or `unanswered: true` when a writer finished since without answering it. A writer answers them; a reader checks the answers, is told to raise a finding again by id only when its answer does not settle it, and to say so when one is unanswered. A finding the next review does not raise again is settled.
  * every question to a person on the Work Item (the newest 10, across Shifts) with a person's answer once given; the Shift's earlier questions to the next Run, and to the reviewer for a reading Run, with a Run's answer once given.
* **A question to a person parks.** When a Round ends and one of its acting Runs asked a person something nobody answered, the Shift closes with reason `question_for_person` and the Work Item goes to `needs_human`; the close message, which the tracker comment and the `work_item.needs_human` audit event carry, quotes each question with its id. A person answers with `POST /api/v1/operator/work-items/{id}/questions/{questionId}/answer` (audited `question.answered`, one answer per question); every later Run of the Work Item is briefed with the answer.
* **Answer, then resume.** `POST .../requeue` with `{"commandId", "resume": true}` resumes the parked Shift instead of restarting it ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)'s requeue, with the Round chosen by Ploeg):
  * the new Shift reopens the plan Round that asked, on the parked Shift's branch; a fix Round maps to the plan's writing Round (a re-opened writer) or its last Round (a re-opened review). Earlier Rounds do not run again and their delivery stands;
  * its Runs are briefed as if the parked Shift went on: the parked Shift's Runs before the Round that asked count as its earlier Rounds (`work_item_requeues.resumes_round`), so the review's findings, the writer's answers and the person's answer reach the reopened Round;
  * its pool is the parked Shift's unspent pool, at least the reopened Round's cap, unless `poolUsd` is given;
  * refused with 409 `question_unanswered` while a question of the parked Round has no answer, 409 `nothing_to_resume` when the last Shift did not stop on one, 400 with `fromRound`; a replayed `commandId` returns the first result. Audited as `work_item.requeued` with `resume: true` and `resumes_round`.
  * The full restart, a requeue without `resume`, is unchanged.
  * Work Item detail carries `resume` while a resume is possible: the parked Shift, the Round that asked, `fromRound`, `roundCapUsd` (the sum of the reopened Round's Role caps, shadows included: the most that Round can be authorized), `poolUsd` (the most the resumed Shift can spend in all, the Rounds after it included), `ready` and the questions still waiting.
* **Reads.** Every Run shape on the operator API carries the four fields with assigned ids. Work Item detail carries `findings` (each with its answer and `unanswered`), `questions` (with `waitingForPerson`) and `reviewDisagreements`: each review Round where the shadow's verdict differs from the acting readers' verdict combined as the review loop combines them, the same pairing `ploeg_shadow_reviews_total` counts.
* **Pull request.** The findings comment lists findings by id after the prose, and a writer's comment lists its answers.
* **Legacy.** A Run without these fields is briefed and published exactly as before; the prose `findings` field and the `briefing` list are unchanged.

### Choices a person should re-check

These were made under delegation and are the ones most likely to be wrong:

1. **Ids are Run-scoped (`f<runId>-<n>`), not content hashes.** A reviewer that rewrites a finding instead of raising it again by id creates a new finding; the old one is then settled by silence.
2. **"Not raised again = settled."** A finding answered `disagree` or `deferred` that the next reviewer does not raise again counts as accepted. A stricter rule would make the reviewer accept or reject each answer explicitly.
3. **A question to a person stops the whole Shift** at the end of its Round, even when the Run also produced work, and the reopened Round runs again from the start: that Round's Runs are paid twice. Answering does not resume by itself; resuming is `resume: true` on the requeue, so it stays one idempotent, audited command next to the full restart.
9. **A resume's pool is the parked Shift's unspent pool** (at least the Round's cap), not the plan's pool, and the fix-round cap counts again from the reopened Round, so a resumed review loop can run up to `maxFixRounds` more fix Rounds within that pool.
4. **One answer per question, first wins**; a later correction needs an operator note.
5. **Answers are accepted in any Work Item state**, including operator-owned items, which notes refuse.
6. **Planner and read-a-pull-request prompts are unchanged**: only the review loop's readers and writers are asked for ids and answers.
7. **Severity has three values**, and only `blocking` is a hint to the writer; nothing in the review loop reads it. The verdict alone still decides a fix Round ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)).
8. **Disagreement is by verdict only**, not by findings; the shadow's findings are on the operator API for a person to compare.

### Consequences

* Good, because each review now checks a short list of answers instead of re-reading the whole change, and a skipped finding is visible to the reviewer and to a person.
* Good, because a Run can stop and ask, and the answer reaches later Runs without an agent ever holding a channel to a person.
* Good, because a person sees on one Work Item where the shadow reviewer disagreed.
* Bad, because the prompts grow by up to 8000 bytes of findings, on top of the briefing's 8000. Accepted: the findings replace re-reading prose the reviewer would read anyway.
* Bad, because a model that ignores the instructions still produces prose only, and the loop then works as before without answers. Accepted: the legacy path is the fallback by design.
* Bad, because parking on a person's question costs a requeue and a new Shift. Accepted against guessing.

### Confirmation

* `go test ./pkg/findings/` derives briefings, unanswered flags, raised-again findings, question routing and shadow disagreements from stored Runs.
* `go test ./pkg/harness/` pins the four fields to `docs/contracts/outcomereport.v1.schema.json`, the briefing fields to `taskspec.v1.schema.json`, refuses malformed entries in the schema and in Go alike, and keeps valid entries through `MergeDropBox`.
* `go test ./pkg/shiftengine/` (`findings_test.go`) resumes a fix Round's writer that asked a person: refused before the answer, then reopened on the same branch with the review's findings and the answer in its briefing, the review not run again, the pool and the audit detail as decided, the replay idempotent; and it runs a review that requests changes with two findings beside an approving shadow, a writer that answers one, and checks the next reviewer is briefed with the answer and the unanswered flag, the operator detail shows both and the disagreement, foreign entries are dropped, and a question to a person parks the Work Item with the question text and its answer briefs the next Shift's writer.
* `go test ./pkg/httpapi/` (`operator_questions_test.go`) posts findings and a question on the outcome endpoint, validates Work Item detail and the answer response against `docs/contracts/operator-api.v1.schema.json`, refuses a second answer and an unknown question, and checks the next claim's briefing; `TestResumeOverTheOperatorAPI` checks the detail's resume offer and its cap, and the requeue's resume refusals and success.
* `go test ./pkg/worker/` (`findings_prompt_test.go`) checks the writer and reader prompts.
* Gate: `mise run verify` and `mise run docs-check`, run in CI.
* Live: one Shift where a reviewer's findings carry ids and the fix Round's writer answers them, read on the operator API.

## Pros and Cons of the Options

### Number findings in the prose and parse them

* Good, because no contract change.
* Bad, because parsing model prose is unreliable, and answers would need a second parsed format.

### Agents reply on the pull request thread

* Good, because a person already reads the thread.
* Bad, because it gives a reading Run a forge write and agents a channel to each other, which the blackboard design rules out, and Ploeg could not validate or brief what was said.

## Re-evaluation triggers

* 20 fix Rounds have run with findings by id: check how often writers leave findings unanswered and whether "not raised again = settled" hid a real problem.
* A reviewer model routinely rewrites findings instead of raising them again by id.
* People answer questions and forget to resume, or ask for answering to resume.
* A resumed Round spends noticeably more than its cap because the Rounds after it run on the parked pool.
* A consumer needs findings agreement between shadow and acting reviewers scored by ploegd.

## More Information

* Ticket VIK-2031; the blackboard design brief of 2026-10-18.
* Migration `pkg/store/migrations/0060_run_findings_and_questions.sql`.
* 2026-10-18: accepted, decided by the owner's delegation.
