---
type: reference
audience: [owner, integrator, contributor, agent]
owner: ploeg
generated_by: "mise run domain"
---

# Business Rules — Ploeg

*Generated from `model.yaml` — do not edit by hand. Cite rules by id in specs.*

## Delivery Candidate

### R17
*Context: Integration*

Publication requires an immutable Delivery Candidate, a matching trusted Verification Receipt and explicit candidate-bound human approval. An ambiguous Publication Operation retains its barrier until trusted positive evidence reconciles the external effect.

**Why:** Neither worker claims nor a timeout establish that publication is safe to repeat.

**Also applies to:** Verification Receipt, Publication Operation

## Follow-Up

### R9
*Context: Dispatch*

Intended behavior — Follow-Ups are routed to the Team owning the source branch and never gate other Teams' new work.

**Why:** Feedback loops must stay local — one team's red CI must not stall the whole factory.

**Also applies to:** Team

## Inference Account

### R15
*Context: Execution*

Management credentials stay in the control plane. Inference Account authorization remains held across uncertain issuance, expiry and blocking until trusted final accounting reconciles it.

**Why:** A crashed executor cannot authorize unrelated work or make unresolved paid work appear free.

**Also applies to:** Run, Executor

## Lease

### R2
*Context: Dispatch*

A Lease must be renewed on a fixed interval by the running Run. Tracker execution expiry enters the configured recovery policy; Operator Execution expiry interrupts work and preserves pending stop intent and unresolved Inference Account authorization.

**Why:** Controller recovery must work when an executor dies without cleanup, while preserving the applicable lane's stop intent and accounting holds.

**Also applies to:** Run

## Operator Execution

### R13
*Context: Dispatch*

Operator Execution commands require authenticated consumer and actor scope, a unique command identity, current revision and current generation. Replaying an accepted command returns its original result.

**Why:** Lost responses must not create duplicate work or silently repeat paid submissions.

**Also applies to:** Operator Consumer

### R14
*Context: Dispatch*

A pending cancellation remains pending until confirmed cancelled; interruption, expiry and restart never turn it into resumable work. A pause requires confirmed stop before explicit resume.

**Why:** A deliberate human stop must survive every recovery boundary.

**Also applies to:** Run

## Outcome

### R4
*Context: Dispatch*

A stuck Outcome carries a mandatory reason and transitions the Work Item to needs_human.

**Why:** Silent stalls are the most expensive failure mode of unattended agents; stuck must always surface to a human with context.

**Also applies to:** Work Item

## Run

### R3
*Context: Execution*

Every Run must retain a queryable outcome or recovery state. The runner submits an Outcome Report; controller expiry and reconciliation handle missing reports. Operator pause, cancel and uncertain external effects must not become automatic retries.

**Why:** Audit completeness — no Run may vanish without a queryable terminal row.

**Also applies to:** Outcome Report, Executor

### R6
*Context: Dispatch*

Authoritative Ploeg execution state lives in Postgres and durable repository evidence lives in git/forge state. Human-session records may live in the delegated workbench store; they cannot independently grant execution authority.

**Why:** Ephemerality is the design axiom; any state trapped in a long-lived process breaks crash-safety and resume.

**Also applies to:** Checkpoint

## Task Spec

### R8
*Context: Harness*

Credentials are delivered to an Agent Container through scoped runtime configuration outside the Task Spec. Only the credentials needed by that Run may be exposed.

**Why:** Task Specs are logged, audited, and checkpointed; secrets in them would leak into every one of those stores.

**Also applies to:** Agent Container

## Team Queue

### R10
*Context: Dispatch*

Team Queue order mirrors the tracker's priority, falling back to oldest-first; Ploeg never owns prioritization.

**Why:** The board is the source of truth for what matters and when; duplicating rank in Ploeg would create a second, silently diverging opinion.

**Also applies to:** Tracker Item

## Tracker Item

### R19
*Context: Integration*

A research ticket asking whether a product is worth building may finish with convincing evidence, a documented business case and a conclusion to stop. A technical design or proof of concept is not required when that conclusion is supported.

**Why:** Research must support a decision; building an unwanted product does not improve the answer.

## Tracker Provider

### R7
*Context: Integration*

Core semantics must never encode a provider-specific workaround; everything vendor-specific lives behind the SPI.

**Why:** SPI stability is the project's compatibility promise; one leaked vendor detail makes every other provider carry it forever. It cuts both ways — a provider never resolves a Team or a Work Target either; it emits a Scope the core compares for equality and never interprets.

**Also applies to:** Forge Provider, Tracker Event, Scope, Routing Rule

## Work Item

### R18
*Context: Dispatch*

An Operator Execution requires authenticated admission. A consumer's own record of the work does not admit it; an admission request does, including for hands-on work. Work a consumer runs without asking Ploeg creates no Ploeg records.

**Why:** Admission follows the configured execution mode and explicit start, not whether a person is currently watching the work.

**Also applies to:** Operator Execution

### R16
*Context: Dispatch*

Binding tracker work to an Operator Execution retains its canonical Work Item identity and tracker origin. Admission atomically excludes unattended claims and requires fresh registered Scope and Work Target expectations. Tracker refresh never implicitly relinquishes operator ownership.

**Why:** Human interaction must not create a duplicate dispatch or restart intentionally stopped work.

**Also applies to:** Operator Execution, Work Target

### R1
*Context: Dispatch*

A Work Item is held by at most one Team at a time; a Lease is unique per Work Item.

**Why:** Two Teams on one item means two writers on one branch and split accountability in the audit log.

**Also applies to:** Lease, Team

### R5
*Context: Dispatch*

Unattended tracker recovery has bounded retries. Infrastructure expiry and reported agent failure use separate counters and backoff rules; exhausted work requires intervention. Operator-owned work follows its own explicit lifecycle and never enters this automatic retry path.

**Why:** Retrying is cheap once and ruinous forever — stale is the circuit breaker that stops burning tokens on repeatedly abandoned work.

**Also applies to:** Lease

### R11
*Context: Dispatch*

Intended target model — a Work Item carries its own Work Target (forge, owner, repository, base branch), resolved at ingest and independent of the Team that claims it. A Team is a capability manifest and never names a repository. A Work Item without a resolved Work Target is not claimable, and the set of reachable Work Targets is closed and operator-declared.

**Why:** A Team is a crew, not a codebase. Binding them makes every capability change a repository migration and vice versa, leaves "two Teams on one repository" and "one Team across many repositories" both unrepresentable, and makes per-Run repo-scoped credentials impossible to express. The closed set keeps tracker content — untrusted input — from pointing a write-scoped credential at an arbitrary repository.

**Also applies to:** Work Target, Team, Routing Rule

## Work Target

### R12
*Context: Dispatch*

A Work Target is pinned once a Run has produced durable git state for it (a Work Item with an assigned branch); re-resolution that would change it is recorded as a divergence and requires a human.

**Why:** Re-dispatch is the review-round mechanic; silently retargeting round n+1 orphans the branch and the open PR that round n produced.

**Also applies to:** Work Item, Run
