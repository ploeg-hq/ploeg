---
status: accepted
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Documentation is CC-BY-4.0, and code and API specifications stay Apache-2.0

## Context and Problem Statement

[ADR 0003](0003-apache-2-0-license.md) put the whole repository under Apache-2.0, documentation included. Section 11(b)(iv) of the CNCF charter says documentation is "received and made available by the CNCF under the Creative Commons Attribution 4.0 International License", and Ploeg is applying to the CNCF Sandbox ([issue #9](https://github.com/ploeg-hq/ploeg/issues/9)). The question is which files become CC-BY-4.0 and which stay Apache-2.0. The answer has to be precise enough for a REUSE tool to apply it to every file.

## Decision Drivers

* **The charter splits by kind, not by directory.** Section 11(b)(iii) makes outbound code Apache-2.0, section 11(b)(iv) makes documentation CC-BY-4.0, and section 11(a)(iv) allows an API specification for the project's own software under Apache-2.0.
* **One rights holder.** Every file was written by the sole maintainer, so relicensing needs no contributor's consent.
* **Implementers copy the contracts into code.** A consumer generates types from `docs/contracts/*.json` and quotes the contract pages in its own source. CC-BY on those files would put an attribution duty on code built from them.
* **The brand is already settled.** [ADR 0022](0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md) keeps the artwork under Apache-2.0 and the mark under a trademark policy, and rejects CC licences for it.
* **Code samples in pages must stay pasteable.** A shell or YAML example under CC-BY alone would make a reader attribute a three-line snippet inside their own code.

## Considered Options

* CC-BY-4.0 for prose and images, with API specifications, brand files, embedded skills and the changelog kept Apache-2.0
* CC-BY-4.0 for everything under `docs/`
* Dual licence: every documentation file under both Apache-2.0 and CC-BY-4.0
* Keep Apache-2.0 for everything and ask the CNCF for an exception

## Decision Outcome

Chosen option: "CC-BY-4.0 for prose and images, with API specifications, brand files, embedded skills and the changelog kept Apache-2.0". It follows the charter's own split, and it keeps everything a program reads, or a consumer turns into code, under the code licence.

`REUSE.toml` carries three annotations. The last one that matches a file wins:

1. `**`: Apache-2.0.
2. `**/*.md`, `llms.txt` and the image types under `docs/`: CC-BY-4.0.
3. `docs/contracts/**`, `docs/brand/**`, `pkg/**` and `CHANGELOG.md`: Apache-2.0.

The third annotation covers four cases. The contracts are API specifications (charter §11(a)(iv)). The brand files fall under ADR 0022. The `SKILL.md` files under `pkg/` are embedded in the worker binary and ship as part of the program. The changelog is generated from commit messages, which come in with the code. YAML that generates documentation (`docs/domain/model.yaml`, `docs/reference/configuration-descriptions.yaml`) is program input and stays Apache-2.0; the pages generated from it are CC-BY-4.0.

Code samples inside CC-BY-4.0 pages are also available under Apache-2.0. `README.md`, `NOTICE` and `CONTRIBUTING.md` say so, and contributions come in under the licence of the file they change.

### Consequences

* Good, because the repository matches the charter before the application, with no exception to ask for.
* Good, because contract consumers keep a single code licence for everything they generate from or embed.
* Bad, because anyone who republishes the pages owes attribution: credit, a link, the copyright notice, the licence name and a note of changes (CC BY 4.0 §3(a)). That includes a consumer that ships the `docs/` tree from the contract bundle. Accepted, because §3(a)(2) lets a link to the source satisfy the duty, and the duty is what the charter asks for.
* Bad, because a page that mixes prose and a long code listing has two licences in one file, and REUSE can't express that at file level. Accepted. The README grants the sample licence for every page, and a listing long enough to matter belongs in a code file.
* Neutral: ADR 0003 still holds for the code. This record narrows its scope to code and does not supersede it.

### Confirmation

* `go test ./internal/licenses/` (in `mise run verify`): `TestREUSEDeclaresTheOutboundLicences` fails unless `REUSE.toml` declares exactly the inventory's `policy.outbound` (Apache-2.0) and `policy.documentation` (CC-BY-4.0). `TestOutboundLicenceRulesFireAndPass` covers the rule both ways.
* `scripts/license-check.sh` (in `mise run verify`) fails when `REUSE.toml` stops declaring CC-BY-4.0, when `LICENSES/CC-BY-4.0.txt` is missing, or when `README.md` stops naming the documentation licence.
* `uvx reuse lint` reports every file with a licence and no unused licence.

## Pros and Cons of the Options

### CC-BY-4.0 for everything under `docs/`

* Good, because one rule is the easiest to read.
* Bad, because it puts JSON Schemas, a YAML record of breaking changes and the brand artwork under a licence written for prose. That reverses ADR 0022 for the artwork and adds an attribution duty to generated code.

### Dual licence for documentation

* Good, because nobody downstream has to change anything.
* Bad, because the charter names CC-BY-4.0 for documentation, and a second licence lets a recipient take the pages under Apache-2.0 instead, a choice the charter didn't make. It also doubles every REUSE expression.

### Keep Apache-2.0 and ask for an exception

* Good, because nothing changes.
* Bad, because section 11(c) reserves exceptions for a licence that a leveraged open source project, or the CNCF's mission, requires. Neither applies here, and the rights holder can simply comply.

## More Information

* CNCF charter section 11, read at cncf/foundation `d9616b9e` on 2026-10-24.
* [Licence inventory](../cncf/license-inventory.md), outbound licences section.
* Narrows [ADR 0003](0003-apache-2-0-license.md) to code; keeps [ADR 0022](0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md) for the brand.
* 2026-10-24: decided by the rights holder and accepted.
