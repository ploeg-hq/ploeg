---
status: proposed
date: 2026-09-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Keep the dedicated dispatch plane, and compete on authorized spend over a self-hosted stack

## Context and Problem Statement

[0005](0005-build-a-dedicated-dispatch-plane.md) chose to build Ploeg after a
July 2026 survey found no candidate combining tracker neutrality, lease-based
crash-safety and a Kubernetes runtime. Eight weeks later the market is
different: the ticket-to-PR pattern has a published specification
(openai/symphony), a second Go + Postgres control plane with leases and Forgejo
support exists (Multica), Databricks' Omnigent runs agent sessions as
Kubernetes Jobs behind a credential proxy with spend policy, and one of 0005's
own triggers has fired (`agents.x-k8s.io` left alpha on 2026-08-28).

The question is whether building a dedicated dispatch plane is still right,
and if so, which claims still justify it.

This record is proposed. On ratification set `supersedes: 0005` and change
0005's Records row to "superseded by 0032" in the same commit.

## Decision Drivers

* **Only claims that survive a source-level comparison may justify the build.**
  A differentiator another open project already ships is not one.
* **Admission before spend.** An unattended, money-spending system must refuse
  work it cannot pay for, not downgrade the model or report the bill afterwards.
* **Tracker and forge neutrality for self-hosted stacks**, carried over from
  0005: Vikunja + Forgejo remains the target.
* **Thin glue**, carried over from 0005: the layers above and below Ploeg have
  standardized (ACP, `agents.x-k8s.io` v1beta1, Agent Skills, AGENTS.md, A2A
  1.0), so Ploeg should consume them rather than grow its own.

## Considered Options

* Keep building Ploeg, with its claims restated
* Adopt Omnigent and move dispatch policy into it
* Adopt Multica as board and control plane
* Implement the Symphony specification instead of Ploeg's own contracts
* Build on kagent v1 (Agent Substrate, `ScheduledRuns`)
* Adopt Warp's Oz platform

## Decision Outcome

Chosen option: **keep building Ploeg, with its claims restated.**

Across roughly 80 projects surveyed on 2026-09-26, none combines all four of:
the tracker kept external and reachable on a self-hosted Vikunja/Forgejo stack;
a webhook-triggered, scale-to-zero Kubernetes Job per Run; a durable Postgres
lease with a TTL; and per-Run model credentials minted at a proxy the agent
cannot bypass, authorized before the Run and settled after it
([0012](0012-two-level-budgets-authorized-and-settled.md)). The combination is
still unserved.

The individual claims are not all still exclusive, and Ploeg stops making the
ones that are not:

* **Retired as a differentiator: event-driven dispatch.** Paperclip's timer
  heartbeats were off by default even in July; its main path is an assignment
  wake. The accurate statement is narrower: *execution scales to zero; the
  control plane stays small.*
* **Retired as a differentiator: the lease itself.** yc-software/qm, Multica and
  the agent-orchestrator cloud edition have equivalent TTL-lease semantics. The
  distinguishing property is that Ploeg's lease sits *under* admission and
  budget.
* **Primary differentiator: authorized spend.** Authorize-then-settle budgets
  with per-Run virtual keys exist nowhere else. The nearest are gh-aw's per-run
  cap at a proxy (GitHub only, and self-described as not an authentication
  boundary against code in the container) and Omnigent's `cost_budget`
  ("a downgrade gate, not a hard stop").
* **Kept, with a known gap: neutrality.** Ploeg has no GitHub or Linear
  `TrackerProvider` and no GitHub `ForgeProvider`. Beside Symphony's five
  tracker adapters, "neutral" with only Vikunja and ClickUp is not yet a
  credible claim. The gap is recorded as work, not as a reason to stop.

Because 0005's trigger "`agents.x-k8s.io` graduates past alpha" has fired,
**kubernetes-sigs/agent-sandbox v1.0.x (`v1beta1`) is the runtime for the second
executor** (backlog #58), consumed through its generated Go clientset, with KEDA
driving a `SandboxWarmPool` rather than a ScaledJob. Per-Run credentials never
travel in `SandboxClaim.spec.env`, which accepts only literal values.

Scope stays as 0005 bounded it: Ploeg owns dispatch and nothing else.

### Consequences

* Good, because Ploeg's public claims now match what a reader can verify in the
  competing source, which makes the 2027-04 review gate answerable.
* Good, because the runtime seam stops being bespoke: Vloer's `SandboxWorkspaces`
  provisioner, OpenShell's Kubernetes driver, OpenSandbox and kagent v0.10 all
  build on the same CRDs.
* Bad, because the defensible core is now one property (authorized spend) plus a
  combination. A well-funded project adding a refusing budget and a tracker
  trigger would close most of the gap; Omnigent is one release away from the
  first.
* Bad, because the neutrality gap means the first external adopter on GitHub
  cannot use Ploeg's unattended path at all.
* Bad, because the Symphony specification shows the pattern can be regenerated
  from a document. Ploeg's defence is the recovery, admission and settlement
  behaviour its tests carry, which a specification does not.

### Confirmation

* `go test ./internal/ledger/` validates this record and its Records row.
* No dependency on Omnigent, Multica, Paperclip, Symphony, kagent or Warp Oz
  appears in `apps/ploeg/go.mod`; a reviewer checks this record against any
  proposal that adds one.
* Ploeg's current documentation stops presenting event-driven dispatch or the
  lease as unique: `grep -rn "heartbeat cron" apps/ploeg/docs` returns only
  records (ADRs and research), never a current page.
* Backlog #58 pins agent-sandbox `v1beta1` at v1.0.x before any executor code
  for it is merged.

## Pros and Cons of the Options

### Adopt Omnigent and move dispatch policy into it

* Good, because it already runs sessions as Kubernetes Jobs and on
  agent-sandbox with warm pools, keeps long-lived secrets in a credential proxy
  outside the sandbox, and has server, agent and session policies.
* Good, because Apache-2.0 and backed by Databricks.
* Bad, because it is session-first: work arrives from its UI, a Slack thread or
  a schedule, never from a tracker, and there is no forge abstraction beyond
  GitHub App tokens.
* Bad, because its budget downgrades models instead of refusing spend, and it
  has no durable work lease. It is alpha.

### Adopt Multica as board and control plane

* Good, because it is the closest architectural twin found: Go, Postgres,
  `FOR UPDATE SKIP LOCKED` claims with a prepare lease, recovery sweeps, and a
  Forgejo/Gitea/GitLab provider.
* Bad, because it is the tracker, which is the seam Ploeg exists to refuse.
* Bad, because its licence forbids offering it as a service to third parties and
  lets the producer change the terms; execution runs in a daemon on a user's
  machine, not on the cluster; and spend is parsed from CLI output with no
  budget at all.

### Implement the Symphony specification

* Good, because it is the most-copied description of the pattern, with a
  read-only tracker kernel that keeps credentials out of the agent.
* Bad, because conformance REQUIRES a Codex app-server client (SPEC §18.1), and
  the specified orchestrator polls and holds claims in memory ("No running
  sessions are assumed recoverable", §14.3). Adopting it would discard the lease
  and admission semantics this record keeps.

### Build on kagent v1

* Good, because Agent Substrate, `Harness` CRDs for Claude Code and Codex, and
  cron `ScheduledRuns` with database leases are real progress.
* Bad, because v1 is alpha on a forked, pre-1.0 substrate, and there is still no
  tracker, forge or spend budget.

### Adopt Warp's Oz platform

* Good, because it is the closest analogue to Ploeg + Vloer as a whole:
  Kubernetes Job workers, attachable sessions, tracker integrations and credit
  caps.
* Bad, because the control plane is proprietary SaaS; self-hosted workers are
  Enterprise-only and transcripts pass through Warp.

## Re-evaluation triggers

Any one of these reopens this record:

* A tracker-triggered, lease-based, Kubernetes-native dispatcher with a refusing
  (not downgrading) budget ships under an OSI licence — carried over from 0005,
  sharpened.
* Omnigent adds a tracker trigger, a forge abstraction beyond GitHub, or a hard
  budget.
* Multica publishes its daemon protocol as a stable API, relicenses under an OSI
  licence, or adds a Kubernetes runtime.
* gh-aw targets Forgejo Actions, or any project ships per-Run virtual model keys.
* The Symphony specification leaves "Draft v1", gains a maintainer outside
  OpenAI, or drops the REQUIRED Codex app-server client.
* kagent v1.0.0 goes GA with `ScheduledRuns` or adds a tracker trigger; Agent
  Substrate completes its CNCF donation and tags v1.
* OpenAI documents a settable per-session budget on the Agents API (its
  `session_budget_exceeded` error already exists without one); see
  [0041](0041-the-openai-agents-api-stays-outside-the-run-until-it-takes-an-authorized-budget.md).
* The 2027-04 project review gate (`design.md` §10) arrives.

## More Information

* Evidence trail:
  [research/2026-09-26-agent-orchestration-landscape.md](../research/2026-09-26-agent-orchestration-landscape.md).
* Re-states [0005](0005-build-a-dedicated-dispatch-plane.md); companion record
  for board control planes: [0033](0033-board-control-planes-are-mined-for-design-never-depended-on.md).
* Backlog #58 (agent-sandbox executor) and #75 (GitHub App tokens "when the
  GitHub provider lands").
