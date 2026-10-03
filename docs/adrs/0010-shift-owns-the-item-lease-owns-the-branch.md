---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Shift owns the work item; a Lease owns the branch

## Context and Problem Statement

Ploeg is to run **teams of heterogeneous personas** on one ticket — an
architect, a builder, a reviewer, a security specialist, a CFO — on different
container images, and *simultaneously* where they do not conflict.

Today one row does all of it. `leases.work_item_id` is the PRIMARY KEY, so a
Work Item admits exactly one holder, and that single row currently carries three
unrelated jobs: mutual exclusion, liveness (the TTL that recovers crashed work),
and the accounting boundary. With one pod per item those three are
indistinguishable. With several pods at once they come apart, and the question
this record answers is **what each of them attaches to**.

`domain/model.yaml` anticipated this and deferred it in the `run vs job`
ambiguity (resolved 2026-07-22):

> A lease-level grouping term is deliberately not introduced until parallel
> strategies demand one.

Parallel strategies now demand one. This record introduces it and closes that
ambiguity.

## Decision Drivers

* **The contended resource is the branch, not the ticket.** Any number of agents
  may *read* a diff concurrently with no conflict whatsoever. What they cannot
  do is both push to `agent/vik-<n>`. Exclusion was never about the item; the
  item is where the branch was hiding.
* **Most personas never write.** A reviewer, a security specialist, a CFO and a
  philosopher read and opine. Only builders mutate the tree. Exclusion is
  therefore needed by a *minority* of runs.
* **R2 — crash-safety must never depend on an agent behaving well at death.**
  Whatever holds exclusion has to expire on its own.
* **R6 — durable state lives only in Postgres and git/forge state, never inside
  an agent process.** This rules out coordination that lives in a conversation
  between two live pods.
* **Money needs an owner with the right lifetime.** A per-run budget cannot
  bound what a ticket costs; a per-team budget cannot bound one ticket either.
* **Avoid a second copy of the claim predicate.** The rejected design below
  required the KEDA scaler query to mirror `Claim` exactly, where undershoot
  stalls items silently and forever.
* **A Lease must enforce what it records.** A row asserting "this Run may write"
  while every pod holds the same static push token is ceremony, not exclusion —
  and it would leave the writer/reader split below as a convention rather than a
  boundary. Push rights are therefore bound to the Lease
  ([ADR-0013](0013-push-rights-are-minted-per-run.md)).

## Considered Options

* Keep one Lease per Work Item (today) — no simultaneity
* Lease per turn: release and re-acquire between personas
* One pod plays every role inside a single Lease
* Follow-Up items plus per-persona Teams
* **Shift owns the item; Lease narrows to the branch**

## Decision Outcome

Chosen option: **a Shift owns the Work Item; a Lease narrows to write access on
the branch.** The three jobs separate by lifetime:

| Concern | Attaches to | Lifetime |
| --- | --- | --- |
| "This team is working this item" — branch, budget, roster, round counter | **Shift** | opens at first Run, closes when the work ends |
| Exclusive right to **write** the branch | **Lease** | one writing Run; TTL as today |
| Liveness and credential | **Run** | one Kubernetes Job |

Two consequences follow immediately and are part of this decision:

**Runs are writers or readers.** A writer takes the Shift's Lease and is alone.
A reader takes none, so any number run at once. A *round* is therefore either a
fan-out of readers or a single writer, never both — which is the entire
concurrency control. No coordination between pods is required, because readers
have nothing to coordinate over.

That split is enforced by credentials, not only by Ploeg's scheduling: a reading
Run receives no push credential at all, and a writing Run's expires with its
Lease ([ADR-0013](0013-push-rights-are-minted-per-run.md)). Holding the Lease
and being able to push are one fact rather than two that can disagree.

**Agents in the same round cannot observe each other.** Runs that start together
see the same injected state and never see each other's output; the next round
sees everything from every earlier one. This is a property, not a defect —
see *Pros and Cons*.

`Shift` is the lease-level grouping term the model deferred. The name is
deliberate: *ploeg* is Dutch for a crew, and *ploegendienst* is shift work.
"Claim" remains barred as a noun (`model.yaml` avoid-list); it stays the verb.

### Consequences

* Good, because simultaneity costs almost no machinery. Readers need no lease,
  so the hard part of concurrent access simply does not arise.
* Good, because `Claim` and the KEDA scaler query are untouched for writers, so
  the drift hazard that sank the rejected design never appears.
* Good, because the Shift gives the per-item budget an owner with exactly the
  right lifetime (ADR-0012) and the findings trail a container (ADR-0011).
* Good, because a plan-less item is bit-identical to today: one Shift, one
  writer, one Lease.
* Bad, because a Shift is a new entity with its own lifecycle, and "when does a
  Shift close" is a real question we now own. Accepted: the alternative is
  reconstructing the same state from scratch on every claim, which is the
  rejected option and strictly more code.
* Bad, because readers can duplicate each other's findings — three personas may
  independently report the same problem. Accepted; deduplication is a synthesis
  concern, and independent duplicates are cheaper than a missed defect.
* Bad, because a long-running writer will not see findings posted during its
  run. Accepted: it sees them next round, and long wallclock between rounds is
  already an accepted property of this design.

### Confirmation

Enforced structurally rather than by review. All in `pkg/store/shift_test.go`,
gated by `go test ./pkg/store/` in `.forgejo/workflows/on_pull_request.yml`:

* Only writing Runs insert into `leases`, which stays unique per Work Item —
  and because a live Shift is unique per Work Item, one-writer-per-item and
  one-writer-per-Shift are the same statement. The existing R1 guarantee is
  therefore untouched rather than replaced.
* `TestReadersRunConcurrentlyWithoutLease` — a whole fan-out of readers claims
  successfully with `SELECT count(*) FROM leases` remaining zero. If this fails,
  simultaneity is gone and the design has collapsed back into the sequential one
  it replaced.
* `TestWriterTakesTheLease` — a writing Round takes exactly one.
* `TestOpenRoundRefusesMixedRounds` — a writer beside readers, two writers, and
  an empty Round are all refused at the source rather than trusted to callers.
* `TestClaimRoleAgreesWithPendingRuns` — the drift guard described below.
* `TestSweptRunCannotReport` — the advance-once proof.
* `TestRoundCompleteTracksItsRuns` — the signal that moves a Shift forward.

### What the implementation changed about this record

Two things were wrong on paper and only appeared on contact with the code. Both
are corrected above and in `migrations/0007_shifts.sql`.

**The advance-once CAS could not stay on the Lease.** `ReportOutcome` opened
with `DELETE FROM leases WHERE run_token = $1 RETURNING` — the statement only
one transaction wins. Readers hold no Lease, so every reader's report would
have failed with `ErrUnknownRun`, and the reader population this record exists
to enable could never have reported an outcome at all. The CAS is now the Run's
own `running -> finished` transition, which works for both kinds of Run.

**Liveness could not stay on the Lease either.** A reader whose pod is
OOM-killed has no lease to expire, so it would sit `running` forever, holding
budget nothing releases and leaving its Round unable to complete. Every Run now
carries its own deadline, swept by `ExpireRuns`. This is the split this record
already argued for — exclusion on the Lease, liveness and accounting on the Run
— simply followed through.

**A Round materialises its Runs.** Opening a Round inserts one `pending`
`agent_runs` row per Role; claiming flips it to `running`. This was not in the
original decision and improves on it: the claim predicate and the KEDA scaler
query become the same statement, so the drift hazard named in the drivers above
is dissolved rather than guarded against.

## Pros and Cons of the Options

### Keep one Lease per Work Item

* Good, because it is what ships today and is proven.
* Bad, because it makes simultaneous personas impossible by construction, which
  is the requirement.

### Lease per turn (release and re-acquire between personas)

* Good, because each persona gets its own pod, image and harness.
* Bad, because the item is unowned between turns, so the pipeline has to be
  reconstructed from the row on every claim: a plan array, a cursor, a parallel
  rework array, a role-scoped `Claim`, one ScaledJob per (team, role), and a
  KEDA scaler query that must mirror the claim predicate exactly. Every one of
  those exists only to carry state across a boundary this option creates.
* Bad, because it contradicts `model.yaml` — "any number of Roles work within
  one Team's Lease" — and the divergence was never recorded, which is precisely
  the failure ADR-0001 exists to prevent.
* Bad, because it is strictly sequential: it cannot express simultaneity at all.

### One pod plays every role inside a single Lease

* Good, because nothing needs remembering — the pod never forgets, so no new
  durable state at all.
* Bad, because one pod is one image. Personas cannot differ by harness or
  container, which is the stated requirement and the reason ADR-0051 in
  `homelab-cluster` opened harness plurality.

### Follow-Up items plus per-persona Teams

* Good, because `Follow-Up` already exists in the model and Teams already differ
  by image, so this adds nearly nothing.
* Good, because the board sequences the work, which honours `design.md` §2.
* Bad, because it cannot express simultaneity on one item either — it serialises
  through ticket creation.
* Bad, because it requires tracker write-backs (backlog #31), today stubs.
* Kept as the fallback if Shifts prove heavier than forecast; the two are not
  mutually exclusive, and cross-item follow-ups remain the right shape.

### Shift owns the item; Lease owns the branch

* Good, because it is the smallest change that admits simultaneity: one new
  entity, one narrowed definition.
* Good, because agents in a round being unable to hear each other is the
  configuration the evidence favours. Multi-agent debate "forms a martingale on
  belief in the correct answer, providing no expected gain beyond independent
  voting"; weak models correct only 3.6% of their stance biases in debate,
  conforming to the majority instead; debate can score *below* a single agent.
  Independent opinions plus a synthesising writer is the stronger arrangement,
  and here it is also the cheaper one.
* Bad, because "Shift" has to be explained once to every reader. Accepted.

## Re-evaluation triggers

* **A round genuinely needs two simultaneous writers** — the writer/reader split
  is the load-bearing assumption; two concurrent mutators of one branch breaks
  it and forces per-writer worktrees or branch-per-persona.
* **Readers start needing to observe each other mid-round** — if independent
  opinions measurably underperform a conversation on real tickets, the round
  boundary is wrong and A2A becomes a live candidate (ADR-0007).
* **Shift lifecycle proves ambiguous in practice** — if "when does a Shift
  close" needs more than one rule, the entity is carrying too much.
* **A Shift's machinery exceeds the rejected design's** — the whole argument is
  that this is smaller. If it is not, Follow-Ups (above) are the fallback.
* **Parallel writers arrive in `Team.strategy`** — `model.yaml` already carries
  `strategy: enum(sequential, parallel)`; this record defines parallel as
  readers-only.

## More Information

* Closes the `run vs job` ambiguity in
  [domain/model.yaml](../domain/model.yaml), open since 2026-07-22.
* Domain rules cited: R2 (crash-safety), R6 (durable state), R4 (stuck carries a
  reason).
* Budget on the Shift: [ADR-0012](0012-two-level-budgets-authorized-and-settled.md).
* Findings transport: [ADR-0011](0011-the-pull-request-is-the-blackboard.md).
* Harness plurality that makes per-persona images possible:
  `homelab-cluster` ADR-0051, and [ADR-0007](0007-a2a-adopt-nothing-watchlist-a-facade.md)
  for why A2A is not the seam here.
* Evidence on debate versus independent opinion: *Not All Flips Are Conformity*
  (arXiv 2606.00820), *Can LLM Agents Really Debate?* (arXiv 2511.07784),
  *Talk Isn't Always Cheap* (arXiv 2509.05396).
