---
status: proposed
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-11
---

# Operator consumers read knowledge and write notes for a named person within scopes ploegd checks

## Context and Problem Statement

People also work with agents outside Ploeg's Runs: an interactive Claude Code or Codex session next to the developer. The owner decided on 2026-10-11 that such sessions read and write the same Tenant knowledge, with the same scopes, through their consumer (for Unfold, its MCP server). Only ploegd holds a Tenant's graph tokens ([0085](0085-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md)), and a graph token on a laptop would read every Client of the Tenant, because Omnigraph cannot filter rows. What does Ploeg serve to an operator consumer, for whom, and what does it check itself?

## Decision Drivers

* Ploeg checks scope itself and does not trust a consumer to have filtered (Unfold system ADR-0017).
* Ploeg names none of its consumers ([0069](0069-ploeg-names-none-of-its-consumers.md)).
* A client user reaches only their own Client's knowledge.
* What a session writes follows the same review line as a Run ([0086](0086-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md)).
* Out-of-scope objects look absent.

## Considered Options

* Operator routes for knowledge, where the consumer names the acting person's Client and role and ploegd checks them against the consumer's Tenant
* A graph actor per person or session
* Read-only knowledge in ploeg-mcp under the operator token

## Decision Outcome

Chosen option: "Operator routes for knowledge, where the consumer names the acting person's Client and role and ploegd checks them against the consumer's Tenant", because it keeps the tokens in ploegd, reuses the same scope filter as Runs and gives client users exactly their Client's view.

* **Routes.** `GET /api/v1/operator/knowledge` (search and read concepts and notes), `POST /api/v1/operator/work-items/{id}/notes` (write a Work Item Note), `GET /api/v1/operator/promotions` and `POST /api/v1/operator/promotions/{id}/accept|reject`, and `POST /api/v1/operator/promotions` to request one.
* **Acting person.** Every request names the person, their role in the Tenant (`viewer`, `member`, `admin`) and, for a client user, their Client. The consumer is bound to one Tenant (Unfold system ADR-0017); ploegd refuses a person outside it, and audits each write with the consumer and the person.
* **Read scopes.** A Tenant member reads every concept and note of the Tenant. A client user reads concepts scoped to their Client and to their Client's repositories, and notes of their Client's Work Items; not Tenant-wide concepts, which may describe the agency's other work.
* **Writes.** A member or admin may write a Work Item Note on a Work Item they can see; it is attributed to them and behaves like a Run's note. A wider scope is a Promotion request. A Tenant admin's request is accepted at once and recorded as their own acceptance. Only a Tenant admin accepts or rejects. A client user may read and may request a Promotion about their own repositories, and nothing else.
* **Absent.** A concept, note, Work Item or Promotion outside the person's scope returns 404.
* **ploeg-mcp** stays read-only on its existing tools and gains no knowledge tool: its operator token names no person.

### Consequences

* Good, because sessions and Runs share one store, one filter and one review line.
* Good, because a session's machine holds only its consumer credential, never a graph token.
* Bad, because ploegd trusts the consumer's statement of the person's Client and role within the consumer's Tenant; a compromised consumer can act as any person of that Tenant, not beyond it.
* Bad, because the operator API grows a write surface with its own idempotency and audit.

### Confirmation

Proposed checks, in the existing `go test ./...` CI step once built:

* `TestOperatorKnowledge_ClientUserSeesOnlyTheirClient`: a client user of Client A gets Client A's concepts and repositories' concepts and their Work Items' notes, a 404 for Client B's concept and no Tenant-scoped concept.
* `TestOperatorKnowledge_PersonOutsideTheConsumersTenantIsRefused`.
* `TestOperatorNotes_WriteIsIdempotentAndAudited`: the same request key twice writes one note, and the audit names consumer and person.
* `TestOperatorPromotions_OnlyATenantAdminAccepts`.
* `operator-api.v1.schema.json` carries the new bodies, checked by `pkg/harness/contract_test.go`'s schema cases.

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
