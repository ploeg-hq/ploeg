---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Every published image is built on a base that scans clean and pulls without credentials

## Context and Problem Statement

Ploeg publishes three images: `ploegd`, `claude-runner` and `ploeg-worker`. A Grype scan on 2026-10-24 found:

* `ploegd:0.2.0-rc.50`: 3 Critical and 24 High, all in the Go standard library. The cause was the builder's toolchain, not the distroless base.
* `claude-runner:0.2.0-rc.50`: 21 Critical and 112 High, in Debian 12 `curl`, `libcurl`, `libc` and `openssh-client`. Debian marks many of them `wont-fix`.
* `ploeg-worker`, built from `development`: 7 Critical and 30 High. That is the Go standard library plus Debian 13 `libcurl3t64-gnutls`, Kerberos, `libc6` and `libsasl2`, mostly `wont-fix`.

A CNCF Sandbox project should be able to say that its images carry no known Critical or High finding, and prove it on every release. A newer Debian does not get there, because Debian leaves those findings unfixed. Which base should each image use?

## Decision Drivers

* **Zero Critical and High findings, measured by the release gate's scanner.** A base whose distribution marks findings `wont-fix` only works through permanent VEX suppressions.
* **Pulls without credentials.** Pull-request CI runs fork code without secrets, and a downstream rebuilder or Harbor proxy must reproduce the build without an account at an image vendor.
* **CNCF container image guidance** (v1.0, 2025-06-04, cncf/foundation `policies-guidance/container-image-guidance.md`): (A)(1) the most minimal base reasonably necessary, with a publicly visible build definition; (A)(2) licence notices and source available; (B)(1) content only from the project's build or a standard package manager.
* **What each image runs.** `claude-runner` needs glibc for Anthropic's linux-x64 Claude Code build, plus bash, coreutils, gawk, sed, grep, curl, jq, gpgv, git and openssh-client for the installer and the runner hooks ([ADR 0141](0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md)). `ploeg-worker` needs git with HTTPS and CA certificates ([ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md)). `ploegd` needs only CA certificates.

## Considered Options

* Docker Hardened Images (DHI)
* Google distroless (Debian 13) for all three, with git and the runner tools copied from Debian
* Wolfi packages: `cgr.dev/chainguard/wolfi-base` for `claude-runner`, and an empty base filled by apk for `ploeg-worker`
* Debian 13 slim for `claude-runner`, with VEX for what stays

## Decision Outcome

Chosen option: "Wolfi packages" for `claude-runner` and `ploeg-worker`, and distroless `static-debian13:nonroot` kept for `ploegd`. These are the bases that measured zero with the pinned scanner, pull anonymously and have public build definitions.

* **ploegd** stays on `gcr.io/distroless/static-debian13:nonroot`. The base has six Debian packages and no finding. The image's findings came from building with Go 1.27.1, and the builder fix (`GOTOOLCHAIN=auto` with `scripts/check-go-toolchain.sh`) removes them.
* **ploeg-worker** runs `apk add --root /rootfs --initdb wolfi-baselayout ca-certificates-bundle git` in a `wolfi-base` build stage and copies that root onto `FROM scratch`. The image has git 2.56 and 39 library packages, no shell and no package manager, and runs as uid 65532. Each package keeps its apk record and SPDX SBOM, so scanners and the licence inventory see every one.
* **claude-runner** runs on `wolfi-base` with `apk add` of the eleven tools above. It keeps busybox and apk-tools, which a person building `FROM` it needs (the [how-to](../how-to/run-claude-code-in-the-claude-runner.md)), and runs as the unprivileged `runner` user.
* Every `FROM` is pinned by digest, and Renovate moves the digests. `apk add` takes the current Wolfi package at build time. The Wolfi repository withdraws old package versions, so a version pin would break rebuilds. Instead, the release gate scans exactly what is published, and the BuildKit SBOM records the versions.

### Consequences

* Good, because the three images scanned at Critical 0 and High 0 on 2026-10-24 (Grype 0.121.0, database built 2026-10-23). `claude-runner` and `ploeg-worker` showed no finding of any severity across 64 and 40 catalogued packages.
* Good, because Wolfi rebuilds a package when its upstream fixes a CVE, rather than waiting for a Debian point release, and a rebuild of the image picks the fix up.
* Good, because `ploeg-worker` loses its shell. A process that takes over the credential-holding container has git and nothing else to run.
* Bad, because `claude-runner` is no longer Debian. An image built `FROM` it that runs `apt-get` breaks, and it must use `apk add` instead. This is recorded in [breaking-changes.yaml](../contracts/breaking-changes.yaml) under `v0.2.0-rc.52` and ships as `feat!`. There is no deprecation window, because the Debian base is the source of the findings this record removes.
* Bad, because two identical builds a week apart can contain different package versions. Accepted, because the gate scans every published digest and the SBOM pins what shipped. A reproducible alternative is apko with a lockfile, which is a re-evaluation trigger below.
* Bad, because Chainguard serves only the `latest` tag of `wolfi-base` free of charge. A digest stays pullable but stops receiving fixes, so Renovate's digest updates are what keep the base current.
* Neutral: `ploegd`'s distroless base and its Debian package licences are unchanged.

### Confirmation

* The CVE gate builds every image in `ops/security/images.yaml`, scans it with the pinned Grype, and applies `ops/vex/statements/<image>.openvex.json`. It fails a pull request, the daily run or a release that exceeds the zero budget in `ops/security/cve-budgets.yaml` ([ADR 0145](0145-a-release-publishes-no-image-over-its-cve-budget-and-attaches-signed-openvex-to-every-digest.md)).
* `go test ./internal/licenses/` fails when a `FROM` line names an image that no inventory component lists, so a base change without an inventory entry cannot merge.
* CI runs `git --version` as uid 1000 in the built `ploeg-worker` image, and `scripts/check-go-toolchain.sh` on every image that ships Go.
* `scripts/test-claude-code-install.sh` passed inside the Wolfi `claude-runner` (with `gnupg` and `python3` added for the fixture) on 2026-10-24. The real installer verified and ran Claude Code 2.1.296, also with a read-only `/opt/claude-code` and no network.

## Pros and Cons of the Options

### Docker Hardened Images (DHI)

* Good, because DHI has been free and Apache-2.0 since 2025-12-17, ships SBOM, OpenVEX, SLSA level 3 provenance and signatures, and has `static`, `debian-base` (Debian 13, glibc) and `-dev` variants with a package manager.
* Bad, because `dhi.io` refuses anonymous pulls. The documentation says "You must authenticate to the Docker Hardened Images registry (dhi.io) to pull DHI Community images", and on 2026-10-24 `https://dhi.io/v2/` and its token endpoint both answered 401 without credentials. Fork pull requests carry no secrets, so the PR gate could not build. Every downstream rebuilder and Harbor proxy would also need a Docker account. That ties a CNCF project's build to one vendor's login, so this option is rejected while the condition holds.
* Bad, because the CVE fix SLA is part of the paid Enterprise tier. The free tier promises none.

### Google distroless (Debian 13) for all three

* Good, because it is minimal, signed and anonymous, and it remains the right base for `ploegd`.
* Bad, because the Debian 13 libraries git and curl need carried `wont-fix` Critical and High findings in the `ploeg-worker` build. Distroless cannot be cleaner than the Debian packages it is made from.

### Debian 13 slim for claude-runner

* Good, because it is the smallest change for anyone building `FROM` the image.
* Bad, because `libcurl`, `libc` and Kerberos carry the same `wont-fix` findings as in `ploeg-worker`, so zero would rest on permanent VEX statements for code that is actually present and reachable. A `not_affected` claim about the HTTP client of an image whose job is HTTP would not be honest.

## Re-evaluation triggers

* `dhi.io` serves anonymous pulls, or offers keyless OIDC pulls usable from a fork pull request.
* A Wolfi package needed by an image is withdrawn from the free repository or moves behind a login.
* Chainguard stops serving `cgr.dev/chainguard/wolfi-base:latest` anonymously.
* A Debian release fixes the `wont-fix` findings in libcurl, libc and Kerberos.
* Ploeg needs byte-for-byte reproducible images. apko with a resolved lockfile would then replace `apk add`.

## More Information

* Evidence: [docs/research/2026-10-24-hardened-image-bases.md](../research/2026-10-24-hardened-image-bases.md), with each scan and source.
* Related: [ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md), [ADR 0141](0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md), [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md), [licence inventory](../cncf/license-inventory.md).
* 2026-10-24: proposed with the change.
