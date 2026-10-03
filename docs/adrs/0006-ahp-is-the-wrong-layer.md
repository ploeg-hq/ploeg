---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# AHP is parked: a live-run surface above Ploeg, not a seam inside it

## Context and Problem Statement

Microsoft's Agent Host Protocol (`agent-host-protocol`) was surveyed on
2026-07-27, prompted by its overlap with the harness seam Ploeg had just
formalised in `pkg/harness`. The question was whether AHP competes with, or
composes with, the adapter contract.

## Decision Drivers

* **Layer fit before maturity.** A protocol at the wrong layer is not made right
  by shipping 1.0.
* **Stability for unattended runs.** The dispatch path spends real money; a
  weekly-breaking draft on it is a liability.
* **Governance breadth** — a single-vendor protocol with one server
  implementation is a bet on that vendor's roadmap.

## Considered Options

* Adopt AHP at the harness seam
* Adopt AHP as a north-facing live-run surface now
* Park it on the watchlist with named triggers

## Decision Outcome

Chosen option: **park it**, tracked as backlog #101.

AHP is a multi-client *session-sync* layer that sits **above** a harness — their
own documentation describes it as "a mutex over ACP". It carries no dispatch,
lease or outcome semantics, so it cannot replace anything Ploeg implements. What
it could plausibly become is a live run surface: a human attaching to an
in-flight agent run and taking over.

That is a real want, and it is deferred for two reasons. It is draft v0.6 with
breaking changes every one to two weeks; and it is single-vendor (the VS Code
team), with VS Code's own agent host as the sole server implementation. Neither
is disqualifying forever, and both are disqualifying for an unattended factory
now.

ACP remains the harness seam (`design.md` §5, backlog #64). Notably, Microsoft's
own AHP documentation names ACP as its downstream layer — the two are stacked,
not competing.

### Consequences

* Good, because the harness seam stays on ACP, which has multi-vendor governance
  and a stable v1.
* Good, because "attach to a live run" is captured as a named want rather than
  quietly lost.
* Bad, because there is no interactive surface on a running agent until this is
  reopened: a run is observable via pod logs and checkpoints, and not steerable.
  Accepted — R6 keeps the worker single-purpose, and an interactive surface
  belongs in a separate service.

### Confirmation

Nothing in `go.mod` or `pkg/harness/` references AHP; the adapter registry in
`pkg/worker/adapters.go` is an explicit switch, so an AHP adapter could not
appear without a reviewed change. Backlog #101 carries the parked scope.

## Pros and Cons of the Options

### Adopt AHP at the harness seam

* Good, because it would arrive with session-sync semantics ACP lacks.
* Bad, because it is the wrong layer — it multiplexes clients over a harness
  session rather than driving one, and Ploeg has exactly one client per run.

### Adopt AHP as a north-facing live-run surface now

* Good, because "watch and take over a run" is genuinely wanted.
* Bad, because a draft spec breaking every one to two weeks on the path to a
  money-spending unattended service is an operational liability, and the only
  server implementation is inside an editor Ploeg does not run.

## Re-evaluation triggers

* AHP reaches a stable version with a compatibility commitment.
* A second, independent server implementation appears — governance breadth is
  the specific gap.
* An interactive "attach to a running agent" requirement becomes real, e.g. a
  human review surface over in-flight runs.
* Its prerequisite lands first: ACP as the harness seam (backlog #64), which is
  what an AHP projector would sit on top of.

## More Information

* Surveyed 2026-07-27. Migrated from `docs/design.md` §8 on 2026-07-29.
* Parked scope: backlog #101 (north-facing projector, a separate service, never
  the worker — R6).
* Related: [0007](0007-a2a-adopt-nothing-watchlist-a-facade.md) parks a
  different protocol at the same unclaimed north-facing seam, for different
  reasons.
