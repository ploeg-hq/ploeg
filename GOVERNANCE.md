# Governance

Ploeg has one maintainer today. This page says how decisions are made now and how that changes as people join. The [scope and stewardship charter](docs/cncf/charter.md) says what the project covers.

## Roles

- **Contributors** open issues and pull requests. Anyone can.
- **Maintainers** review and merge pull requests, cut releases, triage security reports and decide when contributors disagree. [MAINTAINERS.md](MAINTAINERS.md) lists them.

## How decisions are made

| Decision | Where | Who decides |
| --- | --- | --- |
| A code or documentation change | A pull request against `development` | A maintainer who did not author it approves and merges it. While there is one maintainer, that maintainer merges their own changes after CI passes. |
| An architectural decision | A new record in [docs/adrs/](docs/adrs/README.md), proposed in a pull request | Maintainers by consensus. Without consensus after the discussion, a majority of maintainers. The record is append-only: a later record supersedes it. |
| Priority and roadmap | [GitHub Issues](https://github.com/ploeg-hq/ploeg/issues) and its milestones | Maintainers. Anyone may propose or comment. |
| A stable release | A reviewed tag on `main` ([releases](docs/ops/release-versioning.md)) | A maintainer. |
| Adding or removing a maintainer | A pull request to MAINTAINERS.md | Existing maintainers by consensus; see below. |
| Changing this page | A pull request to GOVERNANCE.md | Maintainers by consensus, open for at least seven days. |

A maintainer who has a conflict of interest in a decision says so in the pull request or issue and leaves the decision to the others. With one maintainer this cannot be enforced; the declaration is still made.

## Becoming a maintainer

Anyone may propose a maintainer, including themselves, in a public issue. A nominee needs to have reviewed changes and helped operate or release Ploeg over several months, and must agree to the nomination in the issue. Maintainers decide by consensus and record the result in MAINTAINERS.md. A maintainer who has been inactive for six months, or who asks to step down, moves to an emeritus list.

## Conduct and security

The [code of conduct](CODE_OF_CONDUCT.md) applies everywhere the project works. Report security problems privately as [SECURITY.md](SECURITY.md) describes, never in a public issue.

## Worked examples

1. *A contributor proposes replacing PostgreSQL leases with a different locking scheme.* This is architectural, so it needs a new ADR in a pull request. The maintainers discuss it there and accept or reject the record; the ADR ledger keeps the earlier record and links the new one.
2. *Two contributors disagree on a pull request about the default harness.* A maintainer decides in the pull request and says why. If either contributor thinks the default is a decision about direction, they open an ADR, and the table above applies.

## What is not decided here

Transferring the name, domains, accounts or repositories, or signing any foundation agreement, is outside this page. The charter says those need a separate owner decision.
