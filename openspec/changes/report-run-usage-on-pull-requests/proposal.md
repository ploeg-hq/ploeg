## Why

A Shift spends real money and leaves evidence across several places — a per-Run
gateway account (`run_llm_accounts`), the Run's own `usage` row, the Shift's
ledger and the worker's verification of the pushed commit. Ploeg publishes the
review findings and, when the pool empties, a budget notice
(`pkg/shiftengine/publish.go`), but it never publishes what the Shift **cost** or
**proved**. A person opening the pull request sees the agents' prose and has to
open Ploeg's database, the LiteLLM dashboard and the CI run to answer "what did
this cost and what was checked?" — the two questions `#1305` (usage and evidence
report on every agent PR, 2026-09-28) says should be answered on the pull request
itself. The owner asked to review the plan before any code is written, so this is
the OpenSpec change for it.

## What Changes

**Seam touched:** LLM control (accounting is **read**; no invariant of it
changes), carried over the existing blackboard transport (ADR-0011). The forge
SPI gains two additive methods (R7). It is one change because the artifact is
one: splitting the accounting read from its publication would leave a half
report, and a report is not a lifecycle transition — it changes no Outcome and
no Work Item state.

- **One report comment per agent pull request.** After a writing Run opens or
  updates a pull request, Ploeg publishes a single comment marked
  `<!-- ploeg:usage-report -->` carrying per-Run usage and the Shift's totals.
- **It is updated in place.** A later Round, a fix round, or the settlement
  sweep edits the same comment; refresh is find-by-marker then edit, never
  append.
- **Every figure comes from state Ploeg already records.** Per Run: Role, Round,
  whether it wrote, Outcome and verdict, the models, input and output tokens and
  the cost. In total: authorized, reserved, settled and remaining pool. Tokens
  and cost are read from `agent_runs.usage` and the reconciled account, so **no
  migration is needed**.
- **Provisional is stated as provisional.** A Run whose account is not yet
  reconciled is marked provisional; a Run whose gateway spend could not be read
  is marked unavailable rather than guessed at.
- **Evidence, not a copy of it.** The report names the writing Run's
  verification result and commit — or says plainly that none was recorded — and
  points to the writing Run's findings comment for the full output; it never
  re-posts the verification prose.
- **Best-effort, never in the lifecycle.** A failed accounting read or forge
  call is logged and skipped: no Outcome changes, no Shift closes differently,
  no state transition blocks (R2, R3). The report makes no gateway call, mints
  nothing and settles nothing.
- **Three chart values.** `PLOEG_USAGE_REPORT` in the chart's existing `env` map
  decides whether the publish path runs; it is the kill switch and nothing else
  in the chart moves. `PLOEG_REPORT_GRAFANA_URL` and `PLOEG_REPORT_VLOER_URL`
  carry the two dashboard bases the report links from; unset omits those links
  (and both unset omits the links section).

## Capabilities

### New Capabilities

- `run-usage-report`: rendering a Shift's usage and evidence from stored
  accounting, and maintaining exactly one such comment on the pull request.

### Modified Capabilities

- none. The report is additive transport; the blackboard, verdict, settlement and
  Shift semantics are used as they are.

## Non-goals

- **New accounting or a second metering path.** The report presents what
  `run_llm_accounts`, `agent_runs.usage` and the Shift ledger already record. It
  never calls the gateway; reading spend logs stays `LLMControl.Settle`'s job
  (ADR-0008).
- **A billing statement.** Figures are a gateway observation settled after a
  quiet period; the report says what is provisional and is not a source of truth
  for invoicing.
- **Reading or gating CI.** Ploeg does not fetch check status for the report;
  the forge shows checks beside the comment. The report blocks no merge.
- **A tracker field, a Ploeg UI, or per-person figures.** The report lives on
  the pull request (ADR-0011); it is operational accounting, not a performance
  ranking.
- **Storing report state in Ploeg.** The comment's identity is a marker on the
  forge and the numbers are read live; a new table or column would be a second
  source of truth, and needs a migration the data does not require.

`#1305` was not reachable from the sandbox; the delta spec derives from what
`VIK-1306` states of it plus the seams below, and `adr.md` records the check
against the in-force records (0008, 0011, 0012, 0013, 0014). No requirement
conflicts.

## Impact

- **Code:** `pkg/shiftengine/usage.go` (the pure renderer and evidence parser),
  `pkg/shiftengine/publish.go` (`publishUsageReport`, `RefreshUsageReport`) and
  `engine.go` (the round and close call sites) · `pkg/provider` (`Comment` type
  plus `Comments`/`EditComment` on the SPI) · `pkg/provider/forgejo` and
  `pkg/provider/gitlab` (the comment endpoints) · `pkg/store` (a read-only usage
  query; `UnsettledLLMAccount` carries `shift_id`) · `cmd/ploegd/sweep.go`
  (refresh after a settlement) · `cmd/ploegd/main.go` (the flag and the URLs).
- **Contracts:** `provider` is the internal SPI; the methods are additive and no
  published `docs/contracts/` schema changes.
- **Chart:** `ops/helm/ploeg/values.yaml` adds `PLOEG_USAGE_REPORT`,
  `PLOEG_REPORT_GRAFANA_URL` and `PLOEG_REPORT_VLOER_URL` to the existing
  generic `env` map; `templates/deployment.yaml` renders them. No
  `values.schema.json` change.
- **Money:** no new spend path and no new hold; the report only reads. It prints
  no key value, only the alias `ploeg-<12hex>` (ADR-0008).
- **Rules:** R2 and R3 (a report failure is logged, never a lifecycle failure) ·
  R6 (its state stays in Postgres and the forge, none in the publisher) · R7
  (the new forge methods live behind the SPI).

## Open questions

- **Rollout default.** The design proposes shipping enabled (the outcome is
  "every agent PR") with the chart value as the kill switch. If the owner wants a
  staged rollout, the default flips to disabled and a Team opts in; that is a
  one-line values choice, not a design change.
- **Tracker echo.** Should the tracker comment also carry the totals, or is the
  pull request enough? Not proposed: the tracker comment already links the pull
  request, and duplicating money figures invites drift.
- **Does the report earn an ADR?** Not now — it is transport over already-recorded
  accounting, reversible by a value, and consistent with ADR-0011 and ADR-0012. It
  would earn one if it became the authoritative cost surface a human bills from.
- **Source of the criteria.** Ticket `#1305` was not reachable from this
  sandbox, so the delta spec's scenarios are derived from what this Work Item
  states of it (usage and evidence report on every agent PR; the spend-log
  lookup failure as the error path; edit-in-place with no duplicate comment)
  plus the accounting seams surveyed below. The owner should confirm the mapping
  against the live `#1305` before approving; a missing criterion is a spec edit,
  not a redesign.
- **Conflicts.** No conflict found between `#1305` and an accepted, in-force ADR
  or an existing spec: ADR-0011 makes the pull request the blackboard and Ploeg
  its transport; ADR-0012 makes budgets two-level and already stores both; ADR-0008
  keeps the gateway the single metering seam, which this report reads but does not
  cross.
