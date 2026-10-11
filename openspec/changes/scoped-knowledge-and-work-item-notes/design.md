## Context

ADR-0065 and ADR-0066 (proposed) built the pack and the learnings channel
against directories. Unfold system ADR-0034 and ADR-0035 (proposed) and the
owner's answers of 2026-10-11 set the product shape; ADR-0087 to ADR-0089 hold
Ploeg's durable decisions. This document records how they are built.

Facts the design rests on, checked on 2026-10-11 against the homelab's
Omnigraph bundle and runbook:

- A policy rule grants an actor group actions on one graph, with a branch
  scope of `protected`, `unprotected` or `any`. There is no row filter and no
  branch-name pattern. `invoke_query` cannot be combined with a branch scope.
- `cluster apply` refuses a schema change while a branch of that graph is
  open.
- A merge load upserts by `@key`; a mutation returns an exact commit receipt,
  and a lost response leaves the outcome unknown until it is read back.

## Goals / Non-Goals

**Goals:** a Run reads exactly the knowledge of its Knowledge Scopes; a Run's
notes reach later Runs of the same work without review; nothing reaches other
work before a Tenant admin accepts it; sessions get the same view through the
operator API; no graph token leaves ploegd.

**Non-Goals:** as in the proposal.

## Decisions

### D1 — Two graphs, so policy enforces the review line

The run actor reads both graphs and changes only the notes graph's `main`. It
has no right that writes the knowledge graph, so the review line holds even if
ploegd's run path is wrong. Client and root boundaries inside a graph are
ploegd's filter (D3), because Omnigraph cannot express them.

### D2 — Graph shapes

Both graphs are deployment configuration; Ploeg needs these fields and reads
nothing else.

- Knowledge graph: `Concept` keyed by `slug`, with `scope`
  (`tenant|client|repository`), `client` and `repository` (set when the scope
  needs them), the OKF `type`, `title`, `description`, `tags`, `body`,
  `accepted_by`, `accepted_at` and `from_note`. Links between concepts are
  edges.
- Notes graph: `Note` keyed by `<run trace>/<index>` (or
  `<person>/<request key>` for a session's note), with `work_item`, `root`,
  `repository`, `client`, `type`, `title`, `body`, `tags`, `suggested_scope`,
  `written_by` and `written_at`.

Stored queries `scoped_concepts($client, $repository)` and
`root_notes($root)` read them; ploegd passes the parameters from its own
records. The existing `ploeg-okf to-omnigraph`/`from-omnigraph` conversion
moves from the `memory` schema to these types.

### D3 — Scopes come from ploegd's records

The Run token resolves to the Lease, the Lease to the Work Item, and the Work
Item to its Tenant, Client, Work Target repository and root. Query parameters
in the Run route are ignored. For operator routes the consumer's binding gives
the Tenant and the request names the person; ploegd intersects the person's
scope with the object's.

### D4 — The pack treats graph knowledge as two sources

The knowledge response holds reviewed concepts and Work Item Notes as two OKF
bundles. The worker selects from both within the budget (ADR-0065) and writes
notes under `notes/` in the pack with their own index heading, so the agent
can tell reviewed knowledge from notes. TaskSpec records both graph commit
ids.

### D5 — Notes through an outbox

`POST /outcome` stores the report and one `knowledge_note_outbox` row per kept
learning in one transaction. A ploegd loop merge-loads pending rows into the
notes graph, reads the key back, and marks the row written. A failed or
unknown write retries with backoff; a row unwritten after a day is exported
as a metric an alert can fire on.

### D6 — Promotions without long-lived branches

A Promotion is a row in ploegd: note key, suggested scope, state
(`pending|accepting|accepted|rejected`), the person who decided and the
edited concept. On accept ploegd records `accepting` with a branch name
`promote/<id>`, then as the review actor creates the branch from `main`, loads
the concept, merges and deletes it, then records `accepted` with the merge
commit. On restart an `accepting` row is resolved by reading `main` for the
concept's slug.

### D7 — Read scopes per role

Runs read Tenant, Client, repository and root. A Tenant member or admin
reads everything in the Tenant. A client user reads Client and repository
concepts of their Client and notes of their Client's Work Items, and no Tenant
concept.

## Risks / Trade-offs

- A wrong or injected note steers later Runs of the same root → notes are
  framed as unreviewed below reviewed knowledge; the TaskSpec lists them for
  the pull request's reviewer; retention ends 90 days after the root closes.
- ploegd's filter is the only Client boundary inside a graph → scope tests on
  every route, and the live 403 check for Tenant boundaries.
- Omnigraph at claim time adds latency → one call per graph; an unavailable
  graph never blocks the Run.
- Promotion queue ignored → measure the first 20 (ADR-0088 trigger).

## Migration Plan

Additive. Without the per-Tenant graph map, Runs use directories exactly as
today and learnings still go to `PLOEG_KNOWLEDGE_OUTBOX` when set. Rollback:
remove the map; written notes stay in the graph unused.

### D8 — A repository Promotion also reaches git

Accepting a `repository`-scoped Promotion creates an approved Work Item on that
repository (ADR-0031) with the concept attached as context (ADR-0067). Its Run
writes one OKF file under `knowledge/` and opens a pull request through the
normal delivery path, so forge credentials, budget and review are the ones
every Run already has; ploegd gains no forge write path. Selection keeps the
repository's file over a graph concept with the same slug once the file is on
the default branch.

## Owner answers, 2026-10-11

- A note a person writes in a session reaches Runs at once, like a Run's.
- Only a Tenant admin promotes; a member's request waits.
- An accepted repository Promotion opens a pull request to `knowledge/` now
  (D8).
- ADR-0087 to ADR-0089 stay proposed until the measurement spike (VIK-1859)
  reports.

## Open Questions

- ADR-0066 is superseded by ADR-0088 only when the owner accepts ADR-0088.
