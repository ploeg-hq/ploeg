---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A dependency-update pull request is explained on the pull request by a Team's reading Roles, and its follow-up work becomes created Work Items

## Context and Problem Statement

Renovate opens pull requests on the repositories Ploeg routes: a version table, merge-confidence badges, sometimes the upstream release notes, and nothing about the repository the bump lands in. On 2026-10-17 the owner decided that every Renovate pull request must get a comment from Ploeg that explains what the update changes, what it touches in the repository, how the repository could use the new version, and that the follow-up work it needs or makes possible is done as a pull request of its own when it is needed.

Ploeg only acts on pull requests its own Teams opened. `forgeFollowUps` resolves a forge event to the Work Item whose Shift created the branch ([ADR-0019](0019-a-failed-writing-run-reopens-its-round.md)), a reading Run reviews the branch its Team's writer pushed ([ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)), and findings are published on the pull request a Run of the Shift delivered ([ADR-0011](0011-the-pull-request-is-the-blackboard.md)). A pull request nobody in Ploeg wrote has no Work Item, no Shift and no branch owner, so nothing reads it and nothing can comment on it. The forge webhooks ploegd is built to receive are not registered on every repository either: on 2026-10-17 ploegd on the homelab had recorded no forge event for `webgrip/unfold` in two days, while six pull requests were opened there.

How does a pull request someone else opened become work a Team reads and explains on that pull request, without a Team ever writing to a branch it does not own, and without a new kind of Run?

## Decision Drivers

* The owner's words: a comment on every Renovate pull request with the impact, what the changes do, how to leverage them, and a pull request for the follow-up work when one is needed.
* Nothing of Ploeg's writes to a branch a bot owns. Renovate stops rebasing a pull request a stranger pushed to, and a fix Round on it would race the bot.
* A reading Run and its findings on the pull request already exist. The fewest new concepts win.
* Every automatic source of work needs a stop: a cap per target, a cap per pull request, and an off switch ([ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md)).
* Follow-up work an agent proposes is a proposal a person approves, within the Team's created-work limits ([ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md), [ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md)).
* A missed webhook must not mean a pull request is never explained.

## Considered Options

* **A registered target opts in per author; Ploeg records a Work Item of origin `pull_request` bound to the pull request, runs only the Team's reading Roles on the pull request's own branch, posts the findings on it, and keeps the follow-up work as created Work Items**
* File a tracker ticket per pull request and let assignment dispatch an ordinary Shift, as [ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md) chose for conflicts
* An Ask per pull request ([ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)) whose answer the consumer posts
* A CI job in the repository that calls a model and comments

## Decision Outcome

Chosen option: "**a registered target opts in per author, and a Work Item of origin `pull_request` runs the Team's reading Roles on the pull request's own branch**", because it reuses the reading Run, the findings comment, the Shift pool and the created-work limits as they are, and adds only the binding between a Work Item and a pull request Ploeg did not open.

1. **Opt-in per target.** `targets.<key>.dependencyUpdates` names the `authors` whose open pull requests are explained, the `team` that explains them, and optionally `priority`, `maxOpen` (default 5 explanations queued or running at once) and `maxReviewsPerPullRequest` (default 3). A target without the key explains nothing. The Team's plan must have a reading Role that is neither a shadow nor a planner; ploegd logs an error and creates nothing otherwise.
2. **Two ways in, one Work Item.** On the review reconcile interval (`PLOEG_REVIEW_RECONCILE_INTERVAL`, default 10 minutes) ploegd lists each opted-in target's open pull requests through `provider.PullRequestLister` and considers each one by a listed author against the target's base branch; a `pr_opened` or `pr_synchronized` forge event on such a repository considers that pull request at once. Drafts and pull requests from forks are left out. Both paths call `store.EnsurePullRequestExplanation`, which in one transaction creates the Work Item and the `pull_requests` row that binds it to the pull request, or finds the existing one. A pull request already recorded as a Ploeg Team's own delivery is never explained.
3. **The Work Item is Ploeg's text.** Provider `ploeg`, origin `pull_request`, external id `pr:<forge>:<owner>/<repo>:<number>`, the target's Work Target, `source_branch` the pull request's head branch and `source_pr` its number. Its title is `Explain dependency update #<n>: <pull request title>`. Its description, written by `work.PullRequestBrief`, names the pull request and the five sections the explanation must have (what changes, what it touches here, risk and what to check before merging, how to use the new version, follow-up work), and quotes the pull request description as evidence with its HTML comments removed and at most 12 KiB kept. Agents read it below the delivery contract ([ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)).
4. **Only readers run, on the bot's branch.** The Shift's branch is the pull request's head branch, so the reader checks it out with the base present, as a reviewer does today. The plan is the Team's plan reduced by `plan.TeamPlan.ReviewOnly`: one Round holding the reading Roles of the last Round that has an acting reader, a pool of their caps bounded by the Team's pool, and no fix loop. The reader is never pre-author. A reader's `request_changes` opens no writer Round; a `check_failed` or a person's `request_changes` on that branch creates no repair Follow-Up and no rework, because the branch owner's origin is `pull_request`.
5. **The findings are the comment.** `publishRound` posts each acting reader's findings on the Shift's pull request; for this origin the pull request is `source_pr`, not a Run's delivery. The reader's own delivery contract says so, says that no fix Round follows either verdict, and shows `createdWorkItems` beside the findings. The reader gets the embedded skill `ploeg-explain-a-dependency-update` with `ploeg-verify-before-handoff`.
6. **Where it lands.** The Shift closes when its readers finish. The Work Item settles `awaiting_review` when the acting reader approved or gave no verdict, or when the review failed, so a consumer lists the pull request as waiting for a person; it settles `needs_human` when the reader said a merge as it stands would break the repository. The review watch settles it `done` when the pull request merges and `needs_human` when it is closed unmerged, by `source_pr`.
7. **Again when the pull request moves on.** `pull_requests.title` records the title explained. A pull request whose title changed, which is how Renovate moves one pull request to a newer version, has its Work Item queued again for a new Shift, up to `maxReviewsPerPullRequest`; an unchanged title does nothing. A pull request whose explanation is queued or running is left alone.
8. **Follow-up work is created work.** The reader's `createdWorkItems` are stored as Follow-Ups under the Team's created-work limits ([ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md)): `proposed` until a person approves them, or queued at once when the Team sets `createdWork.autoDispatch`. A queued one runs the Team's full plan on its own branch and opens its own pull request. Nothing is pushed to the bot's branch.

### Consequences

* Good, because every pull request a listed bot opens on an opted-in repository gets an explanation written for its maintainers, for the cost of one reading Round, and the follow-up work reaches the board as proposals a person can approve in one step, or as pull requests when the Team allows it.
* Good, because no new Run kind, budget or publication path exists: the reading Run, the findings comment, the Shift pool and the created-work limits all apply unchanged.
* Good, because a missed webhook delays the explanation by one reconcile interval at most, and a repository whose forge sends no webhooks to ploegd still works.
* Bad, because each explanation of one pull request is a new comment; the earlier one stays. Accepted for now: Renovate's rebases keep the thread short, and a keyed comment edited in place ([ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md)) is one step away once the first explanations have been read.
* Bad, because a push to the pull request that keeps its title, such as a rebase onto a moved base, is not explained again, so an explanation can describe an older head. Accepted: the dependency set, which is what the explanation is about, changes the title.
* Bad, because the reading Run's sandbox reaches only the model gateway and the forge, so a reader explains from the diff, the pull request description and the repository, never from the upstream project directly. The release notes the bot quotes are what it has; a bot configured without them gives the reader only version numbers.
* Bad, because the Team's plan decides the reader's model, and a reviewer tuned to verify a Team's own change may explain less well than a dedicated reader would. A Team made for explanations is configuration, not code.

### Confirmation

* `go test ./pkg/plan/` — `TestReviewOnly_KeepsTheReadersOfTheLastReviewRoundAndNoWriter`, `TestReviewOnly_RefusesAPlanWithoutAnActingReader`, `TestReviewOnly_AnUncappedReaderLeavesThePoolUnmeteredAndTheTeamPoolBounds`.
* `go test ./pkg/work/` — `TestPullRequestBrief_NamesThePullRequestAndWhatToExplain` (the five sections, the quoted body, no hidden comments), `TestPullRequestBrief_CutsALongDescriptionAndSaysSo`, `TestDependencyUpdates_ExplainsListedAuthorsOnly`.
* `go test ./pkg/config/` — `TestDependencyUpdates_ComeFromOptedInTargetsOnly`, `TestDependencyUpdates_RefuseAnIncompletePolicy`.
* `go test ./pkg/provider/forgejo/` — `TestListOpenPullRequests_ReadsTheSummaryAndLeavesForksOut`, `TestPullRequestSummary_ReadsOnePullRequest`.
* `go test ./pkg/store/` — `TestEnsurePullRequestExplanation_CreatesOneQueuedItemBoundToThePullRequest`, `TestEnsurePullRequestExplanation_ExplainsAgainOnlyWhenTheTitleChangedWithinTheCap`, `TestEnsurePullRequestExplanation_CapsHowManyExplanationsRunAtOnce`, `TestEnsurePullRequestExplanation_LeavesAPloegPullRequestAlone`, `TestAReaderOfAnExplanationNeverClaimsAsPreAuthor`.
* `go test ./pkg/shiftengine/` — `TestDependencyUpdates_AReviewerExplainsOnTheBotsBranchAndThePullRequestWaitsForAPerson` (readers only, the pool of their caps, the findings on the pull request, the shadow left out, one Work Item per pull request, a retitle explained again, done on merge), `TestDependencyUpdates_RequestChangesParksForAPersonAndRunsNoWriter`, `TestDependencyUpdates_ConsiderTakesAWebhookOnceAndIgnoresOtherAuthors`, `TestDependencyUpdates_ATeamWithoutAReaderExplainsNothing`.
* `go test ./pkg/httpapi/` — `TestForgeWebhook_OpenedAndPushedPullRequestsReachTheExplainer`, `TestFailedCheckOnAnExplainedPullRequestCreatesNoRepair`.
* `go test ./pkg/worker/ ./pkg/harness/skills/` — `TestComposePrompt_ExplainsADependencyUpdateWithoutAReviewLoop`, `TestForExplainerGetsTheExplainingAndVerifyingSkills`.
* Live: one Renovate pull request on an opted-in repository gets one comment with the five sections from the Team's reviewer, its Work Item shows `awaiting_review` with the pull request in the operator reads, and no Run of Ploeg's pushes to the Renovate branch.

## Pros and Cons of the Options

### File a tracker ticket per pull request

* Good, because the owner sees the work on the board, with the board's priority, and assignment dispatches it as every other ticket.
* Bad, because an assigned ticket runs the Team's full plan: its writer would work a branch of its own, and the pull request to explain would still have no Work Item of its own to comment through. The ticket would need the same binding this record adds, plus a tracker capability to create tasks that no provider has.
* Bad, because the board is not where a person decides about a Renovate pull request; the pull request is.

### An Ask per pull request

* Good, because an Ask is read-only, capped and metered already.
* Bad, because an Ask is about a Work Item and is one model call with no tools ([ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)): it cannot read the repository to say what the update touches, and it cannot propose follow-up work that becomes a pull request.
* Bad, because the consumer would post the comment with its own credential, outside the blackboard rules.

### A CI job in the repository

* Good, because it needs nothing of Ploeg's.
* Bad, because it spends outside Ploeg's authorization and settlement ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)), repeats per repository, and can open no follow-up work.

## Re-evaluation triggers

* The first ten explanations have been read: the comment either becomes a keyed comment edited in place, or the appended comments are kept on purpose.
* A Team is configured for explanations alone, so its plan has no writer and `plan.Validate` must allow a reader-only plan.
* GitHub or GitLab repositories opt in, so their providers implement `provider.PullRequestLister`.
* A pull request is pushed to without a title change in a way the explanation should have followed.
* A target's `maxOpen` is reached in a month, or the explanations of one Team cost more than its deliveries.
* The owner asks for the follow-up work to open pull requests without approval on one target only, which needs a per-target `autoDispatch`.

## More Information

* 2026-10-17 — The owner asked that every Renovate pull request get a comment from Ploeg explaining the impact on the application, what the changes do and how they could be used, and a pull request for the follow-up work when needed. Proposed as built.
* Migration `pkg/store/migrations/0056_pull_request_title.sql`. Configuration in [the configuration reference](../reference/configuration.md) (`config`); operating it in [Explain dependency updates](../how-to/explain-dependency-updates.md).
* `taskspec.v1` gains the origin value `pull_request`.
* Related: [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0011](0011-the-pull-request-is-the-blackboard.md), [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md), [ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md), [ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md), [ADR-0083](0083-a-run-is-told-which-skills-bear-on-its-work-item.md), [ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md).
