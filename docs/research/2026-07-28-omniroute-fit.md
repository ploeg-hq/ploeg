# OmniRoute × Ploeg — tool-fit dossier

> Surveyed 2026-07-28 via four parallel research agents (docs crawl, source-repo dig,
> ecosystem + absence checks, local seam map) plus a manual read of this repo's
> architecture docs. Condensed verdict lives in [design.md §8](../design.md); this file
> is the evidence trail.

**Verdict: adopt nothing.** [OmniRoute](https://github.com/diegosouzapw/OmniRoute) is a
competitor for the seam LiteLLM already occupies, not a new layer — and it loses that
contest on the one API Ploeg actually depends on. It cannot mint budgeted, TTL'd,
alias-tagged per-run keys; it has no Kubernetes deployment story by its own admission;
and its trust posture is close to the inverse of what a credential-holding boundary in
an autonomous factory requires.

## 1. What it is

Self-description: *"Free MIT AI gateway: one endpoint, 290+ providers (90+ free), 500+
models… Quota-aware auto-fallback, RTK+Caveman compression saves 15-95% tokens."*

A local-first Node.js/Next.js proxy on `localhost:20128` exposing OpenAI-, Anthropic-,
and Gemini-compatible endpoints, fanning requests across a four-tier fallback cascade
(Subscription → API key → Cheap → Free) with 19 named routing strategies, per-provider
circuit breakers, and a 12-engine prompt-compression stack. Its purpose is **cost
arbitrage for interactive coding agents** — pooling free tiers and subscription OAuth
accounts (Claude Code, Codex, Copilot), including TLS-fingerprint impersonation
(JA3/JA4) and a MITM proxy that intercepts Cursor traffic.

Provenance and health as of the survey date: created 2026-02-13; an unflagged derivative
of [9router](https://github.com/decolua/9router) and a TypeScript port of
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) per its own README, with
pre-v1.0 history rewritten; 5,885 commits of which ~62% are the single owner; exactly
one formal GitHub release against 100+ tags; 32.3k stars, most of them acquired in a
few days in mid-July, against a 0.5% watcher ratio; 359k Docker pulls and 301k npm
downloads, so real usage exists; **zero named production users**. Its own comparison
doc concedes LiteLLM for "mature production deployment recipes (k8s, Helm charts)" and
consumes LiteLLM's public pricing dataset for cost tracking.

## 2. Layer map

| Seam | Current occupant | OmniRoute's claim |
| --- | --- | --- |
| Ticket ingest | ploegd HMAC webhook (`pkg/httpapi`) | none — zero Vikunja mentions |
| Dispatch / scale | KEDA ScaledJob on a Postgres count | none — zero KEDA mentions |
| Harness | task contract + adapters (`pkg/harness`); ACP is the tracked standard (#64) | none for this seam — its MCP/A2A/ACP endpoints serve its own tooling |
| Inference endpoint (`LLM_BASE_URL`) | LiteLLM `/v1` | **direct competitor** |
| **Per-run key mint / budget / revoke** | LiteLLM admin API (`pkg/litellm`, `pkg/llmbroker`) | **nothing equivalent** — scoped tokens and key pools exist, but no per-key budget + TTL + alias mint/revoke contract |
| Spend ledger joined on `ploeg-<12hex>` | LiteLLM spend by `key_alias` | local SQLite analytics, no alias-joinable export |
| Model routing / failover | inside the LiteLLM proxy — out of scope per design.md §2 | its strongest capability, aimed at a seam Ploeg does not own |

The admin row decides it. Ploeg never sends an inference request; its entire LLM
coupling is key lifecycle. Swapping gateways means reimplementing
`POST /key/generate {key_alias, max_budget, models, max_hours_ttl}` plus the list and
batch-delete calls the sweeper uses for orphan cleanup. Everything else about a swap is
downstream of that.

## 3. Component-by-component

- **Tracker, ingest, dispatch — orthogonal.** OmniRoute sits entirely below the dispatch
  plane and has no concept of tickets, queues, or leases.
- **KEDA — orthogonal.** The scaling trigger is a Postgres row count; a gateway swap
  never touches it.
- **OpenHands — nominally compatible, practically unsupported.** OpenHands speaks to any
  OpenAI-compatible base URL, so pointing `LLM_BASE_URL` at OmniRoute would function.
  But both attempts to add first-class OmniRoute support (OpenHands PRs #15189 and
  #15211, July 2026) were closed unmerged.
- **LiteLLM — competing, and it loses.** Multi-tenant admin API with per-key budgets,
  Helm/k8s recipes, and a spend ledger Ploeg already joins against, versus a single-box
  SQLite app whose own release notes mention seven-minute database health checks on
  large WALs. [backlog.md](../backlog.md)'s boundary check already records that Ploeg
  links to a gateway and never owns one.
- **Security posture — disqualifying.** The gateway is the boundary between a
  prompt-injectable LLM process and real provider credentials. OmniRoute ships a
  hardcoded default JWT secret (unauthenticated admin takeover if unchanged), plaintext
  key storage unless `STORAGE_ENCRYPTION_KEY` is set, fail-open guardrails, and a
  May 2026 Socket.dev block of its npm package. Its free-tier machinery is ToS-gray by
  design.

## 4. The honest scenario, and the tension

OmniRoute is genuinely good at something nothing in this stack does: pooling free tiers
and subscription accounts behind one endpoint for *interactive* tools. Running it on a
workstation, with throwaway credentials, is a legitimate use.

It stays out of the factory because the economics are structurally opposed. The
factory's spend is **metered and attributable** — every token joined to a run, a ticket,
and a commit trailer through `ploeg-<12hex>`, on a boundary the agent cannot bypass.
OmniRoute's spend is **arbitraged and deniable**, by design, behind rotating
fingerprints. Building the second into the first breaks the audit chain that the signing
stack, the Grafana joins, and the per-run budgets exist to guarantee.

## 5. Re-evaluation triggers

- The announced **4.0 modular platform** (`@omniroute/core`, providers-as-plugins) ships
  *with* the roadmapped headless mode and a real k8s story.
- Its admin API reaches **mint/revoke parity** with LiteLLM: per-key budget, TTL, and a
  queryable alias. This is the only change that makes a swap mechanically possible.
- **OpenHands merges** provider support after the two closed attempts.
- Governance matures past a single dominant maintainer, with a **3.9 LTS** that holds
  and security defaults that are secure without configuration.
