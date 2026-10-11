---
status: proposed
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-11
---

# ploegd holds a Tenant's knowledge graphs and serves a Run only its Work Item's scopes

## Context and Problem Statement

[0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md) briefs a Run from OKF directories and leaves "the Tenant's Omnigraph graph by stored query" for later. On 2026-10-11 the owner asked for agents to write knowledge per Work Item, client, repository and organization, and to read every level that applies to them. Unfold's system decision (proposed alongside this record) keeps that knowledge in two Omnigraph graphs per Tenant: a reviewed knowledge graph and a notes graph that Runs write.

Omnigraph's policy grants an actor actions per graph and per branch class (protected `main` or any unprotected branch). It cannot filter rows by a property, and it cannot pin an actor to a branch prefix. Inside one graph, a Client, repository or Work Item boundary is therefore enforced by whoever holds the token, not by Omnigraph. Which component holds a Tenant's graph credentials, and how does a Run receive exactly the knowledge of its own scopes?

## Decision Drivers

* No harness gains a tool or a credential ([0011](0011-the-pull-request-is-the-blackboard.md), [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)).
* A Run never reads another Tenant's graphs, another Client's knowledge or another root Work Item's notes.
* A scope comes from Ploeg's records of the Work Item, never from model output or a request field.
* Reviewed knowledge cannot be written by the actor that serves Runs; Omnigraph refuses it, not only Ploeg's code.
* Knowledge is evidence: a missing graph never stops a Run.
* One code path serves Runs and operator consumers ([0087](0087-operator-consumers-read-knowledge-and-write-notes-for-a-named-person-within-scopes-ploegd-checks.md)).

## Considered Options

* ploegd holds the Tenant's actors; the worker fetches the Run's scoped knowledge through the Run API
* The worker holds a read actor and queries the graph itself
* Runs query the graph through an MCP tool behind the gateway

## Decision Outcome

Chosen option: "ploegd holds the Tenant's actors; the worker fetches the Run's scoped knowledge through the Run API", because the Run's Lease already names one Work Item, so the Run API can answer only for that Work Item's scopes, and no process in the Run's pod ever holds a graph token.

* **Graphs.** Each Tenant has a knowledge graph (accepted concepts, each with a Knowledge Scope of `tenant`, `client` or `repository`) and a notes graph (Work Item Notes). ploegd's configuration maps a Tenant to both graph ids and their tokens.
* **Actors.** A Tenant's run actor may read and invoke stored queries on both graphs and change only the notes graph's `main`. It has no write, branch or merge right on the knowledge graph. A Tenant's review actor may create, change, merge and delete branches on the knowledge graph; ploegd uses it only to apply a Promotion a person accepted ([0086](0086-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md)). Neither actor has any right on a graph outside its Tenant.
* **Scopes from records.** ploegd derives a Work Item's Tenant from its binding, its Client from the tracker source or repository that the Tenant registers to a Client, its repository from its Work Target, and its root from the created-work chain ([0031](0031-runs-create-work-items-held-for-approval-within-limits.md)).
* **What a Run reads.** Concepts scoped `tenant`, concepts scoped `client` for the Work Item's Client (none when it has no Client), concepts scoped `repository` for its repository, and the Work Item Notes of its root. Nothing else.
* **Delivery.** `GET /api/v1/runs/{token}/knowledge` returns those concepts as OKF documents with each graph's commit id. The worker treats them as one more source of the pack and selects within the budget exactly as 0065 says. `TaskSpec.knowledge` records the graph commit ids.
* **Unavailable.** When a graph cannot be read at claim, the Run proceeds without it and `TaskSpec.knowledge` records the source as unavailable. A Run is never stuck for knowledge.

### Consequences

* Good, because the run actor is read-only on reviewed knowledge, so no defect in ploegd's run path can publish an unreviewed concept to every Run.
* Good, because the Lease bounds the read: a Run token cannot ask for another Work Item's scopes.
* Good, because the worker gains no setting and no credential; a deployment without graphs behaves as today.
* Bad, because ploegd now depends on Omnigraph at claim time and holds two tokens per Tenant.
* Bad, because the Client and repository boundaries inside one graph rest on ploegd's filter and its tests, not on Omnigraph policy.
* Neutral, because Tenant isolation still rests on Omnigraph policy: each Tenant's actors are refused every other graph.

### Confirmation

Proposed checks, in the existing `go test ./...` CI step once built:

* `TestKnowledgeRead_ServesOnlyTheRunsScopes`: a fake Omnigraph holds two Tenants, two Clients, two repositories and two roots; a Run of Client A's Work Item gets Tenant, Client A, its repository and its root's notes, and nothing of Client B, the other repository, the other root or the other Tenant.
* `TestKnowledgeRead_ScopeComesFromTheWorkItemNotTheRequest`: a Run token with a query naming another repository or root gets its own scopes only.
* `TestWorkerAndHarnessNeverHoldAGraphToken`: the worker's configuration, the claim response, the knowledge response and the TaskSpec carry no graph credential.
* `TestKnowledgeRead_UnavailableGraphDoesNotStopTheRun`.
* A live check on each deployment: the run actor gets 403 on every graph outside its Tenant and on any write to the knowledge graph.

## Pros and Cons of the Options

### The worker holds a read actor and queries the graph itself

* Good, because ploegd stays out of the read path.
* Bad, because a read actor in the Run's pod can read the whole Tenant graph, every Client included; the scope filter would live next to the agent it guards against.

### Runs query the graph through an MCP tool behind the gateway

* Good, because an agent can ask for what it needs mid-Run.
* Bad, because every harness needs the tool ([0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md) bounds read-only tools by a LiteLLM team, which knows no Client or root), and what a Run looked up is harder to record than what it was given.

## Re-evaluation triggers

* Omnigraph policy gains row filters or branch-prefix rules, so Client or root scope could move into policy.
* The knowledge read passes 1 s at p95 at claim.
* A Tenant needs a scope the four levels do not express (for example a Team).
* A harness gains a native retrieval channel Ploeg could feed instead of files.

## More Information

* Unfold system ADR-0034 (proposed): the product decision on four scopes and two graphs per Tenant.
* Graphs, actors, policies and stored queries are deployment configuration, not Ploeg code; the homelab's are filed on its roadmap.
* Tenant and Client in Ploeg's store arrive with the tenancy work of Unfold system ADR-0017. Until then every Work Item belongs to the default Tenant and has no Client, so Runs read Tenant, repository and root scopes only.
* 2026-10-11 — Proposed after the owner chose two graphs per Tenant, direct Work Item Notes, sessions through Unfold and Omnigraph for hosted Tenants.
