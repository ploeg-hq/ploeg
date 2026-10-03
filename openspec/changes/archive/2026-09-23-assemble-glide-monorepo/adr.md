# ADR Review Manifest

## ADR Review Completed

- Date: 2026-09-12
- Reviewer: Codex, executing the owner's Glide migration request
- Change: assemble-glide-monorepo

## In-Force ADR Context Reviewed

All 28 numbered records and the Records index were read before import. Every `supersedes` field is `none`; the current supersession graph has no edges.

- [0001](../../../docs/adrs/0001-adrs-are-the-decision-ledger.md) — ADRs in docs/adrs/ are the single decision ledger. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0002](../../../docs/adrs/0002-go-as-the-implementation-language.md) — Go is the implementation language. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0003](../../../docs/adrs/0003-apache-2-0-license.md) — Ploeg ships under Apache-2.0. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0004](../../../docs/adrs/0004-forgejo-leading-home-github-mirror-module-path.md) — Ploeg lives on Forgejo, mirrors to GitHub, and takes its module path from the mirror. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0005](../../../docs/adrs/0005-build-a-dedicated-dispatch-plane.md) — Build a dedicated dispatch plane rather than adopt an existing orchestrator. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0006](../../../docs/adrs/0006-ahp-is-the-wrong-layer.md) — AHP is parked: a live-run surface above Ploeg, not a seam inside it. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0007](../../../docs/adrs/0007-a2a-adopt-nothing-watchlist-a-facade.md) — A2A: adopt nothing now; watchlist a north-facing dispatch facade. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0008](../../../docs/adrs/0008-litellm-is-the-credential-and-metering-seam.md) — LiteLLM stays the per-run credential and metering seam. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0009](../../../docs/adrs/0009-paperclip-mine-for-design-never-integrate.md) — Paperclip: mine it for design, never depend on it. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0010](../../../docs/adrs/0010-shift-owns-the-item-lease-owns-the-branch.md) — A Shift owns the work item; a Lease owns the branch. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0011](../../../docs/adrs/0011-the-pull-request-is-the-blackboard.md) — The pull request is the blackboard; Ploeg is only the transport. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0012](../../../docs/adrs/0012-two-level-budgets-authorized-and-settled.md) — Budgets are two-level: a Shift pool, authorized and settled per Run. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0013](../../../docs/adrs/0013-push-rights-are-minted-per-run.md) — Push rights are minted per Run and die with the Lease. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0014](../../../docs/adrs/0014-work-target-is-a-work-item-attribute.md) — Bind the Work Target to the Work Item, not to the Team. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0020](../../../docs/adrs/0020-published-artifacts-name-the-mirror-as-source.md) — Published artifacts name the GitHub mirror as their source, and Forgejo as their URL. Existing language, licensing, authority and distribution constraints remain application-scoped.
- [0022](../../../docs/adrs/0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md) — The name and mark are trademarks under a usage policy, not CC-licensed artwork. Existing language, licensing, authority and distribution constraints remain application-scoped.

Proposed 0015–0019, 0021 and 0023–0028 were also reviewed as context; their status is unchanged.

## Repository-Level ADRs Created

- [0029](../../../docs/adrs/0029-qualify-unfold-before-changing-distribution.md) records the distribution qualification boundary.
- Glide's root ADR 0001 records the owner-approved repository layout before import. It does not accept Ploeg's proposed records.

## Supersessions

None. Existing distribution stays in place until qualified cutover.

## Validation

```text
$ mise exec -- go test ./internal/ledger/
ok github.com/webgrip/ploeg/internal/ledger 0.462s
```

## Notes

0029 remains proposed. The owner's explicit instruction authorizes the local repository assembly and root decision; it does not imply production cutover.

Local migration gates and [evidence](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/docs/research/evidence/glide-2026-09-12/verification.json) passed. No published wire type, queue predicate or database migration changed. The owner requested a direct migration, so this record replaces a PR validation body.
