# Published contracts

The versioned, machine-readable seams of Ploeg (backlog #59). Go types in
`pkg/harness` are pinned to these schemas by `pkg/harness/contract_test.go`;
change either side and the test tells you.

| File | Contract |
|---|---|
| [taskspec.v1.schema.json](taskspec.v1.schema.json) | Harness input: what a run knows (work item, repo, branch, trace id, and the brief of the OpenSpec change the Work Item names). Credentials never travel here (R8). |
| [outcomereport.v1.schema.json](outcomereport.v1.schema.json) | Harness output and the body of `POST /api/v1/runs/{token}/outcome`. Stuck requires a reason (R4). The optional `createdWorkItems` carries the Work Items a Run proposes ([ADR-0031](../adrs/0031-runs-create-work-items-held-for-approval-within-limits.md)). The optional `verification` is the worker's own record of the checks it ran on a writing Run; the worker discards any value an agent sent. |
| [checkpoint.v1.schema.json](checkpoint.v1.schema.json) | The durable progress record (shared by TaskSpec, OutcomeReport, and the checkpoint endpoint). |
| [run-api.v1.schema.json](run-api.v1.schema.json) | All run-API message bodies (claim/renew/checkpoint/outcome/transcript). |
| [operator-api.v1.schema.json](operator-api.v1.schema.json) | Authenticated, team-scoped read projections of teams, activity summaries, work items, shifts, runs, the Run list, a Run's transcript ([ADR-0090](../adrs/0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)), checkpoints and snapshot audit pages; the delivery facts of a Work Item and the facts list, and the keyed pull request comment ([ADR-0079](../adrs/0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)); Asks and the Ask Allowance ([ADR-0082](../adrs/0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)); the Admission of a manual-origin Work Item ([ADR-0102](../adrs/0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)); the claim of a client-carried Run by the person it acts for ([ADR-0112](../adrs/0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md)). |
| [deploy-api.v1.schema.json](deploy-api.v1.schema.json) | `POST /api/v1/deploys`: a pipeline reports that a commit is live in an environment, with its own bearer token ([ADR-0047](../adrs/0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md), [how-to](../how-to/send-deploys-from-a-pipeline.md)). |
| [tracker-execution.md](tracker-execution.md), [v1 schema](tracker-execution.v1.schema.json) | Scoped source lookup and exclusive operator binding of an existing pristine tracker Work Item. |
| [acp-profiles.md](acp-profiles.md) | The `acp` harness profiles: launch command, gateway wiring, instruction files and approval mapping per agent, and what an image needs to run them. |
| [executor.md](executor.md) | The executor SPI: what any launcher (KEDA, CronJob, agent-sandbox, a human with curl) must and must not do. |

## The contract bundle

Every release also publishes these contracts as one OCI artifact, so a consumer pins a released Ploeg by version and digest instead of checking out its source ([homelab ADR-0069](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0069-one-delivery-contract.md), layer 4). A consumer reads it for its contract tests, its glossary, its decision records and the Ploeg pages it republishes in its own documentation.

| | |
| --- | --- |
| Reference | `ghcr.io/ploeg-hq/ploeg-contract:<version>`, the release tag without its `v` |
| Artifact type | `application/vnd.ploeg.contract.v1+json` |
| Layer | one `application/vnd.ploeg.contract.layer.v1.tar+gzip`, `ploeg-contract-<version>.tar.gz` |
| `ploeg-contract.json` | `schemaVersion` 1, `name`, `version`, `commit` (the tagged commit) and `source` |
| `docs/` | every tracked file under `docs/`: these schemas, the domain model, the ADR ledger and the guides |
| `pkg/`, `cmd/` | the non-test Go files, which define the codes no schema lists yet (Shift close reasons, `needs_human` reasons, audit actions) |
| `files.txt` | every tracked path at the tag, one per line, so a consumer can check a link into a file the bundle leaves out |

[scripts/contract-bundle.sh](../../scripts/contract-bundle.sh) builds the whole OCI artifact from a checkout of the tag, not only its layer: `git archive` with the Git settings that change its output pinned (`tar.umask`, `core.autocrlf`, `core.eol`, `core.attributesFile`), GNU `gzip -n -9`, and `oras push --oci-layout` with annotations taken from the commit, so `org.opencontainers.image.created` is the commit's committer date. A build prints the manifest digest a consumer pins, `ghcr.io/ploeg-hq/ploeg-contract:<version>@sha256:…`, and any checkout of the tag prints the same one. [The contract bundle workflow](../../.github/workflows/contract-bundle.yml) publishes it: the release candidate workflow calls it for each candidate, a successful `Release` run for a stable tag starts it, and a maintainer can dispatch it for an existing tag ([ADR 0117](../adrs/0117-every-release-publishes-a-contract-bundle-for-consumers.md)). Publishing copies the built layout with `oras cp`, so the registry holds exactly the manifest the build printed. It never overwrites a tag that resolves to another manifest digest, and a re-run that finds the same digest succeeds without pushing.

- `bash scripts/contract-bundle.sh build v0.x.y <checkout>` writes the archive and the OCI layout to `dist/contract-bundle/` and prints the digest, without publishing. Pin a consumer to that digest only after `reproduce` confirms it: a digest from a hand-run `oras push` with other annotations names a manifest the registry never holds.
- `bash scripts/contract-bundle.sh reproduce v0.x.y <checkout>` builds and compares the digest with the published tag, anonymously, and prints both manifests when they differ.
- [scripts/test-contract-bundle.sh](../../scripts/test-contract-bundle.sh), part of `mise run verify`, checks the contents, builds a second time under another umask, time zone and hostile Git configuration and requires the same archive and manifest digest, and checks that the build refuses a checkout that is not at the tag, has local changes or has a `.git/info/attributes`.
- The `contract-bundle-reproducible` CI job runs [scripts/reproduce-published-contract-bundle.sh](../../scripts/reproduce-published-contract-bundle.sh) on every pull request and trunk push. It takes the newest `v*` tag that is an ancestor of `HEAD` (`git describe --tags --match 'v*' --abbrev=0`) and runs `reproduce` for it, so it follows each release without anyone moving a pin. A tag whose bundle is not published yet, because its release is still running, gives way to the tag before it; any other registry error, and any digest mismatch, fails the job. A pull request that alters the bundle on purpose fails this job and says so; it changes future digests only and never republishes a tag.

The workflow publishes the bundle only once `proxy.golang.org` lists the tag and serves its `.info` and `.mod`, and `sum.golang.org` records it, so a consumer that sees the bundle can also resolve the Go module at the same version. [scripts/go-module-published.sh](../../scripts/go-module-published.sh) polls every 15 seconds for up to 10 minutes and fails the job, so no bundle and no consumer update follow, when the module never appears. [scripts/test-go-module-published.sh](../../scripts/test-go-module-published.sh), part of `mise run verify`, runs it against a local fake proxy.

## Versioning policy

- **v1 is frozen.** Additive *optional* fields are allowed (with a schema
  update in the same commit); renames, removals, type changes, or new
  required fields are v2 — a new schema file and explicit adapter
  negotiation, not an edit.
- **One exception: a deprecated route may leave v1 whole.** A route whose
  response the schema marked deprecated for at least one release may be
  removed from v1 together with the definitions only it used, when no
  response that remains changes. The removal is a breaking change: it is
  recorded in an ADR, the commit (`feat!:` with a `BREAKING CHANGE:`
  footer) and the release notes, and the
  schema's description says the route now answers 404
  ([ADR-0080](../adrs/0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)).
- Consumers must ignore unknown fields (Go's default decoding already does).
- The outcome enum is owned by `pkg/work/types.go`; the schema mirrors it.
  `usage` carries tokens, cost and sessionId (backlog #66/#70), and since
  [ADR-0045](../adrs/0045-keep-run-usage-and-merge-facts.md) the optional
  cache, turn, duration, tool-call, context and per-model figures. A harness
  leaves out any figure it did not measure; it never sends a default zero.
- Since [ADR-0080](../adrs/0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)
  the Run card routes are gone from v1 under the exception above:
  `GET work-items/{id}/card`, `GET cards`, `GET work-items/{id}/crack-candidates`,
  `GET` and `POST work-items/{id}/cracks`, `POST work-items/{id}/evolved`,
  `POST cracks/{crack}/confirm|dispute|resolve` and
  `GET card-legacy-export` answer 404, and every `card*`, `crack*` and
  `legacy*` definition is removed. They were deprecated since ADR-0079. No
  remaining response changed. The fields they carried (grade, rarity,
  condition, set, flow, timeline, CI timing, shape) are the operator
  consumer's to compute from the delivery facts.
- `GET /api/v1/operator/route-refusals` is a new response,
  `routeRefusalsResponse`: the tracker tasks that routing refused under
  [ADR-0038](../adrs/0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)
  in the last 14 days, which have no Work Item. Each row carries a stable
  `code`, the `reason` sentence and the board's `allowedLabels`. A row
  recorded before codes existed answers `unclassified`. An older Ploeg
  answers 404.
- Since VIK-1698 Ploeg keeps the paths a pull request changes at a head
  (`path`, `status` `added`, `modified`, `deleted`, `renamed` or `copied`,
  and `previousPath` for a rename or copy), at most 300 in the forge's
  order. The delivery facts carry the latest list read as a pull request's
  `changedPaths`, with its `headSha` and `truncated`; it is null until a
  list was read, and an empty array is a read list with no paths.
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
  working time. Four optional, nullable facts were added after the first
  release of these responses: the Work Item's `externalScope` (its tracker
  board or project) and `admittedAt` (its first `work_item.queued` or
  `work_item.approved` audit entry), `checkpoints` that recorded a pull
  request URL, and `runBudgetHolds`, the budget each Run still holds
  whether or not it belongs to a Shift. A consumer of an older Ploeg sees
  them absent. `pullRequestCommentResponse` and
  `pullRequestCommentDeleteResponse` answer the keyed comment, whose request
  `pullRequestCommentRequest` refuses unknown fields. The card legacy export
  and the card responses were removed by ADR-0080.
- Since [ADR-0082](../adrs/0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)
  an operator consumer with execute permission admits an Ask on a Work Item
  in its scope with `POST /api/v1/operator/work-items/{id}/asks`
  (`askAdmitRequest`, answered by `askAdmitResponse`), reads it with `GET
  .../asks/{askId}` (`askResponse`) and finishes it with `POST
  .../asks/{askId}/finish` (`askFinishResponse`). An Ask is a read-only Run
  with the Role `ask` and no Shift; it appears among a Work Item's Runs with
  `shiftId` null. Admission returns a LiteLLM key capped at the per-Ask
  Budget once; a replayed `askId` returns the same Ask and never a second
  key. `allowanceRefusalResponse` (402) and `allowanceResponse` (`GET
  /api/v1/operator/allowances?team=`) report the team's monthly Ask
  Allowance and when it resets. Ploeg keeps only the question's SHA-256.
  A Work Item's facts leave Ask Runs out of `runs`, `liveUsage`,
  `runBudgetHolds` and `activityAt` and list them in the optional `asks`
  (`factsAsk`), so Ask spend is reported apart from delivery spend.
- Since [ADR-0090](../adrs/0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)
  a worker ships the Run's agent session as typed entries with `POST
  /api/v1/runs/{token}/transcript` (`transcriptRequest`, answered by
  `transcriptAppendResponse`), accepted only while the Run is running and
  refused with 404 when ploegd has `PLOEG_RUN_TRANSCRIPTS=off`. An operator
  consumer pages through it with `GET
  /api/v1/operator/runs/{id}/transcript?after=<seq>&limit=<1..500>`
  (`transcriptResponse`): entries in `seq` order, `nextAfter` to pass as the
  next `after`, `hasMore`, `truncated` once the Run reached 5,000 entries or
  2 MiB, and `complete` once the Run ended. An older Ploeg answers 404.
- Since [ADR-0093](../adrs/0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md)
  the working Work Item state is `active`, budget kept back for started work
  is a Hold, and every cost status draws from one Cost Status value set. This
  is a second named exception to the frozen v1, made in two steps. **This
  release:** responses report `active` where they reported `leased`, and add
  `heldUsd` beside `reservedUsd` (Shift, summary spend, facts
  `runBudgetHolds`, and the tracker-execution Shift) and `active` beside
  `leased` (summary counts); the old fields stay, equal to the new ones and
  marked `deprecated` in the schema. The `state` filter and requeue's
  `expectedState` accept `leased` as `active`. `$defs/costStatus` is the one
  value set (`observed`, `provisional`, `settled`, `unknown`): Run detail now
  reports `provisional` and `settled` where it said `observed`, and an Ask
  reports `observed` while its account is unsettled where it said
  `provisional`, and `unknown` before any spend was observed. A consumer
  must read `active` and all four Cost Status values before it runs against
  this release, because those values change at once. **The removal
  release** is a later release, never this one, cut once the operator
  consumers in use read only the new forms. It removes `reservedUsd` and the
  `leased` count from v1, makes `heldUsd` and `active` required, drops
  `leased` from the state enums and answers 400 to it as input; it is a
  `feat!` with a `BREAKING CHANGE:` footer and its own record. Until then a
  consumer may read either name, and must have moved off `reservedUsd`, the
  `leased` count and sending `leased` before it upgrades to the removal
  release.
- Since [ADR-0102](../adrs/0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)
  an operator consumer with execute permission admits a manual-origin Work
  Item with `POST /api/v1/operator/work-items` (`workItemAdmissionRequest`,
  answered by `workItemAdmissionResponse`). The request refuses unknown
  fields; `harness` and `model` (ADR-0098, ADR-0103) extend it, and the
  changes that add `attended` and `autoApprove` will. The operator event feed's `detail` gains the optional
  `requestId` on `operator.work_item.admitted`. An older Ploeg answers 405.
- Since [ADR-0128](../adrs/0128-a-chat-message-is-a-read-only-run-about-no-work-item-paid-from-a-teams-chat-allowance.md)
  an operator consumer with execute permission admits a chat message for a
  team in its scope with `POST /api/v1/operator/teams/{team}/chats`
  (`chatAdmitRequest`, answered by `chatAdmitResponse`), reads it with `GET
  .../chats/{chatId}` (`chatResponse`) and finishes it with `POST
  .../chats/{chatId}/finish` (`chatResponse`). A chat message is a read-only
  Run with the Role `chat`, no Work Item and no Shift, metered under the
  team's `(team, ask)` inference policy and paid from the team's monthly Chat
  Allowance. Admission returns a LiteLLM key capped at the per-chat Budget
  once, with the same replay rules as an Ask. `GET
  /api/v1/operator/allowances?team=&purpose=chat` and the 402
  `allowanceRefusalResponse` of a chat admission carry `chatAllowance`;
  without `purpose`, or with `purpose=ask`, the allowance read is unchanged.
  A chat Run is absent from every Work Item's detail and facts and from the
  Run list; its spend and Hold count in the team summary. In
  `unsettledAccountsResponse`, `workItemId` is null for a chat Run, so a
  consumer that assumed a string must accept null. Ploeg keeps only the
  message's SHA-256. An older Ploeg answers 404.
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
`policySha256`, `verifierId`, `minTests`, optional `publicationEnabled` and
optional `publisherLogin`. Publication remains disabled when
`publicationEnabled` is omitted. `publisherLogin` names the forge login that
must have opened a published pull request. Policies and
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
the budget held for them (`heldUsd`, the Hold), and the Runs finished and spend
settled inside the window. Asks are not Runs of the work: they are left out of
`pending`, `running`, `finished`, `failed` and `stuck`, and `asks` counts the
Asks finished inside the window. Their spend stays in `settledUsd`. A canary
Team's Run that reports stuck stopped on purpose ([ADR-0131](../adrs/0131-a-canary-teams-stuck-run-is-a-deliberate-stop-not-stuck-work.md)):
it is left out of `finished` and `stuck` and counted in `canaryStops`, and
every Run shape carries `canary`.
Settled spend is the sum of gateway reconciliation deltas plus the reported
cost of Runs without a gateway account. `lastActivityAt` is the team's latest
bound audit event. Teams appear when they have a Work Item or are registered.

`/runs` lists Runs newest first by id. It filters on `team`, `state`,
`outcome` and `role`, takes `limit` (1 to 200, default 50) and pages with
`before=<runId>`, returning `nextBefore`. Without `role` it leaves Asks out, so
an operator consumer that asks about a running Work Item every few minutes
does not fill the list; `role=ask` lists only the Asks. `externalRef` is the tracker
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
gateway account has `costFinal: true` once it has finished. Run detail's
`costStatus` says the same in one value: `observed` while the cost can still
move, `provisional` inside the correction window, `settled` once final and
`unknown` without a figure. Summary and Run list timestamps are UTC.

Every Run shape (Run list, Run detail, Work Item detail and facts) carries
what the Run was configured with when ploegd claimed or admitted it
([ADR-0091](../adrs/0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md)):
`configuredModel` from its Role's inference policy, `harness` and
`harnessProfile` from the worker's claim, `planHash` of its Team plan and
`ploegdVersion`. `modelMismatch` is true when a gateway model group other
than `configuredModel` served the Run, such as a fallback group. Settlement
records the served groups (LiteLLM's spend-log `model_group`) as
`usage.modelGroups`, beside the provider model names in `usage.models`; the
Run list returns both. Each field is null when unknown, as it is for every
Run recorded before migration 0050, and `modelMismatch` is also null for a
Run settled before served groups were recorded.

Every Run shape also carries `harnessVersion` and `harnessAgent`
([ADR-0097](../adrs/0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md)).
The worker reads the version from its Harness binary's `--version` once at
startup, within 5 s, keeping the first line up to 128 bytes, and sends it as
the claim request's optional `harnessVersion`. It skips the probe for `exec`,
the custom `acp` profile and a binary it cannot resolve before the clone. The
Outcome Report's optional `harness` object (`name`, `profile`, `version`,
`agent`) is set only by the worker, which replaces any value the agent wrote;
`agent` is the agent's own name and version, such as an ACP agent's
`agentInfo`. ploegd records each fact once (migration 0054): a version from
the claim is kept when the report names another, and the difference is
logged. Recording never refuses or changes a claim or an Outcome. Both read
null when unknown, as for every Run recorded before migration 0054.

Every Run shape may also carry `limits`, the limits that stop the Run's
prompt
([ADR-0114](../adrs/0114-each-run-records-the-limits-that-stop-its-prompt.md)):
`{"promptSeconds": 5400, "idleSeconds": 600, "promptStartedAt": "2026-10-17T08:34:05Z"}`.
The worker sends them in a `prompt_started` checkpoint when the prompt
starts. `promptSeconds` counts from `promptStartedAt`: for `acp` it is the
earlier of the profile's prompt timeout and what is left of the worker's
harness timeout, for every other Harness what is left of the harness
timeout. `idleSeconds` counts from the agent's last activity: the `acp`
idle timeout over protocol activity, or the worker's idle watchdog over
output and model traffic for `openhands`, `claude-code` and `exec`. An
absent limit is no limit, as `idleSeconds` is for `claude-environment`.
ploegd keeps the first report (migration 0059) and records nothing when
`promptStartedAt` lies more than five minutes before the Run started or
after its own clock. `limits` is absent, never null or zero, when unknown:
before the prompt starts, from an older worker, for an Ask or an operator
execution, and for every Run recorded before migration 0059.

The Teams read's Roles carry `harness` (`{name, profile}` from
`PLOEG_TEAM_HARNESSES`, which the chart renders from the same values as each
worker's `PLOEG_HARNESS` and `PLOEG_ACP_PROFILE`), `model` (the Role's
default model) and `observedHarness`: the newest version a Run of that team
and Role recorded, with the harness, profile and agent of that Run and
`recordedAt`. Each is null when ploegd does not know it. The observed
version is what a Run reported, not the configuration, and may belong to
another Harness during a rollout. `harnesses` lists what a person can choose
(ADR-0098): each allowlist entry's `key`, `name`, `profile`,
`qualification`, whether it is the `default`, and `observedVersion`, the
newest version a Run of the Role recorded with it. `pendingByHarness` counts
the Role's pending Runs per stored Harness, with `allowed: false` for a
Harness the allowlist no longer contains. `models` and `defaultModel` are
what the Role's inference policy lists (ADR-0103).

A claim that names `harness` (and `harnessProfile`) takes only a pending Run
that stores that Harness key or none, and the claim response carries the
Run's stored `model`, which the worker calls instead of its `LLM_MODEL`
([ADR-0099](../adrs/0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md)).
The Run's inference account and key are scoped to that model alone, and its
`configuredModel` records it.

Every Run shape also carries `shadow`
([ADR-0096](../adrs/0096-a-shadow-reader-records-its-verdict-and-never-acts.md)):
true for a Run of a shadow Role, whose `verdict` and `findings` are recorded
and never act. A shadow verdict is never the pull request's `agentVerdict`,
its findings are never posted or briefed to a later Run, and its cost counts
like any other Run's. To compare a shadow with the reviewer it ran beside, pair
it with the Runs of the same `shiftId` and `round` whose `writes` and
`shadow` are both false.

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
permission, `X-Ploeg-Actor` and team scope. The live Shift closes with reason
`withdrawn_by_operator`, pending Runs are cancelled, running Runs are finished
and their model keys blocked, and the Work Item becomes `withdrawn`. A
repeated call returns `withdrawn: false`. An item bound to an Operator
Execution returns 409; cancel the execution instead.

Cancel takes no body, or a body `{commandId, expectedShiftId}` that guards
it. `commandId` is required in a body. A cancel whose `commandId` already
withdrew the item answers 200 with `replayed: true` and the Shift it closed,
and changes nothing. `expectedShiftId` is the Shift the caller saw; when the
item's open Shift is another one, or none is open, the cancel answers 409
`shift_changed` and withdraws nothing, so a stale view cannot withdraw a newer
attempt. A cancel without a body behaves as before.

`POST /api/v1/operator/work-items/{id}/notes` leaves an instruction for a
running Work Item ([ADR-0068](../adrs/0068-context-added-while-a-shift-runs-reaches-the-next-run.md):
steering reaches the next Run, not the running one). It needs `execute`
permission, `X-Ploeg-Actor` and team scope; `X-Ploeg-Acting-User`, when
given, is recorded as the author. The body `{commandId, text,
expectedShiftId}` needs `commandId` and a trimmed `text` of 1 to 4096
characters. `expectedShiftId` is optional and, when given, must be the open
Shift (409 `shift_changed`). A new note answers 201; a repeated `commandId`
answers 200 with the first note and `replayed: true`, and the same
`commandId` with another body 409 `command_conflict`. A `done`,
`needs_human`, `awaiting_review`, `stale` or `withdrawn` item answers 409
`work_item_terminal`, an operator-owned one 409 `operator_owned`. Each note is
audited as `operator.note.added`.

The next Run of that Work Item that ploegd hands out, other than an Ask, is
briefed with every unconsumed note as a finding with role `operator note`.
The same transaction marks them consumed by that Run and audits
`operator.note.consumed`, so each note reaches exactly one Run. A note is
consumed when the claim response is built; a claim response lost in transit
loses its notes with it.

### Findings, answers and questions

A reading Run may list its findings in `findingItems` next to the prose
`findings`; a writing Run answers the findings it was briefed with in
`findingAnswers` (`fixed`, `disagree` with a reason, `out_of_scope`,
`deferred`); any Run may ask in `questions` (`to`: `person`, `reviewer` or
`next_run`) and answer a briefed question to a reviewer or the next Run in
`questionAnswers`
([ADR-0121](../adrs/0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md)).
ploegd assigns the ids (`f<runId>-<n>`, `q<runId>-<n>`), keeps only what the
Run's kind may report and drops everything from a shadow except its findings.
A report without these fields behaves as before.

The claim response and the Task Spec carry `reviewFindings`, the findings of
the Shift's last review Round each with the writer's answer or `unanswered`,
and `questions`, the questions that reach the Run with their answers. A
question to a person parks the Work Item at `needs_human` when its Round ends,
with Shift close reason `question_for_person` and the question in the close
message.

On the operator API every Run shape carries `findingItems`, `findingAnswers`,
`questions` and `questionAnswers`, and Work Item detail carries `findings`,
`questions` (with `waitingForPerson`) and `reviewDisagreements`, the review
Rounds where a shadow reviewer's verdict differed from the acting readers'.
`POST /api/v1/operator/work-items/{id}/questions/{questionId}/answer` stores a
person's answer `{commandId, answer}` (1 to 4096 characters): it needs
`execute` permission, `X-Ploeg-Actor` and team scope, answers 201, 200 for a
replayed `commandId`, 404 for a question to a person the Work Item does not
have, 409 `already_answered` and 409 `command_conflict`, and is audited as
`question.answered`. It queues nothing: answer, then resume.

A requeue body with `"resume": true` resumes a Work Item whose last Shift
stopped on a question to a person instead of restarting it: the new Shift
reopens the plan Round that asked on the parked Shift's branch, is briefed
with the parked Shift's earlier Rounds, and gets the parked Shift's unspent
pool (at least that Round's cap) unless `poolUsd` is given. It cannot take
`fromRound` (400), answers 409 `question_unanswered` while a question of that
Round waits and 409 `nothing_to_resume` when nothing is parked, and is
audited as `work_item.requeued` with `resume: true`. Work Item detail's
`resume` names the Round it reopens, `roundCapUsd` (the most that Round can
be authorized), `poolUsd` (the most the resumed Shift can spend in all),
`ready` and `waitingQuestionIds`; it is null when there is nothing to resume.

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

`POST /api/v1/operator/work-items` admits a manual-origin Work Item that
`ploeg-worker` executes ([ADR-0102](../adrs/0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)).
It needs `execute` permission, exactly one `X-Ploeg-Actor` and the Team in
the consumer's scope. The body `{requestId, team, target, title, description,
budgetUsd}` names the Work Target by a registered target key, never a
repository URL; an unknown key answers 422 `unknown_target` and a target
whose readiness check fails 422 `target_not_ready`. A Team without a plan
while uniform dispatch is off answers 422 `team_not_dispatched`. `budgetUsd`
is optional, positive and at most the consumer's `maxBudgetUsd` (400
`budget_above_limit`); the Work Item is allotted it, or `maxBudgetUsd`
without it, and the Shift pool is the lower of the plan's pool and that
allotment, so an unmetered plan is metered for admitted work. The Work Item
has provider `manual`, origin `operator` and is not operator-owned: it is
queued for its Team, its Shift opens from the plan as for tracker work, only
a worker claims its Runs, and its Runs count against `maxRunning`. A new
Admission answers 201 and is audited once as `operator.work_item.admitted`
with `requestId` and no request content. `requestId` is the idempotency key
per consumer: the same body from the same actor answers 200 with
`replayed: true` and the Work Item in its current state, and never queues it
again, opens a Shift or creates a Run, also after it became `done`,
`needs_human` or `withdrawn`. Another body or actor answers 409
`request_conflict`, and so does a `requestId` that names an Operator
Execution of the same consumer. The response carries the Work Item's most
recent `shiftId` and its `poolUsd`, null until a Shift opened; if ploegd
stops before opening it, the sweeper does. Follow the Work Item with `GET
/api/v1/operator/events?workItemId=` and steer it with cancel, requeue and
notes as above. Its cancel calls no tracker, and a withdrawn admitted Work
Item stays withdrawn: admit new work with a new `requestId`.

The body may also name a `harness` and a `model`
([ADR-0098](../adrs/0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md),
[ADR-0103](../adrs/0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md)).
`harness` is a key from the Teams read's `harnesses`: `acp/<profile>` (`acp`
alone is `acp/opencode`) or the Harness name. ploegd refuses it with 422
`harness_not_allowed`, naming each Role's allowlist, when no Role of the
plan allows it, and with 422 `harness_unqualified` when it is allowed but
unqualified, no Role's default, and the Team has not set
`allowUnqualifiedHarnesses`. A plan-less Team's allowlist is its configured
Harness. `model` must be listed by at least one Role's managed inference
policy (422 `model_not_allowed`, naming each Role's models), and a Team
whose Roles have no policy answers 422 `model_requires_managed_inference`.
A refusal creates nothing. Both fields are part of the fingerprint, so a
replay with another Harness or model answers 409 `request_conflict`. The
Work Item keeps the request; whenever its Shift creates a pending Run, the
first Round, a retry, a re-opened Round or a Round after a requeue, the Run
stores the requested Harness if its Role admits it and the requested model
if its Role's policy lists it, else the Role's default for each. The
response's `roles` lists what each Role's Runs store.

Execution requests additionally require `X-Ploeg-Actor`, the stable session
owner identity asserted by the authenticated consumer.

Ploeg's person id for an OpenID Connect identity is `oidc-` followed by the
first 32 lowercase hex characters of the SHA-256 of `<issuer>|<subject>`, the
issuer with one trailing `/` removed (ADR 0108). A deployment may configure a
subject namespace to use in place of the issuer, so ids survive an issuer
change. A consumer that names a person in `X-Ploeg-Actor` uses this form;
`operatorclient.PersonID` computes it, and `ploeg-mcp` derives it from the
verified token.

The execution's owner may read and steer it through any execute-capable
consumer whose Teams include the execution's Team, not only the consumer that
admitted it. From such a consumer, `GET /executions/{id}`, `GET
/executions/{id}/events` and the person commands `message`, `pause`,
`resume`, `cancel`, `take-control` and `handback` are accepted when
`X-Ploeg-Actor` names the owner, with the same revision and generation
conflicts. The executor commands `start`, `heartbeat`, `report` and `close`
answer 403 `executor_command_forbidden` there, and `credential`, `block`,
`spend`, `context` and delivery stay with the admitting consumer (404
elsewhere). Another actor, or a Team outside the consumer's scope, answers 404
exactly as an unknown id does. The `operator.execution.commanded` audit event
names the consumer that sent the command in `detail.attribution.consumer`.

`GET /api/v1/operator/work-items/{id}` lists the item's operator executions
under `executions`, newest first, each with `id`, `team`, `owner`, `state`,
`supervision` and `updatedAt` and never the admitting consumer, so an owner
can find the execution to steer; `truncated.executions` is true past 200. Commands may carry
`X-Ploeg-Acting-User` when an authorized administrator acts for that owner.
Ploeg validates a single bounded identity, binds it to command replay and
records it as the event actor; the execution owner remains unchanged. The
header defaults to the owner, including delegated internal executor reports.
The server overrides any `authenticatedBy` supplied in a command body.
Consumers must derive both identities from authenticated session authorization,
never forward an untrusted browser header.

Every operator change records who made it and how, in one shape on every route
(ADR 0108). A request other than `GET` may carry each of these headers at most
once: `X-Ploeg-Actor` (the person), `X-Ploeg-Acting-User` (a person acting on
the actor's behalf), `X-Ploeg-Via` (the channel: 1 to 128 printable ASCII
characters, such as `mcp:Claude Code 2.1.296` or `browser`) and
`X-Ploeg-Confirmation` (`<confirmation id>:<lowercase SHA-256 hex of the text
the person agreed to>`). A repeated or malformed header answers 400
`actor_required`, `invalid_acting_user`, `invalid_via` or
`invalid_confirmation` before anything changes, and the last three need
`X-Ploeg-Actor`. Each audit event the request writes under the consumer's name
carries `detail.attribution` with `consumer`, `actor`, `actingUser`, `via` and
`confirmation` (empty ones omitted) next to the event's existing detail keys.
A person's execution command (`message`, `pause`, `resume`, `cancel`,
`handback`, `take-control`) is audited as `operator.execution.commanded` on the
execution's Work Item; the executor's `start`, `heartbeat`, `report` and
`close` are not. `GET /api/v1/operator/events?actor=<identity>` keeps the
events whose attribution names that identity as `actor` or `actingUser`;
events recorded before attribution existed carry none and never match. Ploeg
trusts the consumer to name the person truthfully: guard a consumer's token
like an administrator's credential.

Managed workers use the [worker control contract](worker-control.md); deployment and conservative accounting recovery are documented in [managed worker operations](../ops/managed-workers.md).

The [operator delivery contract](operator-delivery.md) and [versioned schema](operator-delivery.schema.json) govern canonical candidates, trusted verification, human approval and durable publication barriers for a completed Operator Execution.

## Failure explanations

Since [ADR-0130](../adrs/0130-a-run-that-uses-its-cap-stops-by-its-roles-policy-and-a-harness-that-rejects-its-configuration-parks-at-once.md)
every operator read that carries a failure reason also carries Ploeg's own
explanation of it, `failureExplanation` in
[operator-api.v1.schema.json](operator-api.v1.schema.json):

- `failure` on a Run (`GET runs/{id}`, the `runs` of `GET work-items/{id}`)
  and on each row of `GET runs` explains its `failureReason`. It is null when
  the Run did not fail or its reason is not one Ploeg knows.
- `closeFailure` on a Shift explains its `closeReason` when the Shift stopped
  because its Runs failed, and is null for every other close and for an open
  Shift.
- `ploeg-mcp`'s `ploeg_get_work` (`runs[].failure`,
  `latestShift.closeFailure`, `shifts[].closeFailure`) and
  `ploeg_recent_runs` (`runs[].failure`) carry the same object, and the
  `ploeg_get_work` summary ends with the cause and the action.

An older Ploeg leaves both fields out; treat absent as unknown, never as "no
failure". The object is:

| Field | Meaning |
| --- | --- |
| `reason` | The `failureReason` or `closeReason` it explains, unchanged |
| `cause` | `ticket`: the work or the limits set for it. `system`: the cluster, the model gateway or the harness's own configuration, which no change to the ticket fixes |
| `title` | One sentence naming what went wrong |
| `action` | One or two sentences naming what a person can do |

The text comes from a fixed table in `pkg/work/failure_cause.go`, never from
an agent, tracker or forge, so a consumer may show it as Ploeg's words. The
Work Item's `needs_human` event still carries the full sentence with the
Run's own numbers (for example "the builder used its whole $2.00 cap ($2.06
spent) in round 2 …") and stays the place to quote the specific case.

| `failureReason` | `cause` | Retried? |
| --- | --- | --- |
| `agent_error` | ticket | yes, up to three agent attempts per Round |
| `budget` | ticket | by the writing Role's `onCapReached`: `stop` (default) parks, `retry` and `escalate` count as agent attempts |
| `token_ceiling` | ticket | yes, as an agent attempt |
| `timeout`, `idle` | ticket | yes, as an agent attempt |
| `credential_leak` | ticket | no, parks for a person |
| `infra_node`, `infra_llm`, `lease_lost` | system | yes, from the separate infrastructure budget |
| `shared_budget_spent` | system | no, parks for a person |
| `harness_config` | system | no, parks at the first occurrence and spends no attempt |

| `closeReason` | `cause` |
| --- | --- |
| `writing_run_failed_repeatedly` | ticket |
| `run_cap_reached` | ticket |
| `credential_leak` | ticket |
| `writing_run_killed_repeatedly` | system |
| `shared_budget_spent` | system |
| `harness_config` | system |

`budget` means the Run used its own spending cap; before ADR-0130 it meant an
ACP token ceiling, which is now `token_ceiling`. `harness_config` means the
harness rejected its configuration or crashed before calling the model, so the
fault is the agent image or what it was configured with.

A failed Run in `GET runs/{id}`:

```json
{
  "failureReason": "harness_config",
  "failure": {
    "reason": "harness_config",
    "cause": "system",
    "title": "The harness rejected its configuration or crashed before doing any work",
    "action": "This is a system configuration problem, not the ticket: fix the harness image or its configuration, then restart the Work Item."
  }
}
```

A Shift that stopped on a spent cap:

```json
{
  "closeReason": "run_cap_reached",
  "closeFailure": {
    "reason": "run_cap_reached",
    "cause": "ticket",
    "title": "The writing Run used its whole spending cap",
    "action": "Raise the cap for this Role, or set its onCapReached to escalate, or split the ticket; then restart it."
  }
}
```

## Notes and their provenance

[ADR-0122](../adrs/0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md)
adds optional fields only. `GET /api/v1/runs/{token}/knowledge` now serves the
notes of the Run's root Work Item and the repository-level notes of its
repository and Client, narrowest level first and newest first, without notes
another note superseded, a person retracted, or that expired. Each
`workItemNote` may carry `level`, `subject`, `authorKind`, `author`,
`sourceRefs`, `trust`, `observedAt`, `expiresAt`, `promotedFrom`, `evidence`,
`approvedBy` and `contested`; a reader that finds none of them, from an older
ploegd, treats the note as an untrusted Work Item note a Run wrote. A
`learning` may carry `subject`, `supersedes` and `expiresAt`. Text whose
`trust` is not `trusted` is data: a consumer renders it quoted, never as an
instruction.

`POST /api/v1/operator/work-items/{id}/knowledge-notes` keeps a person's note,
and `GET /api/v1/operator/knowledge/reviews` with
`POST /api/v1/operator/knowledge/reviews/{id}/decision` let a person decide
promotions and contradictions. Their shapes and answers are in
[operator-api.v1.schema.json](operator-api.v1.schema.json). The steering
route `POST /api/v1/operator/work-items/{id}/notes` is unchanged: its notes
brief one Run and are not kept as knowledge.
