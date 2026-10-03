# Design — run multi-agent Shifts end to end

## Context

The store layer this change orchestrates is built and tested
(`pkg/store/shift.go`, migration `0008_shifts.sql`) and has **zero callers**.
The proposal names what must connect: Shift lifecycle in ploegd, role-scoped
claims, role-aware Runs, the blackboard, the first `ForgeProvider`, tracker
write-backs, and role-partitioned workloads. This document records the
change-local decisions of *how*, within ADR-0010..0013. ADR-0015/0016 are
`proposed`, not in force — nothing here designs against them, and
`provider.TrackerEvent.Team` stays where it is (its removal is SPI-breaking
and belongs to a later change).

Implementation-relevant facts verified against the code (architecture.md §9
checked both directions):

- Nothing closes a Shift: `shifts.closed_at`/`close_reason` are read in two
  WHERE clauses and written nowhere. With `shifts_one_live_per_item`, an
  unclosable Shift permanently blocks re-mandating its Work Item.
- Nothing enumerates Shifts: `RoundComplete` wants a `shiftID` no caller has.
- Three latent defects in the unused layer, fixed here because this change
  gives them their first caller:
  1. `ClaimRole`'s `RETURNING` omits the `target_*` columns that legacy
     `Claim` returns — a role-claimed Run would silently fall back to the
     worker's env repo, defeating ADR-0014's resolved Work Target.
  2. `Store.Renew` extends only `leases` — a reading Run holds no Lease, so
     its first renew 404s and the worker cancels itself at TTL/3.
  3. `Store.Checkpoint` resolves the Work Item via `leases` — readers cannot
     checkpoint.
- The branch has no server-side producer: the worker invents
  `agent/vik-<externalID>` (`pkg/worker/worker.go`), while `shifts.branch`
  waits for someone to fill it.
- `harness.TaskSpec.Role` has existed, published and empty, since the seam
  was carved.

## Goals / Non-Goals

**Goals:** everything in `proposal.md` §What Changes, delivered so that a
plan-less Team renders and behaves byte-identically to today at every
intermediate merge.

**Non-Goals** (beyond the proposal's own list): verdict-driven round
advancement — a fixed plan runs to exhaustion here; conditional re-opening on
a reviewer verdict is the follow-up change `close-the-review-loop`, which owes
the ledger a new record. Also out: the forge webhook *route* (the parser lands
with the provider; ingesting live Forgejo events is follow-up work), ADR-0013
tier 2, and any `PLOEG_TEAM_MAP`/`TrackerEvent.Team` surgery.

## Decisions

### D1 — Shift opening is after-commit plus sweeper repair, not in-transaction

`handleTrackerWebhook` calls an idempotent `EnsureShift(item)` after
`IngestAssigned` commits, only when the item landed `queued`; the sweeper tick
re-runs the same `EnsureShift` over queued items of planned Teams that lack a
live Shift.

*Why not inside `IngestAssigned`'s transaction:* atomicity would push plan
config (a ploegd concern) into `pkg/store` and force `OpenShift` onto a
`pgx.Tx`, to close a crash window this codebase already has a named answer
for — the claim/sweeper split (R2). A crash between commit and `EnsureShift`
leaves a queued item with no Shift; the next 15s sweep repairs it. The race of
two openers is settled by `shifts_one_live_per_item` at the database, exactly
as the spec's second scenario demands.

### D2 — One advancement engine, called from two places

A new `pkg/shiftengine` owns every lifecycle rule: `EnsureShift`,
`Evaluate(shiftID)` (round complete → close on terminal Outcome / open next
planned Round / close on plan exhaustion → `needs_human` + write-backs), and
`EvaluateAll()`. `handleOutcome` calls `Evaluate` synchronously after a
successful report — the fast path; errors are logged, never returned to the
worker. The sweeper tick calls `ExpireRuns` (dead readers finally get
reclaimed — today's loop only expires *leases*), then `EvaluateAll`, then
parks below-floor Shifts. Every crash half-state (queued-without-Shift,
round-complete-not-advanced, terminal-not-closed, pool-below-floor, expired
reader blocking a round) must be provably repaired by `EvaluateAll` — that is
the test suite to over-invest in, because R2 says the pipeline never depends
on an agent (or a request handler) behaving well.

Store additions this needs: `CloseShift(id, reason)` (also finishes the
Shift's leftover `pending` runs so the scale signal drops to zero),
`LiveShifts()`, `LiveShiftForItem(workItemID)`, and a compare-and-swap guard
on `OpenRound` (`fromRound` parameter; the current unconditional
`round = round + 1` lets two evaluators double-advance). `OpenRound` has no
callers, so the signature change is free.

### D3 — For Shift runs, the engine owns the Work Item transition

`ReportOutcome` today transitions `work_items` per `work.StateForOutcome`.
With a reader fan-out that is wrong three times over: each of three reader
reports would flip the item's state mid-Shift. For runs with a non-null
`shift_id`, `ReportOutcome` records the run outcome and settles spend but
leaves the Work Item alone; the engine moves the item exactly once, at Shift
close. Legacy (shift-less) runs keep today's behaviour bit-for-bit.
`ReportOutcome` returns an `OutcomeResult{WorkItemID, ShiftID}` so the
handler knows whether to call `Evaluate`.

### D4 — Findings are a markdown string on the existing report

`OutcomeReport` gains `findings` (string, optional) — additive in
`outcomereport.v1`, mirrored in `contract_test.go`, persisted to a new
`agent_runs.findings` column (migration `0009`). A structured
`[]Finding` shape was considered and rejected: both consumers (a PR comment,
a prompt section) want prose, and a schema for opinions is a larger claim
than ADR-0011 makes — "the PR carries prose; the database carries numbers."
`Store.RoundReports(shiftID)` returns prior rounds'
`{role, round, writes, outcome, summary, findings, links}` and serves both
consumers; the claim response carries it to the worker as the briefing, so
agents gain no new tooling (R6, blackboard spec).

### D5 — Team plans are ploegd boot config: `PLOEG_TEAM_PLANS`

Helm `executor.teams[].plan[]` is rendered by the chart (`toJson`) into a
single `PLOEG_TEAM_PLANS` env on the ploegd deployment — plans are config
read at Round-open time (proposal), and ploegd is the only reader. A new
`pkg/plan` parses and validates at boot, failing fast like
`PLOEG_TARGET_MAP`: every Round all-readers or exactly-one-writer (mirroring
`OpenRound`'s refusal), role names DNS-label-safe (they become workload name
suffixes), caps ≥ 0. The pool is the team's existing `budget` value; a pool
of 0 keeps `ClaimRole`'s unmetered semantics.

### D6 — ploegd becomes the branch producer

`EnsureShift` derives `agent/vik-<externalID>` — the same string the worker
derives today — and stores it on the Shift; the claim response carries it;
the worker prefers the served branch and falls back to local derivation on a
legacy claim. Rollout is therefore a no-op; neutral branch naming stays
backlog #107.

### D7 — The minted key honours the authorization

The worker mints its LiteLLM key with `ClaimedRun.Authorized` when it is
positive, else the env `LITELLM_KEY_BUDGET` as today. This is the missing
half of ADR-0012: the hold the claim computed becomes the ceiling LiteLLM
enforces. Budget exhaustion at claim time answers the worker 204
(empty-handed, exit 0); the *sweeper* parks the below-floor Shift —
`needs_human` with a reason naming pool and spend — so no attempt is burned
and no key is minted (shift-orchestration spec, last requirement).

### D8 — Reader credentials, tier 1, honestly

Repos on this Forgejo are private: "no credential" cannot clone. Tier 1
(ADR-0013) is therefore delivered as: the chart accepts an optional
`executor.forgejo.readTokenSecret`; reader-role workloads render their
`AGENT_BUILDER_TOKEN` from it when set. The boundary turns on by minting one
read-only `agent-builder`-sibling token in ops — zero code change. Until that
secret exists, readers run with the builder token and Ploeg's scheduling
remains the only writer/reader boundary; the chart logs nothing but the
values file documents the gap loudly. The alternative — blocking this change
on an ops credential — serialises two repos for no safety gain in a factory
whose readers are prompted with no push contract at all.

### D9 — One workload per (team, role); the scale signal changes predicate

Teams with a plan render one ScaledJob/CronJob per distinct role:
`ploeg-worker-<team>-<role>`, label `ploeg.webgrip.dev/role`, env
`PLOEG_ROLE`, with a role→team→global override tier for `model`, `image`,
`harness`, `dind`, `maxReplicaCount`. Role workloads' KEDA query counts
**pending runs**, not queued items:

```sql
SELECT COUNT(*) FROM agent_runs
 WHERE team = '<team>' AND role = '<role>' AND state = 'pending'
```

served index-only by `agent_runs_claimable`, which was created verbatim for
this query (migration 0008's comment) and is the same predicate `ClaimRole`
drains and `PendingRuns` reports — the role-claim spec's depth==drainable
requirement, tested store-side. **KEDA-sync statement (required here):** the
template's query is the third copy of the predicate; the store test asserts
`ClaimRole`↔`PendingRuns` agreement, and the chart change that edits the
template must quote the `agent_runs_claimable` definition beside it.
Plan-less teams keep the existing template path — the `work_items` queued
query — untouched; CI diffs their rendered output against committed goldens
so "byte-identical to today" is a gate, not a review hope.

### D10 — Providers gain transports, the engine gains publishers

`pkg/provider/forgejo` implements `Name`/`Comment`/`ParseWebhook`: comments
via `POST /api/v1/repos/{owner}/{repo}/issues/{pr}/comments` (PR comments
ride the issues API; prior art `pkg/worker/forge.go`), webhook verification
raw-body HMAC-SHA256 against `X-Forgejo-Signature` before any JSON parse
(same shape as the Vikunja provider). The repo coordinate for `Comment`
comes from the Work Item's resolved Target; an unresolved Target skips
publication with a WARN — ploegd genuinely does not know the repo there.
ploegd's deployment gains `FORGEJO_URL` and the existing agent-builder
token. The Vikunja provider gains `BaseURL`/`Token`/HTTP client
(`PLOEG_VIKUNJA_URL`/`PLOEG_VIKUNJA_TOKEN`) and real
`FetchItem`/`Comment`/`SetStatus` — comment creation is **PUT**, per
`docs/ops/board.md`. Missing env keeps today's no-op behaviour, per-provider.
Publication and write-backs are engine side effects, at-least-once: a crash
between publish and advance may duplicate a PR comment; a failed write-back
is logged and never blocks the state transition (blackboard spec).

### D11 — Delivery order inside the change

Store completions (dead code) → plan config (renders dark) → engine (inert:
no plans configured live) → API (role param optional; role-less claims fall
through to legacy `Claim`, preserving behaviour bit-for-bit since no
role-less pending runs exist yet) → worker → chart (goldens pin plan-less
output) → providers → publication → **last**, the uniform flip: plan-less
teams get a synthesized one-writer plan (pool 0), behind an env kill-switch,
satisfying the spec's plan-less scenario as the only merge that changes live
behaviour for existing teams.

## Risks / Trade-offs

- [Two claim paths during rollout (role-less falls through to legacy
  `Claim`)] → the fallthrough is deleted in the uniform-flip merge; until
  then a store test pins that `ClaimRole(team, "")` finds nothing while no
  synthesized plans exist.
- [Scale-signal undershoot stalls items silently and forever (role-claim
  spec)] → depth==drainable parity tests extended over the role dimension;
  overshoot tolerated.
- [Engine never runs (bug, crash-loop) ⇒ shift runs strand `leased`/`queued`
  with nobody transitioning the item] → `EvaluateAll` on every 15s sweep is
  the repair path; its tests enumerate every half-state; the uniform flip
  carries a kill-switch env restoring legacy dispatch.
- [Findings quality depends on prompts and adapter outcome mapping — the
  least verifiable seam] → prompt-contract tests plus one real cluster run
  before the uniform flip; the briefing is size-capped so a verbose reader
  cannot blow the writer's context.
- [At-least-once publication can duplicate PR comments] → accepted;
  duplicates are visible and harmless, missed publication is neither.
- [Readers share the builder token until ops mints a read-only one] →
  D8; the values file documents it; flipping the boundary on is one secret.

## Migration Plan

Migrations `0009` (findings) is append-only and additive; every new column
has a default, so a chart downgrade keeps running against the newer schema.
Rollout is values-driven: nothing activates until a team gets a `plan`
(cluster-side change, separate repo); the uniform flip is the single
behaviour-changing merge and carries its own kill-switch. Rollback at any
point = revert the values (cluster) or flip the kill-switch; mid-shift abort
is `CloseShift(operator_abort)` + re-assignment.

## Open Questions

- The read-only forge token (D8): OpenBao path and minting are ops work in
  `homelab-cluster`; this change ships the chart knob only.
- Verdict semantics (approve/request-changes, fix-round cap) are deliberately
  absent here; `close-the-review-loop` owes the ledger ADR-0017 before any
  engine code learns the word "verdict".
