---
status: proposed
date: 2026-09-02
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The forge dialect travels on the Work Item; the forge URL and credential stay deployment-global

## Context and Problem Statement

ploegd has spoken GitLab since rc.31 — `pkg/provider/gitlab` comments on a
merge request and verifies inbound webhooks — but `ploeg-worker` only ever
spoke Forgejo. It polled `/api/v1/repos/{owner}/{name}/pulls` and its briefing
named the Forgejo API as the way to open a change request. Against a GitLab
target nothing was ever opened, so the Shift had no change request for a
reviewing Role to comment on, `publishRound` logged "no pull request on this
shift yet", and the review loop could not close. Every step of that is silent.

Making the worker multi-forge asks one question that outlives the answer:
**where is "which forge" decided** — per deployment, per Team, or per Work
Item?

## Decision Drivers

* ADR-0014 already put the repository on the Work Item, resolved at ingest.
  The forge is part of naming a repository, so splitting the two would mean a
  target that says WHICH repo but not WHERE.
* `pkg/work.Target` has carried a `Forge` field since the forge registry
  landed (ADR-0016), and ploegd already resolves findings through it. A second,
  disagreeing answer in the worker is the failure mode to avoid.
* R7 — vendor specifics live behind the SPI, never in core semantics.
* A worker pod holds exactly one forge credential and one forge URL. Anything
  that implies two in one pod is a bigger change than this one.

## Considered Options

* **A. Dialect on the Work Item; URL and credential deployment-global**
* **B. Dialect per Team, in `executor.teams[]`**
* **C. Dialect per deployment only — one forge per release, no per-run answer**

## Decision Outcome

Chosen option: **A**, because it is the only option under which the two halves
of the system cannot disagree. ploegd decides which forge to comment on from
`Target.Forge`; the worker now decides which forge to open a change request on
from the same field. One value, resolved once at ingest, read by both.

The deployment keeps the URL and the credential because a pod has one of each.
`executor.forge` selects the active block and supplies the DEFAULT dialect for
a Work Item whose target names none — the same "empty = the default forge"
promise `pkg/work.Target` and the forge registry already make. A target that
names its own forge overrides it per run.

Concretely: `harness.RepoRef` gains `Forge`, additive and optional on
`taskspec.v1`; empty means `forgejo`, so every Task Spec and every stored
target written before this field keeps its exact meaning.

### Consequences

* Good, because onboarding a repository on a second forge is a routing entry
  and a webhook, not a new Team and not a new release.
* Good, because the dialect is decided in one place. A wrong answer is wrong
  everywhere at once and therefore visible, rather than ploegd commenting on
  one forge while the worker opened the change request on another.
* Good, because an unknown dialect fails loudly. Falling back to Forgejo would
  poll a real endpoint shape against the wrong host and report "no change
  request" forever — indistinguishable from an agent that never opened one.
* Bad, because one deployment still cannot span two forges: the dialect varies
  per run but the URL and credential do not. A Work Item routed to a forge the
  release is not configured for fails at run time, not at boot. Accepted: the
  alternative is a credential set per forge in every worker pod, which widens
  the pod's blast radius to buy a case nobody has.
* Bad, because ADR-0013 tier 2 does not port. ploegd mints per-run push
  credentials through `/api/v1/admin/users/forge`, a Forgejo admin endpoint
  with no GitLab equivalent; GitLab's nearest analogue is a project access
  token, a different escalation with a different blast radius. GitLab runs on
  the shared token — the documented pre-tier-2 behaviour — until that is
  decided on its own evidence. Tier 1, the reader/writer credential split,
  DOES port and is configured.

### Confirmation

* `pkg/harness/contract_test.go` pins `RepoRef` to
  `docs/contracts/taskspec.v1.schema.json`, whose `repo` block is
  `additionalProperties: false`. The Go field and the published schema cannot
  drift: adding one without the other fails `go test ./...` in
  `.forgejo/workflows/on_pull_request.yml`.
* `ops/helm/ploeg/ci/golden/executor-gitlab.yaml`, checked by
  `./scripts/helm-golden.sh check` in the same workflow, pins the rendered
  worker pod for a GitLab deployment — including that a reading Role draws
  `agent-reader-token` and the writing Role draws `agent-builder-token`, so a
  tier-1 regression shows up as a reviewable diff rather than a quiet widening.
* `TestFindPRUnknownForge` and `TestComposePrompt_ForgejoWriterUnchanged` pin
  the two directions that matter: an unknown dialect errors, and the Forgejo
  contract is byte-for-byte what it was.

## Pros and Cons of the Options

### B. Dialect per Team

* Good, because it needs no contract change.
* Bad, because it re-couples capability to codebase — exactly what ADR-0014
  decoupled. A Team is a capability tier; "which forge" is a property of the
  repository it was pointed at, and a tier that works two repos on two forges
  would need splitting for a reason that has nothing to do with capability.
* Bad, because ploegd would still read `Target.Forge` while the worker read
  the team config. Two answers, no gate.

### C. Dialect per deployment only

* Good, because it is the smallest possible change.
* Bad, because it makes the second forge a second release of the whole
  dispatch plane, and leaves `Target.Forge` — already populated, already used
  by ploegd — meaning one thing to the engine and nothing to the worker.

## Re-evaluation triggers

* A GitLab project access token API that can be created and revoked per run
  with repository-scoped push rights, making an ADR-0013 tier 2 for GitLab
  cost a provider method rather than a new escalation.
* A third forge arriving. Two dialects justify a switch; three justify a
  ForgeProvider-style SPI on the worker side, and this record should be
  superseded rather than extended.
* Any deployment needing to span two forges from one release — the accepted
  cost above becoming a real requirement rather than a hypothetical one.

## More Information

* Extends ADR-0014 (the target is a Work Item attribute) and ADR-0016 (the
  forge registry) to the worker half of the system.
* Constrained by ADR-0013 (push rights are minted per run) — tier 1 ports,
  tier 2 does not; see Consequences.
* Domain rules: R7 (vendor specifics behind the SPI), R11 (the repository
  belongs to the work).
