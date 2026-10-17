---
status: accepted
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A credential leak parks the Work Item and closes its pull request

## Context and Problem Statement

The worker's leak guard (`pkg/worker/leakwatch.go`) ends a Run `failed` with failure reason `credential_leak` when the Run's model key, forge token or canary shows up in a forge API request, in the commits it pushed, or in its proposed learnings. The forge proxy then refuses every later forge write of that Run. The pushed commits stay on the branch for forensics.

ploegd treated that Run like any other agent failure. `work.FailureReason.IsInfra` is false for `credential_leak`, so [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md) and [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md) reopened the Round and spent another agent attempt on it. A uniform (synthesized) Shift mapped `failed` to `queued`, and so did a Shift-less Run. Each retry minted fresh credentials for the same agent on the same branch. A pull request the Run opened before the leak stayed open and could be merged.

What does ploegd do with a Run that leaked a credential?

## Decision Drivers

* A leak is a security event, not a transient failure. Retrying cannot make it safe, and only a person can rotate the credential and read what was pushed.
* A retry must never hand new credentials to the agent that leaked the last ones.
* A pull request with leaked content must not be merged by accident while the item waits for a person.
* Parking must not depend on the forge (the blackboard spec: write-back failure never blocks a transition).
* The comment ploegd posts must never carry the leaked value.

## Considered Options

* **Park the Work Item at `needs_human` and close the Run's open pull request**
* Keep retrying under the agent budget (the old behaviour)
* Park the Work Item and leave the pull request open

## Decision Outcome

Chosen option: "**park the Work Item at `needs_human` and close the Run's open pull request**", because it is the only option that both stops new credentials from being minted and stops the leaked change from being merged.

1. **The decision is one predicate.** `work.FailureReason.ParksForHuman` is true for `credential_leak` and for nothing else.
2. **A Shift parks.** In `shiftengine.evaluate`, a completed Round with a Run that ended `failed` with a reason that `ParksForHuman` closes the Shift with close reason `credential_leak` before the stuck check and before `retryFailedRuns`. The item settles at `needs_human` for configured and synthesized plans alike. The Round is not reopened, no budget is spent, and neither the agent budget nor the infra budget is consulted. A shadow reader that leaks parks too: the leak is ploegd's verdict, not the shadow's ([ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md)).
3. **A Shift-less Run parks.** `store.ReportOutcome` moves the item to `needs_human` instead of `queued`. This path does not close pull requests, because it exists only when uniform dispatch is switched off.
4. **The pull request closes.** After the close commits, and only in the call that won it, `closeLeakedPullRequests` asks the Work Item's forge with ploegd's own credential for the open pull requests whose head is the Shift's branch in the target repository, adds the pull request the Shift's Runs recorded if it is still open, closes each one and posts one comment on it. The comment is a fixed text with the marker `<!-- ploeg:credential-leak -->` and no value from the Run. The branch is not deleted. A forge implements this through `provider.PullRequestCloser`; Forgejo, GitHub and GitLab do.
5. **A failed close is recorded, never returned.** Each pull request gets an audit row: `pull_request.closed_for_leak`, or `pull_request.leak_close_failed` with the forge's answer. A failed close is logged at ERROR and posts no comment. The item is already parked.

`ploeg_runs_failed_total{reason="credential_leak"}` and `PloegCredentialLeak` are unchanged. A person who wants another attempt reassigns the item or restarts it ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)).

This refines ADR-0019 and ADR-0021 and replaces neither: every other failure reason is retried under their budgets exactly as before.

### Consequences

* Good, because no Run after a leak gets credentials until a person decides.
* Good, because a leaked change cannot be merged while the item waits.
* Good, because the close reason and the tracker comment name the leak, so the person knows where to look.
* Bad, because the pull request close is best-effort. ADR-0060's outbox is not built, so a forge outage at that moment leaves the pull request open with only an audit row and an ERROR line. Accepted: the item still parks, and `PloegCredentialLeak` already pages a person who reads the runbook's check list.
* Bad, because a reading Round with a leaking reader parks only when its other readers finish, as every Round decision waits for the Round. Their Runs were already authorized; no new Run starts.
* Bad, because a false positive in the leak guard now costs a person's time instead of a retry. Accepted: the guard matches exact credential values, and a false positive is cheaper than a retry after a real leak.

### Confirmation

`go test ./pkg/shiftengine/` —
`TestCredentialLeak_ParksThePlannedShiftInsteadOfReopeningTheRound` (a planned writer's leak closes the Shift `credential_leak` at `needs_human`, leaves nothing to claim and no Lease),
`TestCredentialLeak_ParksAUniformShiftInsteadOfRequeueing`,
`TestCredentialLeak_ClosesTheBranchsOpenPullRequestWithOneComment` (one close, one comment, no leaked value on the pull request or the tracker, one audit row),
`TestCredentialLeak_ParksWhenThePullRequestCannotBeClosed` (the item still parks, no comment, a `pull_request.leak_close_failed` row) and
`TestCredentialLeak_AnAgentErrorStillReopensTheRound` (the ADR-0019 retry is intact).
The first four fail against the code before this record.

`go test ./pkg/store/` — `TestReportOutcome_ACredentialLeakParksAShiftlessItemForAPerson` and `TestReportOutcome_AnAgentFailureStillRequeuesAShiftlessItem`.
`go test ./pkg/work/` — `TestFailureReason_OnlyACredentialLeakParksForHuman`.
`go test ./pkg/provider/forgejo/ ./pkg/provider/github/ ./pkg/provider/gitlab/` — each provider's `OpenPullRequests` and `ClosePullRequest` tests against an `httptest` forge.

## Pros and Cons of the Options

### Keep retrying under the agent budget

* Good, because it needs no change.
* Bad, because each retry mints fresh credentials for the agent that leaked the last ones, up to `MaxRunAttempts` times.
* Bad, because the leaked pull request stays open and mergeable.

### Park the Work Item and leave the pull request open

* Good, because it needs no forge write.
* Bad, because the pull request still looks like ordinary work to a reviewer and can be merged before anyone reads the runbook.

## Re-evaluation triggers

* ADR-0060's outbox for required publications is built: the pull request close should become a required publication there.
* A leak-guard false positive parks a Work Item twice in a month.
* A second failure reason is proposed for `ParksForHuman`.
* A forge refuses `pull_request.leak_close_failed` closes for a reason ploegd could fix, such as a missing permission on its credential.

## More Information

* 2026-10-17 — refined by [ADR-0111](0111-a-spent-litellm-budget-wider-than-the-runs-key-parks-the-work-item.md): `ParksForHuman` is also true for `shared_budget_spent`, which parks without closing pull requests; this record's second-reason trigger was reviewed there. The work-package test is now `TestFailureReason_OnlyACredentialLeakAndASharedBudgetParkForHuman`.
* Owner decision 2026-10-17, VIK-1882.
* [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md) and [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md): the retry budgets this record exempts one reason from.
* [ADR-0013](0013-push-rights-are-minted-per-run.md): per-Run push rights, which a retry would mint again.
* [ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md): the outbox the close should move to.
* [PloegCredentialLeak](../ops/alerts.md#ploegcredentialleak): the alert and its check list.
