## Context

`run-multi-agent-shifts` shipped the blackboard and `close-the-review-loop`
taught Ploeg to act on it. What the blackboard still lacks is the arithmetic:
`publishRound` posts each reading Run's findings and `publishBudgetExhausted`
posts one notice when the pool empties, but the per-Run cost, the tokens, the
settled-versus-provisional state and the verification evidence are only in
Postgres. This change adds a report rendered from those records to the pull
request, so `#1305`'s question ("what did this cost and what was checked?") is
answered where the work is reviewed.

Read-only seams this leans on: `pkg/shiftengine/publish.go`,
`pkg/litellm/client.go` (`SpendLogs`, `Alias`), `cmd/ploegd/sweep.go`
(`managedSettlementSweep`), `pkg/provider/forgejo`. The accounting itself is
unchanged; ADR-0008 keeps LiteLLM the metering seam and this report never
crosses it.

## Goals / Non-Goals

**Goals:** one report comment per agent pull request, from stored state, updated
in place; per-Run usage and Shift totals; provisional/unavailable marked
honestly; verification evidence named and linked; a failure to publish never
changes the Shift.

**Non-Goals:** as in the proposal — no new accounting, no gateway call, no
billing statement, no CI reading, no tracker field or UI, no state stored for the
report itself.

## Decisions

### D1 — The renderer is pure; the inputs are already read

The whole artifact is one function whose input is already read:

```go
// usageReportInput is everything the report renders.
type usageReportInput struct {
    Shift    store.ShiftUsage // id, work item, team, branch, rounds, close reason, Runs
    Ledger   store.ShiftLedger
    TraceID  string           // writing Run's alias ploeg-<12hex>; "" if none
    Evidence Evidence         // what Ploeg observed of the writing Run's verification
    Links    reportLinkConfig // optional dashboard bases; empty omits the links
}

// Evidence is the writing Run's verification as stored; the zero value means
// the report renders "not recorded".
type Evidence struct {
    Result string // "passed" | "failed (<cmd>)" | "incomplete (<reason>)" | ""
    Commit string // short sha from the verification line; "" when absent
}

// usageReport renders the one body. Pure: no clock, no network, no store.
func usageReport(in usageReportInput) string
```

`store.ShiftUsage(ctx, shiftID)` reads `agent_runs` LEFT JOIN `run_llm_accounts`
plus `Ledger`, once. `RunUsage` carries `Role, Round, Writes, Outcome, Verdict,
Alias, Models, InputTokens, OutputTokens, CostUSD, Duration, AccountState`. The
gateway-settled figures (tokens, models, cost) come from `agent_runs.usage`
(`inputTokens`, `outputTokens`, `models`, `costUsd` — the keys
`ReconcileLLMAccountWithUsage` writes); `Duration` is derived from the stored
`started_at` and `finished_at`; the account state comes from
`run_llm_accounts.state` (`reserved|minting|issued|unknown|blocked|reconciled`,
migration 0012). Because both already exist, **no migration is needed** — the
rejected alternative below is the one that would have added one.

`Evidence` is the one input that is not a column, and the plan owes it a named
source. ADR-0035 has the worker append the verification to the writing Run's
`findings` markdown (the `### Ploeg verification` heading, whose first sentence
carries ``commit `<short-sha>` ``) and to its `summary` (`[Ploeg verification
passed]`, `[Ploeg verification failed: <cmd>]`, `[Ploeg verification incomplete:
<reason>]`), and migration 0009 keeps findings deliberately unstructured prose.
`publishUsageReport` derives `Evidence` from the `RoundReports` it reads for the
thread: `parseEvidence(reports)` takes the **last** Run with `Writes` —
the Run whose commit is at the branch tip — reads the result from its summary
marker and the commit from its findings section. The parser matches the literals
`pkg/worker/verify.go` writes (`verificationHeading`, the `commit \`…\``
sentence); a `pkg/worker` test already pins that rendering (`verify_test.go`) and
task 3.4 adds a byte-identical fixture, so a wording change fails both rather
than silently dropping the evidence.

Since VIK-1733 the prose is no longer the source when anything better exists.
An agent could write the same markers into its own summary and findings, and
the first match won. The worker now also sends a structured `verification`
record on the outcome (`outcomereport.v1.schema.json`), which ploegd stores in
`agent_runs.verification` (migration 0034) and `RoundReports` returns.
`parseEvidence` uses that record whenever the last writing Run has one, and
reads the prose only for a Run reported by an older worker, taking the last
marker and the last section, which are the ones the worker appended.

When that Run has no verification section — the worker did not verify it (a
reading Role, or an outcome other than `pr_opened`/`pr_updated`), the operator
configured no `verifyCommands`, or the Shift predates ADR-0035 (still
`proposed`) — `parseEvidence` returns the zero `Evidence`. The report then
renders "verification: not recorded": it never leaves a blank that reads as a
pass and never invents a commit.

### D2 — The report is identified by a marker on the forge, not by stored state

`const usageReportMarker = "<!-- ploeg:usage-report -->"`. Refresh is
`fp.Comments(repo, pr)` → find the comment whose body starts with the marker →
`fp.EditComment(...)`; if none, `fp.Comment(...)`. Nothing about the comment is
written back to Postgres. This is the R6-correct split: the report's identity is
forge state and its numbers are database state, and neither is shadowed in the
publisher. It also means a lost comment id is never a reason to post a second
one.

### D3 — The SPI gains two additive methods

```go
type Comment struct { ID int64; Body string }

// Comments lists a pull request's conversation comments, oldest-first.
Comments(ctx context.Context, repo string, pr int) ([]Comment, error)
// EditComment replaces the body of one existing comment.
EditComment(ctx context.Context, repo string, pr int, id int64, body string) error
```

Both in-tree forge providers implement them on the endpoints they already use
for `Comment`: Forgejo on `GET`/`PATCH /api/v1/repos/{owner}/{name}/issues/
{pr}/comments` and `.../issues/comments/{id}`, GitLab on the merge-request notes
it posts to (`GET /api/v4/projects/{id}/merge_requests/{iid}/notes`, `PUT
.../notes/{note_id}`). Everything forge-shaped stays behind the SPI (R7); the
core only sees `[]provider.Comment`. `ForgeProvider` is Ploeg's internal SPI
(`docs/design.md` §4), not a published wire contract, and the core already calls
both `Comment` (the findings path) and `PullRequestState`
(`pkg/shiftengine/review.go:78`); the two new methods are additive and both
in-tree implementations gain them in this change.

`Comments` returns the **whole** thread: the Forgejo and GitLab implementations
follow pagination to exhaustion (task 1.2/1.3), so a marker on any page is
returned and the core's find-by-marker scan cannot miss it. That completeness is
what makes "exactly one comment" true rather than best-effort — a marker the
reader cannot see is exactly how a second comment gets posted.

### D4 — Publishing is one engine method at three call sites

```go
func (e *Engine) publishUsageReport(ctx context.Context, si store.ShiftInfo)
```

It returns immediately when `!e.UsageReport`; loads `RoundReports` (the thread's
findings and the writing Run's evidence) and `ShiftUsage` (the numbers); resolves
the thread through the existing `pullRequestThread` (so a Shift with no PR, no
target or no provider skips exactly as findings do); renders; finds-or-edits. It
loads the reports itself rather than taking them as an argument so every call
site — fast path, close, settlement — derives the same evidence; one extra
`RoundReports` read per refresh is the price of that.

The three call sites, all after state is durable:

1. `pkg/shiftengine/engine.go` in `evaluate`, after `e.publishRound(...)`
   (currently line 159) — a Round just completed or a PR just appeared.
2. `pkg/shiftengine/engine.go` in `close`, in the `closed && work.Terminal(settled)`
   branch (currently ~line 282) — the Shift is over, so its totals are final
   enough to show.
3. `cmd/ploegd/sweep.go` in `managedSettlementSweep`, after `LLMControl.Settle`
   returns nil — a late settlement edits the same comment to the settled figure.
   This needs the Shift, so `UnsettledLLMAccount` gains `ShiftID` (from the
   existing `agent_runs.shift_id` join) and `ShiftEngine` gains
   `RefreshUsageReport(ctx context.Context, shiftID int64) error`, which loads the
   Shift and calls `publishUsageReport`; an account with a NULL
   `shift_id` (historical rows, migration 0008) is skipped. `Engine` already
   satisfies `ShiftEngine` in `pkg/httpapi`.

The report is published whether or not a Round produced findings, so it cannot
live *inside* `publishRound` (which returns early when `Findings` is empty); its
evidence section reads the writing Run's findings when they exist and says "not
recorded" when they do not.

### D5 — Nothing provisional is presented as settled

`Settled` is true only when every managed account is `reconciled`; otherwise the
report heads with "provisional until settlement" and marks each unreconciled
Run's cost and tokens `provisional`. A Run whose spend could not be read shows
`unavailable` — the report never substitutes the authorization for the cost. A
Run with no managed account (a reading Run does not mint one, ADR-0013) shows
its `agent_runs.usage` figures, which are what the harness reported, and no
account state.

### D6 — Failure is logged, never a lifecycle event

A failed `ShiftUsage` read, `Comments` list or forge write logs and returns; no
Outcome, close reason or Work Item state changes (R2, R3). If the list fails the
report is **skipped rather than posted blind**, because a comment we cannot find
is a duplicate waiting to happen on the next refresh. A killed pod records
nothing partial: the next refresh edits the one comment (R6).

### D7 — Chart values: a kill switch and the two link bases

The chart already renders a generic `env:` map onto the deployment
(`ops/helm/ploeg/values.yaml`: "Plain PLOEG_* env rendered onto the deployment"),
so the switch is `PLOEG_USAGE_REPORT: "true"` added there — **no
`values.schema.json` change**, unlike a new typed values key. The two link bases
ride the same map: `PLOEG_REPORT_GRAFANA_URL` and `PLOEG_REPORT_VLOER_URL`
(default `""`). `cmd/ploegd/main.go` reads the switch with the same
`envOr(...) != "false"` helper as `PLOEG_SHIFTS_UNIFORM`, into
`Engine.UsageReport`, and the URLs into `Engine.GrafanaURL`/`VloerURL`. These are
the only new desired state; rollback is a values edit that removes the env lines
(the code default is the same `true`), not a redeploy of anything but ploegd.
Default `true` because the outcome is "every agent PR"; the switch exists so a
noisy deployment can silence the report without a rollback, and unsetting a URL
drops its links.

### D8 — The report prints the alias, never a secret

The one identifier exposed is the writing Run's alias `ploeg-<12hex>` the
dashboards already join on (`pkg/litellm`). The report never prints a key value
or `gateway_key_id`; it is rendered from numbers and identifiers, and the forge
provider's existing rule (the token never appears in a logged error) is
unchanged.

## Rejected Trade-offs

- **Store the report's data — per-Run tokens or cost — in ploeg-db, or store the
  comment id in a new column.** Rejected because `agent_runs.usage` and
  `run_llm_accounts` already hold every figure, so the read needs **no
  migration**; a second copy would be a source of truth that can drift from the
  one the settlement engine writes, and it would need its own reconciliation.
- **Fetch spend from LiteLLM at render time.** Rejected: reading spend logs is
  `LLMControl.Settle`'s job and LiteLLM is the single metering seam (ADR-0008);
  duplicating it puts an authenticated, paid call on the publish path.
- **Post a fresh comment per Round or per refresh.** Rejected: a thread of
  near-identical reports is noise, and no single comment holds the current
  answer.
- **Piggyback on the writing Run's findings comment.** Rejected: findings are one
  Role's, the report spans the Shift; editing another Role's comment muddies
  attribution and the marker search.
- **Publish the report to the tracker instead of the pull request.** Rejected:
  ADR-0011 makes the pull request the blackboard; the tracker already gets a
  notification that links it.
- **A per-team opt-in** (mirroring `forgeFollowUps`). Rejected for now: the
  report is additive and non-destructive, so one global kill switch is enough
  surface; a Team that wants silence is better served by the global value until a
  real need appears.

## Risks / Trade-offs

- [Listing comments on every refresh is an API cost] → the refresh points are
  bounded by Round transitions and settlements; the list is complete (D3), so the
  marker is found wherever it sits, and the report is created at the first PR
  write so it is near the front of an oldest-first list. The cost is one paginated
  list per refresh, never per Run.
- [Two publishers race and both create a comment] → ploegd is a single
  controller (one process owns the fast path and the sweep), so there is one
  publisher. If ploegd is ever scaled out, the marker scheme needs a lock or a
  stored id — named in Open Questions rather than assumed away.
- [Provisional totals read as final] → the header and per-Run marks say
  provisional, and the settlement sweep edits the same comment when the number
  firms up.

## Migration Plan

None. No schema change, no new credential, no new network path. The report's
evidence is parsed from the writing Run's existing findings and summary prose
(D1), so it adds no storage either. The chart value defaults to on; setting it
false disables the publish call with no other effect. Rollback is the values
edit.

## Open Questions

- Rollout default (on at merge versus a staged opt-in) — owner's call.
- Whether the tracker notification should echo the totals; not proposed.
- Whether the report earns an ADR if it becomes the surface a human bills from.
- The single-publisher assumption above, if ploegd ever runs more than one
  replica.
- **Per-Run harness name.** The Work Item asks each Run to show its harness, but
  `agent_runs` has no harness column and the change forbids a migration
  (protected area). The Run's Role, Outcome and models are shown instead; a
  harness name would need either a new column or reading it from the Run's
  stored usage JSON, which the worker does not write today. Flagged for a
  follow-up rather than invented here.
