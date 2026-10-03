---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Bind the Work Target to the Work Item, not to the Team

## Context and Problem Statement

`team` is one string doing a dozen jobs in ploeg, and one of them is wrong: it
binds a repository. `ops/helm/ploeg/values.yaml` team entries carry
`repoOwner`/`repoName`/`baseBranch`, `values.schema.json:34` makes the first two
**required**, and `cmd/ploeg-worker/main.go:70-74` `requireEnv`s
`REPO_OWNER`/`REPO_NAME` before the worker claims anything — so onboarding a
repository costs a new team **plus** a KEDA ScaledJob **plus** a tracker bot
user, even when model, budget, harness and strategy are identical.

No normative document says a Team has a repository: design.md §3 defines a Team
as "name, specialist roles, harness image + model per role, run strategy,
resource/token budget", and the Team entity in `docs/domain/model.yaml` listed exactly
`name`, `roles`, `strategy`, `budget`. The model's only repository is
`TaskSpec.repo_url` (Task Spec entity, same file), with no statement of how
it is derived. The binding exists only in the chart. This ADR decides where the
repository coordinate lives instead; scope is `pkg/work`, `pkg/store`,
`pkg/worker`, `cmd/ploeg-worker`, and `ops/helm/ploeg`.

## Decision Drivers

* Onboarding a repository must not require provisioning capacity (team,
  ScaledJob, bot user) that already exists.
* Audit must answer "which repository did run *n* touch" from the run's own row,
  not from whichever Helm values were live at the time.
* Re-dispatch (review round *n+1*, `done → queued`) must return the agent to the
  branch and PR round *n* created — never silently retarget.
* R6: durable state lives in Postgres and git/forge, never in an agent process.
* R8: a Task Spec must not carry credentials, so a repository coordinate must be
  expressible without a URL or a token.

## Considered Options

* **Work Target resolved at ingest, pinned on the Work Item row**
* Keep the repository on the Team (status quo): one team per repository
* Resolve the target lazily at claim time from the rule table, store nothing
* Carry the repository in the ticket body, read it in the harness

## Decision Outcome

Chosen option: "**Work Target resolved at ingest, pinned on the Work Item
row**", because it is the only option that makes the Team a pure capability
manifest while still giving audit, re-dispatch and cost attribution one stable,
queryable answer per work item.

A **Work Target** is `(forge id, owner, repo, base branch, branch)` and is an
attribute of a **Work Item**, not of a Team. It is a *coordinate, not a
connection*: it carries a forge **id** that ADR-0016's registry resolves to a
base URL, dialect, git identity and credential source — never a URL, never a
token (R8). Resolution happens once, in the ingest path, from the routing rules
of ADR-0015; the resolved tuple is written to `work_items` in the same
transaction as the queued state change and its audit row (#25).

The Team keeps everything else it already has — roles, harness image + model,
strategy, budget, concurrency cap — and remains the KEDA scaling unit. That
scaling unit becomes *legitimate for the first time*: once the repository
leaves, the worker pod template (`ploeg.workerPodTemplate`) is a pure function
of the Team, which is the only property KEDA's per-team ScaledJob actually
requires. `scaledjob.yaml:44` keeps string-interpolating the team name into the
scaler query (the postgresql scaler has no bind parameters), which stays safe
because `values.schema.json:25` constrains team names to a DNS label — this
decision does not touch that.

Consequential changes:

* `work_items` gains the target columns (`pkg/store/migrations`); `agent_runs`
  inherits the target dimension it lacks today (architecture.md §9.17).
* `repoOwner`/`repoName`/`baseBranch` leave the team entry in
  `ops/helm/ploeg/values.yaml` and stop being required in
  `values.schema.json:34`; `REPO_OWNER`/`REPO_NAME` leave
  `cmd/ploeg-worker/main.go:70-74`. The worker receives its target in the claim
  response, so a boot-time repository is no longer knowable — nor needed.
* The branch name becomes `agent/<provider>-<externalID>` instead of
  `agent/vik-<id>` (`pkg/worker/worker.go:81`), removing a Vikunja token from
  core semantics (R7) and making `(forge, owner, repo, branch)` a unique
  reverse-lookup key — the join R9's follow-up routing has never had (backlog
  #107).

### Consequences

* Good, because the target is an immutable fact of the item: the audit log and
  `agent_runs` answer "which repository, which base, which branch" without
  reconstructing the Helm values of that day.
* Good, because re-dispatch is correct by construction — review round *n+1*
  re-uses the pinned target and cannot retarget the item, which would orphan the
  branch and PR round *n* opened.
* Good, because the claim hot path evaluates zero routing rules; ingest pays the
  cost once per item, not once per attempt.
* Good, because one ScaledJob per team now serves many repositories, which is
  the entire point: capacity and codebase become independent axes.
* Good, because it unblocks per-run repo-scoped forge tokens (#75 —
  `MintRequest.Target` has nothing to bind to today), the per-target mutex
  (#42), and per-product spend rollups.
* Bad, because a routing-rule change does not retroactively move already-queued
  items; they keep the target they were ingested with. That is the intent, but
  it needs an explicit `ploegctl` retarget operation (#96) rather than silent
  drift.
* Bad, because it is a coordinated migration: the store migration, the chart
  schema change and `webgrip/homelab-cluster`'s HelmRelease must land in one
  window — the chart cannot drop a required value while live values still set
  it under a removed key.
* Bad, because the branch rename straddles in-flight work: items whose PR sits
  on an `agent/vik-*` branch must be drained before cutover or renamed by hand.
* Bad, because until ADR-0016 lands, the one global `AGENT_BUILDER_TOKEN` now
  reaches *every* repository a target can name — blast radius grows before the
  credential fix narrows it. This ordering must be stated in the rollout, not
  discovered.

### Confirmation

* `docs/domain/model.yaml` carries Work Target on Work Item and still no repo
  attribute on Team, with the model↔Go alignment CI check (#12) extended to the
  target columns.
* `helm template` with two teams and **no** `repoOwner`/`repoName` anywhere
  renders successfully — the schema no longer requires them.
* An integration test (#88, testcontainers): two items ingested with different
  targets, both claimed by the *same* team, yield two different clone
  coordinates in the claim responses. That is the "one ScaledJob, many repos"
  property, tested directly.
* A grep gate in CI: zero occurrences of `REPO_OWNER`/`REPO_NAME` under `cmd/`
  and `ops/`, and zero occurrences of the literal `agent/vik-` under `pkg/`.

## Pros and Cons of the Options

### Work Target resolved at ingest, pinned on the Work Item row

* Good, because resolution is auditable at the moment of the decision and
  immutable afterwards.
* Good, because it survives R6: the durable answer is a Postgres row, not agent
  or chart state.
* Bad, because it duplicates the rule table's output into rows, so rule edits
  and queued items can disagree.

### Keep the repository on the Team (status quo)

* Good, because it needs no code: it is what runs today.
* Bad, because capacity and codebase are welded together — N repositories cost N
  teams, N ScaledJobs and N bot users at identical model and budget.
* Bad, because it contradicts design.md §3 and the Team entity in `domain/model.yaml`,
  which define a Team without a repository (architecture.md §9.12).
* Bad, because it makes backlog #42's premise ("preventing two teams racing on
  one codebase") structurally unreachable, and leaves #75 unimplementable.

### Resolve lazily at claim time, store nothing

* Good, because a rule edit takes effect immediately for everything queued.
* Bad, because the same item can resolve differently across review rounds — the
  agent gets sent to a repository where its branch and PR do not exist.
* Bad, because the audit log records no target; "which repo did run *n* touch"
  becomes a reconstruction, not a query.
* Bad, because rule evaluation moves into the claim transaction, the one path
  that is `FOR UPDATE SKIP LOCKED` and must stay index-only.

### Carry the repository in the ticket body

* Good, because it needs no schema change and no rule table.
* Bad, because ticket text is untrusted input (#9): a raw repository in the body
  is an arbitrary-write primitive for anyone who can comment on the board.
* Bad, because it puts a connection detail in a document the harness reads,
  which is exactly the direction R8 forbids.

## More Information

* Technical story: architecture.md §9.12 (Team names the repository) · backlog
  #104
* 2026-07-29 — decision approved by the repo owner. Status stays `proposed`
  until it lands end to end; on `development` at this date there are no target
  columns, ingest performs no resolution, and `values.schema.json:34` still
  requires `repoOwner`/`repoName`.
* Refined by [ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md)
  (how a target is chosen) and supported by
  [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md)
  (what a forge id resolves to).
