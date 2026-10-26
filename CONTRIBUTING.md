# Contributing to Ploeg

## Licence of what you send

Ploeg's code is [Apache-2.0](LICENSE) and its documentation is
[CC-BY-4.0](LICENSES/CC-BY-4.0.txt); [REUSE.toml](REUSE.toml) says which covers each file.
Contributions come in under the licence of the file they change. **Inbound equals outbound.** For
code, section 5 of the Apache licence says so by default:

> Unless You explicitly state otherwise, any Contribution intentionally submitted for inclusion in
> the Work by You to the Licensor shall be under the terms and conditions of this License, without
> any additional terms or conditions.

There is **no CLA to sign** and no copyright assignment. You keep the copyright in what you wrote.
What you grant, by opening a pull request, is the Apache-2.0 licence over the code in that
contribution — including its patent grant, which is the clause that lets everyone downstream use your
work without wondering whether a patent claim is coming — and the CC-BY-4.0 licence over its
documentation. A code sample you add to a page also comes in under Apache-2.0, so it can be pasted
into code. This is the split section 11(b) of the CNCF charter asks for.

Two things this does not cover:

- **The name and the mark.** Section 6 grants no rights in trade names or marks, deliberately.
  [docs/brand/TRADEMARK.md](docs/brand/TRADEMARK.md) states what you may do with them.
- **Code you do not have the right to license.** Do not paste in work owned by an employer or
  another project unless its licence permits it and you say which licence and where it came from.

If you need different terms for a contribution, say so in the pull request before it is reviewed
rather than after.

## Before you open a pull request

Run the gates CI runs, including `scripts/license-check.sh` and `scripts/brand-marks.sh`. Read
[AGENTS.md](AGENTS.md) for how this repository expects work to be done, and
[docs/adrs/](docs/adrs/) for decisions that are already settled — an ADR is the place to argue with
one, not a pull request that quietly reverses it.

## Reporting something

Use [GitHub Issues](https://github.com/ploeg-hq/ploeg/issues) for bugs and feature requests and [SECURITY.md](SECURITY.md) for security reports.

Open pull requests against `development`. Run `mise run setup`, `mise run verify` and `mise run docs-check` before review. Sign off your own commits with `git commit -s` to certify the [Developer Certificate of Origin](https://developercertificate.org/). Do not sign on another contributor's behalf. The `dco` CI job checks every commit in a pull request; fix a missing sign-off with `git rebase --signoff development`.

## Review, roles and who decides

A maintainer who did not write a change reviews and merges it; [GOVERNANCE.md](GOVERNANCE.md#how-decisions-are-made) gives the rules, including what happens while Ploeg has one maintainer. Reviewers listed in [MAINTAINERS.md](MAINTAINERS.md) review the areas next to their name. The [contributor ladder](CONTRIBUTOR_LADDER.md) says how to become a reviewer or maintainer, and you may nominate yourself.

If you make a change for an employer, a client or a product built on Ploeg, say so in the pull request. It is reviewed on its merits like any other change; saying who it serves lets reviewers apply the [rules on companies and consumers](GOVERNANCE.md#companies-commercial-users-and-consumers).

Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md), which also says how to report a problem, including one about a maintainer.
