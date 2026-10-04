# Contributing to Ploeg

## Licence of what you send

Ploeg is [Apache-2.0](LICENSE). Contributions come in under the same terms — **inbound equals
outbound** — which is what section 5 of that licence says by default:

> Unless You explicitly state otherwise, any Contribution intentionally submitted for inclusion in
> the Work by You to the Licensor shall be under the terms and conditions of this License, without
> any additional terms or conditions.

There is **no CLA to sign** and no copyright assignment. You keep the copyright in what you wrote.
What you grant, by opening a pull request, is the Apache-2.0 licence over that contribution —
including its patent grant, which is the clause that lets everyone downstream use your work without
wondering whether a patent claim is coming.

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

Open pull requests against `development`. Run `mise run setup`, `mise run verify` and `mise run docs-check` before review. Sign off your own commits with `git commit -s` to certify the [Developer Certificate of Origin](https://developercertificate.org/). Do not sign on another contributor's behalf.
