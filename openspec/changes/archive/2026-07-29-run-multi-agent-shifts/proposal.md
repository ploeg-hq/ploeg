# Run multi-agent Shifts end to end

## Why

The Shift store layer is built and tested (ADR-0010/0012, `pkg/store/shift.go`)
and the ACP adapter reaches Claude, Copilot, Codex, Cursor, Gemini, Goose and
opencode (ADR-0051 in `homelab-cluster`, backlog #64). **Nothing connects
them.** No Shift is opened, no Round advances, no Run knows its Role, and the
chart still renders one workload per Team.

The target is one real run: a ticket picked up by several agents on **different
harnesses and different images**, producing one PR, reviewed by a different
agent than wrote it, ending with a human asked to merge.

## What Changes

**Seams touched:** tracker ingest, store/lease, run API, executor, harness
adapter. That is four of six, which normally means splitting — see *Non-goals*
for the two splits already taken out.

- **Shift lifecycle in ploegd.** Open a Shift when a Work Item is queued; open
  the next Round when the current one completes (`RoundComplete`); close the
  Shift when the plan is exhausted or an Outcome is terminal. A Round is either
  a fan-out of readers or one writer — `OpenRound` already refuses anything else.
- **Team plans.** An ordered list of Rounds per Team, each naming Roles with
  `writes`, harness, image, model and per-Run cap. Config, not state: read at
  Round-open time, never resolved mid-Round.
- **Role-scoped claim.** `POST /api/v1/claim` accepts a `role`; the response
  gains `shift`, `role`, `round`, `branch` and the authorized budget. Backed by
  `Store.ClaimRole`, already built. **BREAKING** for the run API only in the
  additive sense — a role-less claim keeps today's behaviour.
- **`GET /api/v1/queue/depth` gains a role filter**, backed by
  `Store.PendingRuns`. This is the KEDA scale signal and must stay the same
  predicate as the claim (`TestClaimRoleAgreesWithPendingRuns`).
- **Role-aware Runs.** `PLOEG_ROLE` selects what a worker claims;
  `harness.TaskSpec.Role` is finally populated (it has existed and been empty
  since the seam was carved); `ComposePrompt` emits the Role preamble, prior
  Rounds' findings, and the "a PR already exists, update it" clause.
- **The blackboard (ADR-0011).** A reading Run returns findings in its existing
  `OutcomeReport`; ploegd publishes them as PR comments through a **first
  `ForgeProvider` implementation** (Forgejo), and injects earlier findings into
  the next Round's prompt. Agents gain no new tooling.
- **The human handoff.** Vikunja `Comment` and `SetStatus` stop being logging
  no-ops, so `needs_human` actually reaches a person with the PR link.
- **Reader credentials (ADR-0013 tier 1).** A reading Run receives a read-only
  forge credential, or none. This is what makes the writer/reader split a
  boundary rather than a convention.
- **Role-partitioned workloads.** One ScaledJob/CronJob per `(team, role)`,
  `minReplicaCount: 0`, each with its own image, harness and model. This is the
  change that makes "different agents, different harnesses, different images"
  literally true, because pod shape is fixed at render time.

## Capabilities

**New Capabilities**

- `shift-orchestration` — Shift and Round lifecycle: when a Shift opens, how a
  Round advances, when a Shift closes, and what happens when a Round is
  half-finished.
- `role-claim` — the role-scoped claim and its scale-signal mirror.
- `blackboard` — how findings travel from a reading Run to the PR and into the
  next Round's prompt.
- `forge-provider-forgejo` — the first `ForgeProvider`: PR comments, and the
  webhook parsing R9 Follow-Ups need.

**Modified Capabilities**

- `harness-adapter` — `TaskSpec.Role` and prior-Round findings become part of
  the contract the adapter receives.

## Non-goals

- **Parallel writers.** One writer per Round, enforced by `OpenRound` and by
  `leases`. Two agents mutating one branch needs per-writer worktrees; not now.
- **Agents conversing mid-Round.** Runs in a Round never observe each other
  (ADR-0010). The evidence favours independent opinions over debate; revisit
  only if real tickets say otherwise.
- **ADR-0013 tier 2** — minting and revoking a push credential per writing Run.
  Tier 1 (readers get nothing) closes the hole that matters; tier 2 closes the
  zombie-writer case and needs a forge credential broker in ploegd.
- **`org.yaml` and the roster reconciler** (backlog #103). Team plans are Helm
  values here; generating them is a later change.
- **Retro-fitting Shifts onto `openhands`/`exec` outcome inference.** Those
  still park infra failures (architecture.md §9.9); ACP-driven Roles do not.
- **A board UI, persistent agents, model serving, grooming semantics** —
  design.md §2 non-goals, unchanged.

## Impact

**Code.** `cmd/ploegd` (Shift lifecycle, plan config, findings publication) ·
`pkg/httpapi` (role on claim and queue depth) · `pkg/worker` (role claim,
TaskSpec.Role, ComposePrompt, reader credentials) · `pkg/provider/forgejo`
(new) · `pkg/provider/vikunja` (write-backs) · `pkg/store` (queue-depth by
role; the rest is built).

**Contracts.** `run-api.v1` and `taskspec.v1` gain additive fields. Neither
`work.WorkItem` nor the tracker mirror changes shape —
`taskspec.v1#/$defs/workItem` is `additionalProperties: false`.

**Chart.** `executor.teams[].plan[]` (Rounds and Roles), one workload per
`(team, role)`, role-filtered scaler query. A Team with no plan renders exactly
one workload — byte-identical to today.

**External, and genuinely blocking.** `webgrip/infrastructure` must publish an
ACP-capable agent image; `harbor.webgrip.dev/webgrip/opencode-runner:0.1.0` is
referenced in `ci/executor-values.yaml` but its existence is **unverified**.
Until one exists, "different images" cannot be demonstrated. The `opencode`
ACP profile's provider-config keys are likewise unverified against a real
binary — `PLOEG_ACP_CONFIG_JSON` is the escape hatch.

**Capacity.** A Team's maximum concurrent pods becomes the sum of its Roles'
`maxReplicaCount`. Given the Guaranteed-QoS sizing in `values.yaml`, per-Role
`maxReplicaCount: 1` is the safe default.

**Money.** Per-item spend multiplies by roster size. ADR-0012's pool bounds it,
but the `needs_human` transition on an exhausted pool is part of this change,
not the store change that raised `ErrBudgetExhausted`.
