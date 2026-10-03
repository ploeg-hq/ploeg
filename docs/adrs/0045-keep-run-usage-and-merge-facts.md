---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg keeps every usage figure a harness reports and every merge and review fact a forge reports

## Context and Problem Statement

The proposed Run cards and the KPI set in `docs/reference/kpis.md` (repository root) need facts Ploeg receives today and then throws away.

* **Run usage.** The Claude Code adapter reads its result envelope and keeps four fields: input tokens, output tokens, cost and session id. It drops `num_turns`, `duration_ms`, `duration_api_ms`, the prompt cache token counts and the per-model `modelUsage` split (`pkg/harness/adapters/claudecode`). The ACP adapter counts tool calls and tracks the context-window fill in memory to explain a stop, and never reports either (`pkg/harness/adapters/acp`). `harness.Usage` and `outcomereport.v1` have no field for any of them.
* **Merge and review facts.** Forge webhooks are normalized with the review verdict and the actor, but the `forge.<kind>` audit row stores only repo, pull request and branch (`Store.AuditForgeEvent`). Nothing stores when a pull request merged, who merged it, its head commit or its merge commit. `work_item_reviews` holds only requests for changes, without the verdict or the head they were given on. The polling fallback in `ReviewWatch.Reconcile` reads only the state. Data ticket D1 in the KPI page asks for the review type, reviewer and head SHA.

Data that was never stored cannot be backfilled. What does Ploeg keep, and where, so cards and KPIs can be computed from Ploeg alone?

## Decision Drivers

* Unknown stays unknown. An absent figure is never stored or shown as zero; a reported zero stays zero.
* Additive only. `outcomereport.v1` and `operator-api.v1` are frozen except for optional fields (`docs/contracts/README.md`).
* The forge stays the source of truth for a pull request. Ploeg records what the forge said and never infers a merge.
* `work_item_reviews` is the queue of requests for changes a writing Round receives (ADR-0017). Recording other reviews there would send approvals to the writer.
* No new outbound calls on the hot path, except where a payload lacks a fact the forge's API has.

## Considered Options

* **Extend the existing records: optional fields in `harness.Usage`, a `pull_requests` and a `pull_request_reviews` table, and two columns on `work_item_reviews`**
* Store the raw harness envelope and raw webhook payloads, and derive facts later
* Keep facts only in `audit_log` detail
* Read the facts from the forge and the gateway when a card is rendered

## Decision Outcome

Chosen option: "**extend the existing records**", because it keeps each fact next to the record it describes, with types the operator API can expose directly.

1. **Usage.** `harness.Usage` and `outcomereport.v1` gain optional `cacheReadInputTokens`, `cacheCreationInputTokens`, `turns`, `durationMs`, `apiDurationMs`, `toolCalls`, `toolCallsByKind`, `peakContextTokens`, `contextWindowTokens` and `modelUsage`. Pointer fields stay nil when the harness did not report them. The Claude Code adapter fills them from its envelope. The ACP adapter reports its tool-call tally, its split by ACP tool kind, the peak `used` and the `size` of the context window. They reach `agent_runs.usage` unchanged, and gateway reconciliation keeps them.
2. **Model traffic.** A usage with turns or tool calls counts as model traffic in `pkg/worker`'s VIK-586 rule, so keeping the ACP tally cannot turn an agent error into `infra_llm`.
3. **Pull request facts.** Migration `0023_pull_request_facts.sql` adds `pull_requests` (forge, repo owner and name, number, Work Item, Shift, branch, state, head SHA, merge commit SHA, merged at, merged by, closed at) and `pull_request_reviews` (reviewer, verdict, head SHA, received at). A NULL is a fact the forge has not reported. A merged pull request stays merged and keeps its first merge facts. Only pull requests on a Ploeg branch, or already recorded, are stored.
4. **Sources.** The forge webhook handler records facts for `review_submitted`, `pr_merged` and `pr_closed`. When a merge or close payload lacks a fact, it reads the pull request once (`ForgeProvider.PullRequestFacts`). `ReviewWatch.Reconcile` records the facts of a merge or close it finds by polling. Forgejo and GitLab parse the same facts.
5. **Audit.** The `forge.<kind>` audit row adds `actor`, `review`, `head_sha`, `merge_commit_sha`, `merged_by` and `merged_at` when the forge reported them. The review body stays out.
6. **Requests for changes.** `work_item_reviews` gains `state` (existing rows are `changes_requested`) and `head_sha`.
7. **Operator API.** The Run object's `usage` (`/runs/{id}` and work-item detail Runs) exposes the new figures. `item.pullRequest` gains `mergedAt`, `mergedBy`, `headSha`, `mergeCommitSha` and `reviews[]` (reviewer, state, head SHA, received at). Unknown fields are left out. The Run list keeps its current usage shape.

Proposed, not implemented: recording the head SHA when a Work Item enters `awaiting_review` (the rest of D1), and Vloer showing any of these facts.

### Consequences

* Good, because cards and K5 can tell an approval from a request for changes, and merge time from review wait, using Ploeg's own tables.
* Good, because the facts start accumulating from deployment, before any card is built.
* Good, because no stored value means zero by accident: a reader can tell "not reported" from "none".
* Bad, because `ForgeProvider` gains a method every forge provider and fake must implement.
* Bad, because a merge or close event without full facts in the payload, such as a GitLab merge without `merged_at`, costs one forge read.
* Bad, because pull requests merged before this lands have no facts. Accepted: there is nothing to read them from except the forge, and a backfill can be a one-off script if a KPI needs it.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/harness/adapters/claudecode`: a full envelope fixture (`testdata/result_full.json`) keeps every figure, a minimal envelope adds none, and a reported zero stays zero.
* `pkg/harness/adapters/acp`: the tool tally folds updates into their call, counts calls past the tracking cap, and reports the peak context fill. A silent agent still yields nil usage.
* `pkg/harness`: `outcomereport.v1` accepts every new usage field and rejects unknown or negative ones.
* `pkg/worker`: tool calls without tokens are not relabelled `infra_llm`.
* `pkg/provider/forgejo`, `pkg/provider/gitlab`: payloads and API reads yield the merge facts, head and verdict.
* `pkg/store`: migration 0023 applies; facts resolve their Work Item from the branch, never erase a known value, and a merged pull request stays merged.
* `pkg/shiftengine`: `Reconcile` records the facts of a merge and a close it finds.
* `pkg/httpapi`: a review webhook keeps the reviewer and verdict in the audit row and as a review fact (regression); a merge webhook stores its facts; `/runs/{id}` and work-item detail match `operator-api.v1` with the new fields, and leave them out when unknown.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Store the raw envelope and raw webhook payloads

* Good, because nothing is lost, including fields nobody asked for yet.
* Bad, because webhook bodies carry text written outside the factory (backlog #9), and every reader would parse two forges' dialects again.
* Bad, because raw payloads grow without bound and the operator API would still need the typed fields.

### Keep facts only in `audit_log` detail

* Good, because it needs no migration.
* Bad, because audit rows are unbound to the Work Item for forge events, so every card would need a branch join over the whole log.
* Bad, because "the newest merge fact for this pull request" becomes a query over free-form JSON.

### Read the facts when a card is rendered

* Good, because it stores nothing.
* Bad, because harness usage exists only in the Run's report, so it cannot be read later.
* Bad, because Vloer does not read the forge, and Ploeg would make one forge call per card.

## Re-evaluation triggers

* A third forge provider is added. Check that `PullRequestFacts` fits it without forge-specific fields.
* Claude Code or ACP changes its usage shape (a renamed envelope field or a new `usage_update` shape). The adapter tests fail first.
* The first card or KPI query is built on these tables. Check that it needed nothing this record left out.
* `pull_request_reviews` passes 100 000 rows. Revisit indexes and retention.

## More Information

* KPI proposal and data ticket D1: `docs/reference/kpis.md` at the repository root.
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md): why `work_item_reviews` holds only requests for changes.
* [ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md): the harness return path that carries usage.
* [ADR-0011](0011-the-pull-request-is-the-blackboard.md): the pull request stays the forge's record; Ploeg only notes what the forge said.
