---
status: accepted
date: 2026-11-27
decision-makers: Ryan Grippeling
supersedes: 0005
review-by: 2027-05-27
---

# Keep the dedicated dispatch plane, and position Ploeg as the open-source governance plane for coding agents you run yourself

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

On 2026-11-27 the owner tested the record's answer against the code and the
published evidence. Four findings changed it:

* **Credential isolation is not yet true by default.** The agent process runs
  in a container that holds no credential
  ([0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md)),
  but the Run's pod still holds the long-lived builder and bootstrap tokens
  (threat model F-03, residual), and a writing Run without a Shift plan or a
  resolved target still pushes with the shared builder token (F-30). Keeping
  every credential out of the pod is
  [0197](0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md),
  which is proposed and opt-in.
* **Per-Run spend control is largely the gateway's.** LiteLLM already reserves
  budget per request on the key Ploeg mints. What Ploeg adds is the check of a
  ticket's budget before any agent starts, and the real cost of each ticket
  settled from the gateway's own records
  ([0012](0012-two-level-budgets-authorized-and-settled.md)).
* **Requester attribution is not a differentiator.** GitHub Copilot's coding
  agent already co-authors its commits with the requester, and Ploeg's ticket
  path asserts the requester or uses a sponsor rather than proving the person.
* **What lasts is the plane and its evidence.** The fully Apache-2.0,
  customer-run governance plane, and evidence that is published and checked by
  machine: a support matrix that fails `mise run docs-check` when a claim
  exceeds its evidence, a public threat model whose findings have owners, and a
  chart that refuses an unsupported setup.

Adoption on the same day: one maintainer, no outside adopter, 148 candidates of
0.2.0 and no stable 0.2.0, and the scheduled quickstart (#400) and attack drill
(#487) red.

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
the tracker kept external and reachable on a self-hosted stack; a
webhook-triggered, scale-to-zero Kubernetes Job per Run; a durable Postgres
lease with a TTL; and per-Run model credentials minted at a proxy the agent
cannot bypass, authorized before the Run and settled after it
([0012](0012-two-level-budgets-authorized-and-settled.md)). The combination is
still unserved, but a combination is not a reason to adopt. The owner decided
on 2026-11-27 to position Ploeg by what a person running it gets:

> Ploeg is the open-source governance plane for coding agents you run yourself: a ticket starts only within its budget and you see what each ticket really cost, the agent process never sees your credentials, and every claim we make is checked against published evidence, including what is not done yet.

This replaces "authorized spend" as the sole differentiator. Each part is
claimed only as far as the evidence goes:

* **Budget and cost per ticket.** Per-Run spend control is largely the
  gateway's: LiteLLM reserves budget per request on the key Ploeg mints.
  Ploeg's addition is the gate on the ticket's budget before dispatch, so a
  ticket that cannot afford its next Run starts no agent, mints no key and
  spends nothing, and the cost of each ticket settled from the gateway's own
  spend records. Ploeg does not claim per-request enforcement as its own.
* **Credentials.** Until
  [0197](0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md)
  is on by default and qualified, Ploeg says: *the agent process never sees a
  credential; the residuals F-03 and F-30 are published* in the
  [threat model](../security/threat-model.md#findings). The stronger claim, that
  no credential enters the Run's pod, waits for 0197.
* **Evidence.** The [support matrix](../reference/support-matrix.md) fails
  `mise run docs-check` when a cell claims more than its evidence shows, the
  threat model names an owner for each finding, and the chart refuses an
  unsupported setup under `supportProfile: P1`.

Ploeg stops making these claims:

* **Retired as a differentiator: event-driven dispatch.** Paperclip's timer
  heartbeats were off by default even in July; its main path is an assignment
  wake. The accurate statement is narrower: *execution scales to zero; the
  control plane stays small.*
* **Retired as a differentiator: the lease itself.** yc-software/qm, Multica and
  the agent-orchestrator cloud edition have equivalent TTL-lease semantics. The
  distinguishing property is that Ploeg's lease sits *under* admission and
  budget.
* **Retired as the sole differentiator: authorized spend.** It stays part of the
  positioning above, narrowed to the per-ticket gate and the per-ticket cost.
* **Not claimed: requester attribution.** GitHub Copilot's coding agent
  already co-authors its commits with the requester, and Ploeg's ticket path
  asserts the requester or uses a sponsor rather than proving the person.
* **Not claimed until a second harness is qualified in P1: harness
  neutrality.** Ploeg runs several harnesses, but the supported profile has
  qualified one live (`acp/openhands`).
* **Neutrality of tracker and forge, restated.** GitHub Issues as a tracker and
  GitHub as a forge exist and are qualified live (at `2fc4ddc`, against
  github.com from a workstation); Forgejo, GitLab, Vikunja and ClickUp are in
  the source with the support each row of the support matrix states. The gap
  this record named on 2026-09-30, no GitHub provider at all, is closed.

Because 0005's trigger "`agents.x-k8s.io` graduates past alpha" fired,
**kubernetes-sigs/agent-sandbox `v1beta1` is the runtime for the second
executor** (backlog #58). Ploeg speaks its API over the Kubernetes REST API
(`pkg/sandboxlaunch/launcher.go`) and carries no agent-sandbox clientset or
client-go module in `go.mod`. KEDA drives a ScaledJob of launchers; each
launcher creates one cold `SandboxClaim`, and the chart's `SandboxWarmPool`
stays at zero replicas. Whether a warm pool can be used is decided by
[0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md).
Per-Run credentials never travel in `SandboxClaim.spec.env`, which accepts only
literal values.

Scope stays as 0005 bounded it: Ploeg owns dispatch and the governance around
it, and nothing else. Accepting this record supersedes
[0005](0005-build-a-dedicated-dispatch-plane.md).

### Consequences

* Good, because Ploeg's public claims now match what a reader can verify in the
  competing source, which makes the 2027-04 review gate answerable.
* Good, because the runtime seam stops being bespoke: Vloer's `SandboxWorkspaces`
  provisioner, OpenShell's Kubernetes driver, OpenSandbox and kagent v0.10 all
  build on the same CRDs.
* Good, because each part of the positioning is a claim a reader can check
  against the support matrix and the threat model, including the parts that
  are not done.
* Bad, because the defensible core is a narrow property (a per-ticket gate
  before dispatch and the per-ticket cost) plus a licence and a body of
  evidence. A well-funded project adding a refusing per-ticket budget and a
  tracker trigger would close most of the gap; Omnigent is one release away
  from the first.
* Bad, because the credential part of the positioning is narrower than its
  wording until 0197 is on by default and qualified, and the record has to say
  so wherever the positioning appears.
* Bad, because none of this is proven by an outside user: Ploeg has no outside
  adopter and no stable release.
* Bad, because the Symphony specification shows the pattern can be regenerated
  from a document. Ploeg's defence is the recovery, admission and settlement
  behaviour its tests carry, which a specification does not.

### Confirmation

* `go test ./internal/ledger/` validates this record and its Records row.
* No dependency on Omnigent, Multica, Paperclip, Symphony, kagent or Warp Oz
  appears in `go.mod`; a reviewer checks this record against any proposal that
  adds one.
* Ploeg's current documentation stops presenting event-driven dispatch or the
  lease as unique: `grep -rn "heartbeat cron" docs` returns only records (ADRs
  and research), never a current page.
* `grep -F "governance plane for coding agents you run yourself" README.md docs/at-a-glance.md`
  finds the positioning in both, and a reviewer checks that neither page claims
  that no credential enters the Run's pod before 0197's Records row reads
  accepted and the support matrix qualifies it.
* `mise run docs-check` passes: the support matrix makes no claim beyond its
  evidence.
* `pkg/sandboxlaunch/launcher.go` names `extensions.agents.x-k8s.io/v1beta1`.

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
* Hermes Agent's Kanban gains a per-card money budget enforced at a proxy the
  worker cannot bypass, Hermes Enterprise ships per-worker credentials with
  tenant isolation, or Hermes adds a Forgejo or Vikunja adapter; see the Unfold
  record [2026-10-14-hermes-agent-fit](https://forgejo.webgrip.dev/webgrip/unfold/src/branch/development/docs/research/2026-10-14-hermes-agent-fit.md).
* The 2027-04 project review gate (`design.md` §10) arrives.
* A competitor ships a self-hostable, OSI-licensed control plane whose agent
  never holds forge or model credentials.
* A platform ships per-ticket budgets checked before dispatch, across agent
  vendors.
* 2027-05-27 arrives and no outside design partner runs real tickets on Ploeg
  ([0210](0210-scope-is-frozen-until-an-outside-design-partner-runs-real-tickets.md)).

## More Information

* Evidence trail:
  [research/2026-09-26-agent-orchestration-landscape.md](../research/2026-09-26-agent-orchestration-landscape.md).
* Re-states [0005](0005-build-a-dedicated-dispatch-plane.md); companion record
  for board control planes: [0033](0033-board-control-planes-are-mined-for-design-never-depended-on.md).
* Backlog #58 (agent-sandbox executor) and #75 (GitHub App tokens "when the
  GitHub provider lands").
* 2026-10-14: the Hermes Agent sweep ([Unfold record](https://forgejo.webgrip.dev/webgrip/unfold/src/branch/development/docs/research/2026-10-14-hermes-agent-fit.md))
  re-confirmed this bet. Hermes Kanban is a single-host ticket-to-PR loop with
  iteration caps and no money budget; nothing in it trips a trigger above.
* 2026-11-27: owner decision: accepted, amended. The positioning above replaces
  "authorized spend" as the sole differentiator, after the owner's red-team of
  the value proposition (credential isolation not yet default, F-03 and F-30;
  per-Run spend control largely the gateway's; requester attribution not a
  differentiator). Stale facts corrected: the GitHub tracker and forge exist and
  are qualified live, KEDA drives a ScaledJob of launchers rather than a warm
  pool, and the executor uses the REST API rather than a generated clientset.
  `supersedes: 0005`; 0005's Records row reads "superseded by 0032".
