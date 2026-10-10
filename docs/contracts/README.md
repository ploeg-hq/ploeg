# Published contracts

The versioned, machine-readable seams of Ploeg (backlog #59). Go types in
`pkg/harness` are pinned to these schemas by `pkg/harness/contract_test.go`;
change either side and the test tells you.

| File | Contract |
|---|---|
| [taskspec.v1.schema.json](taskspec.v1.schema.json) | Harness input: what a run knows (work item, repo, branch, trace id, and the brief of the OpenSpec change the Work Item names). Credentials never travel here (R8). |
| [outcomereport.v1.schema.json](outcomereport.v1.schema.json) | Harness output and the body of `POST /api/v1/runs/{token}/outcome`. Stuck requires a reason (R4). The optional `createdWorkItems` carries the Work Items a Run proposes ([ADR-0031](../adrs/0031-runs-create-work-items-held-for-approval-within-limits.md)). The optional `verification` is the worker's own record of the checks it ran on a writing Run; the worker discards any value an agent sent. |
| [checkpoint.v1.schema.json](checkpoint.v1.schema.json) | The durable progress record (shared by TaskSpec, OutcomeReport, and the checkpoint endpoint). |
| [run-api.v1.schema.json](run-api.v1.schema.json) | All run-API message bodies (claim/renew/checkpoint/outcome). |
| [operator-api.v1.schema.json](operator-api.v1.schema.json) | Authenticated, team-scoped read projections of teams, activity summaries, work items, shifts, runs, the Run list, checkpoints and snapshot audit pages, the Run Card of a Work Item ([ADR-0046](../adrs/0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)) and the card list by roster login ([ADR-0054](../adrs/0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md)), all deprecated; the delivery facts of a Work Item and the facts list, the keyed pull request comment and the one-time card export ([ADR-0079](../adrs/0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)). |
| [deploy-api.v1.schema.json](deploy-api.v1.schema.json) | `POST /api/v1/deploys`: a pipeline reports that a commit is live in an environment, with its own bearer token ([ADR-0047](../adrs/0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md), [how-to](../how-to/send-deploys-from-a-pipeline.md)). |
| [tracker-execution.md](tracker-execution.md), [v1 schema](tracker-execution.v1.schema.json) | Scoped source lookup and exclusive operator binding of an existing pristine tracker Work Item. |
| [acp-profiles.md](acp-profiles.md) | The `acp` harness profiles: launch command, gateway wiring, instruction files and approval mapping per agent, and what an image needs to run them. |
| [executor.md](executor.md) | The executor SPI: what any launcher (KEDA, CronJob, agent-sandbox, a human with curl) must and must not do. |

## Versioning policy

- **v1 is frozen.** Additive *optional* fields are allowed (with a schema
  update in the same commit); renames, removals, type changes, or new
  required fields are v2 — a new schema file and explicit adapter
  negotiation, not an edit.
- Consumers must ignore unknown fields (Go's default decoding already does).
- The outcome enum is owned by `pkg/work/types.go`; the schema mirrors it.
  `usage` carries tokens, cost and sessionId (backlog #66/#70), and since
  [ADR-0045](../adrs/0045-keep-run-usage-and-merge-facts.md) the optional
  cache, turn, duration, tool-call, context and per-model figures. A harness
  leaves out any figure it did not measure; it never sends a default zero.
- The Run Card (`GET /api/v1/operator/work-items/{id}/card`) is a new
  response, not a change to an existing one. Its `schemaVersion` is the
  number `1`, where the older operator
  responses send the string `"1.0"`. It follows the same rule: a fact nobody
  reported is absent, never zero.
- Since [ADR-0047](../adrs/0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)
  the card always carries `deployments` (earliest first deploy per
  environment) and `release` (an object or null), and each play carries its
  own `deployments`. They are required because Ploeg always sends them, empty
  or null when no deploy was reported. `release.source` is `merge` while the
  repository has never reported a deploy of its release environment.
- Since [ADR-0050](../adrs/0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)
  the card's `grade` is an object or null, computed under the formula version
  it names, with the inputs it used. Since
  [ADR-0051](../adrs/0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md)
  the card may carry `gates` (an object or null) and `evolved`, and the roster
  may name `qa` and `acceptor`. Both new fields are optional, so a consumer
  of an older Ploeg sees them absent.
- Since [ADR-0052](../adrs/0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md)
  the card's `condition` is a `cardCondition` object or null, the grade's
  `formula` may be `2026.2`, and the roster may name `cosigner`. A consumer
  that only knew `condition: null` must accept the object. The attribution
  endpoints are new responses: `crackCandidatesResponse`, `cracksResponse`
  and `crackResponse`, all with `schemaVersion` `"1.0"`. Their requests
  refuse unknown fields.
- Since [ADR-0053](../adrs/0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md)
  the card may carry `set`, a `cardSet` object. It is optional and absent
  when the Work Item belongs to no epic that counts.
- Since [ADR-0054](../adrs/0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md)
  the card list (`GET /api/v1/operator/cards?member=<login>`) is a new
  response, `cardsResponse`: `schemaVersion` the number `1`, as the single
  card sends, `cards` in the card's own shape, and `nextBefore`, an opaque
  cursor or null. A page can hold fewer cards than its `limit`, or none,
  while `nextBefore` is set.
- Since [ADR-0056](../adrs/0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md)
  the card's `rarity` is a `cardRarity` object or null, where it was always
  null before. A consumer that only knew `rarity: null` must accept the
  object; the key itself stays required. The object names its `formula`,
  the `predicted` and `revealed` tiers, the `tier` to show, the `score`,
  `percentile` and `cohort` it was ranked in, its `inputs` and
  `revealedAt`. A revealed tier never changes once sent.
- Since [ADR-0057](../adrs/0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md)
  the card read by the card endpoints may carry `flow`, a `cardFlow` object:
  time in every tracker status, per gate and per kind, lead, cycle and start
  time, flow efficiency, blocked time, reopens, queue and agent time, the
  time from the merge to each environment and the time to mend each
  confirmed crack, each in elapsed and in working seconds. It is optional,
  so a consumer of an older Ploeg sees it absent. Inside it an unknown
  figure is `null`, never `0`, and spans that have not ended say
  `running: true` and grow on every read. These figures describe the card
  and the team's process; they never name a person.
- Since [ADR-0058](../adrs/0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md)
  each play may carry `timeline` (`cardPlayTimeline`: opened, ready, first
  feedback, approvals and merge, review rounds, comments, commits and force
  pushes), `ciTiming` (`cardPlayCITiming`: runs, failures, reruns, queue,
  last green, time to green, job minutes and the slowest jobs) and, once
  merged, `shape` (`cardPlayShape`: indentation complexity, counted lines,
  test ratio, documentation files and languages), and the card may carry
  `pipeline` (`cardPipeline`) and `shape` (`cardShape`) summing them up. All
  five are optional and absent until their facts were captured. The existing
  `ci` keeps its meaning, the combined commit status of ADR-0046. Durations
  are integer seconds named `…Seconds`; inside the objects an unknown figure
  is `null`, never `0`. None of them is an input to the grade or the rarity,
  and waiting for a review describes the team, not a person.
- Since [ADR-0061](../adrs/0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md)
  the grade's `formula` may be `2026.3`, and `cardGradeInputs` always
  carries `missing` and `review.reworkRounds`. A consumer of an older Ploeg
  sees them absent. The grade is computed on read under the current formula,
  so a card read after a formula change shows the new version. The grade
  also carries `evidenceComplete`, true when `inputs.missing` is empty, and
  the card image and pull request summary print it next to the formula
  (VIK-1751). A consumer of an older Ploeg sees it absent.
- `GET /api/v1/operator/route-refusals` is a new response,
  `routeRefusalsResponse`: the tracker tasks that routing refused under
  [ADR-0038](../adrs/0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)
  in the last 14 days, which have no Work Item. Each row carries a stable
  `code`, the `reason` sentence and the board's `allowedLabels`. A row
  recorded before codes existed answers `unclassified`. An older Ploeg
  answers 404.
- Since VIK-1698 each play may carry `changedPaths`, the paths the pull
  request changes at its `headSha` (`cardChangedPath`: `path`, `status`
  `added`, `modified`, `deleted`, `renamed` or `copied`, and `previousPath`
  for a rename or copy), at most 300 in the forge's order, with
  `changedPathsTruncated` when it changes more. Both are absent until the
  paths at the current head were read, and stay absent after a failed read;
  an empty array is a read list with no paths. See
  [ADR-0046](../adrs/0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md).
- Since [ADR-0040](../adrs/0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md)
  a Work Item's `pullRequest` carries `number`, `mergeState`, `baseBranch`
  and `checkedAt`. `mergeState` is `clean`, `conflicted` or `unknown`: the
  review poll reads the forge's `mergeable` flag for every `awaiting_review`
  pull request, and `conflicted` needs two polls in a row at one head SHA
  that report a conflict. A new head resets it to `unknown`, a forge that
  does not report mergeability (GitLab today) leaves it `unknown`, and a
  failed forge read leaves the state and `checkedAt` as they were. Entering
  or leaving `conflicted` writes a `pull_request.conflicted` or
  `pull_request.mergeable` audit row with the Work Item's id, so the item's
  events show it. The merge state never changes the Work Item's state. The
  four fields are optional, so a consumer of an older Ploeg sees them
  absent and must read that as `unknown`, never as clean.
- Since [ADR-0079](../adrs/0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)
  the operator API has five new responses, all with `schemaVersion` `"1.0"`.
  `factsResponse` (`GET /api/v1/operator/work-items/{id}/facts`) and
  `factsListResponse` (`GET /api/v1/operator/facts`) carry `workItemFacts`:
  the Work Item, its Shifts and Runs in the Work Item detail's shapes, live
  usage of running Runs, every stored pull request fact with its reviews,
  conversation events, CI runs, files with their raw indentation
  measurements, changed paths, reverts and deploys, the tracker status and
  delivery gate moves, the deploy environments of its repositories, and the
  roster with factual roles. They carry no grade, tier, KPI, status kind or
  working time. `pullRequestCommentResponse` and
  `pullRequestCommentDeleteResponse` answer the keyed comment, whose request
  `pullRequestCommentRequest` refuses unknown fields. `legacyExportResponse`
  answers `GET /api/v1/operator/card-legacy-export` and is deprecated from
  the start. The card, cards, crack and crack-candidate responses are
  deprecated and are removed in the next minor release.
- `deploy-api.v1` is the body of a pipeline's deploy report. It refuses
  unknown fields, unlike the response contracts, so a misspelled field fails
  the pipeline step instead of being dropped.

## OpenSpec Work Items

A Work Item names an OpenSpec change with a line `openspec: <change-id>` in its
description. The key is case-insensitive, the id may be in backticks, and
tracker HTML is ignored, so the line works from Vikunja, ClickUp, GitLab and
operator admission alike. The id must be kebab-case. A malformed line, two
different ids, or a change the worker cannot find in the clone stops the Run
as `stuck` before the harness starts.

The worker looks for `openspec/changes/<change-id>` at the repository root and
in nested directories (for example `services/api/openspec`), without
following symbolic links. It fills the Task Spec's `openSpec` field with a
brief: from `openspec instructions apply --change <id> --json` when an
`openspec` executable is on the worker's PATH, otherwise from the change's
`proposal.md`, `design.md` and `tasks.md`. The prompt ranks the brief below the
delivery contract.

After a writing Run opens or updates a pull request, and after a reading Run
on the branch under review, the worker checks out the pushed branch and runs
`openspec validate <id> --type change --strict --json --no-interactive`
itself:

| Run | Gate passes | Gate fails | Gate cannot run |
|---|---|---|---|
| Writing | Outcome kept; the summary says it passed | `stuck` with the validation output and the pull request link | `stuck` with the reason |
| Reading | Verdict kept | Verdict `request_changes`; the output is put ahead of the findings | `stuck` with the reason |

The CLI runs with `OPENSPEC_TELEMETRY=0`, `DO_NOT_TRACK=1`, a scratch `HOME`
and no forge token or model key. The worker never downloads it. A harness
image without the CLI can still brief a Run from the files, but every gated
Run then ends at `needs_human`. Baking `@fission-ai/openspec` into the runner
images is proposed, not done.

## Operator read consumers

`PLOEG_OPERATOR_CONSUMERS` is a JSON array of named consumer policies. Each
entry requires `name` and `tokenEnv`; the bearer value is resolved from that
environment reference and retained as a hash. Tokens require at least 32 bytes.
An omitted `teams` grants all teams, an empty array grants none, and a populated
array restricts every resource read. `execute` defaults to false. Consumers
with execution permission may set `maxBudgetUsd` from 0.01 to 10000; the default
is 25. A read credential does not authorize execution commands.

`verify` defaults to false and grants the separate trusted delivery-verifier
operations. Configure that permission on a distinct consumer identity. The
chart forwards `operator.deliveryPolicies` as an array through
`PLOEG_OPERATOR_DELIVERY_POLICIES`; each entry pins `repositoryId`,
`policySha256`, `verifierId`, `minTests` and optional `publicationEnabled`.
Publication remains disabled when that last field is omitted. Policies and
verifier credentials stay in the controller deployment.

The token is supplied as `Authorization: Bearer …`. No consumers configured
means every operator request is refused. Consumer names and policies may live
in configuration; bearer values belong in the deployment's existing secret
provisioning path, supplied through environment references.

Read routes are `GET /api/v1/operator/teams`, `/summary`, `/work-items`,
`/work-items/{id}`, `/runs`, `/runs/{id}` and `/events`. Item/event pages accept `after`
and `limit` (default 50, maximum 200); IDs and cursors are decimal strings.
Item filters are `team`, `state`, `needsHuman=true` and the tracker identity
`provider` plus `externalId`; event filters are `team` and `workItemId`.
`provider` and `externalId` must be given together, otherwise the request
returns 400. Without `team` they match across every team in the consumer's
scope, so a client can ask whether Ploeg holds a given Tracker Item. Detail
embeds the latest 200 records per collection and marks truncated histories.
Unknown costs remain unknown, and `paused` is null because the current team
model has no pause state.

Each team lists its `assignees`: the tracker usernames, lowercased and sorted,
whose assignment routes a Tracker Item to that team. They come from
`teams.<team>.assignees` in the configuration file and from `PLOEG_TEAM_MAP`.
A team no username routes to has an empty list.
Each team also lists its `pinnedScopes`: tracker container ids (a Vikunja
project or ClickUp list, pinned by id with `team:` in the configuration file)
whose items run as that team whoever is assigned. A client that assigns tracker
users uses it to know which team will actually receive the work.

Events default to ascending order. `order=desc` returns the newest events
first and pages older with `before=<id>`; its `nextCursor` is the next `before`
value and `lastCursor` is the newest id in the page, so a client can follow new
events with `after`. Combining `after` with `order=desc`, or `before` without
it, returns 400.

`/summary?window=24h|7d|30d` (default `7d`) reports, per team in scope and in
total, current Work Item counts by state, current pending and running Runs and
reserved budget, and the Runs finished and spend settled inside the window.
Settled spend is the sum of gateway reconciliation deltas plus the reported
cost of Runs without a gateway account. `lastActivityAt` is the team's latest
bound audit event. Teams appear when they have a Work Item or are registered.

`/runs` lists Runs newest first by id. It filters on `team`, `state` and
`outcome`, takes `limit` (1 to 200, default 50) and pages with
`before=<runId>`, returning `nextBefore`. `externalRef` is the tracker
reference agents put in commit trailers, empty for manual Operator Executions.
`settledUsd` is the reconciled gateway spend, or the reported cost of a
finished Run without a gateway account, and null otherwise. It is also null
when the account settled from spend logs with no entries for a minted key:
that cost is unknown, not zero, until an entry arrives. Run detail reports
the same case as `costStatus: unknown`. Both the Run list and Run detail
carry `settledAt` (the account's first settlement), `costFinalAt` (the end of
its correction window, or `settledAt` when it has none) and `costFinal`. A
settled cost with `costFinal: false` is provisional: the controller still
reads the gateway's spend logs again and may raise it. A Run without a
gateway account has `costFinal: true` once it has finished. Summary and Run
list timestamps are UTC.

Audit pages explicitly say `consistency: snapshot`: sequence allocation is
not transaction commit order, so clients must not treat this as a lossless
changefeed. Unbound forge events are excluded. Read projections omit worker
and gateway credentials and native harness session IDs; audit details are
allowlisted, recognizable credential patterns are redacted, and URLs omit
queries/fragments. Team authorization follows the work item's current team.
Each request has a bounded execution time and response size.

The Helm chart's `operator.consumers` entries resolve `tokenSecret: {name, key}`
into controller-only `secretKeyRef` environment variables. The generated
consumer policy references those variables. No value is embedded in Helm
configuration or inherited by a worker. See the [render fixture](../../ops/helm/ploeg/ci/operator-values.yaml)
and the [controller-isolation assertion](../../pkg/config/operator_chart_test.go).

`POST /api/v1/operator/work-items/{id}/cancel` withdraws tracker-originated
work, with the same effect as unassigning its Tracker Item. It needs `execute`
permission, `X-Ploeg-Actor` and team scope, and it takes no body. The live
Shift closes with reason `withdrawn_by_operator`, pending Runs are cancelled,
running Runs are finished and their model keys blocked, and the Work Item
becomes `withdrawn`. A repeated call returns `withdrawn: false`. An item bound
to an Operator Execution returns 409; cancel the execution instead.

`POST /api/v1/operator/work-items/{id}/requeue` restarts stopped work
([ADR-0044](../adrs/0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)).
It needs `execute` permission, `X-Ploeg-Actor` and team scope. Without a body
it restarts from Round 1. A body `{fromRound, poolUsd, note, commandId,
expectedState}` must carry `commandId`; a repeated `commandId` returns the
first result with `replayed: true`, and the same `commandId` with a different
body returns 409 `command_conflict`. Only `needs_human`, `stale` and
`awaiting_review` items with no open Shift restart (409 `not_requeueable`);
an `expectedState` the item is no longer in returns 409 `state_changed`, and
an operator-owned item 409 `operator_owned`. `fromRound` must be a Round of
the Team's current plan (422 `round_not_in_plan`); when the Rounds before it
include a writing Round, an earlier Shift must have recorded `pr_opened` or
`pr_updated` (422 `nothing_to_review`). `poolUsd` defaults to the plan's pool
and may not exceed the consumer's `maxBudgetUsd` (400 `pool_above_limit`). The
item is queued with its attempt counters reset, a new Shift opens on the same
branch at `fromRound`, its Runs are briefed with the previous Shift's close
reason, failed Runs and the `note`, the request is audited as
`work_item.requeued`, and the tracker task and pull request are told who
restarted it.

Execution requests additionally require `X-Ploeg-Actor`, the stable session
owner identity asserted by the authenticated consumer. Commands may carry
`X-Ploeg-Acting-User` when an authorized administrator acts for that owner.
Ploeg validates a single bounded identity, binds it to command replay and
records it as the event actor; the execution owner remains unchanged. The
header defaults to the owner, including delegated internal executor reports.
The server overrides any `authenticatedBy` supplied in a command body.
Consumers must derive both identities from authenticated session authorization,
never forward an untrusted browser header.

Managed workers use the [worker control contract](worker-control.md); deployment and conservative accounting recovery are documented in [managed worker operations](../ops/managed-workers.md).

The [operator delivery contract](operator-delivery.md) and [versioned schema](operator-delivery.schema.json) govern canonical candidates, trusted verification, human approval and durable publication barriers for a completed Operator Execution.
