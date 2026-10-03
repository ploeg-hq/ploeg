---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The pull request is the blackboard; Ploeg is only the transport

## Context and Problem Statement

Personas in a Shift ([ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md))
must build on each other's findings: the writer needs to know what the security
specialist found. Where do those findings live, and who moves them?

The obvious answer — agents write to a shared store — asks a further question:
does that store belong in Ploeg?

## Decision Drivers

* **R6 — durable state lives only in Postgres and git/forge state, never inside
  an agent process.** A finding that exists only in a running pod is lost when
  the pod dies, which it will.
* **Agent-side cost is the real cost.** Anything requiring the agent to call a
  new API needs tooling in *every* harness image — OpenHands, opencode, Claude,
  Copilot, Codex — and re-broken by each vendor's next release.
* **Readers must stay read-only.** ADR-0010's concurrency safety rests on
  readers holding no lease and touching nothing. Giving them forge or git write
  access destroys exactly that property.
* **Humans read this too.** A findings trail a person cannot see during review is
  worth much less than one they can.

## Considered Options

* A `findings` table in Ploeg's Postgres, read and written by agents via a new API
* Files committed to the branch (e.g. `.ploeg/findings/`)
* Adopt a standard shared-state protocol
* **The pull request, with Ploeg as the courier**

## Decision Outcome

Chosen option: **findings live on the pull request; Ploeg carries them.**

A reader agent needs no new capability at all. It already ends every run with an
`OutcomeReport` — the existing contract that every adapter implements. Its
findings ride out in that report. Ploeg publishes them to the PR through the
Forge Provider, and injects prior findings into the next round's prompt via
`ComposePrompt`.

| Side | What it must gain |
| --- | --- |
| Agent | Nothing. It already writes an outcome report. |
| Ploeg | Publish findings to the PR; inject them into the next `TaskSpec`. |
| Storage | The PR — already durable, already authenticated, already read by humans. |

Structured facts that need querying — spend, outcomes, checkpoints — stay in
Postgres, where they already are. The PR carries prose; the database carries
numbers. Neither gains a general-purpose shared-state store.

**No standard was adopted, because none fits.** A2A explicitly does not support
a blackboard: it has "no notion of shared state", and models context as
"distributed interaction memory" where each agent keeps its own portion — the
precise arrangement R6 forbids. Blackboard is a well-understood *architecture*
(HEARSAY-II, 1970s; actively revived for LLM agents), but there is no wire
standard to adopt, only a pattern to apply.

### Consequences

* Good, because it adds no agent-side tooling, no new table, and no protocol.
  The two moving parts already exist as stubs or code.
* Good, because readers keep zero write access, preserving ADR-0010's safety.
* Good, because the findings trail *is* the review thread. A human sees the
  security specialist's objection where they were already looking.
* Good, because it survives every pod dying, satisfying R6 by construction.
* Bad, because it depends on backlog #31 (tracker/forge write-backs), which is
  stubs today. This is now on the critical path and is honest work either way.
* Bad, because forge APIs rate-limit, and a chatty roster makes noisy PRs. Both
  are tunable (fewer readers, terser findings); neither is structural.
* Bad, because findings are prose, so synthesis is the writer's judgement rather
  than a query. Accepted — the alternative is a schema for opinions, which is a
  much larger claim than we can support.

### Confirmation

* A reader Run's forge credential is read-only; the Forge Provider is called by
  `ploegd`, never from a worker pod (R6 keeps the worker single-purpose).
  Verifiable by grep: no forge write call exists under `pkg/worker/`.
* `pkg/worker` gains `TestComposePrompt_IncludesPriorFindings` — a Shift with
  findings from an earlier round renders them into the next `TaskSpec`.
* Contract coverage: findings are carried by the existing
  `outcomereport.v1` schema, pinned by `pkg/harness/contract_test.go`, so a
  field added on one side and not the other fails `go test ./pkg/harness/`.
* Gate: `go test ./...` in `.forgejo/workflows/on_pull_request.yml`.

## Pros and Cons of the Options

### A `findings` table with an agent-facing API

* Good, because findings become queryable and dedupable.
* Bad, because every harness image needs a client for it, maintained against
  five vendors' release cycles — the connector-matrix failure `design.md` §2
  already refuses.
* Bad, because it gives readers write access to Ploeg's own state.

### Files committed to the branch

* Good, because agents already have file tools, so again no new tooling, and git
  is a sanctioned durable medium under R6.
* Bad, because writing a file means pushing, which means branch write access for
  readers — the one thing ADR-0010 depends on them not having.

### Adopt a standard shared-state protocol

* Good, because a standard would outlive our conventions.
* Bad, because the closest candidate, A2A, explicitly lacks shared state and
  keeps context inside agent processes, contradicting R6. There is nothing else
  to adopt: blackboard is a pattern, not a protocol.

## Re-evaluation triggers

* **Forge rate limits bite** — if a normal Shift's findings traffic approaches
  Forgejo's limits, the transport must change even though the model need not.
* **Findings need querying** — the first time "which persona flagged this, how
  often" cannot be answered from the PR, a structured store is justified.
* **A shared-state standard appears** with durability semantics compatible with
  R6 — watch A2A issue tracking for shared state, and the blackboard revival in
  the agent-memory literature.
* **A non-forge target appears** — a Shift that produces no PR (a research or
  grooming Shift) has no blackboard under this decision, and would force one.

## More Information

* Depends on backlog #31 (tracker/forge write-backs).
* Container and lifecycle: [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md).
* Why not A2A more broadly: [ADR-0007](0007-a2a-adopt-nothing-watchlist-a-facade.md).
* Evidence: *Designing Collaborative Multi-Agent Systems with the A2A Protocol*
  (O'Reilly), *Revisiting Gossip Protocols* (arXiv 2508.01531) for A2A's absent
  shared state, *LLM-Based Multi-Agent Blackboard System* (OpenReview) for the
  pattern.
