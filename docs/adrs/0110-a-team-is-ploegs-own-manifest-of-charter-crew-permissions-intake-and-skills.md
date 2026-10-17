---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Team is Ploeg's own manifest of charter, crew, permissions, intake and skills, with borrowed shapes and no borrowed schema

## Context and Problem Statement

The owner asked on 2026-10-17 that team building be a recognised concept in Ploeg: a Team with its own goal, permissions and capabilities, beside its models, harnesses and skills. Today a Team is a declarative manifest of Roles that is the unit of claiming and never names a repository ([the domain model](../domain/model.yaml), R11), but its declaration is split over the chart's `executor.teams[]` (model, harness, sandbox, budget, plan, concurrency, MCP groups) and the config file's `teams.<name>` (roster, plan, follow-up switches, created-work limits); its permissions are implied by the writes flag, the per-Run credential and the egress allowlist; nothing states its goal; and what work it takes is written on boards, labels, targets and admissions rather than on the Team. Backlog #39 planned a Team manifest schema in 2026-07 and the roster verdict of 2026-07-28 chose a bespoke git-owned manifest over Backstage's descriptor.

Before Ploeg writes that manifest, the owner asked whether a standard already defines it. The [2026-10-17 sweep](../research/2026-10-17-team-manifest-standards.md) crawled twelve agent-description specifications, nine multi-agent frameworks, four Kubernetes CRD sets, the permission formats of six coding agents and the budget APIs of three gateways. Which parts of a Team should Ploeg adopt from a standard, which shapes should it borrow, and which parts does it write itself?

## Decision Drivers

* Ploeg knows work sources and change destinations, never vendors ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)); a Team's subject matter belongs to its charter, its instructions and its skills, not to Ploeg.
* A Team never names a repository, forge or credential (R11); intake refers to target keys and boards.
* Permissions and spend are enforced where the agent cannot bypass them: the worker that holds the credentials ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)) and the gateway that meters ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)). The manifest decides; it does not enforce.
* A standard is adopted only where it has cross-org governance, a shipped consumer and a seam Ploeg has; a shape is borrowed where the vocabulary converged without a standard.
* A Team that only reads must be a valid Team ([ADR-0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md)).

## Considered Options

* **Ploeg's own manifest in one place, five parts, borrowing the permission record, the budget shape and the outward description from where they converged**
* Adopt a published team schema (agents.yaml, OpenEnvelope Team Definition, Ark `TeamSpec`) as the manifest
* Adopt an agent-description standard (A2A Agent Card, OASF record) as the manifest and extend it
* Leave the Team as it is and add fields where each need arrives

## Decision Outcome

Chosen option: "**Ploeg's own manifest in one place, five parts, borrowing shapes and no schema**", because no published specification declares a team with a goal, permissions and a budget; the two schemas that declare a team have no named publisher or no consumer; and the agent-description standards describe what an agent offers outward, not what it may do or spend.

A Team is declared once, in the config file, with five parts. The chart renders workloads from it; the database mirrors it; the operator API serves it.

1. **Charter.** `charter` is the Team's goal in words: the default brief of every Run it executes, the text a consumer shows, and the `description` of the Team's outward description. A Role may carry a `description`. Nothing standardises this; Paperclip's `TEAM.md` is the model.
2. **Crew.** Today's plan unchanged: Rounds of Roles with `writes`, `cap`, `planner`, `shadow`, each Role bound to its model, harness, image and sandbox. Nothing surveyed is richer than it.
3. **Permissions.** `may` is an ordered list of `{action, resource, effect}` with `effect ∈ allow | ask | deny`, the shape OpenCode 2 uses and the tri-state Claude Code, Cursor, the Agent Skills `allowed-tools` field and Anthropic's Managed Agents share. Ploeg's own actions stand beside harness actions: `write` (a branch and a push credential), `comment`, `createWork` (`proposed` or `queued`), `egress` ([ADR-0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md)), `tools` ([ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md)). `ask` resolves through `needs_human`. The worker renders the harness actions into each harness's native rules and keeps the credentials; the manifest never carries a grammar of its own.
4. **Spend.** `spend` is `{amount, currency, period}` at the three scopes Ploeg has: the Shift pool, the Role cap per Run, and a periodic allowance (today the Ask allowance of [ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)). Amounts are decimal strings as the chart writes them; `period` is `daily | weekly | monthly`, the vocabulary of Anthropic's spend limits. The gateway enforces, as before.
5. **Intake.** `intake` says what work the Team takes: the boards whose assignment dispatches here (today's roster), the pull requests of listed authors on named target keys with an instruction (ADR-0109's route, moved from the target to the Team), the forge follow-ups it acts on, and whether an operator admission may name it. A target key, never a repository, keeps R11.
6. **Skills.** The Team's catalogue of [ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md), in the Agent Skills unit.

Outward, when [ADR-0007](0007-a2a-adopt-nothing-watchlist-a-facade.md)'s facade comes, each Team's charter and intake are one `AgentSkill` on Ploeg's Agent Card, and an OASF record may carry the same, because that is what those specifications describe: what an agent offers. Nothing of a Team's permissions or spend is published through them.

What is decided now is the shape; the parts land in this order: charter and intake with ADR-0109's route moving to the Team; then `may`, read by the worker and the claim path in place of the scattered switches; then `spend` unifying pool, cap and allowance; then the operator API serving the whole manifest for a consumer's "build a team" page. The chart's `executor.teams[]` and the config file's `teams` merge into one declaration in the second step.

### Consequences

* Good, because a Team becomes one readable declaration a person builds and a consumer shows, and a Team made for one purpose (reading pull requests, writing documentation, a client's read-only crew) is configuration.
* Good, because every permission and budget vocabulary Ploeg writes is one a person already knows from their coding agent or their gateway, and the worker renders it into whatever harness the Role runs.
* Good, because nothing outside Ploeg is depended on for the manifest, and the outward description stays the already-watchlisted A2A card.
* Bad, because `may` and `spend` restate switches that exist today under other names (`forgeFollowUps`, `createdWork.autoDispatch`, `budget`, `cap`, the Ask allowance); both forms are read during the transition and the old ones are retired in a later release.
* Bad, because the manifest is Ploeg's own, so a deployment cannot import a Team from another product; mining Paperclip's field list is as close as that gets.

### Confirmation

* `go test ./internal/ledger/` validates this record and its index row; `mise run docs-check` keeps the domain pages and configuration reference regenerated as each part lands.
* Each part lands with its own tests: the charter in the brief (`pkg/work`), `may` in the worker's rendering and the claim path, `spend` in the store's holds and the allowance, intake in the config loader and the watches, and the operator API's Teams read against `operator-api.v1.schema.json`.
* The review calendar below re-checks the standards named in the research record.

## Pros and Cons of the Options

### Adopt a published team schema

* Good, because a Team written once could run elsewhere.
* Bad, because agents.yaml has no named publisher and no consumer, OpenEnvelope has six stars and no consumer, and Ark's `TeamSpec` is members and a chat strategy on a Kubernetes API Ploeg does not run. None has a budget or Ploeg's seams (Shift, Lease, Work Target).

### Adopt an agent-description standard and extend it

* Good, because A2A 1.0.1 has cross-org governance and a Go SDK, and OASF 1.1.0 is Linux Foundation governed with a registry.
* Bad, because both describe what an agent offers to callers; neither has a word for what it may do or spend, and extending them privately forfeits the reason to adopt them. They stay the outward description.

### Add fields where each need arrives

* Good, because it is the least work today.
* Bad, because it is how a Team came to be split over two files with implied permissions, which is the problem the owner named.

## Re-evaluation triggers

* A foundation-governed specification (A2A, OASF, the Agentic AI Foundation, NIST's agent initiative) adds a team object, a permission grant or a monetary budget to an agent description.
* agents.yaml or OpenEnvelope names a publisher and ships a consumer that reads the team schema.
* kagent or Ark re-adds or extends a Team CRD with a budget or permissions, or kagent is deployed in homelab-cluster.
* Agent Skills' `allowed-tools` leaves experimental, or the spec gains a per-skill permission or budget field.
* The OpenTelemetry GenAI semantic conventions add a cost or budget attribute.
* Paperclip publishes its Team or Agent schema as a standalone, versioned specification.

## More Information

* 2026-10-17 — The owner asked for team building as a recognised concept and for a check of existing standards before Ploeg designs its own. Full evidence trail: [research/2026-10-17-team-manifest-standards.md](../research/2026-10-17-team-manifest-standards.md). Proposed; the owner decides the vocabulary (`crew` for the Roles part is listed under `avoid` in the domain model today) and whether the chart and config file merge.
* Backlog #39 (Team manifest schema), #40 (`ploegctl apply`), #45 (autonomy tier), #102 ("skills = teams" on the A2A facade); [research/2026-07-28-agent-roster-ssot.md](../research/2026-07-28-agent-roster-ssot.md).
* Related: [ADR-0007](0007-a2a-adopt-nothing-watchlist-a-facade.md), [ADR-0009](0009-paperclip-mine-for-design-never-integrate.md), [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md), [ADR-0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md), [ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [ADR-0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md).
