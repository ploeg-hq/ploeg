---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A pull request someone else opened is read by a Team on the pull request, and what the reader delivers is the route's instruction

## Context and Problem Statement

Renovate opens pull requests on the repositories Ploeg routes: a version table, merge-confidence badges, sometimes the upstream release notes, and nothing about the repository the bump lands in. On 2026-10-17 the owner decided that every Renovate pull request must get a comment that explains what the update changes, what it touches in the repository, how the repository could use the new version, and that the follow-up work it needs is done as a pull request of its own when it is needed. The owner also said that explaining dependency updates is not Ploeg's responsibility: Ploeg must make it possible, and a Team made for it does it.

Ploeg only acts on pull requests its own Teams opened. `forgeFollowUps` resolves a forge event to the Work Item whose Shift created the branch ([ADR-0019](0019-a-failed-writing-run-reopens-its-round.md)), a reading Run reviews the branch its Team's writer pushed ([ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)), and findings are published on the pull request a Run of the Shift delivered ([ADR-0011](0011-the-pull-request-is-the-blackboard.md)). A pull request nobody in Ploeg wrote has no Work Item, no Shift and no branch owner, so nothing reads it and nothing can comment on it. The forge webhooks ploegd is built to receive are not registered on every repository either: on 2026-10-17 ploegd on one deployment had recorded no forge event for a repository in two days, while six pull requests were opened there.

How does a pull request someone else opened become work a Team reads and reports on, on that pull request, without Ploeg learning what a dependency bot is, without a Team ever writing to a branch it does not own, and without a new kind of Run?

## Decision Drivers

* Ploeg knows work sources and change destinations, never vendors ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)) and names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). What a reader is asked to deliver about a dependency update is the operator's and the Team's knowledge, not Ploeg's.
* A Team is a capability manifest: Roles, harness, model, budget. A Team that only reads must be a valid Team.
* Nothing of Ploeg's writes to a branch a bot owns. Renovate stops rebasing a pull request a stranger pushed to, and a fix Round on it would race the bot.
* A reading Run and its findings on the pull request already exist. The fewest new concepts win.
* Every automatic source of work needs a stop: a cap per target, a cap per pull request, and an off switch ([ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md)).
* Follow-up work an agent proposes is a proposal a person approves, within the Team's created-work limits ([ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md), [ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md)).
* A missed webhook must not mean a pull request is never read.

## Considered Options

* **A registered target routes the open pull requests of listed authors to a Team with an instruction; Ploeg records a Work Item of origin `pull_request` bound to the pull request, runs only the Team's reading Roles on the pull request's own branch, posts the findings on it, and keeps the follow-up work as created Work Items**
* The same, with Ploeg writing the instruction: a built-in "explain a dependency update" brief and skill
* File a tracker ticket per pull request and let assignment dispatch an ordinary Shift, as [ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md) chose for conflicts
* An Ask per pull request ([ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)) whose answer the consumer posts
* A CI job in the repository that calls a model and comments

## Decision Outcome

Chosen option: "**a registered target routes the open pull requests of listed authors to a Team with an instruction, and a Work Item of origin `pull_request` runs the Team's reading Roles on the pull request's own branch**", because it reuses the reading Run, the findings comment, the Shift pool and the created-work limits as they are, adds only the binding between a Work Item and a pull request Ploeg did not open, and keeps every word about dependency updates in the operator's configuration and the Team's skills.

1. **Routes per target.** `targets.<key>.readPullRequests` is a list. Each route names the `authors` whose open pull requests are read, the `team` that reads them, `instructions` in the operator's words on what the reader delivers (empty takes `work.DefaultPullRequestReadInstructions`: what the change does, what it touches here, the risk and what to check, the follow-up work), and optionally `priority`, `maxOpen` (default 5 readings queued or running at once per target) and `maxReviewsPerPullRequest` (default 3). A target without routes reads nothing. Ploeg validates the route at boot and never names an author itself.
2. **A Team that reads.** The Team's plan needs a reading Role that is neither a shadow nor a planner. A Team whose whole plan is one reading Role is valid (`plan.Validate` requires a writer only when `maxFixRounds` is set), so a deployment makes a Team for this work alone, with its own model, cap and skills, and names it in the route. A Team with a writer and a reviewer also serves: its reviewer reads.
3. **Two ways in, one Work Item.** On the review reconcile interval (`PLOEG_REVIEW_RECONCILE_INTERVAL`, default 10 minutes) ploegd lists each routed target's open pull requests through `provider.PullRequestLister` and considers each one by a listed author against the target's base branch; a `pr_opened` or `pr_synchronized` forge event on such a repository considers that pull request at once. Drafts and pull requests from forks are left out. Both paths call `store.EnsurePullRequestReading`, which in one transaction creates the Work Item and the `pull_requests` row that binds it to the pull request, or finds the existing one. A pull request already recorded as a Ploeg Team's own delivery is never read this way.
4. **The Work Item is Ploeg's framing around the route's words.** Provider `ploeg`, origin `pull_request`, external id `pr:<forge>:<owner>/<repo>:<number>`, the target's Work Target, `source_branch` the pull request's head branch and `source_pr` its number. Its title is `Read pull request #<n>: <pull request title>`. Its description, written by `work.PullRequestBrief`, names the pull request, says that Ploeg posts the findings on it, carries the route's instructions under their own heading, and quotes the pull request description as evidence with its HTML comments removed and at most 12 KiB kept. Agents read it below the delivery contract ([ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)).
5. **Only readers run, on the author's branch.** The Shift's branch is the pull request's head branch, so the reader checks it out with the base present, as a reviewer does today. The plan is the Team's plan reduced by `plan.TeamPlan.ReviewOnly`: one Round holding the reading Roles of the last Round that has an acting reader, a pool of their caps bounded by the Team's pool, and no fix loop. The reader is never pre-author. A reader's `request_changes` opens no writer Round; a `check_failed` or a person's `request_changes` on that branch creates no repair Follow-Up and no rework, because the branch owner's origin is `pull_request`.
6. **The findings are the comment.** `publishRound` posts each acting reader's findings on the Shift's pull request; for this origin the pull request is `source_pr`, not a Run's delivery. The reader's delivery contract ("read a pull request") says so, says that no fix Round follows either verdict, and shows `createdWorkItems` beside the findings. The reader gets the embedded skill `ploeg-read-a-pull-request`, which says how to read someone else's change and report on it without saying what about, with `ploeg-verify-before-handoff`. The repository's own skills under `.agents/skills/` and the Team's skills carry the subject matter.
7. **Where it lands.** The Shift closes when its readers finish. The Work Item settles `awaiting_review` when the acting reader approved or gave no verdict, or when the review failed, so a consumer lists the pull request as waiting for a person; it settles `needs_human` when the reader said a merge as it stands would break the repository. The review watch settles it `done` when the pull request merges and `needs_human` when it is closed unmerged, by `source_pr`.
8. **Again when the pull request moves on.** `pull_requests.title` records the title read. A pull request whose title changed, which is how Renovate moves one pull request to a newer version, has its Work Item queued again for a new Shift, up to `maxReviewsPerPullRequest`; an unchanged title does nothing. A pull request whose reading is queued or running is left alone.
9. **Follow-up work is created work.** The reader's `createdWorkItems` are stored as Follow-Ups under the Team's created-work limits ([ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md)): `proposed` until a person approves them, or queued at once when the Team sets `createdWork.autoDispatch`. A queued one runs its Team's full plan on its own branch and opens its own pull request. Nothing is pushed to the author's branch.

### Consequences

* Good, because every pull request a listed author opens on a routed repository gets a report written for its maintainers, for the cost of one reading Round, and the follow-up work reaches the board as proposals a person approves in one step, or as pull requests when the Team allows it.
* Good, because Ploeg learns nothing about dependency bots: the authors, the instruction and the Team are configuration, and the subject-matter skills belong to the repository and the Team. The same route reads a designer's pull requests, or a client's, with other instructions.
* Good, because no new Run kind, budget or publication path exists: the reading Run, the findings comment, the Shift pool and the created-work limits all apply unchanged.
* Good, because a missed webhook delays the reading by one reconcile interval at most, and a repository whose forge sends no webhooks to ploegd still works.
* Bad, because each reading of one pull request is a new comment; the earlier one stays. Accepted for now: a keyed comment edited in place ([ADR-0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md)) is one step away once the first readings have been read.
* Bad, because a push to the pull request that keeps its title, such as a rebase onto a moved base, is not read again, so a report can describe an older head. Accepted: a change of substance changes the title for the authors this was built for.
* Bad, because the reading Run's sandbox reaches only the model gateway and the forge, so a reader reports from the diff, the pull request description and the repository, never from an upstream project directly. What the author put in the description is what it has.
* Bad, because a reading-only Team that is also an assignee would take ordinary work it cannot write; its Shift would end at `needs_human` after one reading Round. A deployment keeps such a Team off the roster.

### Confirmation

* `go test ./pkg/plan/` — `TestReviewOnly_KeepsTheReadersOfTheLastReviewRoundAndNoWriter`, `TestReviewOnly_RefusesAPlanWithoutAnActingReader`, `TestReviewOnly_AnUncappedReaderLeavesThePoolUnmeteredAndTheTeamPoolBounds`.
* `go test ./pkg/work/` — `TestPullRequestBrief_NamesThePullRequestAndCarriesTheInstructions` (Ploeg's framing, the route's instructions, the quoted body, no hidden comments), `TestPullRequestBrief_CutsALongDescriptionAndSaysSo` (and the default instructions), `TestPullRequestReads_ReadListedAuthorsOnly`.
* `go test ./pkg/config/` — `TestPullRequestReads_ComeFromRoutedTargetsOnly` (two routes on one target), `TestPullRequestReads_RefuseAnIncompleteRoute`.
* `go test ./pkg/provider/forgejo/` — `TestListOpenPullRequests_ReadsTheSummaryAndLeavesForksOut`, `TestPullRequestSummary_ReadsOnePullRequest`.
* `go test ./pkg/store/` — `TestEnsurePullRequestReading_CreatesOneQueuedItemBoundToThePullRequest`, `TestEnsurePullRequestReading_ExplainsAgainOnlyWhenTheTitleChangedWithinTheCap`, `TestEnsurePullRequestReading_CapsHowManyExplanationsRunAtOnce`, `TestEnsurePullRequestReading_LeavesAPloegPullRequestAlone`, `TestAReaderOfAnExplanationNeverClaimsAsPreAuthor`.
* `go test ./pkg/shiftengine/` — `TestPullRequestReads_AReaderReportsOnTheBotsBranchAndThePullRequestWaitsForAPerson` (readers only, the pool of their caps, the route's instructions in the Work Item, the findings on the pull request, the shadow left out, one Work Item per pull request, a retitle read again, done on merge), `TestPullRequestReads_AReaderOnlyTeamReadsWithItsOneRole`, `TestPullRequestReads_RequestChangesParksForAPersonAndRunsNoWriter`, `TestPullRequestReads_ConsiderTakesAWebhookOnceAndIgnoresOtherAuthors`, `TestPullRequestReads_ATeamWithoutAReaderReadsNothing`.
* `go test ./pkg/httpapi/` — `TestForgeWebhook_OpenedAndPushedPullRequestsReachTheReader`, `TestFailedCheckOnAReadPullRequestCreatesNoRepair`.
* `go test ./pkg/worker/ ./pkg/harness/skills/` — `TestComposePrompt_ReadsAPullRequestWithoutAReviewLoop`, `TestForPullRequestReaderGetsTheReadingAndVerifyingSkills`.
* Live: one Renovate pull request on a routed repository gets one comment from the route's Team with what the route's instructions ask for, its Work Item shows `awaiting_review` with the pull request in the operator reads, and no Run of Ploeg's pushes to the Renovate branch.

## Pros and Cons of the Options

### Ploeg writes the instruction: a built-in "explain a dependency update" brief and skill

* Good, because a deployment needs only `authors` and `team` to get a useful report on a Renovate pull request.
* Bad, because Ploeg would then know what a dependency update is, which sections its explanation has and what "worth adopting" means, and every other kind of pull request a Team could read would need its own built-in. That is a vendor's and a consumer's knowledge ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)); the owner said so on 2026-10-17. The first version of this record was built this way and reworked the same day.

### File a tracker ticket per pull request

* Good, because the owner sees the work on the board, with the board's priority, and assignment dispatches it as every other ticket.
* Bad, because an assigned ticket runs the Team's full plan: its writer would work a branch of its own, and the pull request to read would still have no Work Item of its own to comment through. The ticket would need the same binding this record adds, plus a tracker capability to create tasks that no provider has.
* Bad, because the board is not where a person decides about a bot's pull request; the pull request is.

### An Ask per pull request

* Good, because an Ask is read-only, capped and metered already.
* Bad, because an Ask is about a Work Item and is one model call with no tools ([ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)): it cannot read the repository to say what the change touches, and it cannot propose follow-up work that becomes a pull request.
* Bad, because the consumer would post the comment with its own credential, outside the blackboard rules.

### A CI job in the repository

* Good, because it needs nothing of Ploeg's.
* Bad, because it spends outside Ploeg's authorization and settlement ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)), repeats per repository, and can open no follow-up work.

## Re-evaluation triggers

* The first ten readings have been read: the comment either becomes a keyed comment edited in place, or the appended comments are kept on purpose.
* GitHub or GitLab repositories are routed, so their providers implement `provider.PullRequestLister`.
* A pull request is pushed to without a title change in a way the reading should have followed.
* A target's `maxOpen` is reached in a month, or the readings of one Team cost more than its deliveries.
* The owner asks for the follow-up work to open pull requests without approval on one route only, which needs a per-route `autoDispatch`.
* A route needs to pick pull requests by label or title rather than by author.

## More Information

* 2026-10-17 — The owner asked that every Renovate pull request get a comment from Ploeg explaining the impact on the application, what the changes do and how they could be used, and a pull request for the follow-up work when needed; then that explaining dependency updates is not Ploeg's responsibility, Ploeg must make it possible, and a separate Team does it. Proposed as built the second way.
* Migration `pkg/store/migrations/0058_pull_request_title.sql`. Configuration in [the configuration reference](../reference/configuration.md) (`config`); operating it, with a dependency-update Team as the worked example, in [Read pull requests others open](../how-to/read-pull-requests-others-open.md).
* `taskspec.v1` gains the origin value `pull_request`.
* Related: [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0011](0011-the-pull-request-is-the-blackboard.md), [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md), [ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md), [ADR-0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md), [ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md), [ADR-0083](0083-a-run-is-told-which-skills-bear-on-its-work-item.md), [ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md), [ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md).
