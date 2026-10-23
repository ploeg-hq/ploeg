---
type: reference
audience: [owner, contributor, integrator]
owner: ploeg
last_verified: 2026-10-23
verified_by: "go test ./internal/licenses/ against the build; CNCF policy read at cncf/foundation f196b954 on 2026-10-23"
---

# Licence inventory

Every component Ploeg distributes, with its licence and a disposition against the CNCF allowed third-party licence policy. The record is [license-inventory.yaml](license-inventory.yaml). The evidence behind it, with the policy text as read, is in [the research note](../research/2026-10-23-cncf-licence-inventory.md).

## What is distributed

| Artifact | Contents |
| --- | --- |
| Source repository | Ploeg code, chart, contracts, migrations and brand files (Apache-2.0); docs (CC-BY-4.0) |
| `ploeg_<version>_linux_amd64.tar.gz` | `ploegd`, `ploeg-worker`, `LICENSE`, `NOTICE`, `THIRD-PARTY-NOTICES` |
| `ghcr.io/ploeg-hq/ploegd` | distroless static-debian13, `ploegd`, `ploeg-worker`, `ploeg-mcp`, the three notice files in `/usr/share/doc/ploeg/` |
| `ghcr.io/ploeg-hq/claude-runner` | debian bookworm-slim, five apt packages, Ploeg's runner hooks, the Claude Code installer and its pin, `LICENSE` and `NOTICE`. Claude Code itself is installed at start ([ADR-0141](../adrs/0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md)) |
| `oci://ghcr.io/ploeg-hq/charts/ploeg` | the chart; no chart dependencies; defaults that pull `docker:29.8.2-dind` |

## Outbound licences

Code is Apache-2.0, as charter section 11(b)(iii) asks. Documentation is CC-BY-4.0, as section 11(b)(iv) asks ([ADR-0140](../adrs/0140-documentation-is-cc-by-4-0-and-code-and-api-specifications-stay-apache-2-0.md)). [REUSE.toml](../../REUSE.toml) says which applies to each file:

| Licence | Files |
| --- | --- |
| CC-BY-4.0 | Every Markdown file, `llms.txt`, and images under `docs/` |
| Apache-2.0 | Everything else, plus these Markdown files: `docs/contracts/**` (API specifications, which section 11(a)(iv) allows under Apache-2.0), `docs/brand/**` (artwork and trademark policy, [ADR-0022](../adrs/0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md)), the `SKILL.md` files under `pkg/` that the worker embeds, and `CHANGELOG.md`, which is generated from commit messages |

Code samples inside the documentation are also available under Apache-2.0. `go test ./internal/licenses/` fails when REUSE.toml declares a licence the inventory's `policy` doesn't name, or the other way round.

## Dispositions

| Disposition | Meaning |
| --- | --- |
| `apache-2.0` | Same licence as Ploeg. The policy needs no review. |
| `allowlisted` | Every licence is on the allowlist, the component is fetched from its upstream at build time, and it passes the use test (standard library, or a GitHub repository at least 12 months old with at least 10 stars or forks). |
| `own-work` | Written for Ploeg by its rights holders. |
| `not-distributed` | Build or test only; never in a release. The CNCF glossary calls this internal project tooling. |
| `container-guidance` | Operating-system packages in a published image, reviewed under the CNCF container image guidance. |
| `user-fetched` | Not shipped; the user's cluster or browser fetches it. |
| `exception-needed` | Not covered. Needs a Governing Board exception or a replacement. The maintainer decides which. |

## Findings

| Component | Licence | Why it matters |
| --- | --- | --- |
| `github.com/jackc/pgpassfile` | MIT | Shipped in every binary. MIT is allowlisted, but the repository has 6 stars and 4 forks, under the use threshold, and the exceptions database has no entry. The request is drafted in [exceptions/jackc-pgpassfile.md](exceptions/jackc-pgpassfile.md). |
| Name, mark and artwork | Apache-2.0 plus trademark | The charter requires trademark and logo assets to move to the Linux Foundation on acceptance. |
| Base-image packages in both images | GPL, LGPL, MPL-2.0 and others | Reviewed under the container image guidance, not the allowlist. Debian keeps every copyright file. `claude-runner` pins its base by tag only, so its GPL source cannot be pinned down later. |
| `docker:dind` chart default | Apache-2.0 over Alpine | A user-fetched dependency the chart turns on by default. Whether that needs review is a question for CNCF staff. |
| `github.com/modelcontextprotocol/go-sdk` | Apache-2.0 AND MIT | Its LICENSE also names CC-BY-4.0 for documentation, which is not linked. Recorded as allowlisted on the reading CNCF applied to `go-openapi/spec`; confirm. |

Until [ADR-0141](../adrs/0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md), every release republished the closed Claude Code binary inside `claude-runner`, which the container image guidance (B)(1) and (B)(2) send to Legal Committee and Governing Board review. Now the image carries an installer and a pin. The cluster downloads Anthropic's signed release when a pod starts, which makes Claude Code a user-fetched dependency.

Before this change, the release tarball and the images shipped MIT and BSD code without its notices. `THIRD-PARTY-NOTICES` now carries the licence, NOTICE and PATENTS text of every module linked into the binaries and of the Go runtime, and both images and the tarball include it.

## How it stays current

Renovate runs `scripts/update-license-inventory.sh` in every update branch, so a dependency bump arrives with the inventory and notices already regenerated. `go test ./internal/licenses/` runs in `mise run verify` on every pull request and push. It fails when:

- a Go module enters or leaves the build, changes version, or moves between shipped and test-only;
- a module's licence files classify differently ([google/licensecheck](https://github.com/google/licensecheck));
- a disposition is not one of the list above, an `allowlisted` entry names a licence off the allowlist, a shipped module is called `not-distributed`, or an `exception-needed`, `container-guidance` or `user-fetched` entry has no note;
- a Dockerfile `FROM` image or the chart's `dindImage` is not listed under a component's `refs`;
- `THIRD-PARTY-NOTICES` differs from what the shipped modules' files produce.

`scripts/license-check.sh` fails when the images or the tarball stop carrying the notice files.

After a dependency change:

```sh
PLOEG_UPDATE_LICENSE_INVENTORY=1 mise exec -- go test ./internal/licenses/
mise exec -- go test ./internal/licenses/
```

The first command rewrites the Go part and the notices and keeps reviewed fields for unchanged modules. A new module has no disposition, so the second command fails until a person sets one. The gate cannot check the use test or the exceptions database offline; check both by hand for a new shipped module.

Licence detection reads the top-level `LICENSE`, `COPYING`, `NOTICE` and `PATENTS` files of each module. A module that carries a differently licensed file deeper in its tree is not caught; the BuildKit SBOM attached to each image is the second source.
