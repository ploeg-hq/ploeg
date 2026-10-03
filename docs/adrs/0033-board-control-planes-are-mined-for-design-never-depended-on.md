---
status: proposed
date: 2026-09-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Board control planes are mined for design and never depended on

## Context and Problem Statement

[0009](0009-paperclip-mine-for-design-never-integrate.md) decided on 2026-07-29
that Paperclip is mined for design and never integrated. A source-level
re-verification on 2026-09-26 found that two of its supporting facts were wrong
even then, and that a second product now occupies the same pole: Multica, a
board and control plane that, unlike Paperclip, is written in Go on Postgres
and reaches Forgejo.

The question is whether the verdict still holds on corrected facts, and whether
it extends to Multica.

This record is proposed. On ratification set `supersedes: 0009` and change
0009's Records row to "superseded by 0033" in the same commit.

## Decision Drivers

* **Layer ownership**, carried over from 0009: Ploeg refuses the tracker seam;
  a control plane that is the tracker collides with it.
* **One run-tracker per Run.** Two systems each holding their own run state,
  liveness and cost for the same Run cannot both be the source of truth.
* **The record must survive a reader checking the source.** 0009's statements
  that Paperclip dispatches by "per-agent heartbeats" and that integration is
  "not available even in principle" do not.

## Considered Options

* Integrate Ploeg as an executor under Paperclip or Multica
* Adopt one of them and retire Ploeg
* Mine both for design; write no code against either

## Decision Outcome

Chosen option: **mine both for design; write no code against either.**

The verdict of 0009 stands; two of its reasons are replaced.

* **Correction: dispatch.** Paperclip's timer heartbeats default to off, and did
  at the July tag `v2026.722.0`; its main path is an in-process assignment wake.
  The accurate contrast is an always-on control plane with a 30-second scheduler
  tick against Ploeg's scale-to-zero execution.
* **Correction: integration is available in principle.** Paperclip's built-in
  `http` adapter, its plugin `webhooks.receive` and `issues.create` capabilities,
  and `POST /companies/:companyId/cost-events` all existed in July. Multica
  exposes an internal daemon protocol (`/api/daemon/register`, `/ws`,
  `/runtimes/{runtimeId}/tasks/claim`, `/tasks/{taskId}/usage`). Ploeg could
  register as an executor under either.

It is declined anyway, for reasons that hold in both products:

* **Two run-trackers.** Paperclip keeps `heartbeat_runs`, `checkoutRunId` row
  locks and watchdogs; Multica keeps `agent_task_queue` with its own lease and
  sweepers. Ploeg under either duplicates its lease, admission and settlement.
* **The board becomes the product.** Both replace Vikunja rather than feed from
  it. Neither has a tracker-provider interface: Paperclip's bring-your-own
  ticket system is still not started, and Multica has none.
* **Spend is self-reported.** Paperclip parses cost from adapter output and lets
  agents report their own costs; Multica prices usage parsed from CLI output and
  has no budget. Feeding them LiteLLM-metered numbers would make Ploeg's
  settlement a guest in someone else's ledger.
* **Multica's licence** forbids offering it as a service to third parties
  without a commercial licence and lets the producer change the terms.

What is mined, with the backlog as the record of each adoption:

* From Paperclip, as in 0009, plus the narrow `ControlPlanePort` of its
  experimental runner protocol (PRP v1: open a run, append ordered events,
  submit a terminal structured result) as a reference for Ploeg's own worker
  contract.
* From Multica, the split between a short *prepare* lease renewed until start
  and a separate liveness signal while running, and its recovery of a dead
  runtime's tasks at restart.

### Consequences

* Good, because the record now matches what a reader finds in either codebase,
  so it will not be dismissed on its first factual error.
* Good, because two independent products converging on "board plus claimable
  queue plus sweeper" is design evidence for Ploeg's own semantics.
* Bad, because the risk recorded in 0009 has grown: if the mainstream verdict is
  that board and control plane are one product, there are now two popular
  products saying so, and one of them reaches Forgejo.

### Confirmation

* `go test ./internal/ledger/` validates this record and its Records row.
* No Paperclip or Multica dependency appears in `apps/ploeg/go.mod`, and no
  Paperclip or Multica package appears in any `package.json` under `apps/`;
  a reviewer checks any proposal that adds one against this record.
* Each adopted semantic lands as a numbered backlog item tagged
  `*[research: control-plane sweep 2026-09-26]*`.

## Pros and Cons of the Options

### Integrate Ploeg as an executor under Paperclip or Multica

* Good, because both now offer a concrete path (Paperclip's `http` adapter or
  adapter plugin; Multica's daemon protocol), and both have far richer human
  surfaces than Ploeg will build.
* Bad, because every Run would be tracked twice, with two leases and two cost
  records.
* Bad, because Multica's daemon protocol is internal and unversioned, and keeps
  running tasks alive through one daemon-wide heartbeat, so the shim could not
  scale to zero.

### Adopt one of them and retire Ploeg

* Good, because most of Ploeg's roadmap ships in each today.
* Bad, because both own the tracker, execute on a host rather than the cluster,
  and have no refusing budget.

## Re-evaluation triggers

Any one of these reopens this record:

* Paperclip ships bring-your-own ticket system with outbound assignment events
  (carried over from 0009).
* Paperclip's Work Queues milestone ships claimable-queue semantics (carried
  over from 0009).
* Paperclip enables its runner protocol (PRP v1) by default.
* `acpx` reaches a stable 1.0 (carried over from 0009; 0.19.3 on 2026-09-25).
* Multica publishes its daemon protocol as a stable, versioned API, relicenses
  under an OSI licence, or adds a Kubernetes runtime.

0009's trigger "`agents.x-k8s.io` graduates past alpha" fired on 2026-08-28 and
is answered in [0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md).

## More Information

* Evidence trail:
  [research/2026-09-26-agent-orchestration-landscape.md](../research/2026-09-26-agent-orchestration-landscape.md),
  §4; July trail:
  [research/2026-07-28-paperclip-fit.md](../research/2026-07-28-paperclip-fit.md).
* Re-states [0009](0009-paperclip-mine-for-design-never-integrate.md).
