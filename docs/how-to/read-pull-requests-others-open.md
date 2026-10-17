---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-17
verified_by: "Read pkg/config/config.go, pkg/shiftengine/pull_request_reads.go, pkg/store/pull_request_readings.go, pkg/plan/review_only.go, pkg/worker/task.go and cmd/ploegd/main.go; go test ./pkg/shiftengine/ ./pkg/store/ ./pkg/httpapi/ -run PullRequestRead"
---

# Read pull requests others open

Use this when pull requests nobody in Ploeg wrote should get a Team's report on the pull request itself: a dependency bot's updates, a designer's or a client's pull requests, anything a person merges after reading what a reader found. The decisions behind it are in [ADR-0106](../adrs/0106-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md). The worked example is a Team that explains Renovate's dependency updates.

## What happens

| Event | Ploeg does |
| --- | --- |
| A listed author opens a pull request against the target's base branch | Within one review reconcile interval (default 10 minutes), or at once when the forge sends ploegd a `pull_request` webhook, queues one Work Item of origin `pull_request` for the route's Team and opens its Shift |
| The Shift opens | Runs only the reading Roles of the Team's last review Round on the pull request's own branch, with the base branch present. No writer runs, and no verdict opens one |
| The reader finishes | Posts its findings as a comment on the pull request: whatever the route's `instructions` asked for. The usage report follows it |
| The reader approves or gives no verdict | The Work Item waits as `awaiting_review` until the pull request merges (`done`) or is closed unmerged (`needs_human`) |
| The reader requests changes | The Work Item parks as `needs_human`: the reader says a merge as it stands would break the repository or its checks. Nobody rewrites the author's pull request |
| The reader proposes follow-up work | Each proposal is a created Work Item under the Team's created-work limits: `proposed` until a person approves it, or queued at once when the Team sets `createdWork.autoDispatch`. An approved one runs its Team's full plan on its own branch and opens its own pull request |
| The author changes the pull request's title (a bot moving to a newer version) | Queues the same Work Item again for a new Shift, up to `maxReviewsPerPullRequest` times |
| The author pushes without changing the title | Nothing |
| A check fails or a person requests changes on the author's branch | Nothing: forge follow-ups never act on a branch Ploeg only reads |
| The pull request is a draft, comes from a fork, or targets another branch | Nothing |

## 1. Make the Team that reads

A route names a Team; the Team's plan needs a reading Role that is neither a `shadow` nor a `planner`. Two shapes work:

- **A Team made for reading.** One reading Role, no writer. Its plan is valid as long as it sets no `maxFixRounds`. Give it the model, cap and skills the reading needs; keep it off the roster (`assignees`), because ordinary work assigned to it would end at `needs_human` after one reading Round. In the chart:

  ```yaml
  executor:
    teams:
      - name: dependencies
        model: litellm_proxy/<a model that reads release notes and code well>
        budget: "0.60"
        litellmTeamId: ploeg-crews
        harness: {name: acp, dind: false, acp: {profile: openhands, idleTimeout: 10m, promptTimeout: 60m}}
        plan:
          - roles:
              - {name: explainer, writes: false, cap: "0.60"}
  ```

- **An existing Team with a writer and a reviewer.** Its reviewer reads; a shadow beside it reads too and is recorded, never posted ([ADR-0096](../adrs/0096-a-shadow-reader-records-its-verdict-and-never-acts.md)).

## 2. Route the pull requests

In the configuration file (`config:` in the chart), on the registered target, one route per author set:

```yaml
targets:
  app:
    repo: webgrip/app
    branch: development
    forge: forgejo
    readPullRequests:
      - authors: [renovate]               # forge logins, compared without regard to case
        team: dependencies
        priority: 2                       # Work Item priority; default 0
        maxOpen: 5                        # readings queued or running at once per target; default 5
        maxReviewsPerPullRequest: 3       # Shifts one pull request may get as its title changes; default 3
        instructions: |
          This pull request updates dependencies. Write the explanation a person needs before merging it,
          in plain words for the people who maintain this repository, in this order:
          1. What changes: each dependency with its old and new version, and what the upstream release
             notes in the description say changed. Say so when the notes are missing.
          2. What it touches here: the files the pull request changes and where this repository uses each
             dependency.
          3. Risk and what to check before merging: breaking changes, the pull request's own checks and why
             they fail (a stale lockfile or generated file), what a person should verify by hand.
          4. How to use the new version: features or defaults worth adopting here, or say there are none.
          5. Follow-up work: what this update needs or makes possible as a separate change, each also as a
             created Work Item, saying whether it must land before the merge.
```

`instructions` is the operator's text; Ploeg puts it in the Work Item under its own framing (the pull request, how the findings reach people, and the pull request description quoted as evidence). Without `instructions` the reader gets a default: what the change does, what it touches, the risk and what to check, the follow-up work. Subject-matter knowledge that is the repository's, such as where each pin is used and which command regenerates a stale file, belongs in the repository's `.agents/skills/`; knowledge that is the Team's belongs in the Team's skills.

ploegd refuses to start when a route has no `authors`, no `team`, a `team` that is not configured, or a negative cap. A target without routes reads nothing. The forge provider of the target must list pull requests; Forgejo does.

For the follow-up work to open pull requests without a person approving each proposal, set the Team's `createdWork.autoDispatch: true` ([ADR-0031](../adrs/0031-runs-create-work-items-held-for-approval-within-limits.md)). It applies to everything that Team's Runs create, within `maxOpen`, `itemBudgetUsd` and `poolUsd`.

## 3. Verify

- ploegd logs `pull request reads are enabled` per route at boot, then `pull request reading queued` with the pull request number, author and Work Item id when one is queued.
- The Work Item appears in the operator reads with origin `pull_request`, provider `ploeg`, the pull request's branch and `sourcePr`.
- The pull request gets one comment per acting reader of the Round, headed by the Role, and the usage report.
- `audit_log` holds `pull_request_reading.created`, `.requeued` and `.skipped` rows with the pull request and the cap that applied.

## Cost

One reading is one reading Round: each acting reader's cap at most, settled from the gateway like any Run, plus a shadow's cap when the Round has one. With a reader capped at 0.60 USD and one or two bot pull requests a day, the ceiling is about 36 USD a month and the usual spend a fraction of it.

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| No Work Item for a pull request | The author is not in any route's `authors`, the pull request is a draft or from a fork, or it targets another branch than the target's | Check the login the forge shows and the base branch; edit the route |
| `the team's plan has no reading Role that is not a shadow` | The Team's plan has only a writer, or its readers are all shadows or planners | Add a reading Role to the plan, or name another Team |
| `reading not queued ... open_cap` in the log | `maxOpen` readings are queued or running | Wait, or raise `maxOpen` |
| `reading not queued ... capped` | The pull request had `maxReviewsPerPullRequest` Shifts | Raise the cap, or read the last report; the author's next pull request starts fresh |
| `reading not queued ... foreign` | The pull request is a Ploeg Team's own delivery | Nothing: Ploeg's own pull requests are reviewed by their Shift |
| The comment arrived late | No forge webhook reaches ploegd for the repository, so the reconcile interval decides | Register a `pull_request` webhook to `/webhooks/forge/<provider>`, or lower `PLOEG_REVIEW_RECONCILE_INTERVAL` |
| The report says the description had no release notes | The author put none there | For Renovate, name the package's source (`sourceUrl` in a package rule) so its pull requests carry the notes |
