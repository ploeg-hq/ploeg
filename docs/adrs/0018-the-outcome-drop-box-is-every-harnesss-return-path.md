---
status: proposed
date: 2026-08-08
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The outcome drop box is every harness's return path for a reading Run

## Context and Problem Statement

`worker.ComposePrompt` tells every reading Run, on every harness, to deliver
its review by writing JSON to the file named by `PLOEG_OUTCOME_FILE`. That
instruction is harness-independent, but honouring it was not: only `openhands`
and `exec` ever set the variable. On `claude-code` it was never exported, and
`acp` had no notion of a drop box at all.

The failure is silent and total. A reviewer on either harness wrote its review
to the empty string; `agent_runs.findings` and `verdict` stayed blank;
`shiftengine.requestsChanges` was therefore always false; and **ADR-0017's
review loop was inert — every Shift closed `review_approved` regardless of what
the reviewer found**, with nothing published to the pull request for a human to
read either. `ops/helm/ploeg/values.yaml` documents `harness: {name:
claude-code}` on a role as supported, so this was reachable configuration.

The question this record answers: by what channel does an agent return
something the harness protocol has no field for, and who wins when the adapter
and the agent disagree.

## Decision Drivers

* R2 — advancement must be derived from Run state, never taken on an agent's
  word about what should happen next. An agent must not be able to overwrite a
  classification the orchestrator made from evidence.
* A prompt instruction that only some harnesses honour is a defect that
  presents as a working system. It cost nothing to observe and everything to
  trust.
* Harness plurality is the point (ADR-0007, `pkg/harness`): the fifth adapter
  must inherit this, not rediscover it.
* Findings are the reading Run's entire product (ADR-0011). A Run that produced
  one and then died in its shutdown handshake still did the work.

## Considered Options

* **One drop box, defined in `pkg/harness`, honoured by every adapter, with a
  fixed merge precedence**
* Per-adapter native channels — parse Claude Code's result envelope prose, add
  an ACP session-update kind
* Move findings out of the harness contract onto a Ploeg API the agent calls
* Restrict reading Roles to the harnesses that already worked

## Decision Outcome

Chosen option: "**one drop box, defined in `pkg/harness`, honoured by every
adapter, with a fixed merge precedence**".

`harness.DropBoxEnv`, `harness.DropBoxPath`, `harness.ReadDropBox` and
`harness.MergeDropBox` are the whole of it, and every adapter uses them rather
than a copy. The path carries the trace id because `RunEnv.ScratchDir` is
`os.TempDir()` and shared per process.

The merge precedence is the load-bearing half:

1. **Findings and Verdict are the agent's and always survive**, whatever
   outcome the adapter concluded. A review that happened is evidence, and an
   adapter's classification of *how the process ended* says nothing about
   whether the review was written.
2. **Outcome and Summary fill only a gap the adapter left.** An adapter that
   classified a launch failure, a lost lease or a watchdog timeout holds
   structured evidence the agent does not have, and an agent must never
   overturn it by writing a cheerful file. `StuckReason` and `FailureReason`
   ride with Outcome, so R4 holds however the halves combine.
3. **Usage prefers the agent's accounting only where the adapter has none** —
   the gateway remains authoritative (ADR-0008).

What the drop box is NOT: a channel by which an agent asserts forge state. No
adapter may report `pr_opened`; the worker's forge poll stays the sole ground
truth for whether a pull request exists.

### Consequences

* Good, because a reading Role now works on any harness, which makes the
  harness a genuine axis of variation rather than one with a silent hole in it.
* Good, because the precedence is written once, in one function, instead of
  being re-derived by each adapter author — and the two shapes (spawn-and-wait
  and session-protocol) reach it by different routes but obey the same rule.
* Good, because it needs no new contract surface: `findings` and `verdict` were
  already in `outcomereport.v1`, and this is the transport catching up with the
  schema.
* Bad, because a file on disk is a weak channel — an agent that never writes it
  is indistinguishable from one with nothing to say. Accepted: that ambiguity
  already governs writers, where "no drop box" is the normal case and the forge
  poll decides.
* Bad, because `ScratchDir` being process-global means the trace id in the path
  is doing real work; a run with no trace id falls back to a shared name. The
  per-run `ScratchDir` that would remove the hazard is tracked separately.

### Confirmation

`pkg/harness/harnesstest`'s `ReadingRunFindingsSurviveTheAdapter` property.
Every adapter package (`openhands`, `claudecode`, `execbin`, `acp`) already
calls `harnesstest.Run`, so the property runs for all four inside the
`go test ./...` step of
[.forgejo/workflows/on_pull_request.yml](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/.forgejo/workflows/on_pull_request.yml).
It writes findings and a verdict to `$PLOEG_OUTCOME_FILE` from a script that
learns the path only from the environment — a fixture that never exports the
variable fails rather than passing against a path the test happened to know.
The property fails against `claude-code` and `acp` as they stood before this
record.

`pkg/worker`'s outcome-precedence table pins the other half: a structured
report gains the pull request link it could not know, and never overwrites
links it supplied itself.

## Pros and Cons of the Options

### Per-adapter native channels

* Good, because it uses each protocol as designed, with no file on disk.
* Bad, because it is four implementations of one idea, and the fifth adapter
  starts from nothing again — which is exactly how this defect arose.
* Bad, because Claude Code's envelope carries prose, not structure: extracting
  a verdict from it means parsing intent out of English, which the verdict enum
  exists to avoid (ADR-0017).

### An API the agent calls to post findings

* Good, because it is observable at the moment it happens rather than at exit.
* Bad, because it hands the agent a credential and a client it does not
  otherwise need, which R6 forbids — the run pod's authority is deliberately
  minimal.
* Bad, because it makes a review a side effect rather than part of the Run's
  reported outcome, so a Run could settle having posted findings nobody
  attributed to it.

### Restrict reading Roles to the harnesses that already worked

* Good, because it is free and immediate.
* Bad, because it silently deletes the comparison the harness axis exists to
  make, and encodes a bug as a constraint.

## Re-evaluation triggers

* A fifth adapter lands and needs a drop box it cannot express as a file — for
  example a hosted harness with no shared filesystem.
* ACP gains a native structured channel for agent-authored review output at the
  protocol level; a first-class field would supersede a file for that adapter
  and this record should say so rather than keeping both.
* `RunEnv.ScratchDir` becomes per-run, which removes the collision hazard the
  trace-id-in-path exists to mitigate and simplifies `DropBoxPath`.
* Any proposal to let the drop box override an adapter-set `Outcome`, which
  would move this decision's central boundary.

## More Information

* Evidence trail: `docs/research/2026-08-08-benchmarking-the-loop.md` §8 (C12,
  C13, C14) — how the defect was found, and what else it hid.
* [ADR-0011](0011-the-pull-request-is-the-blackboard.md) — findings ride the
  OutcomeReport; this record is how they get there.
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) — the loop
  this defect made inert.
* [ADR-0007](0007-a2a-adopt-nothing-watchlist-a-facade.md) — harness plurality,
  and the ACP naming hazard.
* `docs/contracts/outcomereport.v1.schema.json` — `findings` and `verdict` were
  already specified; only the transport was missing.
