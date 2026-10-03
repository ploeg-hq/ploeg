---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# Paperclip: mine it for design, never depend on it

## Context and Problem Statement

Paperclip (`paperclipai/paperclip`) was surveyed on 2026-07-28 against a local
clone. It is an MIT, ~1M-LOC TypeScript control plane that *is* the tracker —
tasks, org chart, budgets, approvals, skills, secrets, routines, full UI — with
agents attached via per-agent scheduled heartbeats. It reached 74,962 stars five
months after its first commit, ships weekly CalVer releases, is company-backed,
and has a cloud tier emerging.

It is the most uncomfortable entry in this ledger, because it has already
shipped most of Ploeg's phase-2 and phase-3 roadmap.

*Method caveat, stated up front:* unlike the A2A and AHP sweeps this was not a
clean four-agent fan-out. Three Paperclip-side agents were killed mid-dig by an
org spend limit and their work was redone by hand. File-level claims were read
directly and are solid; contributor statistics come from a shallow 50-commit
clone and must not be read as project-lifetime attribution.

## Decision Drivers

* **Layer ownership.** Ploeg deliberately refuses the tracker seam; Paperclip
  owns it and also claims dispatch.
* **An integration seam must actually exist** for integration to be an option.
* **Thin glue** — the rationale behind [0002](0002-go-as-the-implementation-language.md).
* **Honest assessment of where we hold value**, and where we simply do not.

## Considered Options

* Integrate: run Ploeg behind or beside Paperclip
* Adopt Paperclip and retire Ploeg
* Mine it for design semantics; write no code against it

## Decision Outcome

Chosen option: **mine it for design; write no code against it.**

Unlike A2A (which failed on fit despite maturity) and AHP (which failed on
maturity), Paperclip fails on **layer collision**: it is neither too immature nor
too foreign, it is a rival occupying the opposite pole of the same problem.

Integration is not available even in principle. Paperclip has no tracker-provider
interface to plug into — BYO-ticket-system is an unshipped roadmap item — and
there is zero Vikunja, Forgejo, Gitea, KEDA or LiteLLM contact in its tree. Its
dispatch model (per-agent heartbeats plus `checkoutRunId` issue-row locks) is a
second implementation of Ploeg's job, not a layer above or below it. And
fronting a ~5k-LOC Go dispatcher with a 1M-LOC Node server on a weekly breaking
release train inverts the thin-glue rationale entirely.

**Where we structurally hold value they do not:** event-driven scale-to-zero
dispatch (no heartbeat cron burning tokens to discover there is no work);
tracker and forge neutrality for self-hosted stacks; database-lease crash-safety
that does not depend on agent goodwill; and proxy-metered per-run credentials
with active revocation. That last one is a real difference in kind — their spend
ledger is parsed from adapter stdout, so a compromised or lying agent skews the
books, while ours is metered at a LiteLLM boundary the agent cannot bypass and
joined to ticket and commit by `ploeg-<12hex>`
([0008](0008-litellm-is-the-credential-and-metering-seam.md)).

**Where they are simply ahead, stated without flinching:** approval gates,
agent↔human interactions, watchdogs with bounded recovery, per-scope budgets
enforced pre-dispatch *and* pre-invocation, a skills system, a secrets manager
with audited run-bound access, and a production `agents.x-k8s.io` sandbox
builder — while Ploeg's `needs_human` state has nothing behind it, checkpoints
are written and never read, and the promised `ForgeProvider` has no
implementation. Five months of company-scale velocity beat four years of anyone's
spare time.

Their `doc/execution-semantics.md` is the most valuable free design review this
backlog has received. It is folded into backlog #9, #10, #15, #16, #21, #44,
#58, #60, #64 and #86. Steal semantics; write no code against it.

### Consequences

* Good, because a large body of hard-won distributed-systems semantics arrives
  as design input at zero dependency cost — notably fingerprint-bounded recovery
  (#15), checkout-finalisation CAS rules (#16), pre-dispatch budget gates (#44),
  and the treatment of review text as untrusted input to the next run (#9).
* Good, because the differentiators are now stated explicitly rather than
  assumed, which makes the 2027-04 review gate answerable.
* Bad, because if the mainstream verdict is "the board and the control plane
  should be one product", Ploeg's audience shrinks to operators who refuse that
  bundle. That is a real, unmitigated risk, and it is the same narrow-audience
  risk recorded in [0005](0005-build-a-dedicated-dispatch-plane.md).

### Confirmation

No Paperclip dependency exists in `go.mod` or `package.json`, and none may be
added — a reviewer checks this record. Adopted semantics are traceable: each
lands as a numbered backlog item tagged `*[research: paperclip sweep]*` rather
than as an unattributed idea.

## Pros and Cons of the Options

### Integrate: run Ploeg behind or beside Paperclip

* Good, because their UI and approval surface is years ahead of anything Ploeg
  will build.
* Bad, because there is no seam: no tracker-provider interface, and their
  dispatch model duplicates rather than delegates.
* Bad, because a weekly breaking release train on the critical path of an
  unattended money-spending system.

### Adopt Paperclip and retire Ploeg

* Good, because most of the roadmap is already shipped, today.
* Bad, because it owns the tracker, which is the seam Ploeg exists to refuse —
  the Vikunja board stays the source of truth.
* Bad, because heartbeat polling, stdout-parsed spend accounting, and no
  self-hosted-forge story are three independent disqualifications for this
  stack.

## Re-evaluation triggers

* **BYO-ticket-system ships with outbound assignment events** — that would make
  Paperclip a candidate *human surface above* Ploeg, and is the trigger to
  evaluate before building De Vloer equivalents.
* **Their Work Queues milestone ships claimable-queue semantics** — direct layer
  collision; re-run this comparison.
* **`acpx` reaches a stable standalone 1.0** — build backlog #64 against it
  rather than a bespoke engine. (As of 2026-07-29 it is 0.13.0, maintained by
  OpenClaw, and still carries an explicit alpha warning.)
* **`agents.x-k8s.io` graduates past alpha** — accelerate backlog #58.

## More Information

* Full evidence trail, including the method caveat:
  [research/2026-07-28-paperclip-fit.md](../research/2026-07-28-paperclip-fit.md).
* Migrated from `docs/design.md` §8 on 2026-07-29.
* Adopted semantics: backlog #9, #10, #15, #16, #21, #44, #58, #60, #64, #86.
