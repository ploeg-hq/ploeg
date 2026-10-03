---
status: accepted
date: 2026-09-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-12-31
---

# A repo label selects among registered targets, and the board default is the fallback

## Context and Problem Statement

Ploeg routes a tracker item by its board. The Vikunja adapter emits the project id as
the item's Scope, and one rule per project names one repository. Nothing on the ticket
itself reaches routing. On 2026-09-28 one bug (VIK-1296) had to reach
`webgrip/omnigraph-explorer`, and the only way to get it there was a new Vikunja
project, a new route, a Flux reconcile and a webhook and team share. Three routed boards
(Dark Factory, CI/CD, De Vloer) touch several repositories and send every ticket to the
one they touch most, and Forgejo Migration is not routed at all. A scope that matches no
rule is not refused either: the worker falls back to its team's env repository, which is
how PR webgrip/ploeg#30 edited the wrong README.

ADR-0015 already reserved the answer's shape, a `hint` in `(provider, scope, actor,
hint) → (team, target)` that may only select among pre-registered routes, and held it for
rc.14. This record decides what the hint is, what it may select, and what happens when
it is missing, unknown or ambiguous.

## Decision Drivers

* Tracker text is untrusted input (backlog #9). No ticket content may construct an
  owner, a repository or a branch (ADR-0015).
* One Work Item has one Target, pinned at ingest (ADR-0014). The Lease, the pull
  request and the Shift budget each assume one repository (ADR-0010, 0011, 0012).
* Adding a repository must not require a new board, webhook or share.
* The pick-up queue and the `do-next` cap live per board, so the number of boards
  should follow products and programmes, not repositories.
* A routing failure must be loud, in the ticket where the person acted (backlog #120).

## Considered Options

* A. Status quo: one board, one repository
* **B. A `repo/<name>` label selects among registered targets; the board default is the
  fallback**
* C. Every org repository auto-registered from the Forgejo listing
* D. An LLM planner infers the repository from the ticket text
* E. One Work Item spans several repositories
* F. One sub-project per repository under a parent board
* G. The repository declares itself: the registry derived from the homelab repo-config
  model, component labels resolved through Backstage, planner-proposed splits

## Decision Outcome

Chosen option: "**B**, evolving into G1 and G3", because B removes the
one-board-per-repository cost while keeping the target set closed and written by the
operator, and it is the only variant that can ship before homelab work lands. The person
who can set a label is the person who can already assign the bot, so the hint adds a
choice within the operator's allowlist and no new principal. G's registry derivation
(G1) and planner-proposed splits (G3) change who writes the registry and who drafts the
split, not how Unfold reads a hint, so they build on B rather than replace it. G's
component labels (G2) are rejected for routing for now.

The proposed shape:

1. **Strict routing first.** Backlog #108 ships before hints: an item whose route
   cannot be resolved is refused with an audit row and a ticket comment (#120), and the
   worker's env-repository fallback is removed from the tracker path. Without this an
   unknown hint falls through to the team's repository, the failure this record exists
   to remove.
2. **A target registry.** The routing config gains `targets:`, a map from a hint name
   to `(forge, owner, repo, base branch)`. The hint name is the registry key, not the
   repository name, so `repo/erfbeeld` and a legacy `repo/nuala-nalatenschap` can both
   point at one entry, and a renamed repository changes one line.
3. **Boards reference targets.** A tracker project keeps its route and gains `default:`
   (a registry key, optional) and `allow:` (registry keys the board may select). A
   board with no default is hint-required.
4. **The hint is a label title, compared by equality.** A Vikunja label whose title is
   `repo/<key>` for a registered key is a hint. The provider hands label titles to the
   core as opaque strings, read from `FetchItem` (the authoritative item), not from the
   webhook body. The core compares the whole title with registry keys and never parses
   it, which is ADR-0015's equality-only test. Label ids are not used: Vikunja labels
   are global, titles are not unique (two `repo/frontend-toolkit` labels exist today),
   and the title is what a person sees.
5. **Resolution order.** Exactly one hint in the board's `allow` wins over the default.
   No hint uses the default. These are refused: two different hints, a `repo/*` label
   whose key is not registered, a registered key the board does not allow, and no hint
   on a hint-required board. None of them falls back.
6. **Pinned once.** The resolved Target and the matched rule and hint are written to the
   Work Item at ingest (ADR-0014). Changing a label on a queued item changes nothing
   until an operator retargets it (#96).
7. **Several repositories means several items.** A cross-repository change is an
   `Epic:` parent with one child per repository, each child carrying one hint. Ploeg
   does not model a multi-target Work Item.
8. **The registry is derived, later (G1).** Once the homelab repo-config model exists
   (VIK-1310) and the agent bot's write grant is per repository (VIK-1327),
   homelab-cluster fills `targets:` from the repo-config entries that carry the
   `agent-target` flag. The flag is separate from the `agent-driven` protection profile,
   so becoming a target and getting agent-review branch protection are two decisions.
   The entry also carries the agent base branch, which Unfold reads. It starts as a Lint
   check that the two sets are equal and becomes a renderer when the hand-kept list is
   the friction. Unfold reads a generic registry and never a homelab file format.
9. **A readiness gate.** A registry entry whose repository is archived, is a mirror, or
   has no `AGENTS.md` on its base branch is loaded as not ready, and an item that
   resolves to it is refused with that reason. The gate runs at config load and again
   at claim, and each refusal is recorded with its reason.
10. **Planner-proposed splits (G3).** After ADR-0031 is accepted, a planner may propose
    the children of a cross-repository request. Each child is held for approval and
    passes the same registry and allowlist.

### Decisions recorded 2026-09-28

The owner answered four of the five questions this record first asked:

* **`allow:` default.** A board that omits `allow:` may select only its own `default`.
  Reaching any other target takes an explicit `allow:` entry.
* **Label convention.** Hints are `repo/<registry key>`. The label cleanup is board work,
  tracked as tickets and not done by Unfold: retire `repo/ploeg` (the repository is
  archived) and retag its tickets `repo/glide`, merge the duplicate
  `repo/frontend-toolkit`, and create `repo/glide` and `repo/omnigraph-explorer`.
* **Cross-repository work** is an epic with one child per repository.
* **Boards.** Forgejo Migration becomes hint-required (no default). The Omnigraph
  Explorer project folds into the board that owns it. Boards follow work streams, not
  repositories (G4).

### Decisions recorded 2026-09-28, second round

The owner answered the questions left after G was evaluated, and this record moved to
`accepted`:

* **Direction.** B now, with strict routing (VIK-1354) first, then G1 and G3. No G2.
* **G1 selector.** A separate `agent-target` flag on the repo-config entry selects
  routing targets. It is decoupled from the `agent-driven` protection profile.
* **Base branch.** The agent base branch lives on the repo-config entry, and Unfold reads
  it from there.
* **Readiness.** A target that fails the readiness check is refused both at config load
  and again at claim, each time with a recorded reason.

The other options, in brief (evidence in the research note). G2, component labels
resolved through Backstage, fails on today's facts: the catalog discovers from GitHub,
covers none of the agent targets, and stores the component-to-repository mapping in each
repository's own `catalog-info.yaml`, which the agent bot can write. A merged agent pull
request could then steer where the next agent pushes. B's registry key already absorbs a
rename. A keeps forcing a board per
repository and misroutes multi-repo boards. C registers at least 18 repositories that
reject pushes, lets whoever can create a repository widen the set, and registers
repositories nobody prepared. D hands the push target to text an attacker can write, and
is acceptable only as a suggestion a person confirms. E contradicts ADR-0010, 0011,
0012 and 0014 for a problem decomposition already solves. F is A with folders and
splinters the per-board pick-up queue.

### Consequences

* Good, because a new repository costs one registry entry, and Omnigraph Explorer-style
  boards stop being necessary.
* Good, because multi-repo boards route every ticket correctly when it carries a label,
  and Forgejo Migration can dispatch as a hint-required board.
* Good, because every refusal is visible in the ticket, and "why did this go there" is
  answered by the pinned rule and hint on the row.
* Bad, because the PO gains a labelling duty, and a mislabelled ticket goes to another
  allowed repository. The allowlist bounds that, and the pull request is still reviewed
  before merge.
* Bad, because a label's owner can rename it, which changes the hint on every ticket
  carrying it. Pinning at ingest limits this to items not yet ingested, and the
  allowlist limits where they can go.
* Bad, because it changes the provider SPI (hints on the normalized event) and owes
  backlog #34 a compatibility entry.
* Good, because G1 makes "the bot may write here" and "work may route here" one
  reviewed act in homelab git, so they cannot drift apart.
* Bad, because G1 depends on homelab work Unfold does not control (VIK-1310, VIK-1327),
  and until then the registry is a hand-kept list beside a bot that can write every
  org repository.
* Bad, because strict routing will refuse items that dispatch today. That is intended,
  and the first refusal will look like an outage unless the comment says why.

### Confirmation

* Table tests in `pkg/target` cover: default only; one allowed hint over the default;
  two different hints; an unregistered `repo/*` key; a registered key the board does
  not allow; no hint on a hint-required board. Every refusal case asserts no Target and
  a refusal reason.
* A test in `pkg/httpapi` asserts that a refused item gets an audit row and no queued
  Work Item, and that the worker's env repository is never used for a tracker item.
* A test asserts the Vikunja adapter passes label titles through unmodified and that
  `pkg/target` compares them only for equality.
* Table tests for the readiness gate: an archived repository, a mirror and a missing
  `AGENTS.md` each load as not ready and refuse with a reason; a ready entry resolves.
* A claim-time test: an item pinned to a target that became unready after ingest is
  refused at claim with a recorded reason, and no Run starts.
* Once G1 lands, homelab-cluster e2e Lint fails when the `agent-target` repositories and
  the registered targets differ, proven by a mutation test in each direction.
* `go test ./...` in `.forgejo/workflows/on_pull_request.yml` runs all three.

## Pros and Cons of the Options

### A. Status quo

* Good, because tracker text has no path into routing, and there is nothing to build.
* Bad, because every repository costs a board, and multi-repo boards misroute by design.

### C. Org-wide auto-registry

* Good, because a new repository needs no config edit.
* Bad, because the set includes mirrors, archived repositories and templates, and grows
  whenever anyone creates a repository, without review.
* Bad, because it cannot tell whether a repository is prepared for agent work.
* Neutral: registering only the repositories the agent bot can write to would make the
  Forgejo grant the registration act. Worth revisiting (see triggers).

### D. LLM-inferred repository

* Good, because nobody has to label anything.
* Bad, because the push target becomes a nondeterministic function of untrusted text,
  even when the choice is restricted to the closed set.
* Bad, because a model call and its spend precede admission.

### E. Multi-repository Work Item

* Good, because one ticket matches how migration work is written.
* Bad, because one Target per item underpins the Lease, the pull request as blackboard
  and the Shift budget, and a partial success has no Outcome.

### F. Sub-project per repository

* Good, because the scope stays the whole answer, as in A.
* Bad, because the pick-up queue and the `do-next` cap splinter across children, and
  each child is one more webhook and share to keep wired.

### G. The repository declares itself

* Good, because G1 ties the route to one flagged repo-config entry in reviewed git,
  and adds a readiness gate C lacked.
* Good, because G3 is D restricted to a proposal a person confirms, on ADR-0031's
  held-for-approval state.
* Bad, because G2 routes through a catalog that reads GitHub, covers no agent target
  today and is writable by the agent bot, and it adds an external dependency to the
  core for a rename problem B's registry key already solves.
* Neutral: G4 and G5 are conventions B already needs.

## Re-evaluation triggers

* VIK-1310 and VIK-1327 are done: start G1's Lint check.
* ADR-0031 is accepted: start G3.
* Backstage discovers from Forgejo and its catalog covers every registered target:
  revisit G2 as a naming layer that resolves to a registry key.
* A misroute reaches a merged pull request.
* Vikunja makes label titles unique or project-scoped, or ClickUp becomes a production
  tracker: re-check that label titles still work as hints.
* A planner Run (ADR-0031) is trusted to create child Work Items without approval:
  revisit D and E together.

## More Information

* Evidence and the full option matrix:
  [research/2026-09-28-board-routing-options.md](../research/2026-09-28-board-routing-options.md)
* Refines [ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md) (the
  hint) and relies on [ADR-0014](0014-work-target-is-a-work-item-attribute.md) (pinning).
* Backlog #9, #34, #96, #105, #108, #120 and #122; ticket VIK-1340.
* No questions are open. The owner's answers are recorded under Decision Outcome.
* 2026-09-30: points 2 to 7 and 9 are implemented ([pkg/target](../../pkg/target/resolver.go),
  [readiness gate](../../pkg/target/readiness.go)), with the Confirmation tests in `pkg/target`,
  `pkg/httpapi` and `pkg/provider/vikunja`. Hints apply only once `targets:` is configured, so a
  deployment without it routes exactly as before. Every refusal this record lists is refused at
  ingest without fallback. Point 1 is in part: an item that no board rule covers and that carries
  no hint still falls back to the worker's env repository until #108. Recognising a hint needs
  one prefix test (`repo/`), because an unregistered `repo/*` label must be refused; selection
  is still whole-title equality. A board with its own `repo:` and no `allow:` may select a
  registered key whose target is that repository. Labels come only from `FetchItem`; a board
  that selects by label refuses when that read fails. Readiness is checked by the Forgejo
  provider only; a target on any other forge is not ready. At ingest a forge that cannot be
  reached lets the item queue, and the claim-time check decides. How-to:
  [route a board that serves several repositories](../how-to/route-a-multi-repo-board.md).
* 2026-10-03: the GitLab provider also checks readiness
  ([repository.go](../../pkg/provider/gitlab/repository.go), VIK-1753). It reads the project
  by its URL-encoded full path, counts a pull mirror as a mirror, and checks `AGENTS.md` with a
  `HEAD` on the repository files API. A target on any forge other than Forgejo or GitLab is
  still not ready.
