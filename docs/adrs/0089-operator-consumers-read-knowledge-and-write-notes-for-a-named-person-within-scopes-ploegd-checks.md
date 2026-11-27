---
status: accepted
date: 2026-11-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-11
---

# Operator consumers read knowledge and write notes for a named person within scopes ploegd checks

## Context and Problem Statement

People also work with agents outside Ploeg's Runs: an interactive Claude Code or Codex session next to the developer. The owner decided on 2026-10-11 that such sessions read and write the same Tenant knowledge, with the same scopes, through their consumer (for Unfold, its MCP server). Only ploegd holds a Tenant's graph tokens ([0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md)), and a graph token on a laptop would read every Client of the Tenant, because Omnigraph cannot filter rows. What does Ploeg serve to an operator consumer, for whom, and what does it check itself?

## Decision Drivers

* Ploeg checks scope itself and does not trust a consumer to have filtered (Unfold system ADR-0017).
* Ploeg names none of its consumers ([0069](0069-ploeg-names-none-of-its-consumers.md)).
* A client user reaches only their own Client's knowledge.
* What a session writes follows the same review line as a Run ([0088](0088-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md)).
* Out-of-scope objects look absent.

## Considered Options

* Operator routes for knowledge, where the consumer names the acting person's Client and role and ploegd checks them against the consumer's Tenant
* A graph actor per person or session
* Read-only knowledge in ploeg-mcp under the operator token

## Decision Outcome

Chosen option: "Operator routes for knowledge, where the consumer names the acting person's Client and role and ploegd checks them against the consumer's Tenant", because it keeps the tokens in ploegd, reuses the same scope filter as Runs and gives client users exactly their Client's view.

Amended on 2026-11-27 to the routes that were built and to proven-person attribution; the bullets below state what ploegd does.

* **Routes** ([operator_knowledge.go](../../pkg/httpapi/operator_knowledge.go), [operator.go](../../pkg/httpapi/operator.go)). `POST /api/v1/operator/work-items/{id}/knowledge-notes` keeps a person's trusted note at the `work-item` or `repository` level ([0122](0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md)), idempotent on its `commandId`. `GET /api/v1/operator/knowledge/reviews` lists open (or all) promotion and contradiction reviews, and `POST /api/v1/operator/knowledge/reviews/{id}/decision` decides one. `POST /api/v1/operator/work-items/{id}/notes` is the steering note: it briefs the next Run of the Work Item once and is not knowledge (0122).
* **Acting person.** A write names its person through the attribution of [0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md): a verified person token makes the person `proven`, and under `PLOEG_OPERATOR_ATTRIBUTION=proven` ([0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)) a write without one is refused before the route. The consumer no longer states the person's role or Client; ploegd records the person it proved, or the asserted actor where the deployment still allows it.
* **Scope.** Every route acts only on Work Items whose Team the consumer names ([0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)); the Tenant scope of [0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md) applies to these routes once it is built. Reading reviews needs `readContent`; writing a note or deciding a review needs `execute`.
* **Not built.** Search and read of concepts and notes for a person, a client user's narrower view, and Tenant roles (`viewer`, `member`, `admin`) deciding who may accept: none of these exists. A later record decides them once Ploeg records Clients (0178).
* **Absent.** A Work Item or review outside the consumer's scope returns 404.
* **ploeg-mcp** stays read-only on its existing tools and gains no knowledge tool.

### Consequences

* Good, because sessions and Runs share one store, one filter and one review line.
* Good, because a session's machine holds only its consumer credential, never a graph token.
* Bad, because a deployment that stays on asserted attribution (0171) still trusts the consumer to name the person; under `proven` the identity provider proves the person instead.
* Bad, because the operator API grows a write surface with its own idempotency and audit.

### Confirmation

In the existing `go test ./...` CI step:

* `pkg/httpapi`: `TestOperatorKnowledge_APersonWritesAndDecides` (a person's note through `knowledge-notes` and a review decision), `TestOperatorNoteIsIdempotentPerCommand` and `TestOperatorNoteIsInjectedIntoTheNextClaimedRunExactlyOnce` (the steering note), `TestOnlyAConsumerGrantedContentReadsIt` (reviews need `readContent`), and `TestAProvenNoteRecordsTheProvenPersonInTheAudit` (the proven person in the audit row).
* `go test ./pkg/httpapi/ -run 'Attribution|ProvenMode|PersonTokenRequired'` for the refusal of an unproven write under `proven` (0171).

## Pros and Cons of the Options

### A graph actor per person or session

* Good, because Omnigraph would record each person's writes directly.
* Bad, because Omnigraph cannot limit an actor to one Client's rows, so every client user's token reads the whole Tenant, and tokens sit on laptops.

### Read-only knowledge in ploeg-mcp under the operator token

* Good, because it is the smallest change.
* Bad, because the operator token names no person, so it cannot tell a client user from an admin, and sessions still cannot write.

## Re-evaluation triggers

* Consumers can forward a signed person token Ploeg verifies itself, removing the trust in the consumer's statement.
* A Tenant asks for client users to read Tenant-wide concepts.
* Session writes pass 100 notes per Tenant per week.

## More Information

* Unfold system ADR-0035 (proposed): interactive sessions reach Tenant knowledge only through Unfold's MCP server.
* 2026-10-11 — Proposed after the owner chose sessions through Unfold's MCP server.
* 2026-10-11 — Owner answers: a session note reaches Runs at once; a Tenant member's Promotion request always waits for an admin. Kept proposed until the measurement spike (VIK-1859) reports.
* 2026-11-27: owner decision: amended to the routes that were built ([operator_knowledge.go](../../pkg/httpapi/operator_knowledge.go): `…/work-items/{id}/knowledge-notes`, `…/knowledge/reviews` and `…/reviews/{id}/decision`; [operator.go](../../pkg/httpapi/operator.go): the steering route `…/work-items/{id}/notes`) and to proven-person attribution ([0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)) instead of a consumer stating the person's role and Client, then accepted. The Confirmation names the tests that exist; search, client-user views and Tenant roles are marked not built.
