# Origin and separation

The initial tree is derived from `apps/ploeg` in [Unfold](https://forgejo.webgrip.dev/webgrip/unfold), frozen at [`9c1d53f01fbfb65733800aa75e288341734dc23f`](https://github.com/webgrip/unfold/commit/9c1d53f01fbfb65733800aa75e288341734dc23f) on 2026-10-03.

The new repository deliberately starts a fresh Git history. The software predates this repository; this is not a rewrite or a claim of new authorship. Historical commits and release tags remain in their original repositories. Apache-2.0, NOTICE, REUSE attribution and trademark terms are retained. Source files, SQL migrations, contracts, fixtures and the decision ledger are preserved.

The separation changes module imports to `github.com/ploeg-hq/ploeg`, standalone tooling and documentation, GitHub CI, artifact identities, and the new release baseline `v0.1.0`. Historical decisions remain readable; new ADRs supersede hosting and release policy.

[Parent maintainer approval record](https://github.com/webgrip/unfold/issues/1). Ryan approved the separation in the work session on 2026-10-03. This records his approval; the full parent maintainer roster and any additional votes remain to be confirmed publicly.

## Historical sources

- [Unfold source history](https://forgejo.webgrip.dev/webgrip/unfold) and [GitHub mirror](https://github.com/webgrip/unfold).
- [Previous Ploeg distribution repository](https://github.com/webgrip/ploeg). Its version tags are independent of this new module and are not rewritten.
- The previous Forgejo Ploeg URL was not publicly readable during extraction. No claim is made about its retention or availability.

## CNCF preparation

This is an independent project preparing for a possible CNCF Sandbox application, not an accepted CNCF project. The current [application](https://github.com/cncf/sandbox/blob/main/.github/ISSUE_TEMPLATE/application.yml) asks for a repository at least six months old with active development, a public parent-maintainer vote, Apache-2.0 and a populated MAINTAINERS file.

The new repository was created on 2026-10-03. Plan an eligibility review no earlier than 2027-04-03, subject to actual active development and the then-current CNCF rules. Earlier history is provenance, not a claimed age waiver.

The domain-document generator and configuration-reference generator were copied from the same frozen Unfold commit. `docs/domain/consumer-terms.yaml` preserves only the seven imported consumer term definitions so generation no longer requires a sibling Unfold checkout; the original owner and source URL are retained.
