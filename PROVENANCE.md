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

The domain-document generator and configuration-reference generator were copied from the same frozen Unfold commit: `scripts/generate-domain.py` byte for byte, `scripts/docs-configuration.py` adapted to the standalone layout. The initial tree also carried `docs/domain/consumer-terms.yaml`, the seven imported consumer term definitions with their original owner and source URL, so generation did not need a sibling Unfold checkout. Commit `60772d1` removed it when Ploeg stopped naming its consumers ([ADR-0069](docs/adrs/0069-ploeg-names-none-of-its-consumers.md)).

## Checked record

[`scripts/check-provenance.sh`](scripts/check-provenance.sh) reads the block below and fails when the repository disagrees with it. Without arguments, as part of `mise run verify`, it checks this repository: the history has exactly one root commit, that root is `root-commit` with tree `root-tree`, committed on `root-date` (UTC), and its message names `source-commit:source-path`. With `--source`, as CI runs it and as `mise run check-provenance` does, it also fetches `source-commit` from `source-repository` without file contents and checks the source side: the commit date, the tree of `source-path`, how many files the root tree has identical, changed, added and removed against it, that every file under `byte-identical` is unchanged, and that each `copied-unchanged` file equals the file at the same path in the source commit. Git tree ids are hashes over every file below them, so the two tree ids pin both trees exactly.

```provenance
root-commit: 88cce444a638f5c936226019f2a72973adbca3fd
root-tree: 43a3c7e792677aa77cf69f83653965f8a1c5d42d
root-date: 2026-10-03
source-repository: https://github.com/webgrip/unfold
source-commit: 9c1d53f01fbfb65733800aa75e288341734dc23f
source-date: 2026-10-03
source-path: apps/ploeg
source-tree: 406ac6be9032f239937e86e90e4d6d139d57ca8f
files-identical: 463
files-changed: 320
files-added: 16
files-removed: 0
byte-identical: pkg/store/migrations/* docs/contracts/*.json */testdata/*
copied-unchanged: scripts/generate-domain.py
```

Of the 320 changed files, 283 carry the module path rewrite to `github.com/ploeg-hq/ploeg`; the other 37 are documentation and tooling edits of the separation (counted from a full diff on 2026-10-24; the check counts files, not lines). The 16 added files are this one, the governance files, the GitHub workflows, two ADRs on the new home and release baseline, the CNCF charter, the consumer terms and the standalone scripts. No source file was left out.
