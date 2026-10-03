---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# LiteLLM stays the per-run credential and metering seam

## Context and Problem Statement

Every run mints a budgeted, TTL-bounded LiteLLM key aliased `ploeg-<12hex>` and
revokes it on every return path (`pkg/llmbroker`, `pkg/litellm`). The alias is
the join key between a ticket, a commit and its spend in Grafana. This is the
single place Ploeg touches money, so the choice of gateway is a trust decision
rather than a routing convenience.

OmniRoute (`diegosouzapw/OmniRoute`) was surveyed on 2026-07-28 as a candidate
replacement for this seam.

## Decision Drivers

* **Mint/revoke admin API with per-key budget, TTL and alias.** This is the
  entire coupling; without it there is no per-run credential model.
* **Metering the agent cannot bypass.** Spend must be measured at a boundary the
  agent talks *through*, not parsed from what it says it did.
* **Kubernetes and Helm deployment**, since every run is a Job.
* **Trust posture appropriate to a credential-holding boundary** in an
  unattended, money-spending system.

## Considered Options

* Keep LiteLLM
* Replace the seam with OmniRoute
* Run OmniRoute alongside, for interactive/workstation use

## Decision Outcome

Chosen option: **keep LiteLLM.**

OmniRoute is a competitor for this seam and loses on the requirement that
defines it: there is no admin API offering `/key/generate` parity — no per-key
budget, TTL or alias — so the per-run credential model could not be built on it
at all. It is local-first single-box Node/SQLite with no Kubernetes story (its
own comparison document concedes "choose LiteLLM for k8s/Helm") and no
multi-tenancy.

The trust posture is the sharper objection. For a component that holds
credentials in an autonomous factory: a default JWT secret, plaintext keys
unless opted in, fail-open guardrails, a Socket.dev npm block in May 2026, and
core economics that depend on ToS-grey free-tier farming via TLS-fingerprint
(JA3/JA4) impersonation. That is arbitraged-and-deniable spend, against a design
whose entire audit value is metered-and-attributable spend. Add a solo author
(~62% of commits), a 5.5-month rewritten history, and zero named production
users.

Its MCP and A2A endpoints serve its own tooling and do not touch the harness
seam, so there is no consolation integration.

OmniRoute is a reasonable *personal-workstation* router for interactive coding
agents. That use is not forbidden; it is kept away from factory credentials —
the same posture applied to A2A experiments in
[0007](0007-a2a-adopt-nothing-watchlist-a-facade.md).

### Consequences

* Good, because per-run keys, budgets, TTLs and the `ploeg-<12hex>` audit chain
  keep working unchanged.
* Good, because spend is metered at a boundary the agent cannot bypass, so a
  compromised or lying agent cannot skew the books.
* Bad, because Ploeg stays coupled to one gateway's admin API shape. Mitigated
  structurally: the coupling is confined to `pkg/llmbroker`'s `Broker`
  interface, with a `Static` implementation already proving a second backend is
  possible.

### Confirmation

`pkg/llmbroker` is the only package that mints or revokes credentials, and its
`Broker` interface is the seam — a second gateway would be a new implementation
behind it, not a change in `pkg/worker`. Key-lifecycle regressions are pinned by
the deferred-revoke tests in `pkg/worker/worker_test.go` (revoke on success,
error, and context-cancel paths). The alias format is joined by Grafana
dashboards in `webgrip/homelab-cluster`, so drift surfaces as a broken panel.

## Pros and Cons of the Options

### Replace the seam with OmniRoute

* Good, because its routing and failover across providers is richer than what is
  used today.
* Bad, because no per-key budget/TTL/alias admin API — the one thing required.
* Bad, because single-box Node/SQLite, no Kubernetes story, no multi-tenancy.
* Bad, because the trust posture is wrong for a credential boundary, and the
  free-tier-farming economics are the opposite of an attributable audit trail.

### Run OmniRoute alongside for workstation use

* Good, because it is genuinely useful for interactive coding agents.
* Bad, because two gateways means two credential models to reason about if it
  ever drifted toward the factory. Permitted only with factory credentials kept
  out.

## Re-evaluation triggers

* OmniRoute's 4.0 modular platform ships a headless mode and a Kubernetes story.
* Its admin API reaches LiteLLM `/key/generate` parity — per-key budget **and**
  TTL **and** alias. Anything less does not move this decision.
* OpenHands merges OmniRoute provider support (two attempts closed unmerged as
  of 2026-07).
* Its governance matures past a single maintainer with a stable 3.9 LTS.
* Independently: LiteLLM gaining teams-as-code, or `PalenaAI/litellm-operator`
  maturing, would change *how* this seam is configured without changing the
  choice — see `research/2026-07-28-agent-roster-ssot.md`.

## More Information

* Full evidence trail:
  [research/2026-07-28-omniroute-fit.md](../research/2026-07-28-omniroute-fit.md).
* Migrated from `docs/design.md` §8 on 2026-07-29.
* Implementation: `pkg/llmbroker`, `pkg/litellm`; the alias contract is
  `architecture.md` §5.
* Estate context: `webgrip/homelab-cluster` ADR-0044 (metered inference plane).
