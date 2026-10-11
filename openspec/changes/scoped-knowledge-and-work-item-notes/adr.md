# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-11
- **Reviewer**: Claude (session with Ryan Grippeling)
- **Change**: `scoped-knowledge-and-work-item-notes`

## In-Force ADR Context Reviewed

Reviewed the Records index through 0083. Those that constrained this change:

- `0011-the-pull-request-is-the-blackboard.md` — Ploeg injects; the agent
  never fetches. Graph knowledge is injected as pack files.
- `0031-runs-create-work-items-held-for-approval-within-limits.md` — created
  work defines the root a Work Item Note is shared under.
- `0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md` — the
  harness never holds a credential; this change keeps graph tokens in ploegd,
  out of the worker too.
- `0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md` —
  a learning's scope is a suggestion, never a fact ploegd acts on alone.
- `0069-ploeg-names-none-of-its-consumers.md` — the operator routes name a
  person and role, not a consumer product.
- `0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md`
  — why a Run gets no knowledge tool: a LiteLLM team knows no Client or root.
- `0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md` —
  ploeg-mcp gains no knowledge tool, because its token names no person.

Proposed records this change builds on: 0065 (the pack) and 0066 (learnings).

## Repository-Level ADRs Created

- `docs/adrs/0085-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md`
  — ploegd holds the Tenant's graph actors; a Run reads only its scopes.
- `docs/adrs/0086-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md`
  — notes direct within the root; wider scopes through accepted Promotions.
- `docs/adrs/0087-operator-consumers-read-knowledge-and-write-notes-for-a-named-person-within-scopes-ploegd-checks.md`
  — operator knowledge routes for a named person.

## Supersessions

- none recorded yet. 0086 replaces 0066 once the owner accepts 0086; both are
  proposed, so the Records index does not show the supersession before then.

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger
```

## Notes

All three records stay `proposed`. The owner accepts or rejects them; the
measurement spike (VIK-1859) that gates 0065 and 0066 applies to 0085 and 0086
as well.
