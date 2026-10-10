# GitLab-native agent work × Ploeg: triggers in, draft merge requests out

Date: 2026-10-10 · Decision record: none yet

> Method. Ploeg internals were read from source on `development` at `34e6d57`
> (`pkg/provider`, `pkg/worker`, `pkg/httpapi`, `pkg/work`, `cmd/ploegd`, the
> contracts, ADRs 0013, 0016, 0023, 0024, 0026, 0034, 0038, 0055, 0058, 0059,
> 0060, 0065, 0067, 0069, 0070 and the `open-change-requests-on-gitlab`
> OpenSpec change). GitLab behaviour was checked against GitLab's documentation
> on 2026-10-09 and 2026-10-10: webhook events and headers, signing, auto-disable
> thresholds, service accounts, roles, the merge request and notes APIs, closing
> keywords, `CI_JOB_TOKEN`, pipeline triggers and Duo Agent Platform triggers.
> Claims still marked **UNCONFIRMED** must be checked against the cited page
> before anyone builds on them. Claims about Ploeg cite the file they were read
> from.

## Question

How should GitLab-native autonomous agent work connect to Ploeg? A GitLab issue
label, an assignment, an `@mention` comment, a merge request comment or a
failing pipeline starts a Ploeg Run, and the Run comes back to GitLab as a draft
merge request with its run card. What does Ploeg already have, what is missing,
and what has to hold for it to be safe?

## Short answer

**Ploeg already speaks GitLab as a forge, in both directions, for work it
started itself. It does not speak GitLab as a tracker, so nothing on GitLab can
start new work today.** The outbound half (open a merge request, comment, read
pipeline status, post the run card, settle on merge) exists and is tested
against fakes. The inbound half handles only events on branches Ploeg already
owns: a failed pipeline can become a repair Follow-Up, and merges and closes
settle the Work Item.

Recommendation, in order:

1. **Where work is planned in a tracker Ploeg already supports (Vikunja,
   ClickUp), keep that tracker as the trigger and use GitLab as the forge
   (option D).** It needs no new adapter: assignment already queues a Work Item,
   a `repo/<name>` tag already selects a registered GitLab target
   ([ADR-0038](../adrs/0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md),
   `pkg/config/clickup_routing_test.go`), and the worker already opens the merge
   request on GitLab (`pkg/worker/task.go`, `pkg/worker/forge.go`). What remains
   is a draft title, a tracker-compatible reference and webhook dedup.
2. **For GitLab-native triggers, add a GitLab `TrackerProvider` behind the
   existing `/webhooks/tracker/{provider}` route (option A)**, triggered by a
   label or an assignment to the Ploeg service account, and only after
   [ADR-0060](../adrs/0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)'s
   inbox lands, because GitLab deliveries are not deduplicated today.
3. **Do not route GitLab triggers through a CI job acting as an operator
   consumer (option B) for unattended work.** The operator API admits
   *delegated* executions that the consumer runs itself; it has no route that
   queues an unattended Run, and the delegated path has no live publisher
   ([architecture §6](../architecture.md), [operator delivery](../contracts/operator-delivery.md)).
4. **Copy Duo's trigger idiom, not its runtime (option C).** Assigning or
   mentioning a service account is a good trigger; running the agent inside
   GitLab's runtime bypasses Ploeg's gateway budget, for the same reason as
   [ADR-0041](../adrs/0041-the-openai-agents-api-stays-outside-the-run-until-it-takes-an-authorized-budget.md).

## 1. What Ploeg already has

| Capability | Status | Where |
| --- | --- | --- |
| GitLab `ForgeProvider`: MR note, list notes (paginated), edit note, MR state and facts (draft, author, merge user, squash SHA) | Implemented | [pkg/provider/gitlab/gitlab.go](../../pkg/provider/gitlab/gitlab.go) |
| Commit status reader (every job and external status, latest per name, combined) | Implemented | `CommitStatus` in the same file |
| Ancestry, changed paths, activity (notes, versions, pipelines and jobs), card image upload to `/projects/:id/uploads` | Implemented | `pkg/provider/gitlab/{ancestry,changed_paths,activity,attachment}.go`; [ADR-0055](../adrs/0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md), [ADR-0058](../adrs/0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md) |
| Repository readiness for GitLab targets, subgroup paths, pull mirrors | Implemented | `pkg/provider/gitlab/repository.go`, [route a multi-repo board](../how-to/route-a-multi-repo-board.md) |
| Inbound forge webhook `POST /webhooks/forge/gitlab`, `X-Gitlab-Token` compared in constant time | Implemented | `ParseWebhook` in `gitlab.go`; route in [server.go](../../pkg/httpapi/server.go) |
| Webhook kinds normalised: MR open/reopen/update(push)/merge/close/approved, `cannot_be_merged`, MR notes, failed pipelines | Implemented | `ParseWebhook`; issue notes and non-failed pipelines are dropped |
| Failed pipeline on a Ploeg branch → repair Follow-Up | Implemented, opt-in per team (`teams.<name>.forgeFollowUps`) | [forge_followup.go](../../pkg/httpapi/forge_followup.go) |
| Worker opens the change request on GitLab | Implemented: the briefing tells the agent to `POST /api/v4/projects/:id/merge_requests`; the worker reads it back by `source_branch` | `openChangeRequestInstruction` in [task.go](../../pkg/worker/task.go), `listGitLabMergeRequests` in [forge.go](../../pkg/worker/forge.go); [ADR-0023](../adrs/0023-the-forge-dialect-travels-on-the-work-item.md) (proposed), OpenSpec `open-change-requests-on-gitlab` (all tasks done but ratification of ADR-0023) |
| Forge token proxy: the harness reaches only its own project; the writer may `POST merge_requests`, `PUT merge_requests/:iid` and post MR and issue notes | Implemented, opt-in (`PLOEG_FORGE_TOKEN_ISOLATION=proxy`) | `gitlabWriterEndpoints` in [forgeproxy.go](../../pkg/worker/forgeproxy.go); [ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) |
| Delivery facts read from the forge, never from the agent | Implemented | [ADR-0059](../adrs/0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) |
| `awaiting_review` only when checks passed on the pushed commit | Implemented (worker-run checks, not GitLab CI) | [ADR-0070](../adrs/0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md) |
| Run card as one marked MR comment (`<!-- ploeg:run-card -->`) | Implemented, opt-in `teams.<team>.cards.prComment` | [pkg/cardimage/comment.go](../../pkg/cardimage/comment.go), ADR-0055 |
| ClickUp tracker: HMAC `X-Signature`, assignee/status/priority events, tags as labels, comments, done status | Implemented | [pkg/provider/clickup/clickup.go](../../pkg/provider/clickup/clickup.go) |
| Repo tag selects among registered targets, including GitLab ones | Implemented | ADR-0038, `pkg/config/clickup_routing_test.go` (`forge: gitlab`) |
| Operator API: bearer consumers, `X-Ploeg-Actor`, delegated execution admission, tracker binding for Vikunja and ClickUp | Implemented | [operator.go](../../pkg/httpapi/operator.go), [operator_execution.go](../../pkg/httpapi/operator_execution.go), [tracker execution](../contracts/tracker-execution.md) |
| Knowledge pack and context bundles per Work Item | Implemented | [ADR-0065](../adrs/0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md), [ADR-0067](../adrs/0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md) |

## 2. The gap

Read from the code:

1. **No GitLab tracker.** The tracker registry in [cmd/ploegd/main.go](../../cmd/ploegd/main.go) holds Vikunja and, when configured, ClickUp. There is no `TrackerProvider` for GitLab issues, so a label, an assignment or a mention on a GitLab issue cannot create a Work Item. `ParseWebhook` in the forge provider drops notes whose `merge_request.iid` is 0, which is every issue comment.
2. **MR comments never start or steer work.** A GitLab MR note becomes `ForgeReviewSubmitted` with `Review: commented`. `actOnForgeEvent` acts only on `changes_requested`, which the GitLab parser never emits (`approvalState` maps only `approved`). On GitLab a reviewer's comment, `@mention` or "request changes" sends nothing back to the writer; only failed pipelines do.
3. **Events outside Ploeg's branches are ignored.** `forgeEventOwner` looks up the branch owner; a failing pipeline on a human's branch logs "not on a Ploeg branch" and stops. Starting work from an arbitrary failed pipeline is new behaviour.
4. **No draft.** The GitLab briefing asks for `source_branch`, `target_branch` and `title`; nothing asks for a draft and the worker never sets one. GitLab's create-MR API has no `draft` parameter: a draft is made by prefixing the title with `Draft:` (or `[Draft]`/`(Draft)`) or with the `/draft` quick action ([MR API](https://docs.gitlab.com/api/merge_requests/), [drafts](https://docs.gitlab.com/user/project/merge_requests/drafts/)). ADR-0058 reads the draft flag; nothing writes it.
5. **The reference format is Ploeg's own.** `work.Reference` yields `clickup-<id>` and `work.Branch` yields `agent/clickup-<id>` ([pkg/work/branch.go](../../pkg/work/branch.go)). GitLab's ClickUp integration recognises `CU-<id>`, `#<id>` and custom task ids, not `clickup-<id>` ([GitLab ClickUp integration](https://docs.gitlab.com/user/project/integrations/clickup/)), so the MR is never linked back to its task. The same applies to any tracker whose forge integration expects its own id format.
6. **Shared, long-lived GitLab write token.** Per-Run push credentials ([ADR-0013](../adrs/0013-push-rights-are-minted-per-run.md) tier 2) exist only for Forgejo; the configuration reference says so for `executor.gitlab.tokenSecret` ([configuration.md](../reference/configuration.md)). One worker pod holds one forge URL and one credential (`executor.forge`).
7. **GitLab deliveries are not deduplicated.** The handler reads only `X-Forgejo-Delivery` and `X-Gitea-Delivery`; a GitLab delivery reaches `SeenDelivery` with an empty id and is treated as fresh. ADR-0060 (proposed) fixes this with a `webhook_inbox`; no inbox code exists yet.
8. **Tracker binding covers Vikunja and ClickUp only** ([tracker execution](../contracts/tracker-execution.md)). A GitLab issue cannot be bound to an operator execution.
9. **Delegated executions cannot publish.** Candidate verification and approval exist; "the default deployment performs no live forge publication" ([operator delivery](../contracts/operator-delivery.md)).
10. **No decision names the GitLab identity that authors agent MRs.** Ploeg filters its own events by a configured bot; which kind of GitLab identity that bot is (service account, access-token bot, person) is undecided.

The shape of the gap: **intake and trigger policy are missing; delivery is mostly there.** Delivery needs three small changes (draft title, reference format, dedup), intake needs one adapter and a trust policy.

## 3. Design options

### A. GitLab webhook → Ploeg tracker adapter

A `pkg/provider/gitlabissues` (or a tracker face on `pkg/provider/gitlab`) implements `TrackerProvider`: `ParseWebhook` turns an Issue event whose labels gained the trigger label (for example `ploeg::run`), or whose assignees gained the Ploeg service account, into `TrackerAssigned`; a removal into `TrackerUnassigned`; a close into `TrackerClosed`. `FetchItem` reads `GET /projects/:id/issues/:iid`; `Comment` posts an issue note; `SetStatus` swaps scoped labels. The Scope is the project path, so routing needs no board mapping. A label change arrives as `Issue Hook` with `action: update` and `changes.labels.previous/current` ([webhook events](https://docs.gitlab.com/user/project/integrations/webhook_events/)).

- Good: the thin-payload rule holds (the webhook triggers, the API read is truth); same admission, budgets and run card as every other Work Item; GitLab audits label and assignee changes itself.
- Good: `Closes #<iid>` (also Fixes, Resolves, Implements; case-insensitive) in the MR description closes the issue when the MR merges into the default branch, unless the project turned off "Auto-close referenced issues on default branch" ([managing issues](https://docs.gitlab.com/user/project/issues/managing_issues/)).
- Bad: a second webhook per project (tracker path beside forge path), or a provider that fans one delivery into both; it needs ADR-0060's inbox for dedup and retries first.
- Bad: where work is planned in another tracker, GitLab issues become a second backlog.

### B. GitLab CI job as an operator consumer

A pipeline started by a label or comment (through a webhook to a pipeline trigger) calls `POST /api/v1/operator/executions` with a consumer bearer token and `X-Ploeg-Actor`. Read-only jobs that only comment, such as a merge request summary, fit this shape and already exist in deployments: the job admits an execution with a small budget, takes the per-execution gateway key from `/credential`, and posts its own note with its own GitLab token, because an operator-admitted Work Item has no resolved Target for `publishRound` to publish to.

- Good: no inbound path from GitLab.com to the cluster; GitLab enforces who can run the pipeline.
- Bad: admission creates a *delegated* execution; the CI job must run the harness itself, and the delegated path cannot publish. Ploeg is reduced to a budget broker for the job.
- Bad: a pipeline trigger token or CI variable holding a Ploeg operator bearer sits in GitLab; anyone who can edit `.gitlab-ci.yml` on a branch can read it unless it is a protected variable. A trigger token runs with its creator's permissions ([triggers](https://docs.gitlab.com/ci/triggers/)), and `CI_JOB_TOKEN` can only *read* MRs and notes, so a job cannot comment or open an MR without a separate `api` token ([job token](https://docs.gitlab.com/ci/jobs/ci_job_token/)).
- Possible variant: a new operator route that *queues* an unattended Work Item (manual origin) instead of admitting a delegated execution. That is an additive contract change and an ADR.

### C. Duo-style service account

GitLab Duo Agent Platform's external agents are triggered by mentioning or assigning a service account (or assigning it as reviewer) on an issue or MR; GitLab then runs a CI pipeline that receives `AI_FLOW_CONTEXT` (the issue or MR as JSON), `AI_FLOW_INPUT` (the comment), `AI_FLOW_EVENT`, `AI_FLOW_GITLAB_TOKEN` and related variables. They need Premium or Ultimate, "all trigger event types require a human user", and the agent's effective role is the stricter of the triggering user's role and Developer ([triggers](https://docs.gitlab.com/user/duo_agent_platform/triggers/), [external agents](https://docs.gitlab.com/user/duo_agent_platform/agents/external/), [security](https://docs.gitlab.com/user/duo_agent_platform/security)).

- Good: assignment is the idiom Ploeg already uses for trackers, and a human trigger plus a capped role is the right trust model. Option A should adopt both.
- Bad: Duo's runtime and gateway token sit outside Ploeg's gateway budget, so only the trigger idiom is worth copying, not the runtime.

### D. Existing tracker as trigger, GitLab as forge

Assign a tracker task (Vikunja or ClickUp) to the agent user or tag it, Ploeg ingests it, routes it by tag or list to a GitLab target, and the Run opens the MR on GitLab.

- Good: everything on the intake side exists and is tested (`pkg/provider/clickup`, `pkg/provider/vikunja`, ADR-0038), and work is triggered where it is planned.
- Good: tracker binding exists, so the same task can later go to a workbench.
- Bad: not GitLab-native; MR comments still do not steer (gap 2).
- Needed: draft title, a tracker-compatible reference in the branch or MR title (gap 5; for ClickUp `CU-<id>`), and the GitLab token and webhook secret in the deployment. ClickUp's own GitLab integration links an id found in a commit message or the MR, and needs it in a commit message or the MR title for status updates (help page not fetchable, so **UNCONFIRMED** in detail).

## 4. Trust and security

- **Who may trigger.** Issue and comment text reaches the model, so a trigger must come from a person with write access. Editing an issue's labels or assignees needs the **Developer** role, as does creating a merge request; a Guest can comment ([permissions](https://docs.gitlab.com/user/permissions/)). A label or assignment trigger is therefore Developer-gated by GitLab itself. A `@mention` is not, so a mention trigger must re-check the actor's role ≥ Developer through `GET /projects/:id/members/all/:user_id` before admission. Events whose actor is the Ploeg bot itself are ignored.
- **Injection is a live attack.** In April 2026 "Comment and Control" used PR titles, issue bodies and comments to make Claude Code Security Review, Gemini CLI Action and GitHub Copilot Agent run commands and leak their API keys ([SecurityWeek](https://www.securityweek.com/claude-code-gemini-cli-github-copilot-agents-vulnerable-to-prompt-injection-via-comments/)); GitLab Duo leaked private source through a hidden MR prompt in 2025 ([Legit Security](https://www.legitsecurity.com/blog/remote-prompt-injection-in-gitlab-duo)). GitLab's own prompt-injection protection defaults to "Log only" on GitLab.com and does not cover external agents ([security threats](https://docs.gitlab.com/user/duo_agent_platform/security_threats/)). Ploeg's answers are already the right ones and must stay on: the harness gets placeholders while the worker keeps credentials (ADR-0034), the forge proxy limits the harness to its own project, context ranks below the delivery contract ([ADR-0030](../adrs/0030-target-repository-instructions-rank-below-the-delivery-contract.md)), and no secret sits in the worker's environment or process list. The proxy does not limit branches, so GitLab protected branches are the backstop and must be on for the default branch.
- **Identity.** Use a GitLab **service account** for the bot: any GitLab.com tier can create them (Free up to 100 per top-level group), only top-level group Owners create them, they use no seat, they take a role per group or project, and they authenticate with their own personal access token ([service accounts](https://docs.gitlab.com/user/profile/service_accounts/)). Project and group access-token bots need Premium or Ultimate on GitLab.com ([project tokens](https://docs.gitlab.com/user/project/settings/project_access_tokens/), [group tokens](https://docs.gitlab.com/user/group/settings/group_access_tokens/)). Give it Developer on the target projects only, and separate read from write grants.
- **Webhook authenticity.** `X-Gitlab-Token` is a shared secret echoed verbatim, not a body MAC (comment in `gitlab.go`). GitLab now signs deliveries: a signing token (introduced in 19.0, generally available in 19.1) adds `webhook-signature` (space-separated `v1,<base64>` HMAC-SHA256 over `webhook-id.webhook-timestamp.body`), `webhook-id` and `webhook-timestamp`, following the Standard Webhooks spec ([webhooks](https://docs.gitlab.com/user/project/integrations/webhooks/)). The provider should verify that signature, reject stale timestamps, and keep `X-Gitlab-Token` only as a fallback.
- **Retries, dedup and speed.** GitLab disables a hook temporarily after **4** consecutive failures (from 1 minute up to 24 hours) and permanently after **40**; a timeout counts as a failure, and GitLab.com waits **10 seconds** ([webhooks](https://docs.gitlab.com/user/project/integrations/webhooks/), [GitLab.com settings](https://docs.gitlab.com/user/gitlab_com/)). Answer 2xx fast and do the work from the inbox. Dedup on `webhook-id`, which stays the same across retries of one delivery (`Idempotency-Key` is the legacy name); `X-Gitlab-Event-UUID` identifies the event for non-recursive hooks.

## 5. Lessons for anyone wiring this up

- **The forge half is not the hard part.** Ploeg already opens, reads and settles GitLab MRs. The work is intake, trust and credentials.
- **Arm credentials before routes.** Routing a tracker to a GitLab target is a few lines of configuration; it does nothing until the GitLab token, the webhook secret and the tracker webhook are in place. Make the token-seeding path the first deliverable, not the last.
- **Draft is a title, not a flag**, and ready-for-review should follow ADR-0070's verified checks, not the agent's say-so.
- **Speak the tracker's reference format.** A branch or MR title the tracker's integration cannot parse silently breaks the link back to the task.
- **A label or an assignment is a better trigger than a mention**: GitLab already restricts it to Developers and audits it. Mentions need a role check of their own.
- **Copy Duo's trigger model** (human trigger, capped role, service account), **not its runtime**, which would sit outside the budget.
- **Treat every issue body and comment as hostile input.** The 2025–2026 incidents all came through exactly these fields.
- **Keep the first experiment small**: a handful of well-scoped, well-described issues, measured on mergeable-without-rework, reviewer minutes and cost per MR, before building GitLab-native intake.

## 6. Minimum viable slice

Option D, plus the outbound fixes:

1. **Configuration**: `executor.forge: gitlab`, `executor.gitlab.{url,tokenSecret,readTokenSecret,webhookSecret}`, `PLOEG_GITLAB_BOT`, a tracker list or tag route to the GitLab target, the tracker webhook and secret, a team with a configured `verify` command, `cards.prComment: true`, `forgeFollowUps.repairFailedChecks: true`, and a GitLab project webhook for MR, note and pipeline events to `/webhooks/forge/gitlab`.
2. **Draft MR** (small code change): the GitLab briefing prefixes the title with `Draft:`, and the worker removes the prefix (`PUT merge_requests/:iid`, already allowed by the proxy) only when ADR-0070 verification passed. A regression test in `pkg/worker`.
3. **Reference format** (small code change): a per-provider reference so ClickUp items produce `CU-<id>` in the branch or MR title, keeping `VIK-<id>` byte for byte. Changes `pkg/work/branch.go`; existing branches keep their names because Follow-Ups use `SourceBranch`.
4. **Dedup for GitLab** (small code change, or ADR-0060's inbox): read `webhook-id` (fall back to `Idempotency-Key`) in `handleForgeWebhook`, so retries do not create a second repair Follow-Up.
5. **Signature check**: verify `webhook-signature` when a signing token is configured.
6. Leave GitLab issue triggers, MR-comment steering and per-Run GitLab tokens for after the experiment, recorded as follow-ups.

## 7. Open questions

- Which identity authors agent MRs on GitLab: a service account per deployment, or per team? It needs a decision record (gap 10).
- Should GitLab "request changes" (reviewer state) and MR `@mention` notes map to `changes_requested` so the review loop works on GitLab (gap 2)?
- Should a read-only MR summary from a CI job (option B) and the run card coexist on agent MRs, or does the run card replace it there?
- ADR-0023 is still `proposed` although its code shipped; ratify it before relying on GitLab delivery.
- Per-Run GitLab push credentials (ADR-0013 tier 2) for GitLab, or is a Developer-scoped service account with protected branches enough?

## Sources

GitLab documentation, checked on 2026-10-09 and 2026-10-10: [webhooks](https://docs.gitlab.com/user/project/integrations/webhooks/) (headers, signing token, auto-disable), [webhook events](https://docs.gitlab.com/user/project/integrations/webhook_events/), [GitLab.com settings](https://docs.gitlab.com/user/gitlab_com/), [service accounts](https://docs.gitlab.com/user/profile/service_accounts/), [project access tokens](https://docs.gitlab.com/user/project/settings/project_access_tokens/), [group access tokens](https://docs.gitlab.com/user/group/settings/group_access_tokens/), [roles and permissions](https://docs.gitlab.com/user/permissions/), [merge requests API](https://docs.gitlab.com/api/merge_requests/), [drafts](https://docs.gitlab.com/user/project/merge_requests/drafts/), [notes API](https://docs.gitlab.com/api/notes/), [closing issues](https://docs.gitlab.com/user/project/issues/managing_issues/), [CI job token](https://docs.gitlab.com/ci/jobs/ci_job_token/), [pipeline triggers](https://docs.gitlab.com/ci/triggers/), [Duo triggers](https://docs.gitlab.com/user/duo_agent_platform/triggers/), [Duo external agents](https://docs.gitlab.com/user/duo_agent_platform/agents/external/), [Duo security threats](https://docs.gitlab.com/user/duo_agent_platform/security_threats/), [GitLab ClickUp integration](https://docs.gitlab.com/user/project/integrations/clickup/).

Other: [Claude Code GitLab CI/CD](https://code.claude.com/docs/en/gitlab-ci-cd), [OpenHands GitLab](https://docs.openhands.dev/openhands/usage/cloud/gitlab-installation), [ClickUp webhooks](https://developer.clickup.com/docs/webhooks), [Comment and Control (SecurityWeek)](https://www.securityweek.com/claude-code-gemini-cli-github-copilot-agents-vulnerable-to-prompt-injection-via-comments/), [Remote prompt injection in GitLab Duo (Legit Security)](https://www.legitsecurity.com/blog/remote-prompt-injection-in-gitlab-duo).

Ploeg (read on `development`, `34e6d57`): files and ADRs linked inline.
