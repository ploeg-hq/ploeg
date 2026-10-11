# Scoped knowledge and Work Item Notes

## Why

The owner asked on 2026-10-11 for agents to write knowledge per Work Item,
client, repository and organization, and for every agent to read the levels
that apply to it, in Omnigraph. Today a Run reads OKF directories only
(ADR-0065), its learnings go to a local outbox that nothing reads (ADR-0066),
and nothing models a Client, repository or Work Item boundary for knowledge.
Interactive sessions have no path to the same knowledge at all.

Omnigraph's policy separates whole graphs and protected from unprotected
branches; it cannot filter rows or pin a branch prefix, and an open branch
blocks a schema change. The owner chose, on that basis: two graphs per Tenant
(reviewed knowledge and notes), Work Item Notes without review, review only
for a wider Knowledge Scope, sessions through their consumer, and Omnigraph for
hosted Tenants too.

## What Changes

**Seams touched:** the Run API (one Run route), the Harness contract (one
optional OutcomeReport field) and the operator API (knowledge routes). Each
has its own capability; they ship as three increments in that order.

- **Tenant knowledge (ADR-0087).** ploegd holds each Tenant's run actor and
  review actor for its knowledge graph and notes graph. A Run fetches the
  concepts and notes of its own Knowledge Scopes with its Run token; the
  worker selects them like any other pack source. The worker and the harness
  never hold a graph token.
- **Work Item Notes (ADR-0088).** Every kept learning becomes a Work Item Note
  written without review and read by later Runs under the same root. An
  optional `learnings[].scope` lets the agent suggest a wider scope, which
  becomes a Promotion a Tenant admin accepts or rejects. Notes are written
  through an outbox so a restart writes each once.
- **Operator knowledge (ADR-0089).** Operator routes to read knowledge, write a
  Work Item Note and request, accept or reject a Promotion, for a person the
  consumer names, checked by ploegd against the consumer's Tenant and the
  person's Client and role.
- **Domain.** New terms Tenant, Client, Knowledge Scope, Work Item Note and
  Promotion.

## Capabilities

### New Capabilities

- `tenant-knowledge`: which graphs and actors a Tenant has, what a Run reads
  from them and how it reaches the worker.
- `work-item-notes`: how a Run's learnings become notes and Promotions, and
  how a Promotion becomes reviewed knowledge.
- `operator-knowledge`: knowledge reads and writes for a named person through
  the operator API.

### Modified Capabilities

None in `openspec/specs/`. The `run-learnings` requirement "A learning is a
proposal" in the unarchived change `add-knowledge-and-context-bundles` is
replaced by `work-item-notes` once ADR-0088 is accepted.

## Non-Goals

- Deploying graphs, actors, policies or stored queries: that is deployment
  configuration, filed for the homelab on its own roadmap.
- Recording Tenant and Client in Ploeg's store (Unfold system ADR-0017,
  VIK-1876). Until it lands every Work Item is in the default Tenant without a
  Client, and the Client scope stays empty.
- Selection quality: keyword selection stays until the selection spike.
- A live knowledge tool inside a Run (ADR-0087 rejects it for now).
- The review screen: it belongs to the consumer.

## Impact

- Contracts: `run-api.v1` (`GET /api/v1/runs/{token}/knowledge`),
  `outcomereport.v1` (`learnings[].scope`), `taskspec.v1` (graph commit ids in
  `knowledge`), `operator-api.v1` (knowledge, notes and promotion bodies).
  Additive and optional.
- Store: migrations for the note outbox and Promotions.
- Configuration: a per-Tenant map of graph ids and token references in ploegd.
  Without it, Runs behave as today.
- Dependencies: Omnigraph's HTTP API from ploegd only.
