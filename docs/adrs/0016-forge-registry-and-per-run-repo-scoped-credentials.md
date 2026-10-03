---
status: proposed
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Resolve forges through a registry and mint forge credentials per Run

## Context and Problem Statement

The forge is a global singleton in three separate places: one `FORGEJO_URL` and
one `AGENT_BUILDER_TOKEN` rendered into every worker
(`ops/helm/ploeg/templates/_helpers.tpl:142-148`), and a hardcoded
`agent-builder` git identity (`pkg/worker/worker.go:118,125`). The dialect is a
fourth: `provider.ForgeProvider` (`pkg/provider/provider.go:47-70`) has **zero**
implementations, while `pkg/worker/forge.go:22-55` speaks Forgejo REST directly.

ADR-0014 makes a Work Target carry a forge **id** rather than a URL or a token,
so something must turn that id into a base URL, a dialect, a git identity and a
credential. And the moment one team can be sent at many repositories, the single
long-lived token stops being "the token for our repo" and becomes a key to every
repository any target can name. Backlog #75 already wants per-Run repo-scoped
tokens and cannot express `MintRequest.Target` today. Scope: `pkg/provider`,
`pkg/worker`, and the credential wiring in `ops/helm/ploeg`.

## Decision Drivers

* ADR-0014 requires forge ids to resolve to something; nothing resolves them
  today.
* Least privilege: a run should reach the repository it was dispatched at, and
  no other.
* R8: credentials reach the container out-of-band, never inside a Task Spec.
* Forgejo PATs never expire, so any minted token must be actively deleted —
  leaks are permanent by default.
* The published contracts and the provider SPI are the seams; a second forge
  must not require touching `pkg/worker`.

## Considered Options

* **A forge registry keyed by forge id, plus per-Run repo-scoped credentials
  minted by a broker**
* Keep one global forge; add per-team overrides in the chart
* Registry for URL/identity, but keep the single long-lived token
* Machine user per team with its own long-lived token

## Decision Outcome

Chosen option: "**A forge registry keyed by forge id, plus per-Run repo-scoped
credentials minted by a broker**", because it is the only option that makes the
forge id of ADR-0014 resolvable *and* stops a single credential from following
the newly widened set of reachable repositories.

A **Forge** is a registry entry keyed by the id a Work Target carries: base URL,
dialect, git identity (name + email used for commits), and a credential source.
It is configuration, not a per-team knob and not a per-item field — the Target
names it, the registry resolves it. `agent-builder` stops being a constant in
`pkg/worker/worker.go:118,125` and becomes the git identity of a registry entry.

The dialect field is what finally gives `provider.ForgeProvider` a caller: the
worker asks the registry for the provider bound to the target's forge id and
calls the SPI, instead of `pkg/worker/forge.go:22-55` hardcoding Forgejo REST
paths. Forgejo becomes the reference implementation (#32) rather than the
implicit only one — including the PR/branch-state read that design.md §4
promises and the interface currently lacks.

Credentials are minted **per Run**, scoped to the target's repository, and
actively deleted on every terminal path — the same three-layer discipline
already proven for the LiteLLM key (architecture.md §5): worker defer-revoke,
sweeper revoke on lease expiry plus boot-time orphan sweep, and a TTL as the
final net. Delivery is env-only via a credential helper / `GIT_ASKPASS` (#77) —
never `.netrc`, never a token in a remote URL, and never inside the Task Spec
(R8). The broker authenticates the job with a projected, audience-bound
ServiceAccount token (#76), which is compatible with the pod keeping
`automountServiceAccountToken: false` for the Kubernetes API itself.

### Consequences

* Good, because a compromised or prompt-injected run holds a credential for one
  repository, bounded by the run's own lifetime, instead of an
  organisation-wide PAT that never expires.
* Good, because `ForgeProvider` gets its first caller, so the second forge
  (GitHub, a second Forgejo instance) is a registry entry plus an SPI
  implementation — not a patch to `pkg/worker`.
* Good, because the commit identity becomes a property of the forge rather than
  a Go constant, which is a prerequisite for signing commits per forge later.
* Good, because it closes the widening this migration otherwise causes: ADR-0014
  lets one team reach many repositories, and this ADR is what keeps the
  credential from following.
* Bad, because the broker is a new privileged component — it holds the minting
  credential and must be operated, audited and rate-limited. The threat model
  (#79) has to cover broker compromise explicitly.
* Bad, because Forgejo PATs never expire: a missed delete is a permanent leak,
  so the revoke path needs the full three layers *and* an alert on orphaned
  tokens, mirroring `slo-ploeg-run-key-leak`.
* Bad, because rollout order matters. Between ADR-0014 landing and this ADR
  landing, the one global `AGENT_BUILDER_TOKEN` temporarily reaches every
  repository a target can name. That window must be kept short and stated in the
  rollout plan, not discovered in an incident review.
* Bad, because it is another coordinated change with `webgrip/homelab-cluster`:
  the registry, its SOPS-managed credential sources, and the removal of the
  global token env all live in the HelmRelease.

### Confirmation

* `helm template` renders no `FORGEJO_URL` and no `AGENT_BUILDER_TOKEN` into the
  worker container; a registry with two entries renders both.
* `grep -rn "agent-builder" pkg/` returns nothing — the identity comes from the
  registry.
* `pkg/worker/forge.go` contains no forge REST paths; the Forgejo dialect lives
  behind `provider.ForgeProvider` and is exercised by the conformance kit (#33).
* An integration test asserting mint → use → delete on all terminal paths
  (`pr_opened`, `no_change_needed`, `stuck`, sweeper-declared `failed`), plus a
  test that the minted credential is rejected by a repository other than the
  target's.

## Pros and Cons of the Options

### Forge registry + per-Run repo-scoped credentials

* Good, because scope and lifetime both shrink to the run.
* Good, because it is the only shape in which a Work Target's forge id means
  anything.
* Bad, because it adds a privileged broker and a revoke path that must be
  correct on every exit.

### One global forge, per-team overrides in the chart

* Good, because it is a small chart change with no new component.
* Bad, because it re-binds the forge to the team, which is the coupling ADR-0014
  exists to remove — a team could then be sent at a target on a forge it has no
  entry for.
* Bad, because it leaves `ForgeProvider` without a caller and the dialect
  hardcoded.

### Registry for URL and identity, keep the single long-lived token

* Good, because multi-forge works with no broker.
* Bad, because the credential is exactly what needs to shrink: after ADR-0014
  one token reaches every reachable repository, permanently (Forgejo PATs do not
  expire).
* Bad, because #75 stays blocked and the security items (#71–#79) keep a hole in
  the middle.

### Machine user per team with its own long-lived token

* Good, because it is the documented pre-v15 fallback in #75 and needs no
  minting API.
* Neutral, because it bounds blast radius by team rather than by run — better
  than today, worse than per-run.
* Bad, because it re-introduces per-team provisioning, the exact onboarding cost
  ADR-0014 removes.

## More Information

* Technical story: architecture.md §9.15 · backlog #106 (with #32, #75, #76,
  #77, #79)
* 2026-07-29 — decision approved by the repo owner. Status stays `proposed`
  until it lands end to end; on `development` at this date the globals are still
  live in `_helpers.tpl:142-148` and `pkg/worker/worker.go:118,125`,
  `ForgeProvider` still has zero implementations, and no broker exists.
* Supports [ADR-0014](0014-work-target-is-a-work-item-attribute.md) — it
  resolves the forge id a Work Target carries.
