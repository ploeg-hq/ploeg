---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# A2A: adopt nothing now; watchlist a north-facing dispatch facade

## Context and Problem Statement

A2A (Agent2Agent, `a2aproject`, Linux Foundation) was surveyed on 2026-07-28 via
a four-agent research fan-out. Unlike the AHP sweep, the "pre-1.0 plus single
vendor, therefore dismiss" heuristic does **not** apply: A2A is spec v1.0.0
(2026-03-12), governed by an LF TSC with eight organisational seats
(Google, Microsoft, AWS, Cisco, Salesforce, ServiceNow, SAP, IBM), has a GA
official Go SDK (`a2a-go/v2`), and ships in all three hyperscaler agent
platforms. IBM's competing "ACP" merged into A2A on 2025-08-29.

The question this record exists to answer is therefore sharper than usual: does
a mature, well-governed, genuinely adopted protocol belong in this stack?

## Decision Drivers

* **Seam fit.** A2A claims the *cross-organisational agent↔agent peer* seam.
  Ploeg must have such a seam for A2A to be relevant.
* **Queue semantics.** The executor seam needs claim/lease/backpressure, not
  point-to-point RPC.
* **The board stays authoritative** (`design.md` §2 non-goals). Any second front
  door must not create work items the tracker has never seen.

## Considered Options

* Adopt A2A at the harness seam
* Adopt A2A at the executor seam (dispatcher → worker)
* Adopt A2A for intra-factory role/team delegation
* Expose ploegd as an A2A remote agent (north-facing facade)
* Adopt nothing; watchlist the facade

## Decision Outcome

Chosen option: **adopt nothing now; watchlist the facade** as backlog #102.

A2A fails on **fit, not maturity** — and that distinction is the reason this
record exists rather than a one-line dismissal. It addresses whole agentic
systems as black boxes across organisational trust boundaries. Ploeg has exactly
one trust boundary, and every seam it does have is the wrong shape:

* **Harness** — A2A drives opaque remote services, not local permission-brokered
  sessions. The strongest available external validation: OpenHands, the harness
  this factory runs today, closed its A2A tracking issue as `not_planned`
  (`OpenHands/software-agent-sdk#1060`) and shipped Zed-style ACP instead.
* **Executor** — point-to-point RPC. A2A's own top-reacted issue
  (`a2aproject/A2A#1029`) asks for the queue/pub-sub semantics the Postgres
  lease queue already provides.
* **Intra-factory delegation** (backlog #39/#43) — one trust boundary, N²
  point-to-point HTTP, no lease semantics. Explicitly rejected.

The one honest fit is a **north-facing facade**: ploegd as an A2A remote agent.
The lifecycle maps almost 1:1 — `queued`→`SUBMITTED`, `leased`→`WORKING`,
`needs_human`→`INPUT_REQUIRED`, `done`→`COMPLETED`, PR link as an `Artifact`,
`CancelTask` ≈ unassignment. That cheapness is a reason it *would* be easy, not
a reason to build it.

The facade carries a named tension: a second front door beside the Vikunja
webhook violates "the tracker stays authoritative". The only architecture-honest
shape is a **separate service** (never the worker — R6) that creates and assigns
a real ticket via the TrackerProvider, then projects state back out. That makes
tracker write-backs (backlog #31, stubs today) a hard prerequisite.

Near-term curiosity is routed through LiteLLM's A2A Agent Gateway, which is
already deployed — kept away from factory credentials.

### Consequences

* Good, because confidence in ACP as the harness seam is raised, not merely
  unchanged: the incumbent harness evaluated both and chose ACP.
* Good, because the facade is captured with prerequisites and triggers instead
  of being rediscovered every quarter.
* Bad, because Ploeg has no standard programmatic dispatch API, so an external
  system must speak Vikunja to enqueue work. Accepted until a real client exists.

### Confirmation

`go.mod` contains no `a2a-go` dependency and `pkg/httpapi/server.go` exposes no
`/.well-known/agent-card.json`; both would be visible in review. Backlog #102
carries the watchlisted scope and its prerequisites.

## Pros and Cons of the Options

### Adopt A2A at the harness seam

* Good, because one protocol would cover many vendors' hosted agents.
* Bad, because it models a remote opaque peer, not a local session with
  permission brokering — and OpenHands rejected exactly this.

### Adopt A2A at the executor seam

* Good, because it would standardise dispatcher→worker.
* Bad, because it has no queue or lease semantics; adopting it would mean
  reimplementing `SKIP LOCKED` claims over HTTP.

### Adopt A2A for intra-factory delegation

* Good, because a standard beats a bespoke internal protocol, in principle.
* Bad, because inside one trust boundary it is, by its own community's
  assessment, chat-shaped overhead: N² HTTP, tight availability coupling, and
  backpressure pushed into every implementation.

### Expose ploegd as an A2A remote agent (now)

* Good, because the state mapping is nearly free and it is the seam A2A actually
  wants.
* Bad, because no A2A-speaking client exists anywhere in the stack, and the
  prerequisite (#31 write-backs) is unbuilt. An interface with no caller is the
  failure mode `design.md` §6 already warns against.

## Re-evaluation triggers

* **A real A2A client lands in the webgrip stack** — above all kagent (A2A-native)
  deployed to `homelab-cluster`; watch `kagent-dev/kagent#1941` for its spec-1.0
  migration.
* **OpenHands reverses course** on `OpenHands/software-agent-sdk#1060`.
* **A2A grows a queue/pub-sub transport** (`a2aproject/A2A#1029`) — that would
  make it a genuine executor-seam candidate rather than only a facade.
* **Vikunja or Forgejo ship any A2A or agent endpoint** (both at zero today).
* **The 2027-04 review gate** turns Ploeg into a product — external adopters
  will want a standard dispatch API, and A2A is the obvious front door.

## More Information

* Full evidence trail:
  [research/2026-07-28-a2a-fit.md](../research/2026-07-28-a2a-fit.md).
* Migrated from `docs/design.md` §8 on 2026-07-29.
* Watchlisted scope and prerequisites: backlog #102 (needs #31 tracker
  write-backs and a ploegd single-item read endpoint).
* Naming hazard: Zed's Agent **Client** Protocol (backlog #64) is unrelated to
  IBM's former Agent **Communication** Protocol, which merged into A2A.
* 2026-09-29 — Re-confirmed, unchanged. The "no standard programmatic dispatch
  API" consequence is now addressed for MCP clients by the proposed
  [Unfold ADR-0011](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/adr/adr-0011-unfold-is-reachable-over-mcp-through-a-read-first-server.md):
  a separate read-first server on the operator API, which creates `proposed`
  Work Items that a person approves. The A2A facade stays watchlisted; its
  triggers are unchanged.
