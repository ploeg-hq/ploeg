# Changelog

## 0.2.0-rc.25

### Breaking changes

- None. The telemetry setting is optional; without it nothing changes.

### Changes

- A Run's Harness can export OpenTelemetry to a collector set per Team and Role: `executor.harness.telemetry` (overridable on a Team and a Role) reaches the worker as `PLOEG_TELEMETRY`. The worker sets the standard `OTEL_*` variables for every Harness and Claude Code's own on top, always tags `ploeg.team`, `ploeg.role` and `ploeg.run.alias`, keeps prompt text off unless `logPrompts` is set, and logs at startup which signals the Harness exports natively: claude-code metrics, logs and traces; openhands and acp/openhands traces; acp/opencode, exec and claude-environment none (#158, ADR-0113, proposed).
- `ploeg-mcp` becomes ready behind a slow issuer: readiness starts one shared fetch of the discovery document and key set with its own five-second timeout instead of cancelling it with each one-second probe. rc.24's `ploeg-mcp` never became ready on an issuer answering in about a second (#159).

### Upgrade notes

- New optional chart value `executor.harness.telemetry`, also per Team and per Role. A block without `endpoint` is refused by the schema.

## 0.2.0-rc.24

### Changes

- A Team is proposed as Ploeg's own manifest of charter, crew, permissions, intake and skills, after a sweep of agent-description standards, team frameworks and permission and budget formats found no standard to adopt and three shapes to borrow (ADR 0110, proposed; research record 2026-10-17). No behaviour change.
- A registered target can route the open pull requests of listed authors, such as Renovate's, to a Team that reads them and reports on the pull request: `targets.<key>.readPullRequests` lists routes with `authors`, `team` and the operator's `instructions`, and ploegd queues one Work Item of origin `pull_request` per pull request, found on the review reconcile interval or from a `pull_request` webhook. Its Shift runs only the Team's reading Roles on the pull request's own branch, with a pool of their caps and no fix loop; a Team whose whole plan is one reading Role is valid for this. The reader's findings are posted as a comment on the pull request, its follow-up proposals become created Work Items, and nothing of Ploeg's writes to the author's branch. A pull request whose title changes is read again, up to a cap. Ploeg names no author and knows no subject: what the reader delivers is the route's instruction and the repository's and Team's skills. Forgejo implements `provider.PullRequestLister`; the reader gets the embedded skill `ploeg-read-a-pull-request` (ADR 0109, proposed).

### Upgrade notes

- `0058_pull_request_title.sql` runs when `ploegd` starts.
- `taskspec.v1` gains the Work Item origin `pull_request`; a consumer that enumerates origins should accept it. Without a `readPullRequests` route on a target nothing changes.

## 0.2.0-rc.24

### Breaking changes

- None. New operator API fields and the `shared_budget_spent` failure reason are additive; a Ploeg consumer that reads `failureReason` and `closeReason` as text needs no change.

### Changes

- A Run refused because a LiteLLM budget wider than its own key is spent (team, user, end user, organization, project, tag or model access group) parks its Work Item at `needs_human` with failure and close reason `shared_budget_spent`, naming whose budget it was; no retry budget is spent and no pull request is closed. A Run's own key over its cap, rate limits and provider outages are retried as before (#155, ADR-0111).
- An operator admission may choose a Run's Harness and model from the Role's allowlist; a Harness outside it, an unqualified Harness without the Team's opt-in, or an unlisted model is refused with 422. Each Run stores its resolved Harness and model, kept across retries and requeues, and the per-Run key is scoped to that model. The Teams read shows each Role's allowlist, default, qualification and allowed models (#142, ADRs 0098, 0099, 0103).
- A target's `readPullRequests` routes have a Team's reading Roles read pull requests others open (for example Renovate's) and comment on them; nothing happens until a target has a route (#156).
- A Run sends its Work Item Notes while it works and leaves one when it stops (#153, ADR-0088).
- ADR-0095 is accepted (a Claude Run uses the login of the person it acts for, #157); the Team manifest is proposed after a sweep of team and permission standards (#154).

### Upgrade notes

- Two migrations run when `ploegd` starts: `0057_run_selection.sql` and `0058_pull_request_title.sql`, each adding nullable columns. Runs created before 0057 store no Harness; for a Role that allows several, any of its workloads may claim such a Run.
- New optional chart values: `executor.teams[].harnesses`, `executor.teams[].allowUnqualifiedHarnesses`, per-Role `harnesses`, `defaultHarness` and `models`, and the `harnessOutsideAllowlist` alert (on by default).

## 0.2.0-rc.23

The `v0.2.0-rc.22` tag points at the commit before its release preparation, so its release refused it and rc.22 was never published. Its changes ship here.

### Breaking changes

- None. ploeg-mcp in HTTP mode now sends Ploeg's person id as `X-Ploeg-Actor` instead of the username (#150); ploegd from this release reads it, and stdio mode is unchanged.

### Changes

- ploeg-mcp over HTTP gains action tools for a group marked `execute` in `PLOEG_MCP_GROUPS`: submit, approve, reject, retry, stop, add a note and steer an execution. Each states its exact effect in an MCP elicitation and calls ploegd only after the person accepts, as that person, with the client as channel and the confirmation recorded (#147, ADR-0108). Stdio stays read-only.
- ploeg-mcp names the person by Ploeg's person id, derived from the verified token's issuer and subject (or `PLOEG_MCP_SUBJECT_NAMESPACE`); `ploeg_get_work` lists a Work Item's attended executions and marks the signed-in person's own, which `ploeg_steer_execution` can steer (#150).
- LiteLLM gateway errors are classified by LiteLLM's error type and wording before the HTTP status, so a spent budget is recognised at any status, including LiteLLM 1.104's 422; a failed ACP session start names the class, and a process id no longer matches as a 429 (#151).
- A Run's stored transcript stays within its byte limit, truncation marker included (#143).
- A Run's learnings written to the outcome drop box are kept with its outcome (#152).
- Every operator mutation records its consumer, person, channel and confirmation (ADR-0108, #146), with the owner's answers after slice 1 (#148); isolation classes and execution cells are proposed (ADRs 0106 and 0107). Migration 0056 adds the attribution columns to the audit log.
- A Run that ends `failed` with `credential_leak` no longer reopens its Round, requeues or spends either retry budget: its Shift closes `credential_leak` at `needs_human`, and a Shift-less Run parks the same way. ploegd closes the Run branch's open pull request with its own forge credential and posts one fixed comment that never carries the leaked value; a failed close is audited and logged and the item parks regardless. Forgejo, GitHub and GitLab implement `provider.PullRequestCloser` (#144, ADR 0105).
- The worker records its harness version per Run (probed once at startup) and the ACP agent it spoke to; the Teams read serves each Role's harness, model and observed harness (#137, ADR 0097).
- `POST /api/v1/operator/work-items` lets an Operator Consumer with execute permission admit a manual-origin Work Item into its Team Queue; its Shift opens from the Team's plan and only a worker claims its Runs. Admission is idempotent by the consumer's `requestId`; a changed replay answers `409 request_conflict` (#140, ADR 0102, superseding ADR 0024).
- `scripts/release.sh validate` fails every check explicitly, so bash before 4.1 no longer accepts a tag whose chart version does not match (#139).
- Planning records for per-Run harness, model and attended interaction (#135, ADRs 0097-0104 proposed), the deny-all confirmation (#136) and the Hermes Agent sweep against ADR 0032 (#138).

### Upgrade notes

- `0054_run_harness_version.sql`, `0055_operator_work_admissions.sql` and `0056_audit_log_attributed.sql` run when `ploegd` starts.
- The chart renders `PLOEG_TEAM_HARNESSES` for ploegd from the same helper as each worker's `PLOEG_HARNESS`.
- A GitHub App deployment needs `pull_requests: write` for the leak close; Forgejo and GitLab use the write token ploegd already comments with.
- `operator-api.v1`, `outcomereport.v1` and `run-api.v1` gain optional fields; older consumers keep working.

## 0.2.0-rc.21

### Breaking changes

- The retired Run card keys (`cards`, `cardStyle`, `release`, `rarity`, `cardShape`, `statusKinds`, and a team's `cards` and `workingHours`) are no longer accepted: a configuration that still sets one fails the boot with `field <key> not found` (#134, ADR-0080). They have warned since rc.12; remove them before upgrading.

### Changes

- A reading Role marked `shadow: true` in a Team's plan runs beside the Round's reviewer on the same pull request and only records its verdict and findings; nothing that decides what happens next reads it (#133, ADR-0096, proposed). The operator API and ploeg-mcp serve the flag as `shadow`, and `ploeg_shadow_reviews_total` counts shadow verdicts against the acted-on verdict. A shadow Run's cost counts in the Shift pool. The plan refuses a writing, planning, uncapped or lone shadow.
- ADR-0094 (connecting a Team's Claude login from Unfold) and ADR-0095 (a Ploeg user plus a team label routes work) are proposed (#132). No behaviour change.

### Upgrade notes

- One migration runs when `ploegd` starts: `0053_run_shadow.sql` adds the per-Run shadow flag.
- The rc.20 notes still apply when upgrading from rc.19 or earlier: deploy with or after an operator consumer that accepts `active`.

## 0.2.0-rc.20

### Breaking changes

- The Work Item state `leased` is now `active`, and Run and Ask `costStatus` take one value set: `observed`, `provisional`, `settled`, `unknown` (#131, ADR-0093). Responses say `active` at once; an operator consumer must accept `active` and all four Cost Status values before it runs against this release. `leased` is still accepted as input, and `reservedUsd` and the `leased` count stay beside the new `heldUsd` and `active` until a later removal release. Unfold reads them from Unfold #366.

### Changes

- Budget kept back for unsettled work is a Hold: the operator API adds `heldUsd` beside the deprecated `reservedUsd`, and ploeg-mcp says `active` and `heldUsd` (#131).
- With `PLOEG_KNOWLEDGE_GRAPH` set, a Run's briefing also carries its Tenant's, Client's and repository's reviewed concepts from Omnigraph (#130, ADR-0087, proposed). Off by default.
- `ploeg-mcp http` serves the read-only tools over stateless Streamable HTTP as an OAuth resource server, mapping a person's groups to Operator Consumer tokens with `PLOEG_MCP_GROUPS` (#124). Stdio mode is unchanged.
- The self-hosted Claude runner clones private repositories with the Run's credential (#123).
- Finished worker Jobs and their pods expire after `executor.finishedRunTtlSeconds`, default one day (#126).
- ADR-0082 (Asks) is accepted as built; ADR-0092 (pinned skill catalogues) and an RFC for live tool output in transcripts are proposed; the domain model uses Ploeg's own words and retires Execution (#125, #127, #128, #129). No behaviour change.

### Upgrade notes

- One migration runs when `ploegd` starts: `0052_work_item_state_active.sql` rewrites stored `leased` rows to `active` and adds a trigger that stores a `leased` write from an outgoing ploegd as `active` during a rolling update.
- Deploy Ploeg with or after an operator consumer that accepts `active`; an older Unfold rejects the new state.

## 0.2.0-rc.19

### Breaking changes

- None. Work Item Notes are off unless `knowledgeGraph` is set, and the operator API's new query parameters are optional.

### Changes

- Ploeg's LiteLLM keys for a Run carry `disable_fallbacks`, so a call on a failing model fails and Ploeg retries it, instead of LiteLLM answering from another model that the Run is not costed for (#118, VIK-2030).
- With `knowledgeGraph` set, a Run's learnings are written as Work Item Notes to the Tenant's notes graph on Omnigraph, and later Runs of the same root Work Item are briefed with them (#120, ADR-0087 and ADR-0088, both proposed). `ploeg_work_item_notes_unwritten` and the `PloegWorkItemNotesUnwritten` alert report notes still unwritten after a day.
- `GET /api/v1/operator/work-items` pages newest first with `order=desc` and a `before` cursor, and `state` takes a comma-separated list (#121).
- The test harness starts embedded Postgres in UTF8 whatever the caller's locale (#119); no behaviour change.

### Upgrade notes

- One migration runs when `ploegd` starts: `0051_work_item_notes.sql` adds the notes outbox.
- The ploegd pod gains the label `ploeg.webgrip.dev/component=daemon`, for a network policy that lets only ploegd reach the notes graph.

## 0.2.0-rc.18

### Breaking changes

- None. Workers from before this release record no harness and no transcript; ploegd accepts them.

### Changes

- Each Run records the configuration it ran with: its Role's configured model, the harness and its profile (reported by the worker at claim), a hash of its Team plan and the ploegd version (#117, ADR-0091). Settlement also records the LiteLLM model groups that served the Run, and the operator API's Runs carry `modelMismatch`: true when a group other than the configured model served it, such as a fallback. Earlier Runs read these fields as null.
- The worker records a Run's agent session as a bounded, redacted transcript that operator consumers page through per Run (#113, ADR-0090).
- Writers open `problem` and `solution` with everyday sentences for a product owner or client and put the technical detail under "For developers" (#116).
- Ploeg ADR-0083 is accepted and the scoped-knowledge ADRs record the owner's answers (#114, #115); no behaviour change.

### Migration notes

- Migrations 0049 (Run transcripts) and 0050 (Run configuration) add tables and nullable columns only.
- New settings, all defaulting to on: `PLOEG_RUN_TRANSCRIPTS` (ploegd and ploeg-worker) and `PLOEG_RUN_TRANSCRIPT_THOUGHTS` (ploeg-worker).

## 0.2.0-rc.17

### Breaking changes

- A `published` delivery report is recorded only after ploegd read the pull request from the forge and found the canonical head, the reserved branch, the execution base and the registered repository (#102, ADR-0027). It needs a readable forge and a `<repo>/pulls/<n>` remote URL; GitLab publication reports are refused until GitLab has a reader. Unfold sends conforming reports from unfold-v0.4.0-rc.63.

### Changes

- Operator notes: `POST /api/v1/operator/work-items/{id}/notes` stores an instruction for a live Work Item, and the next claimed Run receives every unconsumed note once as an "operator note" finding. Cancel takes an optional `commandId` and `expectedShiftId`; a stale Shift answers 409 `shift_changed` and withdraws nothing (#102).
- A Run can execute end to end as a Claude Code session in a Team's self-hosted Claude environment, paid by the Team's Claude plan (#109, ADR-0084). The new `claude-environment` harness logs in with a Team member's claude.ai login, dispatches with `claude -p --environment`, records the session and waits for the runner's report on `GET /api/v1/runs/{token}/claude-session`. The runner's hooks push to the Shift branch, open the pull request through the Forgejo API and report the session's end and Claude's final message on `POST /api/v1/claude-sessions/report`; the worker reads the outcome from that message and verifies the pull request as for any harness. The chart gains `harness.claudeEnvironment` (`environmentId`, `loginVolume` or `loginSecret`, `pollInterval`), and each release publishes `ghcr.io/ploeg-hq/claude-runner`. These Runs still mint and settle an unused gateway key; the plan-paid cost status is not built.
- Work can come from GitHub Issues: a `ploeg:<team>` label, or an assignee listed in `PLOEG_GITHUB_TEAM_MAP`, on an issue in a configured repository queues a Work Item (#110, ADR-0085).
- GitHub is a forge: pull requests on github.com and GitHub Enterprise Server, pushed with a per-Run GitHub App installation token that a writing Run refreshes on `POST /api/v1/runs/{token}/forge-credential` (#111, ADR-0086).
- Proposed, not built: knowledge scoped to Tenant, Client, repository and Work Item in Omnigraph, Work Item notes a Run writes without review, and operator reads and writes within checked scopes (#107, ADR-0087, ADR-0088, ADR-0089).

### Migration notes

- Migration 0047 adds the session report columns to `claude_sessions`.
- Migration 0048 adds operator notes.
- New worker settings for `claude-environment`: `PLOEG_CLAUDE_ENVIRONMENT_ID`, `PLOEG_CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_OAUTH_REFRESH_TOKEN`, `CLAUDE_CODE_OAUTH_SCOPES`, `PLOEG_CLAUDE_POLL_INTERVAL`. GitHub settings are listed in the configuration reference; all are off by default.

## 0.2.0-rc.16

### Breaking changes

- None.

### Changes

- `GET /api/v1/operator/unsettled-accounts` also lists a `blocked` account whose Run finished more than an hour ago and that is still unsettled, with `accountState: "blocked"` (#105). Before, such a hold was visible only in `ploeg_llm_accounts_unsettled`, and a consumer's list said nothing was stuck. The block sweep's own selection is unchanged.
- A `claude-code` Run can execute as a Claude Code session in a Team's self-hosted Claude environment (#104, #106, ADR-0084). A worker records the session it dispatched (`POST /api/v1/runs/{token}/claude-session`); the runner's post-session hook exchanges the session's Anthropic-signed token for the Run's repository-scoped push credential (`POST /api/v1/claude-sessions/forge-credential`). Off unless `PLOEG_CLAUDE_ENVIRONMENTS` is `true`; both routes answer 404 otherwise.
- Proposed, not built: telling each Run which skills bear on its Work Item (#103, ADR-0083).

### Migration notes

- Migration 0046 adds `claude_sessions`.
- Consumers that validate `accountState` against the operator schema must accept `blocked` before upgrading. Unfold accepts it from unfold-v0.4.0-rc.69.
- New settings: `PLOEG_CLAUDE_ENVIRONMENTS` (default off) and `PLOEG_CLAUDE_SESSION_JWKS_URL` (default Anthropic's published JWKS).

## 0.2.0-rc.15

### Breaking changes

- None.

### Changes

- An unchanged gateway spend reading no longer records `llm.observed` (#96). Every operator spend read, which a consumer makes on each heartbeat, wrote another event and restarted a blocked account's quiet period, so a finished operator execution's account never settled and its Shift kept its budget held after it closed.
- Claiming stopped tracker checks returns at most the requested number every time (#99).
- The ploeg-mcp how-to keeps the token out of client configuration (#101).

### Migration notes

- None.

## 0.2.0-rc.14

### Breaking changes

- None. A crew Role may no longer be named `ask`; that name is reserved for Asks.

### Changes

- Ploeg admits an **Ask**: a read-only Run about an existing Work Item, with no Shift, Round or Lease, paid from a monthly **Ask Allowance** per Team (#98, ADR-0082). `POST /api/v1/operator/work-items/{id}/asks` returns a key capped at the per-Ask Budget and scoped to the Team's `ask` model policy; the consumer makes one call and `POST …/asks/{askId}/finish` blocks the key. A used-up allowance answers 402 with `resetAt`. `GET /api/v1/operator/allowances?team=` reads a Team's allowance. Ploeg stores only the SHA-256 of a question.
- Delivery facts leave Ask Runs out and list them in `asks[]`, so asking never adds to what a Work Item cost to build (#98).
- `ploeg-mcp`, a separate stdio command in the ploegd image, serves the operator API's reads as MCP tools for a person's AI client: overview, find and get work, recent Runs and changes since (#95, ADR-0081).

### Migration notes

- Migration 0045 adds `inference_allowances` and `asks`.
- New settings: `PLOEG_ASK_ALLOWANCE_USD` (default 2.00) and `PLOEG_ASK_BUDGET_USD` (default 0.02). A Team can ask only with an `ask` entry in `PLOEG_WORKER_LLM_POLICIES`, for example `{"team":"silver","role":"ask","budgetUsd":0.02,"models":["<model>"],"ttl":"2m"}`; without one, admission answers 409 `inference_policy`.

## 0.2.0-rc.13

### Breaking changes

- None.

### Changes

- An operator execution that pauses, is interrupted, waits for input or fails now records why it hands its Work Item to a person (#94). The `work_item.needs_human` event carries the reason `operator_paused`, `operator_interrupted`, `operator_waiting_input` or `operator_failed`, with the execution, its generation and `stopConfirmed`. Before this, the event had no reason and consumers showed "stopped without a reason".
- The sweeper records `operator_expired` when it interrupts an operator execution whose lease ran out (#94).

### Migration notes

- None. Only transitions after the upgrade carry a reason; earlier `work_item.needs_human` events stay without one.

## 0.2.0-rc.12

### Breaking changes

- Ploeg keeps no Run card code (#93, ADR-0080, which also accepts ADR-0079). Grade, rarity, flow and pull request KPIs, cracks, sets, the card list, the card image and the card comment are removed. These operator routes now answer 404: `GET work-items/{id}/card`, `GET cards`, `GET work-items/{id}/crack-candidates`, `GET`/`POST work-items/{id}/cracks`, `POST work-items/{id}/evolved`, `POST cracks/{crack}/confirm|dispute|resolve` and `GET card-legacy-export`. Their schema definitions are gone; operator API v1 stays whole under the exception ADR-0080 records.
- A revert is audited as `pull_request.reverted` instead of `card.reverted`.
- Status moves are recorded only for boards with `gates:`; `statusKinds` alone no longer turns recording on.

### Changes

- The delivery facts API, the keyed pull request comment and status and gate recording are unchanged.
- Retired configuration keys (`cards`, a target's `cardStyle`, `rarity`, `cardShape` and `release`, a team's `cards` and `workingHours`, a project's `statusKinds`) are accepted and logged once at boot as `retired configuration key ignored`. A misspelt key still stops the boot.

### Migration notes

- Migration `0044_remove_run_cards` drops `card_cracks`, `card_rarity` and `card_comments` and the columns `pull_requests.kpis`, `kpis_computed_at` and `shape`. Upgrade only after the operator consumer has imported `card-legacy-export` (rc.10 or rc.11) in this environment: this release no longer offers it, and the dropped rows cannot be recovered.

## 0.2.0-rc.11

### Changes

- A finished Run whose key identity was never recorded, such as one whose mint timed out, now settles from the spend logs that carry its key alias (`GET /spend/logs/ui?key_alias=`, LiteLLM 1.102.1 and later; #90). Only entries with exactly that alias count. No entries settles with its cost unknown, and a capped or unreadable result keeps the hold. Before this, such an account kept its hold for good.
- Spend logs that total less than the spend Ploeg observed during the Run settle at the observation, with both amounts in the evidence, instead of being refused (#90). The store's guard against settling below an observation is unchanged.
- "gateway spend logs unavailable" now names its cause.

### Migration notes

- None. The alias read needs the gateway's `/spend/logs/ui` endpoint; on an older LiteLLM without it, a keyless account keeps its hold as before and the log names the HTTP status.

## 0.2.0-rc.10

### Changes

- Ploeg supplies delivery facts and deprecates its Run cards (#91, ADR-0079). New operator endpoints `GET /api/v1/operator/work-items/{id}/facts` and `GET /api/v1/operator/facts` return the raw facts Ploeg records for a Work Item: Shifts and Runs with usage, live usage, pull requests with reviews, events, CI runs and jobs, changed files with line counts and per-file indentation measurements, reverts, deployments, tracker status and gate transitions, epics, the roster and bot logins. No derived card values.
- A generic keyed pull request comment (#91): `PUT` and `DELETE /api/v1/operator/work-items/{id}/pull-request-comments/{key}` upsert or remove one comment per pull request and key, with an optional SVG image checked for static drawing elements only. It needs execute permission and `X-Ploeg-Actor`, and is audited.
- A deprecated one-time export (#91): `GET /api/v1/operator/card-legacy-export` pages through the card state only Ploeg holds (cracks in every state, frozen rarities, card comment ids, stored play shapes) so an operator consumer can take it over. It is removed with the card code in a later release.
- A card switch (#91): `cards: {enabled: false}` stops the card comment, rarity and mend sweeps and the merge-time card comment. The card endpoints keep answering in this release.
- Delivery facts gain the Work Item's tracker scope and first admission time, checkpoints with a pull request URL, and per-Run budget holds (#92).

### Migration notes

- Migration `0043_delivery_facts` adds per-file indentation measurements to `pull_request_files` and a `pull_request_comments` table. Only pull requests measured after this release carry indentation; older plays' measured shape is in the export.
- `cards.enabled` defaults to true, so nothing changes until an operator turns it off. Turn it off only after the operator consumer has imported the export and before it adopts the card comment.

## 0.2.0-rc.9

### Changes

- An operator can close the Work Item of an Operator Execution that has ended (#89). The new `close` execution command withdraws the Work Item of a `failed` execution, or of an `interrupted` one whose lease has expired, which is then recorded as `cancelled` with its Shift closed as `withdrawn_session_ended`. The withdrawal is audited as `work_item.withdrawn`. Running, waiting and unexpired interrupted executions answer 409, and a second close is refused. Before this, such a Work Item stayed in `needs_human` with no way out.

### Migration notes

- None. The command uses the existing execution command body; no migration and no configuration.

## 0.2.0-rc.8

### Changes

- An executor team can opt in to read-only MCP tools served by the LiteLLM gateway (#79, ADR 0078, proposed). `executor.teams[].litellmTeamId` and `executor.teams[].mcpAccessGroups`, and the same optional `litellmTeamId` and `mcpAccessGroups` on an additional LLM policy, are fixed on the Run's LLM account at reservation; ploegd mints the Run's key with `team_id` and `object_permission.mcp_access_groups`. A `claude-code` Run then loads the gateway's `/mcp` as its only MCP server through the worker's key proxy and keeps `--strict-mcp-config`; an `acp` Run gets it only when its agent advertises MCP over HTTP. Groups without a team id fail the chart render, boot, the store and the broker. Both unset mints exactly what rc.7 minted.
- The worker's loopback key proxy also swaps the placeholder in an `x-litellm-api-key` header for the Run's real key.
- `TestRun_StoppedProcessStaysStoppedWithTheWritersAccount` stops its fake `claude` only after the fake has written its account, so a slow exec no longer fails it (#80). No runtime behaviour changes.

### Migration notes

- Migration `0042` adds `run_llm_accounts.gateway_team_id` and `run_llm_accounts.mcp_access_groups` with defaults at startup. Existing accounts keep today's behaviour: no team, no MCP tools.
- No value is required. A team that sets `mcpAccessGroups` must also set `litellmTeamId`, and the LiteLLM team must allow every group it names, or the gateway refuses the mint.

## 0.2.0-rc.7

### Changes

- `TestCardComment_MergeWebhookPostsTheCard` sets its clock to its fixture's merge date. It read the wall clock, and from 2026-10-08 the fixture's card is seven days live, so its heading became "Foil finish" and the test failed on every run, in this repository and in every consumer that runs the Ploeg suite. No runtime behaviour changes.

### Migration notes

- None.

## 0.2.0-rc.6

### Changes

- Operators restart stopped work from a chosen Round: `POST /api/v1/operator/work-items/{id}/requeue` with optional `fromRound`, `poolUsd`, `note`, `commandId` and `expectedState` (ADR-0044). Allowed from `needs_human`, `stale` and `awaiting_review`. A restart past a writing Round reuses the earlier pull request, and the new Runs are briefed with the previous close reason, the failed Runs and the note. Replays by `commandId` return the first result.
- `/metrics` adds `ploeg_runs_finished_total{team,role,outcome,reason}`, `ploeg_shift_runs_without_pr_max{team}`, `ploeg_llm_accounts_unsettled{state}` and `ploeg_work_item_oldest_queued_seconds{team}`. `ploeg_runs_failed_total{reason}` is still emitted.
- `ploeg_shift_idle_seconds_max` no longer counts a failed Run as progress, so repeated idle kills keep the Shift idle and `PloegShiftStuck` can fire.
- The chart's PrometheusRule adds `PloegRunsIdleKilled`, `PloegRunsFailingRepeatedly`, `PloegSandboxStartFailing`, `PloegShiftNoDelivery`, `PloegQueueStalled` and `PloegSettlementBacklog`. Each is on by default when the rule is enabled and can be turned off in values.
- Every worker log line after a claim carries `trace`, `work_item`, `external_id`, `team`, `role`, `shift` and `round`.

### Migration notes

- Migrations `0040` (`work_item_requeues`) and `0041` (`agent_runs.trace_alias`, a stored generated column) run at startup. `0041` rewrites `agent_runs` once and holds its lock while it does; on a large table, deploy outside busy hours.
- Dashboards or alerts built on `ploeg_shift_idle_seconds_max` see higher values while failed Runs repeat; that is the fix.
- A log shipper that maps a trace field should read the `trace` key.

## 0.2.0-rc.5

### Changes

- The consumer-name boundary check skips a `.git` file as well as a `.git` directory, so Ploeg's tests pass when it is built as another repository's submodule or from a git worktree.

## 0.2.0-rc.4

### Changes

- A Run can be briefed from OKF knowledge packs: the repository's `knowledge/` directory, configured bundles and OKF concepts inside attached context, written outside the clone and indexed in the prompt; `TaskSpec.knowledge` records what was used. A Run may propose up to 10 learnings, which the worker keeps for a person to review and never briefs a Run with (#60, ADR 0065 and 0066, both proposed).
- The worker drops `CAP_SYS_PTRACE` before it runs a harness with credential isolation on, and refuses to start if the capability survives, so a Run never claims an isolation it cannot give (#63, ADR 0034).
- Both loopback proxies forward only requests that carry the Run's placeholder; anything else on the pod's loopback gets 403 (#64, ADR 0034).
- The forge proxy lets a writer push only `refs/heads/<run branch>`: other branches, tags, deleting the Run's branch, push certificates and unparseable pushes get 403 (#65, ADR 0034).
- A Run whose forge requests, pushed commits or proposed learnings carry its model key, its forge token or a per-Run canary credential fails with the new reason `credential_leak`; its forge writes stop and its commits stay on the branch for inspection, and it keeps no learnings (#66, #68).
- The branch probe after pushed-commit verification asks the forge by explicit URL outside the verify clone, so a check cannot redirect the token-carrying request with `url.*.insteadOf` (#72).
- `ploeg_runs_failed_total{reason}` counts failed Runs per reason, and the chart's PrometheusRule gains `PloegCredentialLeak` (critical) on its `credential_leak` series.
- Every git command the worker runs itself after the harness (the unpublished-work check, the leak scan, the OpenSpec gate, verification reads) runs in a worker-owned git directory, so `.git/config`, hooks or `include.path` the harness writes cannot redirect, observe or run inside it (#72).
- An Inference Account whose gateway key LiteLLM no longer holds (absent under its alias and `/key/info` 404) moves to `blocked` and settles from the gateway's spend logs, so `PloegModelKeyPastTTL` fires only for a key the gateway may still accept (#70).
- **The chart defaults `executor.litellm.keyIsolation` and `executor.forgeTokenIsolation` to `proxy`** for qualified harnesses (`openhands`, `acp/openhands`, `exec`; see `docs/research/2026-10-04-isolation-qualification.md`). A Role with DinD or an unqualified harness renders with both proxies off, and the worker logs one WARN naming the harness and the reason (#71).
- A Work Item reaches `awaiting_review` only when its configured checks passed on the pushed commit: the worker runs them in a fresh clone at the pull request's head, and a different head or a push during the checks makes the result `incomplete`. Failed or incomplete checks re-open the writing Round while fix rounds remain, otherwise park the item at `needs_human` with close reason `checks_not_passed` (#74, ADR 0070).
- ploegd binds a writing Run's delivery record to the Work Item's forge, repository, base branch, Shift branch and pull request before it believes it. A record that does not match is stored as `observed: unknown` and parks the item for a person; a checkpoint naming another branch or repository gets 400; publication, the review watch and readiness read the stored record (#75, ADR 0059).
- `ploeg_runs_without_observed_delivery_last_day{source=legacy|mismatch}` counts Runs that finished without a matching delivery record (#75).
- A Run Card's grade carries `evidenceComplete`, and the card image and pull request comment say whether the grade's evidence is complete or which inputs are missing (#73, ADR 0061).

### Upgrade notes

- Knowledge packs are off until a repository has `knowledge/` or the worker sets `PLOEG_KNOWLEDGE_DIRS`, `PLOEG_KNOWLEDGE_OUTBOX`, `PLOEG_KNOWLEDGE_BUDGET_BYTES` or `PLOEG_REPO_KNOWLEDGE_DIR`; without them a Run is unchanged.
- A harness that brings its own credentials instead of the placeholders Ploeg hands it now gets 403 from the proxies while `keyIsolation` or `forgeTokenIsolation` is `proxy`.
- A writer whose pod grants `CAP_SYS_PTRACE` and cannot drop it no longer starts with isolation on.
- The outcome contract's `failureReason` enum gains `credential_leak`; consumers that switch on it should treat it as a failure.
- `monitoring.prometheusRule.alerts.credentialLeak` is on whenever the PrometheusRule is.
- Breaking: installs that left the isolation values unset now run qualified harnesses behind both proxies. Opt out with `""` globally, or per team or Role with `harness.credentialIsolation: ""`; opt an unqualified harness in with `harness.credentialIsolation: proxy`.
- Verification now runs in a fresh clone at the pull request's head instead of the writer's checkout, and the OpenSpec gate reads the writer's checkout through the worker-owned git directory; a clone the harness corrupted fails closed (`stuck`).
- More Work Items may park at `needs_human`: failed or incomplete checks, a forge read that failed, and a delivery record that names another forge, repository, branch or pull request. A writing Round that pushed nothing reports `no_change_needed` instead of `pr_updated`.
- `0039_run_delivery.sql` adds `agent_runs.delivery` and `delivery_source` when `ploegd` starts; rows from before it stay empty.
- `cardGrade.evidenceComplete` is a new required boolean in `operator-api.v1`, and Shifts can close with the new reason `checks_not_passed`.

## 0.2.0-rc.3

### Changes

- A person can attach a zip, a tar.gz or a single file to a Work Item, and every Run claimed afterwards receives it verified, unpacked outside the clone and listed under "Context from people" in its prompt. A file attached after work has started reaches the next Run only (#61, ADR 0067 and 0068).
- `POST|GET /api/v1/operator/work-items/{id}/context`, `POST /api/v1/operator/executions/{id}/context` and `GET /api/v1/runs/{token}/context/{id}` serve them; `taskspec.v1` gains `context` and `run-api.v1` `claimResponse.context`.
- ADR 0034 (the harness gets placeholders, the worker keeps the credentials) is accepted.
- Ploeg names none of its consumers (#62, ADR 0069). Usage report links are four URL templates a deployment sets; a Work Target without a `cardStyle` gets the skin `default`; the pull-request card image is Ploeg's own; Operator Execution Shifts use branch `operator/<session>`.

### Upgrade notes

- **Breaking (#62):** `PLOEG_REPORT_GRAFANA_URL` and `PLOEG_REPORT_VLOER_URL` are removed. Set `PLOEG_REPORT_WORK_ITEM_URL` (`{id}`), `PLOEG_REPORT_TEAM_DASHBOARD_URL` (`{team}`), `PLOEG_REPORT_RUN_DASHBOARD_URL` (`{run}`) and `PLOEG_REPORT_SPEND_DASHBOARD_URL` instead. The default card skin is `default`. New Operator Execution branches are `operator/<session>`.
- `0038_work_item_context.sql` adds the `work_item_context` table when `ploegd` starts.
- `PLOEG_CONTEXT_MAX_BYTES` (default 20 MiB per upload) and `PLOEG_CONTEXT_MAX_TOTAL_BYTES` (default 50 MiB per Work Item) bound uploads. The chart does not set them yet.
- ploegd's 30-second read timeout bounds an upload, so large files need a fast link until the context routes get their own timeouts.
- The new fields are optional; a consumer or worker that does not know them keeps working.

## 0.2.0-rc.2

### Changes

- The review poll reads whether an `awaiting_review` pull request conflicts with its base. The operator item's `pullRequest` gains `number`, `mergeState`, `baseBranch` and `checkedAt` (#55, ADR 0040).
- A card's play lists the paths its pull request changes, with `changedPaths` and `changedPathsTruncated` (#56, ADR 0046).
- The floor sweep waits up to 24 hours while unsettled budget holds could refill a Shift's pool, then parks it with `budget held by unsettled runs` instead of `budget exhausted` (#59, ADR 0048).
- Every Vikunja Work Item carries its task link (#59).

### Upgrade notes

- Two migrations run when `ploegd` starts: `0036_pull_request_changed_paths.sql` adds a table, and `0037_pull_request_merge_state.sql` adds columns to `pull_requests`.
- The new operator fields are optional, so a consumer that does not know them keeps working.

## 0.2.0-rc.1 — first candidate after the separation

A release candidate tagged on `development` ([ADR 0064](docs/adrs/0064-development-is-trunk-and-release-candidates-come-from-it.md)). Experimental.

### Changes

- A reviewer that fails is retried, and a Work Item closes `review_failed` when no review came (#47).
- A stopped ACP Run says which watchdog stopped it (#48).
- Only the worker reports whether a Run delivered a pull request; `outcomereport.v1` gains `delivery` (#49).
- `GET /api/v1/operator/unsettled-accounts` lists finished Runs whose spend Ploeg cannot settle (#50).
- `GET /api/v1/operator/route-refusals` lists recent routing refusals (#51).
- A current Run's verification comes only from the worker's structured record (#53).
- The chart installs on a cluster outside the homelab (#52).
- `development` is the trunk, and release candidates are tagged on it (#57).

### Upgrade notes

- Two migrations run when `ploegd` starts: `0036_route_refusals.sql` adds an index on `audit_log`, and `0036_run_evidence_version.sql` adds a nullable column to `agent_runs`.
- The chart no longer carries the homelab's defaults. An install names `database.existingSecret.name`. With the executor enabled, it also names the forge URL, `executor.scaler.host` for the `keda` and `sandbox` executors, and an agent image (`executor.harness.image`, or `harness.image` on a team or Role); rendering fails without them. [`ci/required-values.yaml`](ops/helm/ploeg/ci/required-values.yaml) shows a complete set.
- `nodeSelector` and the workers' node selector default to empty, `executor.runnerImage` is empty, `executor.dindImage` is the Docker Hub digest, and the Vikunja webhook secret is unset, so Vikunja webhooks are refused until you name it.

## 0.1.0 — independent baseline

New repository and module: `github.com/ploeg-hq/ploeg`. Source extracted from Unfold as documented in [PROVENANCE.md](PROVENANCE.md); standalone GitHub CI and independent releases. This version is experimental.

Earlier release history is preserved in [the historical changelog](docs/history/legacy-changelog.md). Those versions belong to the previous repositories and module.
