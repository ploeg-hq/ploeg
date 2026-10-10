---
status: proposed
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run card's pull request, CI and change-shape figures are read from the forge and kept per play

## Context and Problem Statement

On 2026-10-02 the owner asked for the important KPIs on the Run card: how long CI took, how long a change took to get merged and to get its first feedback, how complex it was, and more like it. [ADR-0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md) covers the tracker side (time per status, lead and cycle time, queue and agent time, merge to deploy). This record covers the forge side: the pull request's timeline, its commits, its CI runs and the shape of its change.

Ploeg keeps some of these facts already ([ADR-0045](0045-keep-run-usage-and-merge-facts.md), [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md), [ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md)): `pull_requests` has the merge facts, the diff size and the last combined commit status (final state per check, no timings), `pull_request_reviews` has every review a webhook reported, and `pull_request_files` has the lines of each changed file. Several facts are missing:

* `first_seen_at` is when Ploeg first stored the pull request, not when the forge says it was opened. Nothing records when a draft became ready, who commented, or when pushes and force pushes happened.
* Nothing records when a CI job was queued, started or finished, or whether it was rerun.
* Nothing measures how complex a change is. Rarity lists `complexity` as not collected.

Which forge reads supply them, when they happen, how they stay bounded, what is stored, and how the figures stay facts about the change instead of a score of a person?

## Decision Drivers

* Ploeg sends facts and deterministic derived values only, and an unknown figure is null, never 0 ([ADR-0045](0045-keep-run-usage-and-merge-facts.md), [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)).
* Card facts, not people. Waiting for a review measures the team's response, not the author's work. No figure here may rank a person or feed the grade ([ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)) or the rarity ([ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md)); formula `2026.1` of both stays frozen.
* No forge read when a card is read, and the card list (up to 50 full cards) stays as fast as it is.
* Forge reads happen at the capture points that exist (the pull request fact sync of every recorded webhook event, and the merge), are bounded in count, size and time, and back-fill nothing.
* Ploeg never stores comment or review text, code, or CI logs.
* `operator-api.v1` changes only additively.

## Considered Options

* **Read the forge's activity and CI history at the existing capture points, store minimal events and runs, and store the derived figures per play**
* Derive every figure when the card is read, from the stored events and runs
* Read the forge when the card is read
* Receive CI timings from a pipeline step, like deploys ([ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md))

## Decision Outcome

**Narrowed on 2026-10-10 by [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md).** Ploeg keeps points 1 to 4: the opening facts, the activity events, the CI runs with their jobs and when they are read. Of point 7 it keeps only the diff read at the merge, which now keeps each changed file's indentation (method `indentation/2026.1`) and never the diff. They reach the consumer in the delivery facts. The rest of point 7 and points 5, 6 and 8 to 11 (timeline, CI timing, change shape, card summaries, `pull_requests.kpis`, `pull_requests.shape` and the `cardShape` rules) are gone; migration 0044 drops the two columns. The text below is the decision as first recorded.

Chosen option: "**read the forge at the existing capture points and store the derived figures per play**", because it needs no forge read and no extra query when a card is read, keeps the raw facts small enough to recompute from, and works for every repository without changing its pipelines.

1. **Opening facts.** The pull request read that already happens on every recorded webhook event now also keeps the forge's creation time (`pull_requests.opened_at`), the login that opened it (`author`) and whether it is a draft now (`draft`). Forgejo reports `created_at`, `user` and `draft`; GitLab `created_at`, `author` and `draft` (older GitLab `work_in_progress`).
2. **Activity.** The optional `provider.PullRequestActivityReader` returns who did what and when, never the text:
   * Forgejo: `GET /repos/{o}/{r}/issues/{n}/timeline` gives comments (`comment`), inline review comments (`code`), pushes and force pushes (`pull_push`, whose body names `is_force_push` and the commits; the last one is the new head) and title changes (`change_title`): leaving the WIP prefixes `WIP:` or `[WIP]` is `ready`, gaining one is `draft`. `GET .../pulls/{n}/reviews` gives each review's verdict, time and commit. `GET .../pulls/{n}/commits` gives each commit's author date.
   * GitLab: `GET .../merge_requests/{iid}/notes` gives comments and inline comments (`DiffNote`); its system notes give approvals, requests for changes and draft changes. `.../versions` gives each push with its head, without the pusher. `.../commits` gives author dates. GitLab reports no force push.
   * Bounds: 500 events and 250 commits; a read that reaches them is `truncated`. The events replace the play's rows in `pull_request_events` (kind, actor, time, verdict, head; migration 0033). The commit count, the earliest author date and the force-push count (null on GitLab) go on `pull_requests`.
3. **CI history.** The optional `provider.CIHistoryReader` returns every CI run of the pull request with its jobs, never a log:
   * Forgejo 15 (checked against forgejo.webgrip.dev, `15.0.2+gitea-1.22.0`): `GET /repos/{o}/{r}/actions/runs?ref=refs/pull/{n}/head` lists the pull request's runs across its heads, with commit, workflow file, status, `created`, `started` and `stopped`. Runs on `refs/heads/{branch}` count when their commit is one of the heads Ploeg saw. Forgejo exposes no jobs per run and no attempt number, and its `/actions/tasks` lists every task attempt of the repository without a commit filter (asked without `page`, it returned all 1 771 tasks, 857 KB, whatever the `limit`), so it is not used. Job timings come from the commit status history instead: `GET .../commits/{sha}/statuses` keeps every status change, and Forgejo Actions posts "Blocked by required conditions", "Waiting to run", "Has started running" and a final status per job, and posts them again on a rerun. Queue seconds run from the last "Waiting to run" to "Has started running"; a pending after a final status is the next attempt. A job is credited to the latest run of its commit created before it. A cancelled run that never started reports `started` as 1970-01-01, which is read as unknown.
   * Degraded Forgejo: without the runs endpoint (HTTP 404), or when the pull request has no Actions run (a CI outside Forgejo Actions), each head commit's status history becomes one run (source `statuses`). A check that does not use Forgejo Actions' wording has no queue time and starts at its first pending status; a check that posts only a final status has no start. Two workflows on one commit can have their jobs credited to the newer run.
   * GitLab: `GET .../merge_requests/{iid}/pipelines` lists the pipelines; `GET /projects/:id/pipelines/:pipeline_id/jobs?include_retried=true` gives each job's `started_at`, `finished_at` and `queued_duration`, retried attempts included, numbered by job id.
   * Bounds: 30 runs, the jobs of the newest 10 runs or head commits, 100 jobs per run, at most four status pages per commit. The runs replace the play's rows in `pull_request_ci_runs` (run, commit, workflow, status, times and a JSON array of jobs with name, status, start, end, queued seconds and attempt).
4. **When.** Every recorded webhook event on a Ploeg pull request recomputes the play's figures from what is stored, so a webhook review shows at once. The activity and CI history are read again in the background at a merge or a close, and otherwise at most once per 10 minutes per pull request (`activity_captured_at`), with at most four such reads at once and 30 seconds each. A read that fails is logged and keeps what is stored. The review poller recomputes the figures of the pull requests it records. Nothing is back-filled: a play recorded before this change gets its figures at its next event, and a play merged before it never does.
5. **Timeline** (`play.timeline`, `cardPlayTimeline`):
   * `openedAt`; `readyAt`, equal to `openedAt` unless the first draft change was a `ready` (the play was opened as a draft) or it is a draft now with no change recorded, then the first `ready`, else null; null until an activity read succeeded.
   * `firstFeedbackAt`: the first review, inline review comment or comment by a human who is neither the author nor a forge login Ploeg acts as (`PLOEG_FORGEJO_BOT`, `PLOEG_GITLAB_BOT`). Webhook reviews and the forge's reviews join, and the same reviewer's verdict within two minutes counts once.
   * `firstApprovalAt` and `lastApprovalAt` (the latest approval at or before the merge), `mergedAt`.
   * In seconds: `toFirstFeedbackSeconds` and `toFirstApprovalSeconds` from `readyAt`, never below zero; `approvalToMergeSeconds` from the last approval; `openToMergeSeconds`.
   * `reviewRounds` as the grade counts it: distinct head commits humans reviewed, from the webhook reviews. `comments` counts the comments and inline comments of those humans, null without an activity read; `reviewers` the distinct humans who submitted a review.
   * `responseSeconds`: the median time from a request for changes to the author's next push (a push without a known pusher counts as the author's).
   * `commits`, `firstCommitAt`, `forcePushes` (null on GitLab) and `codingSeconds` from the first author date to `readyAt`.
6. **CI timing** (`play.ciTiming`, `cardPlayCITiming`). `ci` stays the combined status of ADR-0046. `runs` counts runs across every head; `failedRuns` those that ended in failure or error; `reruns` a second run of the same workflow on the same commit plus every job attempt beyond the first (a flakiness signal). `lastGreenSeconds` is the wall time, queue excluded, of the latest successful run on the play's head. `queueSeconds` adds every job attempt's wait for a runner. `timeToGreenSeconds` runs from `readyAt` to the first time every workflow's latest run on one head was green. `minutes` adds every job attempt's duration, to one decimal. `slowest` lists up to three jobs by their longest attempt. `firstPassGreen` says whether the first run on the head that was ready (the last push at or before `readyAt`) succeeded with no job rerun; it is null while that run runs or when it was cancelled or skipped.
7. **Change shape** (`play.shape`, `cardPlayShape`). At the merge, once the changed files are recorded, Ploeg reads the unified diff once (Forgejo `GET .../pulls/{n}.diff`; GitLab's `diffs` assembled into one), at most 1 MiB cut at a line, and keeps only numbers. Every figure leaves out the Work Target's `rarity.sizeExclude` files (lockfiles, generated and vendored code).
   * **Complexity** is indentation complexity, a language-agnostic proxy for McCabe complexity (Hindle, Godfrey and Holt 2008). Each added or removed line counts its logical indentation level: one per leading tab, plus leading spaces divided by the file's indent unit. The unit is the most frequent indentation step between 2 and 8 spaces among the file's added and context lines, else its smallest indentation in that range, else 4. `added` and `removed` sum the levels, `net` is their difference, `maxDepth` is the deepest added line and `hotspots` the three files with the most added complexity. Blank lines, binary files and header lines count nothing. `method` is `indentation/2026.1`; any change to the measure changes it.
   * `files` and `countedLines` (counted as rarity counts them, with `rarity.Matcher.CountedLines`); `testLines` and `testRatio` (test lines over the other counted lines, null when a file's lines are unknown or nothing else changed); `docsTouched`; `languages`, the top three by file extension; `truncated` when the file list or the diff reached its bound.
   * Test and documentation files match the Work Target's `cardShape.testPaths` and `cardShape.docPaths`, in the rarity path syntax, next to its `rarity` rules. A nil list uses the defaults (`playkpi.DefaultTestPaths`, `playkpi.DefaultDocPaths`); an empty list means none. The shape is measured with the rules in force at the merge and kept.
8. **Card summaries.** `card.pipeline` (`cardPipeline`) takes `toFirstFeedbackSeconds` and `firstPassGreen` from the first play that has figures and `openToMergeSeconds` from the latest merged play, adds up `reviewRounds`, `comments` and the CI counts, minutes and queue, and holds medians over the plays. `card.shape` (`cardShape`) adds up the merged plays' shapes, merges hotspots by path and languages by name, keeps the deepest `maxDepth`, and says whether every merged play was measured. Both are absent when no play carries their figures.
9. **Stored, not derived on read.** `pkg/playkpi` derives the timeline and CI figures whenever their facts change and stores them in `pull_requests.kpis`; the shape is stored in `pull_requests.shape`. The card's plays query reads both columns, so a card or the card list costs no extra query. The bot list in force when the figures were computed applies until the next event.
10. **Card facts, not people.** These figures describe a change. They are not inputs to the grade or the rarity, both formulas are unchanged, and rarity keeps `complexity` in `notCollected`. Feeding complexity into rarity would be a new rarity formula version and a new record. The figures are served only where the card is served, under the same team scope and visibility (binders private, team pages for the team, clients team aggregates only), and nothing totals them per person.
11. **Schema.** The new fields are optional: `cardPlay.timeline`, `cardPlay.ciTiming`, `cardPlay.shape`, `card.pipeline` and `card.shape`, with `cardPlayTimeline`, `cardPlayCITiming`, `cardSlowJob`, `cardPlayShape`, `cardComplexity`, `cardLanguage`, `cardPipeline` and `cardShape` in `operator-api.v1.schema.json`. Durations are flat integer seconds named `…Seconds`, as in ADR-0057's `queueSeconds`; they have no working time, so ADR-0057's `cardDuration` and `cardSpan` do not fit.

### Consequences

* Good, because the questions the owner asked (how long CI took, how long until the first feedback and the merge, how complex the change was) are answered on the card from facts the forge reported, for both forges.
* Good, because a card read and the card list cost nothing more: no forge read and no extra query.
* Good, because only who, what and when are kept: no comment text, no code, no logs.
* Good, because indentation complexity needs no parser per language and works on any diff.
* Bad, because every recorded webhook event now costs one store recomputation, and an open pull request costs up to one activity and CI read per 10 minutes. A busy pull request with many heads can take tens of forge calls per read.
* Bad, because Forgejo's job timings come from status descriptions Forgejo Actions writes. A Forgejo that changes that wording loses queue times and starts until the parser follows, and a CI that is not Forgejo Actions has no queue time.
* Bad, because waiting for a first review measures the team, and a reader may still take it as the author's speed. The schema and the run cards page say so.
* Bad, because indentation complexity is a proxy: it rewards nothing and punishes nothing, but deeply nested data files (YAML, JSON) score high. Teams can leave such paths out with `rarity.sizeExclude`.
* Bad, because plays merged before this change have no figures, and a merge found only by polling gets no shape (as it gets no files).

### Confirmation

Since 2026-10-10 the store and HTTP tests that ADR-0080 names check what this record still decides. The card tests listed below were removed with the card.

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/playkpi`: the timeline of a draft that became ready, a pull request never marked draft, a draft never made ready, bots, the author and unknown actors left out of feedback, the median response to a request for changes, figures without an activity read, approvals after the merge; CI runs, failures, reruns (a second run and a second attempt), queue, minutes, slowest jobs, last green, time to green across two workflows, first-pass green with and without a rerun and while running, no runs as a known zero; indentation with tabs, two and four spaces, an aligned continuation, excluded files, a truncated diff, a deleted-only diff, binary files and header-like lines, unit detection; the shape's test ratio, documentation, languages, excluded files, unknown lines and truncation; default and configured test and doc patterns; the card summaries.
* `pkg/provider/forgejo`, `pkg/provider/gitlab`: the activity, CI history and diff readers against `httptest` fakes, including Forgejo's status history of a real pull request, a rerun in place, the statuses fallback without the runs endpoint or without any Actions run, the zero start time, the read bounds, and the opening facts.
* `pkg/store`: migration 0033 applies with nullable columns and no text column; recorded activity and CI derive the card's timeline, CI timing and pipeline; a webhook review refreshes them without a read; the grade and rarity are unchanged; unknown force pushes stay null; the capture window; the shape from the recorded files and the diff, never storing code, with default and configured rules; counted lines through `rarity.Matcher.CountedLines`.
* `pkg/httpapi`: a merge webhook captures the timeline, CI timing and shape from a fake Forgejo and the card validates against `operator-api.v1`; the activity is read at most once per window while a review still refreshes the figures; a failed activity read keeps the webhook reviews and still reads CI.
* `pkg/config`: `cardShape` loads per Work Target and refuses bad and duplicate patterns, unknown keys, rules without a repository and conflicting rules.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Derive every figure when the card is read

* Good, because a changed bot list or definition applies to every card at once.
* Bad, because the card list would decode and derive up to 50 plays' events and runs per card on every request, and it would need two more queries per card.

### Read the forge when the card is read

* Good, because nothing is stored.
* Bad, because a card is read far more often than a pull request changes, Vloer and a client portal would wait on the forge, and ADR-0046 rules out forge reads on read.

### Receive CI timings from a pipeline step

* Good, because a pipeline knows its own timings exactly, whatever CI runs it.
* Bad, because every repository would need a pipeline change before its cards show anything, and the forge already has the timings of Forgejo Actions and GitLab CI.

## More Information

* Abram Hindle, Michael W. Godfrey and Richard C. Holt, "Reading Beside the Lines: Indentation as a Proxy for Complexity Metrics", 16th IEEE International Conference on Program Comprehension (ICPC 2008), pp. 133–142, doi:10.1109/ICPC.2008.13. It found that the variance and maximum of logical indentation correlate with McCabe and Halstead complexity across languages.
* [Run cards](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/concepts/run-cards.md) explains what each figure means and where it misleads; [count tests and docs on Run cards](https://github.com/ploeg-hq/ploeg/blob/2d9595371064e7528fbda4344b6f116215a1b789/docs/how-to/count-tests-and-docs-on-run-cards.md) shows the configuration.
* [ADR-0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md): the tracker-side flow figures; [ADR-0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md): rarity, its size exclusions and counted lines; [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md): the grade's review rounds.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. The facts this record keeps stay and are exposed raw through the delivery facts endpoints; the card figures built on them are removed in the next minor release.
* 2026-10-10 — Narrowed by [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md) to the facts it records.

## Re-evaluation triggers

* Forgejo exposes jobs per run or an attempt number: read them instead of the status history.
* The forge's metrics or rate limits show the capture reads, or more than 1 % of merge captures time out.
* The owner wants complexity in rarity: a new rarity formula version uses `complexity.added` and drops it from `notCollected`.
* A card list request passes 1 s at p95.
* Twenty measured cards show hotspots or test ratios that mislead, such as generated code outside `sizeExclude`.
* GitLab reports force pushes, or a third forge provider is added.
