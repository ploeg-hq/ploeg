# Hardened image bases

This note is the evidence for [ADR 0144](../adrs/0144-every-published-image-is-built-on-a-base-that-scans-clean-and-pulls-without-credentials.md). It records which base each published image should use, what each base scanned at, and what the hardened-image offerings require.

## Findings before the change

The published `0.2.0-rc.50` images were scanned with Grype's latest database. Local builds of `development` at `5b91fcc` gave the same counts for `ploegd` and `claude-runner`.

| Image | Critical | High | Where |
| --- | --- | --- | --- |
| `ploegd` | 3 | 24 | Go standard library `go1.27.1`, fixed in 1.27.2 |
| `claude-runner` | 21 | 112 | Debian 12: `curl`, `libcurl4`, `libc6`, `openssh-client` and others, many `wont-fix` |
| `ploeg-worker` (from #240) | 7 | 30 | Go standard library 1.27.1 (9), Debian 13 `libcurl3t64-gnutls` (15), Kerberos (10), `libc6` (2), `libsasl2-2` and `zlib1g` |

The Go findings came from the builder. The `golang:1.27-trixie` digest in both Dockerfiles carried `GOLANG_VERSION=1.27.1` and `GOTOOLCHAIN=local`, so `go.mod`'s `toolchain go1.27.3` was ignored. The fix is in [ci-and-infra](../ops/ci-and-infra.md#dependency-updates).

The Debian 13 findings in `ploeg-worker` show that moving `claude-runner` from Debian 12 to Debian 13 would not reach zero either. Debian marks the libcurl, libc and Kerberos findings `wont-fix`.

## Findings after the change

All three images were built from the change on 2026-10-24 and scanned through a Syft SBOM, so the package count shows what the scanner could see.

| Image | Base | Packages catalogued | Critical | High | Any severity | Size |
| --- | --- | --- | --- | --- | --- | --- |
| `ploegd` | `gcr.io/distroless/static-debian13:nonroot` | 36 | 0 | 0 | 0 | 38 MB |
| `ploeg-worker` | `scratch`, with Wolfi git installed by apk | 44 | 0 | 0 | 0 | 71 MB |
| `claude-runner` | `cgr.dev/chainguard/wolfi-base` | 64 | 0 | 0 | 0 | 81 MB (was 170 MB) |

In the Wolfi `claude-runner`:

- `ploeg-install-claude-code` downloaded Claude Code 2.1.296, verified `manifest.json` against Anthropic's key with `gpgv`, checked the sha256, and ran `2.1.296 (Claude Code)`. The binary also ran from a read-only `/opt/claude-code` with `--network none`.
- `git ls-remote` over HTTPS and `ssh -V` worked.
- The Stop hook wrote the final message.
- `scripts/test-claude-code-install.sh` passed in the image, with `gnupg` and `python3` added for the fixture only.

In `ploeg-worker`, `git ls-remote https://github.com/ploeg-hq/ploeg HEAD` worked as uid 1000 and as the default uid 65532, and `/bin/sh` does not exist.

## Docker Hardened Images

| Question | Finding | Source |
| --- | --- | --- |
| Licence and price | Free and Apache-2.0 since 2025-12-17. The catalogue is built on Debian and Alpine and ships SBOM, CVE data, SLSA Build Level 3 provenance and signatures | [Docker press release](https://www.docker.com/press-release/docker-makes-hardened-images-free-open-and-transparent-for-everyone/) |
| Paid tier | DHI Enterprise adds the CVE fix SLA (Critical in under 7 days), FIPS and STIG images, customisation and extended lifecycle support. The free tier has no SLA | same release |
| Registry and authentication | `dhi.io`. "You must authenticate to the Docker Hardened Images registry (dhi.io) to pull DHI Community images." Probed 2026-10-24: `https://dhi.io/v2/` and `https://dhi.io/token?scope=repository:static:pull` answer 401 without credentials, and `docker pull dhi.io/static:…` fails with `unauthorized` | [Use an image](https://docs.docker.com/dhi/how-to/use/) |
| CI without a personal login | Docker's answer is to mirror the images, build them from source, or use an organisation access token (Docker Team plan). OIDC is "being worked on", with no date | [discussion 101](https://github.com/orgs/docker-hardened-images/discussions/101) |
| Variants | Runtime images have no shell or package manager. `-dev` images add a shell, a package manager and root. glibc images are Debian, and musl images are Alpine. `static`, `debian-base` (Debian 12 and 13), `golang`, `node`, `git` and `curl` exist | [available images](https://docs.docker.com/dhi/explore/available/), [catalogue definitions](https://github.com/docker-hardened-images/catalog) |
| VEX | OpenVEX statuses, fetched with `docker scout vex get` and usable with `grype --vex` and `trivy --vex` | [scan](https://docs.docker.com/dhi/how-to/scan/), [VEX](https://docs.docker.com/dhi/core-concepts/vex/) |

The authentication requirement decides the matter for Ploeg. Pull requests from forks run without secrets, so the CVE gate could not build them. Every rebuilder and pull-through proxy downstream would also need a Docker account.

## Google distroless

`static-debian13`, `base-debian13`, `base-nossl-debian13` and `cc-debian13` publish `latest`, `nonroot`, `debug` and `debug-nonroot` tags. They are signed keyless (`keyless@distroless.iam.gserviceaccount.com`) and track Debian's security updates ([distroless README](https://github.com/GoogleContainerTools/distroless)). A distroless image is only as clean as the Debian packages in it, which is why `ploegd` (no libraries) scans clean and the git-carrying `ploeg-worker` did not.

## Chainguard and Wolfi

- `cgr.dev/chainguard/wolfi-base:latest` pulls anonymously. On 2026-10-24, `https://cgr.dev/token?scope=repository:chainguard/wolfi-base:pull` answered 200 without credentials.
- The free tier publishes only `latest` and `latest-dev`. Older digests stay pullable but stop receiving fixes ([Chainguard](https://www.chainguard.dev/unchained/scaling-chainguard-images-with-a-growing-catalog-and-proactive-security-updates)).
- The image's definition is public. Its `org.opencontainers.image.source` label points at [chainguard-images/images, images/wolfi-base](https://github.com/chainguard-images/images/tree/main/images/wolfi-base), which is not archived.
- The pinned `wolfi-base` points apk at `https://apk.cgr.dev/chainguard`. `https://packages.wolfi.dev/os` serves the same current versions: git 2.56.0-r3 from both on 2026-10-24. Both are anonymous.
- Wolfi's package recipes are Apache-2.0, and every package must carry an open-source licence ([wolfi-dev/os](https://github.com/wolfi-dev/os)).
- Every installed package keeps an SPDX SBOM under `/var/lib/db/sbom` with its licence and source.

## CNCF container image guidance

[Container image guidance v1.0](https://github.com/cncf/foundation/blob/main/policies-guidance/container-image-guidance.md) (2025-06-04):

- (A)(1) asks for the most minimal base reasonably necessary, with a publicly visible build definition.
- (A)(2) asks for a base that makes licence notices and source available.
- (B)(1) admits only the project's own build output and standard package manager content.
- (B)(3) asks the project to tell users which base it uses.

It says nothing about registries or pulling without authentication. The anonymous-pull driver in ADR 0144 is Ploeg's own requirement, which comes from fork pull requests and downstream rebuilders.
