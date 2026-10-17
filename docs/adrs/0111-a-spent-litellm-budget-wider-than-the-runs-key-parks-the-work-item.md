---
status: accepted
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A spent LiteLLM budget wider than the Run's key parks the Work Item

## Context and Problem Statement

LiteLLM enforces budgets at several levels: the key, and above it the team, the user, the end user, the organization, the project, the tag and the model access group. Ploeg mints every Run's key inside the Team's LiteLLM team when `litellmTeamId` is set (ADR-0078), so that team's budget bounds all of the Team's Runs together.

Since #151, `litellm.ClassifyError` recognises a spent budget from LiteLLM's error type and wording at any HTTP status (429 up to LiteLLM 1.103, 422 from 1.104) and names whose budget it was. The ACP adapter then labelled every recognised gateway refusal `infra_llm`. `infra_llm` is an infrastructure failure (ADR-0021), so the Shift reopened the Round under the infrastructure retry budget, up to `MaxInfraFailures` times. A Shift-less Run requeued. The non-ACP harnesses labelled a Run that exited without model traffic `infra_llm` too, and one that exited with an error `stuck` with only a log tail as the reason.

A spent team budget does not recover on retry. Every new key under that team is refused the same way until a person raises or resets the budget. The retries spend pods, start-up time and the infra budget, and the Work Item finally parks as `writing_run_killed_repeatedly`. That close reason sends the person to the cluster, not to LiteLLM.

What does ploegd do with a Run the gateway refused because a budget wider than the Run's own key is spent?

## Decision Drivers

* A retry that cannot succeed must not be spent. ADR-0021's infrastructure budget assumes the failure goes away by itself.
* Only a person can raise a team, user or organization budget. Ploeg does not manage LiteLLM's budgets outside its own keys (ADR-0008).
* The person must learn from the park reason whose budget is spent, without reading logs.
* The Run's own key is different. Its cap is the Run's authorization (ADR-0012), and the next Run gets a fresh key with a fresh cap.
* Agent output can quote gateway errors from the work itself, for example when an agent works on Ploeg's own budget code. Reading those quotes as a refusal must not park healthy work.

## Considered Options

* **A new failure reason, `shared_budget_spent`, that parks the Work Item for a person**
* Keep `infra_llm` and let the infrastructure budget run out
* Park every recognised spent budget, the Run's own key included

## Decision Outcome

Chosen option: "**a new failure reason, `shared_budget_spent`, that parks the Work Item for a person**", because it is the only option that stops retries that cannot succeed and leaves the per-Run key behaviour alone.

1. **Classification.** `litellm.GatewayError.SharedBudgetSpent` is true for a spent budget whose owner the gateway named and that is not the key: team, user, end user, organization, project, tag or model access group. `GatewayError.BudgetSpent` names the owner and its id, such as "the LiteLLM team budget for t2 is spent".
2. **Where a Run reports it.**
   * The ACP adapter reports `failed` with `shared_budget_spent` when `session/new` fails that way, and when `session/prompt` fails that way. Only the agent's stderr and the transport error are read, never the agent's messages or tool output. The summary names whose budget is spent.
   * `pkg/worker`'s `resolveOutcome` does the same for an LLM harness (claude-code, openhands) that made no model traffic, whether it exited with an error, exited cleanly or reported `failed` without a reason. It reads only the last 4 KiB of the harness output. A Run with no model traffic made no tool calls, so its output cannot quote the work, and a prompt the harness echoes sits before that window.
3. **Parking.** `work.FailureReason.ParksForHuman` is true for `shared_budget_spent` as well as `credential_leak` (ADR-0105). A Shift closes with close reason `shared_budget_spent` at `needs_human` before the stuck check and before `retryFailedRuns`, for writers and readers and for configured and synthesized plans. A Shift-less Run parks through `store.ReportOutcome`. Neither retry budget is consulted, and no pull request is closed: the work is not suspect.
4. **The park reason.** The item's `needs_human` reason says which Role could not call the model in which Round, quotes the Run's summary with whose budget is spent, and says to raise or reset that budget in LiteLLM and then restart the item (ADR-0044). It is audited as `work_item.needs_human`, and the Shift's `shift.closed` row carries `shared_budget_spent`. The operator API and ploeg-mcp serve both.
5. **Unchanged.** The Run's own key over its budget stays `infra_llm` at session start and `agent_error` mid-prompt, as before. A spent budget the gateway did not name stays `infra_llm`. A rate limit, provider quota, rejected key, timeout or provider outage stays an infrastructure failure. A Run with model traffic that the harness ended with an error stays `stuck` with its log tail, which already parks at `needs_human`.

This refines ADR-0019, ADR-0021 and ADR-0105 and replaces none of them.

### Consequences

* Good, because a spent team budget costs one Run, not `MaxInfraFailures` of them.
* Good, because the person reads whose budget is spent in the park reason, the tracker comment and ploeg-mcp.
* Good, because a Run's own cap keeps the retry ADR-0012 and ADR-0021 give it.
* Bad, because a Work Item parked for a spent budget does not resume by itself when the budget resets. A person restarts it. Accepted: a reset date is LiteLLM's configuration, and Ploeg cannot see it.
* Bad, because a harness that prints a budget refusal from a component other than the gateway in its last 4 KiB without model traffic parks wrongly. Accepted: such a Run never started real work, and a person reading the reason sees the quoted text.
* Bad, because a claude-code or openhands Run that hits the team budget after it has made model traffic stays `stuck` with a log-tail reason instead of naming the budget. Its behaviour is right (it parks); only its label is generic.
* Neutral, because a new worker that reports `shared_budget_spent` to an older ploegd is refused by the outcome contract. ploegd and the worker ship in one release.

### Confirmation

`go test ./pkg/litellm/` — `TestGatewayError_SharedBudgetSpentNamesWhoseBudget` (the captured LiteLLM 1.102.4 and 1.105.1 team and user refusals are shared and named; the key refusals are not) and `TestGatewayError_BudgetSpentPerOwner`.
`go test ./pkg/harness/adapters/acp/` — `TestBuild_SessionStartRefusalsFromEveryCapturedLiteLLM` (every captured refusal at session start: a shared budget is `shared_budget_spent` at both HTTP 429 and 422, everything else stays an infrastructure reason) and the `TestBuild_StopReasonMatrix` rows for a spent team budget mid-prompt, a spent key budget mid-prompt and a quoted budget in the agent's own message.
`go test ./pkg/worker/` — `TestResolveOutcome_ASharedBudgetRefusalParksForAPerson` and `TestResolveOutcome_ReadsOnlyTheEndOfTheLogForBudgetWords`.
`go test ./pkg/shiftengine/` — `TestSharedBudgetSpent_ParksThePlannedShiftWithoutSpendingARetry`, `TestSharedBudgetSpent_ParksAReadingRoundInsteadOfRetryingTheReader`, `TestSharedBudgetSpent_ParksAUniformShiftInsteadOfRequeueing` and `TestSharedBudgetSpent_GatewayFailuresAPersonCannotFixStillRetryAsInfrastructure` (a key budget, a rate limit and a provider outage reopen the Round and charge the infrastructure budget once).
`go test ./pkg/store/` — `TestReportOutcome_ASpentSharedBudgetParksAShiftlessItemForAPerson` and `TestReportOutcome_AnInfraLLMFailureStillRequeuesAShiftlessItem`.
`go test ./pkg/work/` — `TestFailureReason_OnlyACredentialLeakAndASharedBudgetParkForHuman`.

## Pros and Cons of the Options

### Keep `infra_llm` and let the infrastructure budget run out

* Good, because it needs no change.
* Bad, because every retry is refused the same way, and the Work Item parks under a close reason that points at the cluster.

### Park every recognised spent budget, the Run's own key included

* Good, because it is one rule.
* Bad, because the Run's own key is minted per Run. The next Run's key has a fresh cap, so parking takes away a retry that can succeed.

## Re-evaluation triggers

* LiteLLM reports when a spent budget resets, in its error or through an API Ploeg already calls: the park could become a hold that resumes at the reset.
* A Work Item parks `shared_budget_spent` wrongly twice in a month.
* LiteLLM changes the wording that names a budget's owner; the captured fixture in `pkg/litellm/testdata/errors.json` then fails.
* A third failure reason is proposed for `ParksForHuman`.

## More Information

* Owner decision 2026-10-17 ("3B").
* [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md) and [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md): the retry budgets this record exempts one more reason from.
* [ADR-0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md): the first reason that parks, and the predicate this record extends.
* [ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md): Runs minted inside a LiteLLM team.
* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md): the Run's own authorization, which its key's cap carries.
