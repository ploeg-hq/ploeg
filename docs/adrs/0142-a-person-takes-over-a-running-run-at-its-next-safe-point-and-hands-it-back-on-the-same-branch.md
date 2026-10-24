---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A person takes over a running Run at its next safe point and hands it back on the same branch

## Context and Problem Statement

A person watching a worker Run sometimes sees that they would finish it faster themselves: the agent is going in circles, needs a decision only they can make quickly, or is one small edit away from done. Today they have two choices, and both lose work. A cancel ([withdraw](../contracts/README.md)) stops the Run mid-step and withdraws the Work Item, so the agent's uncommitted changes die with its pod. Waiting means paying for the Run until it ends. `take-control` and `handback` exist, but only for an Operator Execution, whose executor runs outside Ploeg ([ADR-0024](0024-operator-work-uses-one-execution-authority.md)); they flip its supervision and nothing more. A client-carried Run ([ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md)) is the person's from the start. How does a person take a running worker Run over without losing its work, and how do they give it back so the next Run continues from what they did?

## Decision Drivers

* Nothing the agent did is lost: committed, pushed or only in the checkout, it reaches the forge, where the person can pick it up.
* The agent stops between two steps, never in the middle of an edit, wherever the Harness can tell where a step ends. A Harness that cannot is refused up front, never stopped halfway.
* The forge stays the ground truth ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)): what Ploeg records about the yielded work is what the worker read on the forge after it pushed.
* No credential reaches the forge through the yield. The work in progress gets the same credential scan as every writer's push.
* A take-over costs the work nothing it had not already spent: the unspent part of the Run's authorization goes back into the work's budget.
* Every command names its person and channel ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)), and only the person a Work Item acts for takes it over when Ploeg knows that person.
* Reuse what exists. A Work Item parked for a person already resumes through `requeue` with `resume` ([ADR-0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md)); a take-over should park and resume the same way.

## Considered Options

* **Yield at a safe point, push a marked work in progress commit to the Work Item's branch, park for the person, hand back through a resume**
* Push the work in progress to a separate ref instead of the branch
* Stop the Run as a cancel does and let the person rebuild the work from the transcript
* Extend the Operator Execution's `take-control` to worker Runs

## Decision Outcome

Chosen option: "**Yield at a safe point, push a marked work in progress commit to the Work Item's branch, park for the person, hand back through a resume**", because it keeps all of the agent's work on the one branch the person and the next Run both already use, and every step reuses a mechanism Ploeg already trusts: the lease renewal, the worker's isolated git reads, the credential scan, the Shift close and the resume.

**1. Take-over.** `POST /api/v1/operator/work-items/{id}/take-over` with `{commandId, expectedShiftId?}` and `X-Ploeg-Actor`, from an execute-capable consumer ([operator API](../contracts/operator-api.v1.schema.json)). When the Work Item acts for a known person (`work_items.acts_for`, the person of its admission or the tracker user mapped to one), only that person takes it over (403 `person_forbidden`); an administrator acts for them with `X-Ploeg-Acting-User`. Otherwise any person an execute-capable consumer names may. ploegd refuses before anything changes when the Work Item has no open Shift or no running Run (409 `nothing_running`), a running Run is client-carried (409 `run_client_carried`), a running Run's Harness cannot yield (409 `yield_unsupported`, naming each Harness), or a person already took it over (409 `take_over_pending`). Otherwise it marks every running Run of the open Shift `yield_requested_at`, cancels the Shift's Runs nobody claimed yet so the Round ends with the yield, records the take-over and audits `operator.work_item.taken_over`, and answers 202. `commandId` is the idempotency key per consumer.

**2. Yield at the next safe point.** The worker learns of the request from its next lease renewal, whose answer carries `"yield": true` ([run API](../contracts/run-api.v1.schema.json)); at the default 60-second lease that is within 20 seconds. It closes `harness.RunEnv.Yield`. An adapter that implements `harness.Yielder` stops its agent at its next safe point and returns `harness.ErrYielded`. The worker says at its claim whether its Harness yields (`"yields": true`), so ploegd can refuse the take-over of one that cannot. Which Harnesses yield:

| Harness | Yields | Safe point |
| --- | --- | --- |
| `acp` (every profile: `opencode`, `qwen-code`, `goose`, `openhands`, and any ACP agent behind `custom`) | yes | Once no tool call the agent announced is still pending or in progress (at most `YieldGrace`, two minutes), `session/cancel` ends the prompt turn; the agent answers with the `cancelled` stop reason. |
| `claude-code` | no | `claude -p` takes one prompt and offers no channel to end it between steps. |
| `claude-environment` | no | The session runs in the Team's own environment; the worker can only follow it. |
| `openhands` | no | Spawn and wait, and retired ([ADR-0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md)). |
| `exec` | no | A command, not an agent. |

A Harness that cannot yield answers 409 `yield_unsupported`; a cancel is the way to stop it.

**3. The work goes to the Work Item's branch.** The worker commits what the agent left uncommitted, as it is, in its own git directory over the checkout (never the checkout's configuration or hooks), as one commit on top of the checkout's `HEAD`, with the subject `wip: work in progress handed to a person`, the Work Item reference and `Agent-Trace-Id` trailers, and a `Ploeg-Yield: <trace>` trailer. It scans every commit since the Run started for the Run's credentials before it pushes; a hit pushes nothing and reports the leak as every leak is reported. It pushes to `refs/heads/<branch>` without forcing: a branch that moved on the forge past the checkout is never rewritten, and the yield then says the work is not on the forge and why. It reads the branch back and reports the outcome **`yielded`** with `yield: {branch, commit, workInProgress, pushed, detail}` ([Outcome Report](../contracts/outcomereport.v1.schema.json)). ploegd refuses `yielded` from a Run nobody asked to yield.

**4. The Work Item parks for the person.** When the Round ends, every Run of it yielded or finished, the shift engine closes the Shift with `taken_over` and the Work Item settles at `needs_human`, with a reason that names the person, the branch and the commit. The take-over parks ahead of a stuck Run and of a failed Run's retry: the person asked for the work, whatever the Run did meanwhile. A credential leak still parks first, as a leak. A Run asked to yield that finished another way, or whose pod died, still parks the Work Item for the person. The Work Item detail shows `takeOver` (`state`: `yielding`, `taken_over`, `handed_back` or `ended`, who, branch, commit, whether it was pushed) and its `resume` offer with `reason: taken_over`.

**5. Hand back is a resume.** `POST .../requeue` with `resume: true` reopens the Round that yielded on the same branch, as ADR-0121's resume does, and `note` is the person's message to the next Run. While a person holds the Work Item, any requeue is accepted only when `X-Ploeg-Actor` names them (409 `taken_over_by_another`), and it hands the Work Item back. The first writing Run of that Round gets `handback: {branch, yieldedCommit}` on its claim: the worker checks the branch out at its head on the forge before the agent or the writer baseline sees it, and briefs the Run with the commits the person added since `yieldedCommit`, by subject, quoted as data. The restart briefing says who took it over and repeats their note.

**6. Budget.** The resumed Shift's pool is the parked Shift's unspent pool, at least the reopened Round's caps, as ADR-0121 defines it, plus what the yielded Runs were authorized and did not spend while their model-key accounts still hold the whole authorization: the authorization less the spend the gateway observed. A Run without an account, or whose account settled, already returned it to the parked Shift's pool.

**Why the branch and not a separate ref.** The person takes the work over to continue it, and the next Run continues from what they did. Both work on the Work Item's branch, which is what the pull request shows and what every Run of the Work Item already pushes to. A separate ref (`refs/ploeg/yield/<trace>`) is invisible in the forge UIs Ploeg supports and not fetched by a default clone, so the person would have to know git plumbing to find the work, the hand back would have to merge it back, and the GitHub and Forgejo branch protections would not cover it. The marked subject and the `Ploeg-Yield` trailer let anyone squash or drop the commit before merge.

### Consequences

* Good, because a person can take over a Run without losing a line of its work, and the next Run continues from what they did, told what changed.
* Good, because a take-over that cannot be honoured is refused before it changes anything, with the Harness named.
* Good, because the yield reuses the lease renewal, the isolated git directory, the credential scan, the Shift close and the resume; the new state is one table and two columns.
* Bad, because a work in progress commit can update an open pull request and start its CI. Accepted: the work is the person's from that moment, and the commit is marked.
* Bad, because a yield waits up to a lease renewal interval plus the tool grace before the agent stops, and a Harness other than ACP cannot yield at all. Accepted: stopping mid-step is what this decision avoids, and a cancel remains for the others.
* Bad, because the unspent part a hand back returns uses the gateway's observed spend, which a later settlement may raise. Accepted: the resumed Shift is still bounded by its own pool, as every resume is.
* Neutral: a reading Run yields too and pushes nothing; it has nothing to push.

### Confirmation

* `go test ./pkg/harness/adapters/acp/` (`yield_test.go`): a yield waits for the running tool call, then cancels the turn over the protocol and returns `ErrYielded`; a tool that never finishes does not hold it past its grace; an unasked Run does not yield.
* `go test ./pkg/worker/` (`yield_test.go`): the work in progress reaches the branch as one marked commit with its trailers and without ignored files; work carrying the Run's canary is never pushed and reports the leak; a branch the forge moved is never forced; a renewal answering `yield` stops a real-wire ACP agent and the worker reports `yielded`; the hand back's writer stands on the person's head and is told their commits.
* `go test ./pkg/httpapi/` (`take_over_test.go`): the take-over, its replay and conflicts, the attributed audit event, the renewal's `yield`, the park at `needs_human` with `takeOver` and the `taken_over` resume offer, the returned unspent authorization, the hand back by the taker only, and the `handback` claim; refusals for nothing running, a Harness that cannot yield, another person's Work Item and an unasked `yielded`; a Run that finished first still parks. Every response is validated against the published schema.
* `go test ./pkg/operatorclient/`: the client's `TakeOver` is attributed and idempotent.
* `go test ./pkg/harness/` pins the Outcome Report and run API schemas to the Go types.

## Pros and Cons of the Options

### Push the work in progress to a separate ref

* Good, because a pull request on the branch does not change until the person decides.
* Bad, because the person cannot see the work in the forge UI, a default clone does not fetch it, and the hand back has to bring it onto the branch anyway.

### Stop the Run as a cancel does

* Good, because it needs no Harness support.
* Bad, because the agent's uncommitted work is lost, and a cancel withdraws the Work Item.

### Extend the Operator Execution's `take-control`

* Good, because the command names already exist.
* Bad, because an Operator Execution's executor is outside Ploeg and `take-control` only changes who supervises it; a worker Run needs its agent stopped, its work pushed and its Shift parked, which is a different contract on a different resource.

## Re-evaluation triggers

* `claude -p` or the Claude Agent SDK used by the `claude-code` adapter gains an interrupt Ploeg can send between steps.
* The ACP protocol gains a pause or checkpoint request, or v2 changes `session/cancel`.
* Ten take-overs have been handed back, or a person reports a lost change after a yield.
* A forge rejects work in progress pushes to a protected branch, or a reviewer asks for the separate ref after all.
* A take-over between Rounds, when nothing runs, is asked for more than twice.

## More Information

* Contracts: [operator API](../contracts/operator-api.v1.schema.json) (`takeOverRequest`, `takeOverResponse`, `takeOver`, `resumeOffer.reason`), [run API](../contracts/run-api.v1.schema.json) (`yields`, `yield`, `handback`), [Outcome Report](../contracts/outcomereport.v1.schema.json) (`yielded`, `yield`), and the [contracts README](../contracts/README.md).
* Code: `pkg/store/take_over.go`, `pkg/store/migrations/0073_run_take_overs.sql`, `pkg/shiftengine/take_over.go`, `pkg/httpapi/take_over.go`, `pkg/worker/yield.go`, the ACP adapter's `awaitPrompt`.
* Related: [ADR-0024](0024-operator-work-uses-one-execution-authority.md) (Operator Executions and their `take-control`), [ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md) (requeue), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) (attribution), [ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md) (client-carried Runs), [ADR-0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md) (park and resume).
* Not built: a take-over between Rounds, when nothing runs; the person's own notes on the take-over itself (the hand back's `note` carries them); a take-over that waits for a Harness that cannot yield to finish.
