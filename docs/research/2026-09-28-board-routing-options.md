# Routing by board: what it blocks, and seven ways out

Date: 2026-09-28 · Ticket: VIK-1340 · Decision record:
[ADR-0038](../adrs/0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)

## Why this came up

On 2026-09-28 one bug (VIK-1296) had to reach `webgrip/omnigraph-explorer`. No board
routed there, so the owner created a whole Vikunja project, "Omnigraph Explorer"
(id 118), added a route for it to the homelab-cluster HelmRelease, and waited for the
`ploeg-tracker-wiring` CronJob to register the webhook and share the team users. Five
artefacts for one ticket. The owner asked what routing by board blocks, what it
enables, and what the alternatives cost.

## How routing works on `development` today

Every fact below was read from the code or from live state on 2026-09-28.

- The Vikunja adapter emits the task's `project_id` as an opaque Scope
  ([vikunja.go](../../pkg/provider/vikunja/vikunja.go), `payload.Data.Task.ProjectID`).
  Nothing else on the ticket reaches routing. `work.WorkItem` has no label field.
- `config.File.TargetSpec` ([resolve.go](../../pkg/config/resolve.go)) turns the
  `trackers.vikunja.projects` list into the `PLOEG_TARGET_MAP` wire format, and
  `target.MapResolver` ([resolver.go](../../pkg/target/resolver.go)) resolves
  `(scope, team)` to one `work.Target`. A rule is `<scope>[/<team>]`, exact match,
  team-qualified rules first.
- An unmatched scope is **not** refused. `Server.resolveTarget`
  ([server.go](../../pkg/httpapi/server.go)) logs a warning and leaves the Target nil,
  and the worker falls back to its team's `REPO_OWNER`/`REPO_NAME`
  (`ops/helm/ploeg/templates/_helpers.tpl`). In the live HelmRelease bronze and copper
  fall back to `webgrip/erfbeeld` and silver to `webgrip/glide`. Backlog #108
  (`PLOEG_TARGET_STRICT`) would refuse instead, and it has not shipped. This is the
  same class of failure as the wrong-repo PR webgrip/ploeg#30 (2026-07-30): a ticket
  lands in whatever repository the team happens to carry.
- The live routing table (homelab-cluster
  `kubernetes/apps/ploeg/ploeg/app/helmrelease.yaml`) has ten boards. Three of them
  (Dark Factory, CI/CD, De Vloer) touch several repositories and take the one they touch
  most as their only target. Forgejo Migration is deliberately not routed, because any
  single default would be wrong more often than right. Vellum has no repository.
- Wiring a routed board (webhook plus read-only share of every team user) is now
  automated by the `ploeg-tracker-wiring` CronJob every 15 minutes, driven by the same
  config file. What stays manual per board is the route entry, the Flux reconcile and
  the ploegd restart that re-resolves project names.
- Per-Run forge tokens are scoped to the Run's own repository
  ([forgebroker/forgejo.go](../../pkg/forgebroker/forgejo.go)). A wrong Target therefore
  writes to exactly one wrong repository, bounded by what the bot account can reach.

### Board and label state (Vikunja, 2026-09-28)

18 projects: Inbox (1), Homelab Roadmap (3), Erfbeeld (4), Dark Factory (5), Vellum (6),
Forgejo Migration (7), CI/CD (9), Ploeg (10), Ploeg Test (11), De Vloer (14),
Ploeg Bench (48 and 49, same name), twente.dev (50), Internal Delivery Platform (83),
widgets poc (84), compyutah (85), Omnigraph Explorer (118), plus a saved filter.

The board already carries fourteen `repo/*` labels. They were made for humans, and it
shows:

| Observation | Labels | Consequence for routing |
| --- | --- | --- |
| Two labels with the same title | `repo/frontend-toolkit` (174 and 175) | Vikunja labels are global and titles are not unique. A title must never be treated as an identity. |
| Name of an archived repository | `repo/ploeg` (182); Forgejo `webgrip/ploeg` is archived, the code moved to `webgrip/glide` | Taken as a repository name, it points at a repository that rejects pushes. |
| Old and new name of one product | `repo/nuala-nalatenschap` (82) and `repo/erfbeeld` (181) | Two spellings for one target. |
| Routed repositories with no label | no `repo/glide`, no `repo/omnigraph-explorer` | The taxonomy is incomplete for exactly the repositories agents work in. |

### Forgejo org state (2026-09-28)

The unauthenticated org listing shows 44 repositories under `webgrip`. 12 are pull
mirrors (push rejected), 6 are archived, several are templates. Private repositories
such as `erfbeeld` and `omnigraph-explorer` do not appear without a token, so the full
count is higher. Registering "every org repository" would register at least 18 targets
no agent can push to.

## The options

The same questions for each option: what it enables, what it blocks, who gets to choose
where code is pushed, what it costs to run, how it fails, what it does to the
product-owner workflow (per-project pick-up queues, at most ten `do-next`), and what it
costs to build in Glide.

### A. Status quo: one board, one repository

- **Enables.** Zero ambiguity: the board is the answer. Routing is a pure function of a
  number a human cannot type into a ticket. Nothing new to build.
- **Blocks.** Any ticket whose repository differs from its board's. That forces one of
  two workarounds: a new board per repository (Omnigraph Explorer), or a ticket filed on
  a board about a different product. Multi-repo boards dispatch part of their work to
  the wrong repository by design. Forgejo Migration cannot dispatch at all.
- **Security.** Strongest. Tracker text has no path into routing.
- **Operational cost.** One route entry, one project and one reconcile per repository.
  The CronJob covers webhook and share.
- **Failure modes.** A ticket on a multi-repo board lands in the default repository
  (the ploeg#30 class). An unrouted board falls back to the team's env repository
  until #108 ships.
- **PO workflow.** Projects multiply with repositories, not with products. Each new
  project carries its own pick-up queue and its own `do-next` budget, so priority
  splinters.
- **Effort.** None.

### B. A `repo/<name>` label selects among registered targets; the board default is the fallback

The ADR-0015 hint, held for rc.14.

- **Enables.** A board can serve several repositories. Omnigraph Explorer becomes one
  registry entry plus a label on a ticket on an existing board. Forgejo Migration can be
  routed with no default at all, so every ticket must carry a hint. Boards go back to
  meaning products or programmes.
- **Blocks.** Targets that nobody registered. That is the point, and it keeps one config
  edit per new repository.
- **Security.** A hint only selects, by equality, from a closed set the operator wrote.
  It never supplies an owner, a repository or a branch. The person who can add a label
  to a ticket is a person with write access to the project, which is the same authority
  that can assign the bot, so the hint opens no new principal. Two caveats. First,
  Vikunja labels are global and their owner can rename them; a rename changes the hint
  on every ticket carrying that label. The per-board allowlist bounds that. Second, a
  label title is not unique (174/175), so the resolver compares the title string, not
  the label id, and two different registered hints on one ticket refuse instead of
  picking one.
- **Operational cost.** One `targets:` entry per repository, with no new project,
  webhook or share. Existing boards keep their route.
- **Failure modes.** A mislabelled ticket goes to another registered repository, one the
  board allows. An unknown label is refused with a ticket comment, not silently
  defaulted. The label is read once at ingest and pinned (ADR-0014), so relabelling a
  queued ticket does nothing until someone retargets it with `ploegctl` (#96).
- **PO workflow.** Unchanged in shape. Queues stay per board and `do-next` stays at ten
  or fewer. The PO adds one label on a cross-repository ticket, and the Definition of
  Ready gains one check: a ticket on a hint-required board carries exactly one `repo/*`
  label.
- **Effort.** Medium, days. A `Hints []string` on the normalized event, filled from
  labels in `FetchItem` (the authoritative read, not the webhook body). A `targets:`
  registry and per-project `default`/`allow` in `pkg/config`. `Resolve` takes hints. The
  string wire format becomes a struct. Strict refusal (#108), the dead-letter comment
  (#120), and table tests for every refusal path. It touches the SPI, so it owes #34 a
  compatibility entry.

### C. Every org repository auto-registered from the Forgejo listing

- **Enables.** No config edit per repository. Any `repo/*` label routes the day the
  repository exists.
- **Blocks.** Nothing at the routing layer, which is its weakness.
- **Security.** Still a closed set, but the set is "whatever exists in the org", so the
  authority to create a repository becomes the authority to route agent work, and the
  set changes without a review. The forge token bounds a Run to one repository, but
  that is the last line of defence, not the policy.
- **Operational cost.** Low per repository. It needs a Forgejo token that can list
  private repositories and a reconcile loop in ploegd.
- **Failure modes.** At least 18 registered targets reject pushes (mirrors, archived).
  Readiness is unknown: the listing does not say whether a repository has an
  `AGENTS.md` and a verify command ([prepare a repository](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/how-to/prepare-a-repository.md)),
  so an unprepared repository gets agent work. The base branch comes from the forge's
  default branch, which is right for most repositories and must be overridable.
- **PO workflow.** Same as B.
- **Effort.** B plus a registry reconciler. A narrower variant is useful later: register
  only repositories the agent bot has write access to. That turns the Forgejo grant,
  which onboarding already needs (step 4 of the
  [onboarding report](2026-08-28-onboarding-friction.md)), into the registration act.

### D. An LLM planner infers the repository from the ticket text

- **Enables.** Zero labelling. It handles tickets whose authors do not know which
  repository they touch.
- **Blocks.** Auditable routing. The answer to "why did this go there" becomes a model
  output.
- **Security.** ADR-0015 forbids a hint that constructs a target, and backlog #9 records
  why: tracker text is untrusted input. A planner that constructs `owner/repo` from the
  description is a free-text repository-write primitive for anyone who can edit a
  ticket. A planner restricted to the closed set is still steerable: an injected
  sentence picks which allowed repository gets the push, and the choice changes between
  runs. It also adds a model call, and spend, before admission.
- **Operational cost.** Per-ingest model spend, and a model outage becomes a routing
  outage.
- **Failure modes.** Nondeterministic misroutes that nobody can reproduce.
- **PO workflow.** Nothing to do up front, then surprises.
- **Effort.** Medium. It is safe only as a suggestion: the planner comments "this looks
  like `repo/x`", and a person applies the label. ADR-0031's held-for-approval Work
  Items are the same pattern.

### E. One work item, several repositories

- **Enables.** "Port CI in telemetry-service, common-charts, …" as one ticket, which is
  how Forgejo Migration and Dark Factory tickets are written.
- **Blocks.** It collides with four accepted or approved decisions. ADR-0014 pins one
  Target per Work Item. ADR-0010 makes the Lease own one branch. ADR-0011 makes one pull
  request the blackboard. ADR-0012 budgets one Shift. A multi-target item needs N
  branches, N PRs, N leases and a partial-success state no current Outcome can express.
- **Security.** Neutral if every target passes the closed set.
- **Failure modes.** Half-merged changes across repositories with one ticket state.
- **PO workflow.** Fewer tickets. Ticket state no longer matches delivery state.
- **Effort.** Large. It reshapes the domain model.
- **Better shape.** Split the work. An `Epic:` parent with one child per repository,
  each child carrying one `repo/*` label, fits the board contract's epic convention as
  it stands. Later, a planner Run (ADR-0031) can propose the children, each still
  passing the closed set and waiting for approval.

### F. A sub-project per repository under a parent board

- **Enables.** Keeps A's "the scope is the answer" property with less sprawl in the
  sidebar, because Vikunja nests projects.
- **Blocks.** Nothing new at the routing layer. It is A with folders.
- **Security.** Same as A.
- **Operational cost.** A route per child project. The CronJob wires webhook and share
  per project, so each child is one more thing to reconcile and one more place for
  wiring to drift.
- **Failure modes.** A ticket filed on the parent instead of a child: the parent needs
  its own route or a refusal. A ticket moved between children after ingest keeps its
  pinned target.
- **PO workflow.** Worst of the seven. The pick-up queue lives in each project's
  description and the stages view is per project, so every child needs its own queue,
  or the PO reads a parent that has no tickets. The `do-next` cap of ten splinters the
  same way.
- **Effort.** None in Glide. The cost is paid on the board.

### G. The repository declares itself

Proposed on 2026-09-28, after the owner asked for a more strategic option than B. G has
five parts, and each one is judged separately below, because they do not stand or fall
together.

1. **The registry is derived from the repo-config model.** homelab-cluster's
   [repo-config RFC](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/rfc/rfc-forgejo-repo-config-gitops.md)
   (accepted 2026-09-28, epic VIK-1302) declares every repository's profile in git. A
   repository with the `agent-driven` profile gets the agent bot's write grant and is a
   Glide target, provided it passes a readiness gate: an `AGENTS.md` on the base branch,
   not archived, not a mirror. No per-repository Glide config.
2. **Tickets name a component.** A `component/<name>` label resolves through the
   Backstage catalog (`catalog-info.yaml` maps a component to its repository and owner),
   so the ticket speaks domain language and survives repository moves and splits.
   `repo/<key>` stays as an explicit override.
3. **Cross-repository work is split by a planner** into one child per repository, as a
   proposal the owner confirms in the PO flow. This is D made safe.
4. **Boards are work streams**, not repository pointers.
5. **Strict routing (#108) first**, as in every variant.

#### What exists today (read on 2026-09-28)

- **The repo-config model does not exist yet.** VIK-1310 (model, bot and `baseline`
  imported, plan-only) is `ready` and not started. The `agent-driven` profile is defined
  in the RFC and starts on `webgrip/glide` only (RFC §7, decision 2). It is a
  branch-protection profile: one `agent-reviewer` approval, owner-only merge.
- **The agent bot's write grant is not per repository.** `agent-builder` is in the
  `agents` team with `includes_all_repositories: true`
  (homelab-cluster `kubernetes/apps/forgejo/forgejo-actions-secrets/app/agent-builder-provisioner.job.yaml`,
  the team create and PATCH calls). It can push branches to every org repository today.
  Per-repository team grants are one of the model's three gaps (RFC §8.1 row 17), waiting
  on the upstream provider PR svalabs/terraform-provider-forgejo#155 (VIK-1326, VIK-1327).
  So "the profile grants write" is a future state, not a fact.
- **The Backstage catalog does not cover the targets.** The deployed catalog
  (`webgrip/backstage-application`, `src/app-config.yaml`, image 1.0.4) discovers from
  **github.com**, through the `githubOrg` provider and two `github` providers that read
  `/catalog-info.yaml` from `main` and `master`. The estate is Forgejo-leading and GitHub
  is only a push mirror on its way out. Of 73 local clones of `webgrip` repositories, 26
  carry a root catalog file, and 18 of those are named `catalog-info.yml`, which the
  configured `catalogPath` does not match. `glide`, `de-vloer`, `workflows`,
  `common-charts`, `renovate-config` and `ai-skills` have none. `erfbeeld` has one (a
  System with several Components), and `glide`'s default branch is `development`, which
  the providers do not read. The live pod's log (2026-09-28) shows entities such as
  `system:homelab/talos-cluster`, `component:webgrip/traefik`,
  `component:webgrip/backstage-application` and the monitoring-platform components:
  infrastructure, not the repositories agents work in. Backstage itself runs on
  `fringe-workstation` with 4 restarts in 2.5 days.

#### Part by part

**G1, registry derived from the model.**

- **Enables.** One act makes a repository agent-writable and routable, so the two can no
  longer drift apart. Today they can: a registered target the bot cannot write fails at
  push time, and the bot can write repositories nobody registered. The readiness gate
  moves "is this repository prepared" from a how-to page into a check.
- **Who chooses, and trust.** The profile is assigned in `repos.yaml` in homelab-cluster,
  whose `main` only the owner can push (ADR-0050 whitelist, guarded by the model's own
  lock-out precondition). That is the same authority that writes B's `targets:` today,
  so G1 opens no new principal. It is strictly better than C: the set is closed and
  reviewed, not "whatever exists in the org".
- **Wiring cost.** Glide is an Apache-2.0 product, so it must not read a homelab file
  format. The split that keeps it clean: Glide reads a generic `targets:` registry (B's),
  and homelab-cluster fills it from `repos.yaml`. The cheapest first step is a check, not
  a renderer: an e2e Lint rule that the set of `agent-driven` repositories equals the set
  of registered targets. A renderer can replace the hand-kept list later. The model
  needs one field B's registry has and it lacks: the agent base branch (`glide` works on
  `development`).
- **Failure modes.** A repository that leaves the profile while items are queued: the
  pinned Target (ADR-0014) still names it, and the push fails once per-repository grants
  exist. The readiness gate should run at claim as well as at load. The gate can check
  that `AGENTS.md` exists; it cannot check that the file names a working verify command,
  because that is prose ([prepare a repository](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/how-to/prepare-a-repository.md)).
- **Glide effort.** B plus a readiness check through the forge contents API (small). The
  derivation is homelab work, gated on VIK-1310 and, for the write-grant half, on
  VIK-1327.

**G2, component labels resolved through Backstage.**

- **Enables.** Domain language on tickets, and resilience to a repository rename or split.
- **Blocks.** Routing to anything the catalog does not know, which today is every agent
  target. Before G2 routes one ticket, Backstage needs Forgejo discovery (it reads
  GitHub), a `development`-branch rule, renamed `.yml` files, and new catalog files in
  the target repositories.
- **Who chooses, and trust.** This is the weak point. The component-to-repository mapping
  lives in each repository's own `catalog-info.yaml`, which anyone with write access to
  that repository can edit, and that includes `agent-builder`, which has write access to
  every repository. A merged agent pull request, or a compromised catalog entry, can
  claim a component name or change a source location, and so decide where the next
  agent pushes: agent-written text steering agent routing. Two entities claiming one
  name are a Backstage conflict, resolved by processing order rather than by review. G2
  is safe only as a naming layer that resolves to a key and then passes the same closed
  registry and board allowlist as a `repo/*` label, which makes it B with one more hop.
- **Failure modes.** Catalog drift (an entity lags the repository, the `.yml` class
  above), and Backstage down at ingest. Under strict routing both refuse the item rather
  than misroute it, which is loud but blocks work on an unrelated outage.
- **PO workflow.** Nicer to read, but the PO must know component names, and most targets
  are one component per repository today, so the label carries the same information as
  `repo/<key>`.
- **Glide effort.** A catalog client in the core is a new external dependency for an OSS
  product. It belongs behind the provider SPI, if anywhere. Large relative to its value
  now.
- **What B already gives.** B's registry key is not the repository name, so a rename
  already costs one line, and `repo/erfbeeld` and `repo/nuala-nalatenschap` can point at
  one entry. Only a repository *split* needs G2's extra indirection, and no split is
  planned.

**G3, planner-proposed splits.** Sound. It is D restricted to a suggestion, and Glide
already has the state for it: `work.StateProposed` (`pkg/work/types.go`) and ADR-0031's
held-for-approval Work Items. Each proposed child still passes the closed set. It needs
ADR-0031 accepted and a planner Role, so it comes after B.

**G4, boards as work streams.** Agreed, and B already enables it: once a label selects
the repository, a board no longer has to be one. Omnigraph Explorer folding into its
owning board is the first instance.

**G5, strict routing first.** Same as B.

## Summary

| Option | Enables | Blocks | Who chooses the push target | Ops cost per new repo | PO impact | Glide effort |
| --- | --- | --- | --- | --- | --- | --- |
| A board → repo | nothing new | cross-repo tickets, multi-repo boards | operator only | project + route + wiring | projects multiply | none |
| **B label over registry** | cross-repo boards, hint-required boards | unregistered repos | operator's closed set; ticket writer selects within the board's allowlist | one registry entry | one label, one DoR check | medium (days) |
| C org auto-registry | any repo, no config | nothing | whoever can create an org repo | none | as B | B + reconciler |
| D LLM infers repo | no labelling | auditability | the model, steerable by ticket text | none | surprises | medium; unsafe as authority |
| E multi-repo item | one ticket for N repos | ADR-0010/0011/0012/0014 invariants | as B, per target | as B | state ≠ delivery | large |
| F sub-projects | tidy sidebar | as A | operator only | project + route + wiring | queues splinter | none |
| G1 registry from repo-config model | grant and route in one reviewed act; readiness gate | repos outside `agent-driven` | owner, in homelab git (same as B) | one `repos.yaml` line (+ board `allow`) | as B | B + readiness check; homelab derivation waits on VIK-1310/1327 |
| G2 component via Backstage | domain names, survives splits | everything the catalog lacks (today: every target) | whoever can write `catalog-info.yaml`, incl. the agent bot, unless bounded by the registry | catalog file per repo + Forgejo discovery | component names to learn | large; new external dependency |
| G3 planner-proposed split | N children from one request | nothing (proposal only) | owner confirms each child | none | one confirmation | after ADR-0031 |

## Verdict

**B now, then G1 and G3; not G2.** Updated 2026-09-28 after evaluating G.

1. Strict routing (#108) ships first. Without it an unknown hint, or a target a
   readiness gate rejects, falls through to the team's env repository, the failure every
   option here exists to remove.
2. B's shape stays: a closed registry of targets keyed by a stable name, a per-board
   `default` and `allow`, and `repo/<key>` labels compared by equality. B is the only
   variant that can ship before homelab work lands.
3. G1 is the strategic improvement, and it changes who writes the registry, not how
   Glide reads it. Once the repo-config model exists (VIK-1310) and grants are per
   repository (VIK-1327), homelab-cluster derives the registry from the `agent-driven`
   profile: first as a Lint check that the two sets are equal, later as a renderer.
   Glide adds a readiness gate at load and at claim.
4. G3 follows ADR-0031: a planner proposes one child per repository, and the owner
   confirms. E stays answered by decomposition.
5. G2 is rejected for routing today. The catalog reads GitHub, covers none of the
   agent targets, and its mappings are writable by the agent bot. B's registry key
   already absorbs renames. Revisit when the catalog discovers from Forgejo and covers
   every registered target, and then only as a naming layer that resolves to a registry
   key.
6. G4 is adopted as a board convention: boards follow work streams, and a repository
   is chosen by label.

C's narrow variant (register what the bot can write) is what G1 becomes once grants are
per repository, so it is no longer a separate re-evaluation item.

Owner decisions, 2026-09-28: the direction above is confirmed. G1 selects targets with a
separate `agent-target` flag on the repo-config entry, not the `agent-driven` protection
profile. The agent base branch lives on that entry. A target that fails the readiness
check is refused at config load and again at claim, with a recorded reason. See
[ADR-0038](../adrs/0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md).
