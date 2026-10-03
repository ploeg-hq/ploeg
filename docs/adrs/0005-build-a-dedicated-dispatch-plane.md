---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Build a dedicated dispatch plane rather than adopt an existing orchestrator

## Context and Problem Statement

A ticket assigned on a board should become an ephemeral, leased, budgeted agent
run that ends in a pull request. Something has to own *how work gets executed* —
claims, leases, retries, outcomes, audit — without owning *what to do*, which
stays with the tracker.

A 2026-07 market survey asked whether anything already did this. Seven
candidates were examined across three categories: board-orchestrators that
attach agents to tickets, Kubernetes runtime layers, and generic workflow
engines. Building is the expensive answer and needed to be justified against all
of them.

## Decision Drivers

* **Tracker and forge neutrality for self-hosted stacks.** Vikunja + Forgejo is
  the target; every mainstream product assumes GitHub, Jira, GitLab or Linear.
* **Event-driven, scale-to-zero dispatch.** Assignment fires a webhook and a Job
  spawns. No heartbeat cron burning tokens to discover there is no work.
* **Crash-safety independent of agent goodwill.** A claim is a database row with
  a TTL. A dead pod releases its ticket mechanically.
* **Auditability as rows**, not as parsed agent stdout.
* **Thin glue.** The value is the semantics, so the artefact should be small
  enough that abandonment costs an adopter little.

## Considered Options

* Build a dedicated dispatch plane (Go + Postgres + KEDA)
* `misospace/dispatch`
* `kandev`
* `vibe-kanban`
* `untra/operator`
* kagent / kars / `agent-sandbox`
* Argo Workflows as the substrate
* Forge-native agents (GitLab Duo, GitHub)

## Decision Outcome

Chosen option: **build a dedicated dispatch plane.**

No candidate combined tracker neutrality, lease-based crash-safety and a
Kubernetes runtime. The closest semantic match (`misospace/dispatch`) was
GitHub-only, polling, and a 2★ single-maintainer project — i.e. it failed the
same bus-factor test that this project openly fails too, without offering the
neutrality that would have made the trade worthwhile.

The runtime layers (kagent, `agent-sandbox`) are not competitors at all: they
sit *under* a dispatcher, and `agent-sandbox` is a planned runtime under Ploeg
rather than an alternative to it.

Scope is bounded by what this record does *not* claim: Ploeg owns dispatch and
nothing else. No board UI, no persistent agents, no model serving, no grooming
semantics, no core-maintained connector matrix.

### Consequences

* Good, because the semantics that matter — leases, claims, outcomes, audit —
  are ours to make correct, and they live in Postgres where they can be queried.
* Good, because tracker and forge stay behind a provider SPI, so the
  self-hosted-stack niche is servable at all.
* Bad, because Ploeg starts as a 0★ single-maintainer project: exactly the
  profile the survey rejected in others. Mitigations are structural rather than
  aspirational — durable state is plain Postgres, the codebase is thin glue, the
  conventions are portable, and the contracts are published as versioned schemas
  in `docs/contracts/`. Abandonment should cost an adopter a migration, not a
  rewrite.
* Bad, because the audience — self-hosted-stack platform operators — is
  genuinely narrow. Forge vendors own the mainstream and always will.

### Confirmation

The exit criterion is behavioural, not documentary: **the originating dark
factory runs on Ploeg in production**, dispatching real tickets to real PRs.
That is true today for the dispatch core and both executors. Scope discipline is
confirmed by `docs/backlog.md`'s explicit exclusion list and by `design.md` §2
non-goals, which a reviewer checks any proposal against.

## Pros and Cons of the Options

### `misospace/dispatch`

* Good, because the closest semantic match found: leases, lanes, and an audit
  trail — the same primitives, independently arrived at. Several of its ideas
  were adopted outright (backlog #14, #16, #18).
* Bad, because GitHub-only, a polling model, and 2★ with a single maintainer.

### `kandev`

* Good, because the best-maintained board-orchestrator in the survey.
* Bad, because GitHub/Jira/Linear/GitLab only, no Kubernetes runtime, no
  teams/leases/audit, and its centre of gravity is an interactive workspace
  rather than unattended dispatch.

### `vibe-kanban`

* Good, because a working product with real users.
* Bad, because the company shut down in 2026, it is community-maintained, and it
  is scoped to a workstation rather than a cluster.

### `untra/operator`

* Good, because the ticket-first concept matches exactly.
* Bad, because it is an alpha TUI driving local tmux, at 33★.

### kagent / kars / `agent-sandbox`

* Good, because they solve runtime and policy properly, and
  `agents.x-k8s.io` is the right long-term sandbox primitive.
* Bad, because they are not dispatch semantics at all — wrong layer.
  `agent-sandbox` is a planned runtime *under* Ploeg (backlog #58), not an
  alternative to it.

### Argo Workflows as the substrate

* Good, because the exit-handler and DAG story is strong, and it is battle-tested
  at scale. Its synchronization semantics were adopted as backlog #41/#42.
* Bad, because it is a second orchestration system to operate for a workload
  that is currently "sequential specialists on one branch". Revisit if team DAGs
  outgrow that shape.

### Forge-native agents (GitLab Duo, GitHub)

* Good, because they serve the mainstream well and will keep improving.
* Bad, because they structurally cannot serve self-hosted or mixed stacks — which
  is the entire niche. This is a durable structural fact, not a feature gap.

## Re-evaluation triggers

* **2027-04 review gate** (`design.md` §10): external users or contributors
  exist → invest in product-ness (versioned SPI, docs, provider certification).
  None → Ploeg is personal infrastructure and the README says so plainly.
* A tracker-neutral, lease-based, Kubernetes-native dispatcher appears with more
  than one maintainer — the "why not adopt" answer would change overnight.
* Team DAGs outgrow "sequential specialists on one branch" → re-open Argo
  Workflows as the substrate.
* `agents.x-k8s.io` graduates past alpha → accelerate backlog #58.
* Vikunja or Forgejo ship native agent-dispatch endpoints — the niche would
  start closing from below.
* A quarterly market re-scan stays on the operator's board regardless
  (`design.md` §10).

## More Information

* Migrated from `docs/design.md` §8 (rows 1–7) on 2026-07-29. Individual "why
  not" verdicts are preserved above as Pros and Cons.
* Survey date: 2026-07. Adopted ideas are tagged `*[research]*` in
  `docs/backlog.md`.
* Later protocol- and product-fit verdicts have their own records:
  [0006](0006-ahp-is-the-wrong-layer.md),
  [0007](0007-a2a-adopt-nothing-watchlist-a-facade.md),
  [0008](0008-litellm-is-the-credential-and-metering-seam.md),
  [0009](0009-paperclip-mine-for-design-never-integrate.md).
