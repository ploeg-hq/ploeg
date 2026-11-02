---
status: proposed
date: 2026-11-02
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-02-28
---

# Ploeg reads CI through a CI port, gates only on required checks, and handles each failure by its cause

## Context and Problem Statement

[ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) holds a Work Item back from `awaiting_review` until the forge's CI passed on its pull request's head. ADR-0189 ([pull request #408](https://github.com/ploeg-hq/ploeg/pull/408)) (proposed) makes a forge that cannot re-run CI wait for a person to re-run a cancelled check, and lets the review watch follow a CI-parked item to `done` or back to `awaiting_review`. Both read CI through one fact: the combined commit status at a commit.

The code shows what that one fact cannot carry:

* **Every check gates.** `sortForgeCI` in `pkg/shiftengine/forge_ci_gate.go` sorts every check the forge reports on the head. Nothing in Ploeg reads branch protection, so a check that the forge does not require for merging (a coverage report, a lint run someone added as advisory) can open a fix round or park the item.
* **Two causes, one reason.** A failing check is either the writer's (it opens a fix round) or excused (cancelled, or also failing on the base commit). Every excused path ends at `ci_not_passed`, the close reason ADR-0189 also uses for a cancelled run nobody re-ran. A consumer cannot tell a red base branch from a lost runner from a person's cancel.
* **Cause is guessed from text.** On Forgejo a cancel is recognized from the status description "Has been cancelled" (`cancelledStatus` in `pkg/provider/forgejo/forgejo.go`), although the same provider already reads Forgejo Actions runs with a structured `cancelled` status for pull request activity (`PullRequestCI` and `actionStatus` in `pkg/provider/forgejo/activity.go`, [ADR-0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md)). GitLab's provider marks `canceled` cancelled and drops the job's `failure_reason`. Nothing separates a lost runner, an out-of-memory kill or a job that never started from a test failure.
* **The capabilities are scattered.** `CommitStatusReader`, `CIReRunner` with `ErrNoReRun`, and `CIHistoryReader` live in `pkg/provider`. Only GitHub implements `CIReRunner` (it calls `rerun-failed-jobs` on every non-successful workflow run at the commit). Only Forgejo and GitLab implement `CIHistoryReader`. No provider reads a job log, so ADR-0119 rule 4's "tail of its log" was never built.
* **The wait counts queue time.** `forge_ci_gates.waiting_since` starts at the first reading that holds the Shift, so a job that waits an hour for a runner uses up `ci.waitTimeout` before it has run.
* **Consumers see less than the gate.** `forgefacts.Facts` stores each check as a context and a state (`store.PullRequestCheck`); the cancel flag, description, target URL and time are dropped, so `GET /api/v1/operator/work-items/{id}/facts` cannot say why CI is red.

Forgejo 15 (checked against the swagger of Forgejo 15.0.10) serves `GET /repos/{owner}/{repo}/actions/runs`, `GET /repos/{owner}/{repo}/actions/runs/{run_id}`, `GET /repos/{owner}/{repo}/actions/tasks` and `GET /repos/{owner}/{repo}/branch_protections`, whose `status_check_contexts` and `enable_status_check` name the required checks. It has no API call that re-runs, cancels or reads the log of an Actions job.

How does Ploeg read CI on every forge it supports without locking a repository into one CI system, which checks may stop a Work Item, and what does it do with each kind of red?

## Decision Drivers

* **Only what blocks merging blocks review.** The forge's branch protection already says which checks a pull request needs. A second, stricter list inside Ploeg makes `awaiting_review` stricter than merging, for no gain.
* **A fix round costs money and must go to a failure the writer can fix** (ADR-0119 driver). Base breakage, flakes, infrastructure and a person's cancel are not.
* **No lock-in.** A repository whose CI has no adapter, or a CI Ploeg has never heard of, must keep working through the forge's commit statuses.
* **Capabilities are optional and discovered** ([ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md)). A CI system that cannot re-run says so by omission, and the effect of that omission is written down once.
* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). It exposes CI facts and actions on its own contracts; consumers render them.
* **Log text is untrusted** ([ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md)). Anything a job printed reaches a prompt only inside a frame it cannot close.

## Considered Options

* A CI port with an adapter per CI system, optional capabilities, the forge's commit statuses as the fallback, required checks only, and a cause class per failure
* Keep the combined commit status and add causes inside each forge provider
* Let each route list the checks that gate, in Ploeg's configuration

## Decision Outcome

Chosen option: "A CI port with an adapter per CI system, optional capabilities, the forge's commit statuses as the fallback, required checks only, and a cause class per failure", because it is the only option that reads the forge's own idea of "required", tells a person why CI is red, and keeps a repository on any CI working.

### 1. The CI port

CI becomes a port of its own beside the Tracker and Forge ports of ADR-0183. A Work Target's CI adapter is chosen by its route (`ci.adapter`), and defaults to the adapter of its forge's own CI. The port has no required operation beyond its name; everything is a capability, declared by implementing an interface and discovered through `provider.CICapabilities`, as `TrackerCapabilities` does for trackers.

| Capability | Interface | What it does |
| --- | --- | --- |
| `ci_job_reader` | `CIJobReader` | Lists the runs and jobs at a commit: name, the check context each job reports, state, attempt, queued, started and finished times, the job's URL, and the adapter's cause hint (section 3). |
| `required_checks_reader` | `RequiredChecksReader` | Reads the checks branch protection requires for merging into a base branch: `known` (false when there is no protection, or Ploeg may not read it) and the required contexts or patterns. |
| `ci_job_log_reader` | `CIJobLogReader` | Returns the last bytes of one job's log, cut at a line, at most a size Ploeg passes. |
| `ci_re_runner` | `CIReRunner` (exists) | Re-runs the failed and cancelled jobs at a commit, or named jobs. `ErrNoReRun` keeps meaning "this CI cannot". |
| `ci_canceller` | `CICanceller` | Cancels the runs at a commit that are still running. |

The existing `CommitStatusReader` stays on the Forge port and is the **fallback adapter**, `statuses`: a CI without an adapter (Woodpecker, Jenkins, Buildkite, anything that posts commit statuses) still gates through it, with no job timing, log, re-run or cancel. The existing `CIHistoryReader` keeps serving pull request activity and moves to the CI port with the others in ADR-0183's step 5.

Three adapters are built in full; Bitbucket Pipelines is not built now.

| Capability | Forgejo Actions (Forgejo 15) | GitHub Actions | GitLab CI |
| --- | --- | --- | --- |
| `ci_job_reader` | `GET .../actions/runs?ref=` and `.../actions/tasks` for runs and job attempts; the commit status history (`Waiting to run`, `Has started running`) for queue and start times, as `PullRequestCI` reads today | `GET .../actions/runs?head_sha=` and `.../actions/runs/{id}/jobs`, plus check runs and statuses posted outside Actions | `GET .../pipelines?sha=` and `.../pipelines/{id}/jobs`, with each job's `failure_reason` |
| `required_checks_reader` | `GET .../branch_protections`: the rule matching the base branch, `enable_status_check` and its `status_check_contexts` patterns | `GET .../branches/{branch}/protection/required_status_checks` and the rulesets of `GET .../rules/branches/{branch}` | no per-check list: when the project requires a successful pipeline, every job without `allow_failure` is required and an `allow_failure` job is advisory |
| `ci_job_log_reader` | **none**: Forgejo 15 has no log API | `GET .../actions/jobs/{id}/logs` | `GET .../jobs/{id}/trace` |
| `ci_re_runner` | **none**: a person re-runs in the forge (ADR-0189) | `POST .../actions/runs/{id}/rerun-failed-jobs`, `.../actions/jobs/{id}/rerun` | `POST .../jobs/{id}/retry`, `.../pipelines/{id}/retry` |
| `ci_canceller` | **none** | `POST .../actions/runs/{id}/cancel` | `POST .../pipelines/{id}/cancel` |

Each adapter passes a CI conformance suite, `pkg/provider/citest`, built the way ADR-0183 section 4 builds `trackertest`: a fake CI API over a neutral world of runs, jobs, protection and logs, whose cases run only for the capabilities an adapter declares. The `statuses` fallback passes the same suite with no capabilities.

Whether to give Forgejo a re-run and log API upstream is a separate, undecided question. It is a re-evaluation trigger here, not part of this decision.

### 2. Only required checks gate

The gate reads the required checks of the pull request's base branch through `required_checks_reader`.

* **Protection known.** Only checks that branch protection requires can hold, fix or park a Work Item. A required check that has not reported yet is pending. Every other check is **advisory**: it is read, classified and reported, and never stops or fixes a Work Item.
* **No protection, protection not readable, or no `required_checks_reader`.** Every check gates, as today.
* **Ploeg's own statuses** (the keyed statuses of [ADR-0176](0176-an-operator-consumer-sets-a-keyed-commit-status-on-a-pull-requests-head-through-ploegs-forge-credentials.md)) never gate, even when protection requires them, because they are set after review.

ADR-0119 rule 2 then reads: a Work Item reaches `awaiting_review` when every gating check at the pull request's head is `success` (or skipped), or the forge reports none.

### 3. Each red is classified by its cause

The gate gives every gating check that is not green one **cause class**, from the adapter's facts first and the forge's statuses second, and handles it by its class. Caps are per Shift and head commit, kept with the existing wait in `forge_ci_gates`.

| # | Cause class | How it is recognized | Handling | Close reason when it ends at a person |
| --- | --- | --- | --- | --- |
| 1 | `change_failure` | A required job is red, the same check is green on the base commit, and it is still red after the flake re-run (or no re-run is possible) | A fix round under the existing cap and budget (ADR-0017). The writer's briefing names each failing job and, where `ci_job_log_reader` serves it, a log excerpt (section 4). | the existing `fix_round_cap_reached` or `budget_exhausted_before_fix_round`; `ci_change_failed` where no fix round can run (no writing Round, or the item is already in review under ADR-0119 rule 6) |
| 2 | `flaky` | Red, then green after a re-run at the same head | **One** automatic re-run before any fix round. A pass records a flake for the check context on the route (section 6). | none: it proceeds |
| 3 | `base_red` | The same check fails on the base commit | Hold, wait until that check is green on the base branch's newest commit, then update the pull request's branch from its base through the forge (a merge with no conflicts) and re-run CI. Comment once on the pull request and on the tracker item that the base is red and Ploeg is waiting. A conflicting update goes to a person. | `ci_base_red`, after `ci.baseWaitTimeout` or a conflicting update |
| 4 | `infrastructure` | The adapter reports it: runner lost or system failure, killed out of memory, disk full, timed out waiting for a runner, `context canceled` without a person's cancel | Re-run with backoff up to `ci.infraRetries`. Never a fix round, never counted as the writer's failure or against the Work Item's `infra_failures`. On a CI without `ci_re_runner`, wait for a person to re-run, as ADR-0189 does. | `ci_infrastructure` |
| 5 | `cancelled` | Cancelled, and the adapter reports no infrastructure cause | **Pause**: wait for a re-run up to `ci.waitTimeout` (ADR-0189 rule 1 on every forge). Ploeg does not re-run what a person stopped. | `ci_cancelled` |
| 6 | `queued` | A required job has not started | Hold. `ci.waitTimeout` counts from the job's start, not from when it was queued; a job queued longer than `ci.queueTimeout` is `infrastructure`. Queue time is reported per job. Without job timing (the `statuses` fallback), the wait counts from the first reading, as today. | `ci_not_finished` when a started job outruns `ci.waitTimeout` |
| 7 | `advisory` | Red, not required (section 2) | Reported only. | none |
| 8 | `policy` | The check context is listed under the route's `ci.policyChecks` (CVE scans, DCO, secret scans), each as `writer` or `person` | `writer`: as `change_failure`, without the flake re-run. `person`: straight to a person, spending no fix round. | `ci_policy` |
| 9 | none (no CI) | The forge reports no check at the head and protection requires none | Proceed, with the Work Item's CI facts flagged `none`. | none |

Cancelled versus infrastructure is the adapter's call, from the facts its CI gives. GitLab separates `canceled` from `failed` with a `failure_reason`. GitHub gives a job's conclusion (`cancelled`, `timed_out`) and its runner. Forgejo 15 reports neither who cancelled nor why a job failed and has no log API, so on Forgejo a cancel is `cancelled` (the safe reading: Ploeg waits and spends nothing), a job that never started is `queued` and then `infrastructure`, and a lost runner that reports `failure` reads as a test failure. That gap is accepted and is a trigger below.

Caps, all per route under `ci` with defaults: `reRunsPerHead` (3: one flake re-run plus infrastructure retries), `infraRetries` (2, backoff 2 then 10 minutes), `baseWaitTimeout` (4 hours), `queueTimeout` (2 hours). Fix rounds keep the existing cap. `ci_not_passed` stays readable on old Shifts, and no new Shift closes with it.

### 4. The fix round reads the failing job's log

When a fix round opens for `change_failure` or a `writer` policy check, the writer's briefing carries, per failing required job and for at most three jobs, the last 16 KiB of its log, cut at a line, from `ci_job_log_reader`. Ploeg strips ANSI escapes and masks values its secret scanner recognizes. The excerpt goes inside the `forge-ci` frame that ADR-0170 already defines for failed checks. Without `ci_job_log_reader` (Forgejo, the `statuses` fallback) the briefing names the job and its URL, as today.

### 5. What Ploeg exposes

Generic facts and actions on the operator API and ploeg-mcp, under the existing Team scope ([ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)) and attribution rules ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)).

* **CI facts read.** `GET /api/v1/operator/work-items/{id}/ci` returns, for the pull request's current head: the adapter and its capabilities; whether required checks are `known`; per job or check its name, context, `required`, state, cause class, attempt, queued, started and finished times, job URL, `logExcerpt` (the URL of the log route below, only when `ci_job_log_reader` is declared), and whether it is ignored or marked flaky; plus `reRunAvailable`, the gate's current step (`proceed`, `hold`, `fix`, `park`), and the re-run and base-wait counts against their caps. `GET /api/v1/operator/work-items/{id}/ci/jobs/{job}/log` returns the bounded excerpt and needs the `readContent` permission. ploeg-mcp serves the read as `ploeg_get_ci`.
* **Re-run CI.** `POST /api/v1/operator/work-items/{id}/ci/rerun` re-runs the failed and cancelled required jobs at the head, counted against `reRunsPerHead`. Without `ci_re_runner` it answers `409 rerun_unavailable` with the forge URL where a person re-runs. ploeg-mcp: `ploeg_rerun_ci`.
* **Ignore a check for Ploeg's gate.** `PUT /api/v1/operator/work-items/{id}/ci/checks/{check}/ignore` with a `reason`, and `DELETE` to stop ignoring. It needs a proven person under every attribution setting, writes an audit row (`ci_check.ignored`, `ci_check.unignored`) and removes the check from this Work Item's gate for every later head. It only lets the item reach review: the forge still enforces required checks for merging. ploeg-mcp: `ploeg_ignore_check`.
* **Mark a check flaky.** `PUT /api/v1/operator/work-items/{id}/ci/checks/{check}/flaky` with a `reason`, and `DELETE` to clear it, records the check context as flaky on the Work Item's route, with an audit row (`ci_check.marked_flaky`, `ci_check.unmarked_flaky`). A red on a flaky check gets its re-run before any fix round, and on a CI without `ci_re_runner` waits for a person to re-run instead of opening a fix round. ploeg-mcp: `ploeg_mark_check_flaky`.

All three actions need the `execute` permission, and their ploeg-mcp tools ask the person to confirm. `{check}` is the check context, path-escaped.

Ploeg also keeps **one keyed comment** on the pull request (the keyed-comment mechanism of [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md), key `ploeg-ci`), edited in place, naming each gating check that is not green, its cause and what Ploeg is doing about it. It replaces ADR-0189's one-off cancel comment. When an item goes to `needs_human` for a CI reason, the **tracker write-back** sets the `needs_human` status and posts one comment naming the cause class and the checks, through the existing tracker comment and status contracts.

### 6. Build order

1. Ploeg reads jobs, required checks and cause: `CIJobReader` and `RequiredChecksReader` for the three adapters, the cause classes, the required-only gate, the new close reasons and the job-start timeout.
2. The log excerpt in the fix round: `CIJobLogReader` for GitHub and GitLab.
3. The consumer-facing facts and actions of section 5, the keyed comment and the tracker write-back.
4. Flake history per check context, and `CICanceller`.

### Consequences

* Good, because an advisory check can no longer cost a fix round or park a Work Item, and `awaiting_review` asks exactly what merging asks.
* Good, because a red base branch, a lost runner and a person's cancel each cost a wait or a re-run, never a model-paid fix round, and each has its own close reason a person and a consumer can read.
* Good, because a writer fixing a red job sees its log, not only its name.
* Good, because a CI without an adapter still gates through commit statuses, so no repository is tied to one CI.
* Bad, because a base-red wait keeps a Shift open, holding its budget, for up to `ci.baseWaitTimeout`. Accepted: the alternative is a fix round the writer cannot win.
* Bad, because updating a pull request's branch from its base is a forge write by ploegd's own identity that a person did not ask for. It is a merge with no conflicts that leaves the pull request's diff as it was; a conflict goes to a person.
* Bad, because on Forgejo, without a log API, Ploeg cannot tell a lost runner that reports `failure` from a failing test, and can neither re-run nor cancel. Forgejo keeps ADR-0189's wait for a person.
* Bad, because an ignored check lets an item reach review that the forge will not merge until the check passes. The audit row and the facts read say so.
* Neutral, because GitLab has no list of required jobs: on GitLab, required means "not `allow_failure`" when the project requires a successful pipeline.

### Confirmation

* Now: `mise run docs-check` and `go test ./internal/ledger/ ./internal/boundary/`; the record names no consumer.
* As each slice ships, in CI (`mise run verify`):
  * `go test ./pkg/provider/...` runs `pkg/provider/citest` against the Forgejo Actions, GitHub Actions, GitLab CI and `statuses` adapters, and fails an adapter that does not declare a capability the matrix in section 1 lists for it;
  * `go test ./pkg/shiftengine/` covers one test per cause class in `pkg/shiftengine/forge_ci_gate_test.go` (proposed names: `TestAnAdvisoryRedCheckDoesNotGate`, `TestOnlyRequiredChecksGateWhenProtectionIsKnown`, `TestAFlakyCheckIsReRunOnceBeforeAFixRound`, `TestABaseRedCheckWaitsForTheBaseThenUpdatesAndReRuns`, `TestInfrastructureFailureReRunsWithBackoffThenParks`, `TestACancelledCheckPausesForARerun`, `TestTheWaitCountsFromTheJobsStart`, `TestAPersonOnlyPolicyCheckParks`, `TestTheFixRoundBriefingCarriesAFramedLogExcerpt`);
  * `go test ./pkg/httpapi/` covers the facts read, the log route's `readContent` check, the three actions, their audit rows and the `409 rerun_unavailable`;
  * `ploeg_awaiting_review_with_unsuccessful_ci` counts required checks only and stays at 0.

## Pros and Cons of the Options

### Keep the combined commit status and add causes inside each forge provider

* Good, because it adds no port and no configuration.
* Bad, because the combined status has no required flag, no job timing and no log, so the gate still cannot tell advisory from required or queued from running, and a CI that is not the forge's own still gets nothing beyond statuses.

### Let each route list the checks that gate, in Ploeg's configuration

* Good, because it works on every forge, including GitLab's missing per-job list.
* Bad, because it is a second copy of branch protection that drifts from it; a check renamed in a workflow silently stops gating or blocks every Work Item.

## Re-evaluation triggers

* Forgejo's API gains a call to re-run, cancel or read the log of an Actions job, or the owner decides to propose one upstream; then the Forgejo Actions adapter declares the capability and the ADR-0189 wait applies only where it is missing.
* A route needs a CI that is not Forgejo Actions, GitHub Actions or GitLab CI beyond what commit statuses give (Bitbucket Pipelines, Woodpecker, Jenkins).
* More than five Work Items a month close as `ci_base_red`, which calls for gating dispatch on a green base instead.
* More than five Work Items a month close as `ci_infrastructure` on one route, or a fix round is found to have been spent on a failure an adapter misread as `change_failure`.
* A check marked flaky stays marked for more than 30 days, or one route's flake record passes ten flakes a week.
* A forge changes how it reports required checks (GitHub rulesets replacing branch protection, Forgejo changing `status_check_contexts`).

## More Information

* Amends [ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md): rule 2 gates on required checks only, rule 4 gains the log excerpt, rule 5 splits into the cause classes of section 3, and rule 3's timeout counts from a job's start. Builds on ADR-0189 ([pull request #408](https://github.com/ploeg-hq/ploeg/pull/408)): its wait for a person becomes the `cancelled` and no-re-run `infrastructure` handling, its one comment becomes the keyed `ploeg-ci` comment, a cancelled run nobody re-ran closes `ci_cancelled` instead of `ci_not_passed`, and the review watch also follows items closed `ci_cancelled`, `ci_infrastructure`, `ci_base_red` and `ci_policy`. A head Ploeg's own branch update produced counts as the delivered head for ADR-0189 rule 2.
* [ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md) is the port shape and suite this follows; [ADR-0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md) is the CI history Forgejo and GitLab already read; [ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md) frames the log excerpt; [ADR-0176](0176-an-operator-consumer-sets-a-keyed-commit-status-on-a-pull-requests-head-through-ploegs-forge-credentials.md) statuses never gate; [ADR-0188](0188-ploeg-merges-a-work-items-pull-request-at-a-named-head-for-a-proven-person-with-a-merge-identity-no-run-holds.md) leaves merging to branch protection, which the required-only gate now mirrors.
* Code today: the gate is `gateOnForgeCI` and `sortForgeCI` in `pkg/shiftengine/forge_ci_gate.go`, its wait and re-run are `forge_ci_gates` (`pkg/store/migrations/0066_forge_ci_gate.sql`), the CI capabilities are in `pkg/provider/provider.go` and `pkg/provider/activity.go`, and the stored check shape is `forgefacts.Facts` in `pkg/forgefacts/forgefacts.go`.
