# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-13
- **Reviewer**: Claude (session with Ryan Grippeling)
- **Change**: `pin-skill-catalogues`

## In-Force ADR Context Reviewed

Reviewed the Records index through 0091. Those that constrained this change:

- `0083-a-run-is-told-which-skills-bear-on-its-work-item.md` — selection stays deterministic and bounded; no skill comes from tracker content, so the `skill:` label only reorders admitted skills.
- `0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md` (proposed) — installation directories and role skills; OCI volumes considered and not chosen for skills.
- `0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md` — Runs gain no egress; ploegd fetches.
- `0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md` (proposed) — `opened` is read from transcript tool calls.
- `0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md` (proposed) — precedent for a per-Run configuration record.
- `0030-target-repository-instructions-rank-below-the-delivery-contract.md` — catalogue skills rank below the delivery contract, like selected skills.

## Repository-Level ADRs Created

- `docs/adrs/0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md` — Agent Package Manager catalogue per Team, fetched and verified by ploegd, served by digest.
