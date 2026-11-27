---
status: proposed
date: 2026-11-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-02-26
---

# A Run draws on the scoped Omnigraph knowledge layer before and during its work

## Context and Problem Statement

On 2026-11-27 the owner reopened the direction of [0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md) (decision 5A): the knowledge layer is built on Omnigraph with proper scope partitions; agents draw on it during their work through a tool or MCP server, and get context staged before a Work Item is picked up. This record states that direction so it can be designed against once accepted.

Much of it exists. A Run is briefed before it starts: the worker writes an OKF pack outside the clone ([0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md), `pkg/knowledge`, `pkg/okf`, the pack in `pkg/worker`). ploegd holds the Tenant's graphs and serves a Run only its Work Item's scopes through `GET /api/v1/runs/{token}/knowledge` ([0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md), `handleRunKnowledge` and `BriefingNotes` in [knowledge.go](../../pkg/httpapi/knowledge.go)), with the scope derived from Ploeg's records by `KnowledgeScopeFor` in [work_item_notes.go](../../pkg/store/work_item_notes.go). The Omnigraph clients are [pkg/omnigraph](../../pkg/omnigraph/client.go) and [pkg/knowledgegraph](../../pkg/knowledgegraph/notes.go). A Run writes notes without review and a wider level needs evidence or a person ([0088](0088-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md), [0122](0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md)). The Tenant and Client partitions come from [0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md), which is accepted and not built.

What does not exist is a way for a Run to ask the knowledge layer a question while it works. 0087 rejected that option ("Runs query the graph through an MCP tool behind the gateway") because every harness would need the tool and what a Run looked up is harder to record than what it was given. How does a Run query the knowledge layer during its work without a graph token in its pod and without reaching outside its own scopes?

## Decision Drivers

* No graph token, and no credential of any kind, enters a Run's pod ([0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [0197](0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md)).
* A Run reads only its own scopes, Tenant, Client, repository and Work Item, derived from Ploeg's records and never from the request or the model (0087, 0122, 0178).
* Knowledge is evidence: an unavailable graph never stops a Run (0087).
* What a Run read is recorded, as the pack's provenance is today (0065).
* Learnings keep the review line of 0088 and 0122.

## Considered Options

* Stage a pack before pick-up and let the Run query the graph during work through a tool that ploegd serves or proxies, bound to the Run's capability
* Stage a pack only (0065 and 0087 as they are)
* Give the Run's pod a read actor on the graph

## Decision Outcome

Proposed option: "**stage a pack before pick-up, and query during work through a tool ploegd serves or proxies**", because it keeps every guarantee of 0087 and adds only the part that is new.

1. **Before pick-up: context is staged.** When a Run is claimed, the worker builds its pack from the repository's bundle, the configured bundles and the Run's scoped graph read, as 0065 and 0087 say today. Nothing changes here except that the pack's index tells the agent the query tool exists.
2. **During work: a scoped query tool.** A Run may query the knowledge layer within its own scopes through a tool, served as an MCP server that ploegd serves, or proxies through the credential gateway's MCP route family ([0199](0199-mcp-servers-and-skill-sources-are-registered-per-tenant-attached-to-a-teams-roles-by-id-and-reached-only-through-the-credential-gateway.md), `pkg/mcpgateway`). The Run authenticates with its own capability; ploegd resolves the scope with `KnowledgeScopeFor` from the Run, as for the briefing, and queries Omnigraph with the Tenant's run actor. No graph token is in the pod.
3. **Learnings.** What a Run learns goes through the notes channel of 0088 and 0122, unchanged: Work Item notes without review, a wider level only with evidence or a person.

The new part is item 2. Items 1 and 3 restate decisions already accepted.

### Consequences

* Good, because an agent can look up what it needs when it needs it, beyond what fit the pack's budget.
* Good, because scope enforcement stays in one place, ploegd's, and the pod still holds no graph credential.
* Bad, because every query is a call from the Run to ploegd and on to Omnigraph while the Run works; latency and load grow with use.
* Bad, because a harness must reach the tool, which ties this to the MCP route family of 0199 and its qualification per harness.
* Bad, because what a Run looked up must be recorded per call to keep 0065's provenance, which a pack records once.

### Confirmation

Proposed, nothing of item 2 is built. Once built, in the existing `go test ./...` CI step: a test that a Run's query returns only its own scopes whatever the request names (as `TestKnowledgeRead_ScopeComesFromTheWorkItemNotTheRequest` does for the briefing), a test that the worker, the harness and the tool's responses carry no graph credential (as `TestWorkerAndHarnessNeverHoldAGraphToken`), a test that an unavailable graph answers the tool with an error and does not stop the Run, and a test that each query is recorded on the Run. `go test ./internal/ledger/` validates this record.

## Pros and Cons of the Options

### Stage a pack only

* Good, because it is built and recorded once per Run.
* Bad, because a Run cannot ask for what the selection missed, which is the owner's reason for reopening 0066.

### Give the Run's pod a read actor on the graph

* Good, because it is the least machinery.
* Bad, because Omnigraph cannot filter rows, so the actor reads the whole Tenant, and it is a credential in the pod, which 0197 forbids.

## Open questions

Each needs an answer before this record is accepted:

1. **Query surface and limits** (owner: the maintainer). Stored queries only, or a search over concepts and notes; the page size and result budget per call; the number of calls per Run.
2. **Scope enforcement** (owner: the maintainer, with 0178's build). Whether the tool reuses `KnowledgeScopeFor` and `notes.ForRun` unchanged, and how a Client and Tenant scope apply once 0178 is built.
3. **Cost and latency** (owner: the owner). Who pays for a query (the Run's budget or the installation) and the latency budget per call before a query is refused.
4. **Audit** (owner: the maintainer). Whether each query is an audit row, a field of the Run's `TaskSpec.knowledge`, or both, and how a reviewer sees what a Run looked up.

## Re-evaluation triggers

* The 2027-02-26 measurement of 0065 shows packs reduce none of cost, Rounds or stuck rate: reconsider whether in-Run querying is worth building.
* Omnigraph gains row-level policy, so scope could move into the graph's own policy.
* A harness gains a native retrieval channel that Ploeg could feed instead of a tool.

## More Information

* Direction from the owner's decision of 2026-11-27 (5A), recorded on [0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md).
* 2026-11-27: proposed. Nothing of item 2 is built.
