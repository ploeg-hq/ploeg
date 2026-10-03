# Incident: Work Item 138 ran for three hours and ended with no pull request

Date: 2026-09-29 · Ticket: [VIK-1305](https://vikunja.webgrip.dev/tasks/1305) · Work Item 138 ·
Shift 113 · Team bronze · Format: blameless, evidence first

Every fact below was read on 2026-09-29 from ploeg-db (`work_items`, `shifts`, `agent_runs`,
`audit_log`, `checkpoints`), the launcher pods' logs, the sandbox pods' logs in VictoriaLogs,
Kubernetes scheduler events in VictoriaLogs, the Vikunja task, the Forgejo refs of
`webgrip/glide`, and the code on `development` at `7f4b779` (deployed image `ploegd:0.4.0-rc.11`).
Behavior this report says Ploeg *should* have is labelled **proposed**.

## Summary

The owner assigned an approved, implementation-ready ticket to bronze at 05:58:52 UTC. The ticket
named its OpenSpec change `report-run-usage-on-pull-requests`, which had been on `development`
since 2026-09-28. Three hours and seven minutes later, at 09:05:56, the board got its first and
only message. It said Ploeg "stopped working this item without opening a pull request", and in the
next line it asked a person to "review and merge". Nothing had been written. The branch
`agent/vik-1305` was never pushed, and there is no pull request.

| What the owner lost | Amount |
| --- | --- |
| Time from assignment to first signal | 3 h 07 min, all of it silent |
| Model spend, settled | US$ 0,87 (Run 196 US$ 0,42 · Run 200 US$ 0,42 · Run 201 US$ 0,03) |
| Tokens | 33,3 M input, 0,21 M output |
| Work delivered | none: no commit, no branch on the forge, no pull request, no findings stored |
| Owner action needed | read the logs to learn what happened, then re-dispatch |

The brief this report was written from needs two corrections:

- **Two** builder attempts on Work Item 138 failed as `infra_node` (Runs 189 and 193), not three.
  A third launcher timed out in the same window, but its failure landed on Work Item 130
  (Run 192). See [factor 1](#1-kata-capacity-and-a-10-minute-start-timeout).
- **The reviewer did not agree.** Run 201 ended with a full review that said `request_changes`:
  "Core feature not present … The change is only partially implemented". That review was lost on
  the way into Ploeg, so the Run was recorded as `no_change_needed` with no verdict, and the Shift
  closed as though nobody had objected. See [factor 5](#5-the-reviewers-request-for-changes-was-lost).

Three findings matter most:

1. **The builder never claimed "no change needed".** Run 200 read the OpenSpec change and wrote
   a correct ten-step plan. It then ended its turn by asking a question ("Let me know if you'd like
   me to start with a different sub-task…"), and OpenHands exited 0. For a writer that exits 0
   with no pull request and no outcome file, Ploeg records the fallback label `no_change_needed`
   ([`resolveOutcome`](../../pkg/worker/worker.go)). The label describes the absence of a pull
   request. It is not the agent's judgement.
2. **The reviewer's `request_changes` would have triggered exactly the right recovery**, a fix
   round with its findings attached ([ADR-0017](../adrs/0017-the-review-loop-is-verdict-driven-and-capped.md)).
   It was dropped because the agent printed its JSON as its last chat message instead of writing
   it to `PLOEG_OUTCOME_FILE`. It also put the verdict in the `outcome` field. Even if it had
   written the file, the current code would have discarded its findings and verdict.
3. **The idle watchdog kills busy OpenHands Runs.** Run 196 made 193 model calls (US$ 0,42)
   and was killed for "no output" at 45 minutes, because OpenHands headless prints nothing until
   it finishes. Twelve Runs across bronze and silver died this way that day, with an average
   lifetime of 45,5 minutes and US$ 4,07 spent. Three silver Work Items were closed
   `writing_run_failed_repeatedly`. This was already known: see VIK-1291 and homelab-cluster
   `52d87560`.

## Timeline (UTC)

The homelab-cluster commits are `+02:00` in git and are converted to UTC here.

| Time | Event | Evidence |
| --- | --- | --- |
| 05:54:09 | `292a9407` requests 768Mi memory per Run | homelab-cluster git |
| 05:54:29 | `b75cab92` moves bronze to the agent-sandbox executor under Kata | homelab-cluster git |
| 05:58:52 | Vikunja webhook queues Work Item 138. Shift 113 opens Round 1 (builder) with an US$ 8,00 pool | `audit_log` 1093–1094 |
| 05:58:53 | KEDA starts launcher `z7fkc`. Its SandboxClaim pod stays `Pending`: "0/6 nodes are available: 1 Insufficient cpu, 2 Insufficient memory, 3 node(s) didn't match Pod's node affinity/selector" | launcher log, scheduler events |
| 06:08:56 | 10-minute start timeout. The claim is deleted and **Run 189** is claimed and failed in the same second, `infra_node`. The round reopens (attempt 2) | `agent_runs` 189, `audit_log` 1102–1105 |
| 06:09:05–06:19:11 | Launcher `4wfzm` also times out. Its failure is written to Run 192 on **Work Item 130**, the next pending bronze builder | launcher log, `agent_runs` 192 |
| 06:19:30 | Launcher `5mhvr`: pod Pending with the same scheduler message | scheduler events |
| 06:26:48 | `188d43d8` lowers the Kata Run's CPU request so it fits beside CI on worker-1 | homelab-cluster git |
| 06:29:26 | **Run 193** claimed and failed, `infra_node`. The round reopens (attempt 3) | `agent_runs` 193, `audit_log` 1129–1132 |
| 06:33:58 | Launcher `xqp2w` gets a ready sandbox, but the claim goes to Work Item 130 (Run 194), the older pending Run. Bronze's builder ScaledJob has `maxReplicaCount: 1`, so Work Item 138 waits | launcher log, ScaledJob spec |
| 07:19:18 | Run 194 (Work Item 130) is killed at the idle timeout after opening its PR | `agent_runs` 194 |
| 07:25:24 | **Run 196** claims Work Item 138. The worker finds the OpenSpec change under `apps/ploeg` and briefs from the `openspec` CLI (4 895 bytes). OpenHands 1.16.0 starts at 07:25:39 and prints its banner at 07:25:53 | sandbox log `vc2fv` |
| 07:25:32 | Checkpoint `branch_created` for `agent/vik-1305`. It is written *before* the harness runs and means a local branch, not a pushed one | `checkpoints` 213 |
| 07:25:53–08:10:54 | No harness output for 45 minutes while the Run makes 193 model calls | sandbox log; reconciliation evidence `entries=193` |
| 08:10:58 | **Run 196** fails `timeout`: "harness produced no output within its idle timeout after 45m0s". Spend US$ 0,42 (16,3 M input tokens). The round reopens (attempt 4) | `agent_runs` 196, `audit_log` 1188–1191 |
| 08:11:24 | **Run 200** claims the Work Item on a fresh clone with `briefing=0`, so it knows nothing about Run 196. It is briefed from the same OpenSpec change | sandbox log `hdpzb` |
| 08:45:06 | OpenHands prints its only output: 201 agent messages. The last one announces the scope in ten steps, says the "Immediate next step (Phase 2 – Read before write)" is to check the store layer, and ends: "*Let me know if you'd like me to start with a different sub-task…*" | sandbox log `hdpzb` |
| 08:45:15 | Agent exits `rc=0` with no PR and no outcome file. **Run 200** is recorded `no_change_needed`, "openhands run finished without opening a PR". Spend US$ 0,42 (15,5 M input tokens) | `agent_runs` 200, `audit_log` 1208 |
| 08:45:20 | The writer did not fail, so the plan advances: Round 2 (reviewer) opens | `audit_log` 1209 |
| 08:46:14 | **Run 201** (reviewer) finds no `agent/vik-1305` on the forge ("couldn't find remote ref"). It reviews the base branch under a prompt that says "you are running BEFORE the author" | sandbox log `c4cx8`; [`ComposePrompt`](../../pkg/worker/task.go) |
| 08:53:37 | ploegd restarts and the ploeg-db pods roll (08:53–08:56). Run 201 carried on, and no effect on this Work Item was found | pod start times |
| 09:05:42 | The reviewer's last chat message is a JSON object: `"outcome": "request_changes"`, with findings saying the renderer, the provider SPI, the store types, the chart value, the tests and the docs are all missing | sandbox log `c4cx8` |
| 09:05:56 | Agent exits `rc=0` and no outcome file exists. **Run 201** is recorded `no_change_needed` with no verdict and no findings. The Shift closes `plan_exhausted`, and the Work Item goes to `needs_human` with "plan complete; a person is asked to review and merge" | `agent_runs` 201, `audit_log` 1226–1228 |
| 09:05:56 | Vikunja comment 979, posted under the owner's own account `ryangr0`: "Ploeg stopped working this item without opening a pull request. **Outcome:** plan complete; a person is asked to review and merge … No pull request was opened … 5 agent run(s) across 2 round(s)." | Vikunja task 1305 |
| 09:20:56 | Last LLM account settles. The Shift has spent US$ 0,8682 of its US$ 8,00 pool | `audit_log` 1229, `shifts` 113 |

## Contributing factors

### 1. Kata capacity and a 10-minute start timeout

Bronze moved to Kata four minutes before the ticket arrived. The first Kata pods could not be
scheduled. The one node that can run Kata with memory to spare (worker-1) lacked CPU, and the
other nodes lacked memory or did not match the affinity. The launcher waits
`PLOEG_SANDBOX_START_TIMEOUT` (600s), deletes the claim, and then
[`FailUnstartedRun`](../../pkg/worker/unstarted.go) claims *whichever* bronze builder Run is
pending and fails it as `infra_node`. The failure therefore belongs to the queue, not to a Work
Item: Work Item 138 took two of the three timeouts, and Work Item 130 took the third. Once
`188d43d8` cut the CPU request, the next sandbox was ready in 4 minutes.

These failures counted against the infrastructure budget (`MaxInfraFailures = 10`,
[ADR-0021](../adrs/0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)), not
against the agent's three attempts, as designed. The cost was time, not attempts. Between
assignment and the first real Run, 1 h 26 min passed: 20 minutes of start timeouts, then 56
minutes queued behind Work Item 130 on bronze's single builder slot.

### 2. The idle watchdog counts only output, and OpenHands headless is silent

[`RunCommand`](../../pkg/harness/adapter.go) resets its idle clock only on harness
stdout/stderr. OpenHands 1.16.0 headless prints a banner and then nothing until the conversation
ends. Run 200's whole transcript appeared at 08:45:06, 33 minutes after start. So the watchdog
cannot tell a hung agent from a busy one: any Run that needs more than `idleTimeout` dies,
whatever it is doing. On 2026-09-27 this killed Run 134 at the 15-minute default.
homelab-cluster `52d87560` raised the timeout to 45 minutes as a mitigation, and VIK-1291 was
filed to count model traffic as activity. It is still open.

On 2026-09-29, **twelve Runs** died this way, each between 45,2 and 46,1 minutes: 185, 190, 191,
194, 196, 197, 198, 199, 202, 203, 204 and 205. Together they spent US$ 4,07. Silver ran on runc,
not Kata, so this failure is independent of the executor change. Silver Work Items 129, 132 and
133 each used their three agent attempts on it and closed `writing_run_failed_repeatedly`.

A kill also loses the work. Run 196 pushed nothing, and the retry (Run 200) started from a fresh
clone with an empty briefing. It paid again to read the same code.

### 3. A writer that stops to ask a question is recorded as "no change needed"

The writer's delivery contract ([`ComposePrompt`](../../pkg/worker/task.go)) says: push, open a
pull request, and "if the Work Item cannot be completed, explain why on stderr and exit non-zero".
Writers are not asked for an outcome file. Nothing in the prompt says that no one will answer a
question mid-Run. Run 200 behaved like an interactive assistant: after 201 messages of reading it
presented its plan and asked for confirmation. OpenHands treats the end of the agent's turn as the
end of the task and exits 0.

[`resolveOutcome`](../../pkg/worker/worker.go) then takes its `runErr == nil` fallback: no new
PR, no valid structured report, some LLM traffic → `no_change_needed`, "openhands run finished
without opening a PR". `stuck_reason` stays empty and the last message is not stored, so ploeg-db
cannot tell "the agent decided nothing needs changing" apart from "the agent stopped halfway".
VIK-1365 already describes this path from Work Items 124 and 128 on 2026-09-28. Its three cases
include one "Please confirm the scope…" stop, the same shape as this incident.

**Did the builder see the OpenSpec change?** Yes. Both Run 196 and Run 200 logged "work item
names an OpenSpec change … root=apps/ploeg" and "briefed from the OpenSpec change … source=cli".
Run 200's last message names the change's task groups (provider SPI, store, renderer, publish
path, settlement sweep, chart, docs, tests).

**Was it confused by earlier attempts?** No evidence of that. Every attempt starts from a fresh
`emptyDir` clone ([`worker.go`](../../pkg/worker/worker.go) removes `vik-<id>` first), and
`briefing=0`. Run 200 had no knowledge of Run 196.

**What did its summary say?** Ploeg's summary is the generic fallback string. The agent's own
last message (quoted in the timeline) is only in VictoriaLogs.

Its plan did drift from the ticket in two places. It proposed store changes ("add `ShiftID` to
`UnsettledLLMAccount`… DB fields") that the ticket's "no migration" rule forbids, and a
`PLOEG_USAGE_REPORT` flag where the ticket asked for `PLOEG_REPORT_GRAFANA_URL` and
`PLOEG_REPORT_VLOER_URL`. A reviewer would have caught both, had there been a diff.

### 4. The Run was billed on different models than it asked for

Every sandbox log shows `model=litellm_proxy/deepseek-chat`. LiteLLM billed Run 196 on
`deepseek/deepseek-flash`, Run 200 on `deepseek-flash` **and**
`fireworks_ai/…/gpt-oss-120b`, and Run 201 entirely on `gpt-oss-120b` (`agent_runs.usage.models`).
This report cannot show that the model swap caused the mid-task question. It does mean the
owner's choice of model was not what ran, which is VIK-1408.

### 5. The reviewer's request for changes was lost

Run 201 did its job. It found that nothing was implemented and asked for changes. Three things
kept that from reaching the Shift:

1. **Wrong channel.** The reader contract says to write the JSON "to the file named by the
   PLOEG_OUTCOME_FILE environment variable, as the LAST thing you do". The agent printed it as its
   last chat message instead. The file did not exist, so
   [`ReadDropBox`](../../pkg/harness/dropbox.go) returned "no structured signal"
   ([ADR-0018](../adrs/0018-the-outcome-drop-box-is-every-harnesss-return-path.md)).
2. **Wrong field.** It wrote `"outcome": "request_changes"` instead of
   `"outcome": "no_change_needed", "verdict": "request_changes"`. Had it used the file,
   `report.Outcome.Valid()` would have been false. The `runErr == nil` branch of `resolveOutcome`
   carries over only `Usage` and `CreatedWorkItems`, so **findings and verdict would still have
   been dropped**. No existing ticket covers this. VIK-1366 covers only a file that does not
   decode.
3. **Wrong premise.** With no branch on the forge, the reviewer was told "No work has been
   written for this Work Item yet — you are running BEFORE the author". In Round 2 that is false.
   It reached the right conclusion anyway, but a reviewer should not have been started at all. A
   writer Round that produced no branch has nothing to review, and VIK-1365 asks for the reader
   Round to be skipped in that case.

Because the stored verdict was empty, [`nextFixRound`](../../pkg/shiftengine/reviewloop.go)
chose `plan_exhausted` over a fix round. The fix round was the one automatic path that would have
retried the builder with the reviewer's findings attached.

One smaller point: the reviewer's first finding was "Build fails due to missing module downloads".
That is the sandbox's egress policy, not a defect, and `apps/ploeg/CLAUDE.md` says to skip such a
gate rather than report it. Had the verdict survived, this finding would have been noise in the
builder's briefing.

### 6. The closing message contradicts itself

[`closeMessage`](../../pkg/shiftengine/reviewloop.go) maps `plan_exhausted` to "plan complete; a
person is asked to review and merge" without checking that a pull request exists.
[`trackerMessage`](../../pkg/shiftengine/publish.go) then puts that line between "stopped working
this item without opening a pull request" and "No pull request was opened". The owner is asked to
review and merge a pull request that does not exist. It is also not told why the Shift stopped,
what was spent, or that the reviewer objected. VIK-1304 covers the neighbouring case of a failed
reviewer.

### 7. The owner heard nothing for three hours

- **Board.** Ploeg writes to the tracker only when a Shift reaches a terminal state
  ([`notifyTracker`](../../pkg/shiftengine/publish.go)). Two infrastructure failures, one
  45-minute kill and a Round change produced no comment. `SetStatus` deliberately drops anything
  other than `done`, so the card did not move either.
- **Notification.** The single comment was posted under the owner's own Vikunja account
  (`ryangr0`). Vikunja normally does not notify people of their own comments, so the comment
  probably raised no notification at all (not verified). VIK-1331 asks Ploeg to comment as its own
  user.
- **Vloer.** Vloer's Ploeg overview reads the operator API and would have shown the Work Item and
  its Runs. This report did not check what it showed during the incident. VIK-1370 notes that a
  team's recent Work Items can sit behind page one.
- **Evidence.** The facts that explain this incident (the builder's closing question and the
  reviewer's JSON) exist only in VictoriaLogs, in pod logs of sandboxes that were deleted when
  they finished. `agent_runs.summary` holds a generic string for every Run of this Work Item.
- **Checkpoint naming.** Three `branch_created` checkpoints for `agent/vik-1305` sit in
  `checkpoints` and `audit_log`, but the branch was never on the forge. The phase is written
  before the harness runs.
- **Alerting.** Twelve identical kills at 45 minutes over seven hours paged no one.

## What went right

- **Money stayed bounded.** Each Run was minted a key capped at its authorization (US$ 2,00 per
  builder, US$ 0,40 for the reviewer), and each key was revoked on exit. Every account settled
  from LiteLLM spend logs with evidence. The Shift used 11% of its pool. Unstarted Runs were
  reconciled at US$ 0 ("mint-never-began").
- **Infrastructure failures were classified and budgeted correctly.** `infra_node` for a sandbox
  that never started ([ADR-0021](../adrs/0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md))
  did not use up the agent's attempts. The claim was deleted rather than left to start late.
- **The retry machinery worked.** A failed writer reopened its Round in place
  ([ADR-0019](../adrs/0019-a-failed-writing-run-reopens-its-round.md)), four times, with no
  person needed.
- **The OpenSpec path worked.** The directive was parsed from HTML, the change was found under
  `apps/ploeg`, and the CLI brief reached every Run.
- **Nothing unsafe happened.** No push to `development`, no merge, no credential in a log. The
  forge token and the LLM key stayed behind the worker's loopback proxies
  ([ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)).
- **The evidence was recoverable.** VictoriaLogs kept the sandbox pods' output, and the audit log
  and spend evidence were enough to rebuild the timeline to the second.
- **The reviewer, as an agent, was right.** Its reading of the change was accurate. The loss
  happened in the plumbing.

## Non-golden paths: what the owner should experience (proposed)

A client or owner should never have to open logs to learn what happened. The rule proposed here:
**Ploeg retries silently only what is both cheap and fast. It tells the owner about everything
else when it happens, in one comment it keeps up to date, and never in a message that
contradicts itself.** Everything in this table is **proposed**.

| Failure mode seen here | Retry automatically? | Owner is told | When | What the message must say |
| --- | --- | --- | --- | --- |
| Sandbox cannot be scheduled (cluster capacity) | Yes. Keep the Work Item **queued** as "waiting for capacity" and do not create failed Runs for it. Attribute the wait to the Work Item that caused the launch | Yes, once, if the wait exceeds 10 minutes; edit the same comment when it starts | +10 min, then on start | "Waiting for a machine since 05:58: the cluster has no CPU or memory free for a sandbox. Nothing has been spent." |
| Harness killed with no output while the model is busy | Should not happen: model traffic counts as activity (VIK-1291). A true hang (no output **and** no model traffic) is retried once with the previous attempt's evidence in the briefing | Yes, on the first kill | at the kill | "The builder stopped responding after 45 minutes and US$ 0,42. Retrying once, with what it had found (attempt 2 of 3)." |
| The same kill repeats across a Team | Stop dispatching to that Team after two identical kills in a row | Operator alert, plus one line on each affected Work Item | at the second kill | "Paused: bronze's Runs are being killed at the idle timeout. This is a Ploeg problem, not your ticket." |
| Writer exits 0 with no PR and no outcome | No blind retry. Record `failed`, not `no_change_needed` (VIK-1365). If the last message is a question, record `stuck` with that question | Yes | at the Run's end | "The builder stopped before writing any code and asked: *'…start with a different sub-task?'* Answer on this ticket, or re-dispatch as is." |
| Writer really finds nothing to do | No retry | Yes | at the Run's end | Only with proof: an outcome file with `no_change_needed` plus evidence (the files and acceptance criteria checked, and why each is already met). For an OpenSpec Work Item with pending tasks, `no_change_needed` is refused unless every task is shown done. Without proof it is `failed` |
| Writer Round produced no branch | Do not start the reviewer (VIK-1365) | covered by the row above | | |
| Reviewer answer not in the outcome file, or malformed | Recover it if the transcript's last message is a valid report. Otherwise `failed`, retried once within the infra budget (VIK-1366, VIK-1304) | Yes, if not recovered | at the Run's end | "The review could not be read; retrying the review." Never "plan complete" |
| Reviewer says `request_changes` | Yes: one fix round per [ADR-0017](../adrs/0017-the-review-loop-is-verdict-driven-and-capped.md), with findings as the briefing | Yes | when the fix round opens | "The reviewer found 7 problems; the builder is fixing them (fix round 1 of 2)." |
| Shift closes without a PR | No | Yes | at close | Headline states the real reason ("the builder stopped mid-task", "the reviewer found nothing implemented"), then Runs, spend against pool, the agent's last words, and the next step. Never "review and merge" |
| Model served differs from the model authorized | Fail the Run (VIK-1408) | Yes | at the Run's end | "LiteLLM served gpt-oss-120b; this Team is authorized for deepseek-chat. Nothing was kept." |

What a `no_change_needed` outcome must prove (**proposed**):

1. It comes from the agent's own structured report, never from a fallback.
2. It cites the acceptance criteria one by one, with the file or check that shows each is
   already met.
3. On an OpenSpec Work Item, `openspec` shows no pending tasks, or the report explains each
   pending one.
4. The spend is plausible for a read-only conclusion. Two Runs of US$ 0,42 over 15 M tokens is
   not "nothing to do".

Without that proof, the outcome is `failed` or `stuck`, and the owner is told which.

## Proposed tickets

**Proposed only; none of these is on the board.** Existing tickets this incident confirms, and
which should be done first: VIK-1291 (idle watchdog counts model traffic), VIK-1365 (writer exit
0 without PR fails), VIK-1366 (undecodable reviewer outcome fails), VIK-1304 (failed reviewer is
not "plan complete"), VIK-1331 (Ploeg comments as its own Vikunja user), VIK-1408 (model outside
key scope fails the Run). Each ticket below adds something those do not cover.

| # | Title | Problem | Outcome |
| --- | --- | --- | --- |
| 1 | `worker: keep a reader's findings when its outcome is invalid` | A report with `"outcome": "request_changes"` loses its findings and verdict in `resolveOutcome`. | Findings and verdict always survive, and a verdict value in `outcome` is read as the verdict. |
| 2 | `worker: fail a reading Run that leaves no outcome file` | A reviewer that printed its review instead of writing the file became a silent `no_change_needed`. | A reader with no file is `failed` and retried once, or recovered from its last message. |
| 3 | `worker: tell headless agents nobody answers questions mid-Run` | Run 200 stopped after 201 messages to ask for confirmation. The prompt never says it is unattended. | Writer and reader contracts say "no one will reply; finish or exit non-zero", with a golden test. |
| 4 | `worker: record an agent's closing question as stuck` | A final message that asks the user something is thrown away and labelled `no_change_needed`. | The Run ends `stuck`, with the question as `stuck_reason`, shown on the board. |
| 5 | `worker: store the agent's last message on every Run` | The explanation for this incident existed only in VictoriaLogs; `summary` was generic. | `agent_runs` keeps the last message (bounded) for every outcome, visible in the operator API. |
| 6 | `worker: brief a retried writer with the previous attempt` | Run 200 restarted from zero after Run 196 spent US$ 0,42 and was killed. | A reopened writer gets the prior attempt's failure reason, spend and last message as its briefing. |
| 7 | `worker: require evidence for a writer's no_change_needed` | "No change needed" can be recorded with no claim from the agent at all. | Only a structured report citing each acceptance criterion, with OpenSpec tasks all done, yields it. |
| 8 | `shiftengine: word a no-PR close by its real cause` | The board was asked to "review and merge" a pull request that did not exist. | `closeMessage` and `trackerMessage` name the cause, the spend and the next step when there is no PR. |
| 9 | `tracker: post one live status comment per Shift` | The owner learned nothing for 3 h 07 min through two retries and a kill. | One comment, edited in place, reports each retry, kill and Round change within a minute. |
| 10 | `sandbox: show capacity waits as queued, not failed Runs` | Start timeouts fail whichever Run is pending next, so failures land on the wrong Work Item. | A Work Item waiting for capacity reads "waiting for capacity (reason)" and gets no failed Run. |
| 11 | `alerts: page when a Team's Runs die at the idle timeout` | Twelve identical 45-minute kills over seven hours alerted no one. | Two consecutive idle kills per Team fire an alert and pause that Team's dispatch. |
| 12 | `worker: name the pre-harness checkpoint for what it is` | `branch_created` is recorded for a branch that never reached the forge. | The phase is renamed (for example `workspace_ready`), and `branch_pushed` is recorded only after the push is seen on the forge. |

## Evidence sources

- ploeg-db: `work_items` id 138, `shifts` id 113, `agent_runs` ids 189, 193, 196, 200 and 201 (and
  every Run started on 2026-09-29 for comparison), `audit_log` ids 1093–1229, `checkpoints` ids
  213, 215 and 216.
- Launcher pod logs: `ploeg-worker-bronze-builder-{z7fkc,4wfzm,xqp2w,vc2fv,hdpzb}` and
  `ploeg-worker-bronze-reviewer-c4cx8`.
- VictoriaLogs (`vlsingle-victorialogs`): pods `sbx-ploeg-worker-bronze-builder-vc2fv-968lk`
  (Run 196), `sbx-ploeg-worker-bronze-builder-hdpzb-wt2vx` (Run 200) and
  `sbx-ploeg-worker-bronze-reviewer-c4cx8-vjzvb` (Run 201), plus `FailedScheduling` events in
  namespace `ploeg` from 05:58 to 06:35.
- Forgejo: `git ls-remote` of `webgrip/glide` shows no `agent/vik-1305` ref.
- Vikunja task 1305: one comment (id 979).
- Live configuration: `PLOEG_HARNESS_IDLE_TIMEOUT=45m`, `PLOEG_HARNESS_TIMEOUT=100m`,
  `PLOEG_SANDBOX_START_TIMEOUT=600s`, and the bronze builder ScaledJob's `maxReplicaCount: 1`.
- homelab-cluster: `52d87560`, `292a9407`, `b75cab92` and `188d43d8`.
