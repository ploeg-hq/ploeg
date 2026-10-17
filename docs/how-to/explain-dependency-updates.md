---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-17
verified_by: "Read pkg/config/config.go, pkg/shiftengine/dependency_updates.go, pkg/store/pull_request_explanations.go, pkg/worker/task.go and cmd/ploegd/main.go; go test ./pkg/shiftengine/ ./pkg/store/ ./pkg/httpapi/ -run DependencyUpdates|PullRequestExplanation"
---

# Explain dependency updates on their pull requests

Use this when a dependency bot such as Renovate opens pull requests on a repository Ploeg routes, and the people who merge them want to know first what the update changes, what it touches in the repository, what to check, what the new version makes possible, and which follow-up work it needs. The decisions behind it are in [ADR-0106](../adrs/0106-a-dependency-update-pull-request-is-explained-on-the-pull-request-by-a-teams-reading-roles.md).

## What happens

| Event | Ploeg does |
| --- | --- |
| A listed author opens a pull request against the target's base branch | Within one review reconcile interval (default 10 minutes), or at once when the forge sends ploegd a `pull_request` webhook, queues one Work Item of origin `pull_request` for the configured Team and opens its Shift |
| The Shift opens | Runs only the reading Roles of the Team's review Round on the pull request's own branch, with the base branch present. No writer runs, and no verdict opens one |
| The reader finishes | Posts its findings as a comment on the pull request: what changes, what it touches here, risk and what to check before merging, how to use the new version, follow-up work. The usage report follows it |
| The reader approves or gives no verdict | The Work Item waits as `awaiting_review` until the pull request merges (`done`) or is closed unmerged (`needs_human`) |
| The reader requests changes | The Work Item parks as `needs_human`: the reader says a merge as it stands would break the repository or its checks. Nobody rewrites the bot's pull request |
| The reader proposes follow-up work | Each proposal is a created Work Item under the Team's created-work limits: `proposed` until a person approves it, or queued at once when the Team sets `createdWork.autoDispatch`. An approved one runs the Team's full plan on its own branch and opens its own pull request |
| The bot changes the pull request's title (a newer version in the same pull request) | Queues the same Work Item again for a new Shift, up to `maxReviewsPerPullRequest` times |
| The bot rebases without changing the title | Nothing |
| A check fails or a person requests changes on the bot's branch | Nothing: forge follow-ups never act on a branch Ploeg only explains |
| The pull request is a draft, comes from a fork, or targets another branch | Nothing |

## 1. Give the Team a reader

The Team named in the policy needs a plan with a reading Role that is neither a `shadow` nor a `planner`. The explanation runs the reading Roles of the plan's last such Round, with a pool of their caps. A bronze or silver plan with `builder` then `reviewer` qualifies; the reviewer's model and cap decide the explanation's quality and cost. A Team whose plan has only a writer explains nothing, and ploegd logs `the team's plan has no reading Role that is not a shadow`.

## 2. Opt the repository in

In the configuration file (`config:` in the chart), on the registered target:

```yaml
targets:
  app:
    repo: webgrip/app
    branch: development
    forge: forgejo
    dependencyUpdates:
      authors: [renovate]           # forge logins, compared without regard to case
      team: silver                  # its reading Roles explain
      priority: 2                   # Work Item priority; default 0
      maxOpen: 5                    # explanations queued or running at once; default 5
      maxReviewsPerPullRequest: 3   # Shifts one pull request may get as its title changes; default 3
```

ploegd refuses to start when `authors` is empty, `team` is missing or names no configured team, or a cap is negative. A target without `dependencyUpdates` explains nothing. The forge provider of the target must list pull requests; Forgejo does.

For the follow-up work to open pull requests without a person approving each proposal, set the Team's `createdWork.autoDispatch: true` ([ADR-0031](../adrs/0031-runs-create-work-items-held-for-approval-within-limits.md)). It applies to everything that Team's Runs create, not only to explanations, within `maxOpen`, `itemBudgetUsd` and `poolUsd`.

## 3. Verify

- ploegd logs `dependency update pull requests are explained` per target at boot, then `dependency update explanation queued` with the pull request number, author and Work Item id when one is queued.
- The Work Item appears in the operator reads with origin `pull_request`, provider `ploeg`, the pull request's branch and `sourcePr`.
- The pull request gets one comment per acting reader of the Round, headed by the Role, and the usage report.
- `audit_log` holds `pull_request_explanation.created`, `.requeued` and `.skipped` rows with the pull request and the cap that applied.

## Cost

One explanation is one reading Round: each acting reader's cap at most, settled from the gateway like any Run, plus a shadow reader's cap when the Round has one. With a reviewer capped at 0.40 USD and one or two bot pull requests a day, the ceiling is about 25 USD a month and the usual spend a fraction of it.

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| No Work Item for a bot pull request | The author is not in `authors`, the pull request is a draft or from a fork, or it targets another branch than the target's | Check the login the forge shows and the base branch; edit the policy |
| `the team's plan has no reading Role that is not a shadow` | The Team's plan has only a writer, or its readers are all shadows or planners | Add a reading Role to the plan, or name another Team |
| `explanation not queued ... open_cap` in the audit log | `maxOpen` explanations are queued or running | Wait, or raise `maxOpen` |
| `explanation not queued ... capped` | The pull request had `maxReviewsPerPullRequest` Shifts | Raise the cap, or read the last explanation; the bot's next pull request starts fresh |
| `explanation not queued ... foreign` | The pull request is a Ploeg Team's own delivery | Nothing: Ploeg's own pull requests are reviewed by their Shift |
| The comment arrived late | No forge webhook reaches ploegd for the repository, so the reconcile interval decides | Register a `pull_request` webhook to `/webhooks/forge/<provider>`, or lower `PLOEG_REVIEW_RECONCILE_INTERVAL` |
| The explanation names no release notes | The bot found none for that package | Give the bot the package's source URL (Renovate: `sourceUrl` in a package rule) so its pull request description carries the notes |
