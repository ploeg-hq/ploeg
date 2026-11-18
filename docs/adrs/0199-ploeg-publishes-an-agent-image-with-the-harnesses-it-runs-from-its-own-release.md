---
status: proposed
date: 2026-11-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-18
---

# Ploeg publishes an agent image with the harnesses it runs from its own release

## Context and Problem Statement

Ploeg runs the OpenHands harness and the OpenCode, Qwen Code, goose and OpenHands [ACP profiles](../contracts/acp-profiles.md), holds their adapters and owns the harness conformance suite. Yet it publishes no image that contains them. `values.yaml` says so ("Ploeg publishes no agent image, so the render fails without one"), and [ADR 0099](0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md) records it as a fact. Every installation therefore builds an agent image of its own or borrows one. The image the existing installations use, `agent-runner`, is built privately by the maintainer outside this project, for amd64 only, from a registry proxy and a skills repository nobody else can reach. An installation cannot start a real Run from public artifacts alone.

Should Ploeg publish an agent image, and if so, how is it built, versioned and signed?

## Decision Drivers

* **Ploeg owns the harness seam.** The adapters and the conformance suite decide which harness versions work; the image that carries those versions belongs with them.
* **A public installation needs no private build.** The image must pull without credentials and build from public sources only ([ADR 0144](0144-every-published-image-is-built-on-a-base-that-scans-clean-and-pulls-without-credentials.md)).
* **One publish path.** Every published image is built, CVE-gated, tagged, signed and attested by the release ([ADR 0145](0145-a-release-publishes-no-image-over-its-cve-budget-and-attaches-signed-openvex-to-every-digest.md)); a new image must not invent a second one.
* **amd64 first, arm64 when it builds.** Installations run on arm64 nodes and on arm64 workstations' kind clusters, but the copied image was only ever built for amd64.
* **No Anthropic executable** in any Ploeg artifact ([ADR 0141](0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md)).
* **Change one thing at a time.** The first published image should behave like the one installations already run, so a difference in a Run is not also a difference in harness versions.

## Considered Options

* Publish `ghcr.io/ploeg-hq/agent-runner` from the release, as a copy of today's `agent-runner` rebuilt on Wolfi with patched dependencies
* Keep publishing no agent image and document how to build one
* Publish the image from its own workflow, on its own version line
* Publish the copy on Alpine, as it was built before

## Decision Outcome

Chosen option: "Publish `ghcr.io/ploeg-hq/agent-runner` from the release, as a copy of today's `agent-runner` rebuilt on Wolfi with patched dependencies", because it is the only option that gives every installation a public, signed image through the publish path that already exists, within the CVE budget every Ploeg image meets, without changing harness versions at the same time.

1. **Source.** [`ops/docker/agent-runner/`](../../ops/docker/agent-runner/README.md) holds the Dockerfile, the entrypoint, the per-Run LiteLLM key helper, the OpenHands constraints and the npm lockfiles. The image carries the same harnesses and tools at the same versions as the `agent-runner` 1.4.2 image it copies: OpenHands CLI 1.16.0 installed under its release's lockfile, OpenCode 1.19.5, Qwen Code 0.26.5, goose 1.57.0, moon 2.6.5 and openspec 1.16.0. Two things are removed because they named private infrastructure: the entrypoint no longer defaults `LLM_BASE_URL` to a private gateway and refuses to start without one, and the build no longer bakes skills from a private repository, so `OPENHANDS_SKILLS_PROFILE` adds skills only when `OPENHANDS_SKILLS_REPO` names a repository.
2. **Base.** The copy was built on Alpine images behind a credentialed registry proxy. It is now built on the digest-pinned `cgr.dev/chainguard/wolfi-base` that `claude-runner` and `ploeg-worker` use ([ADR 0144](0144-every-published-image-is-built-on-a-base-that-scans-clean-and-pulls-without-credentials.md)), with `python-3.12`, `nodejs-24`, `git` and `bash` from Wolfi. That makes the C library glibc, so Python installs manylinux wheels. npm is installed only in the build stage that runs `npm ci` for the tools, and the published image carries `node` without npm: nothing a Run executes needs npm, and npm's bundled packages carried findings of their own. A first build on Docker Hub's Alpine images measured 19 Critical and 178 High findings, 22 of them in Alpine packages.
3. **Patched dependencies.** OpenHands installs under its release's lockfile, [openhands-1.16.0.constraints.txt](../../ops/docker/agent-runner/openhands-1.16.0.constraints.txt), except for the packages [openhands-overrides.txt](../../ops/docker/agent-runner/openhands-overrides.txt) names. uv installs those at the override's version whatever OpenHands and its dependencies declare. The overrides raise every package with a Critical or High advisory to its first fixed version, plus the packages a raised one needs newer (`cffi`, `yarl`, `pydantic-settings`, `fastapi` for `starlette` 1.x) and the one it adds (`joserfc`). `openhands-constraints.sh` leaves every overridden package out of the lockfile it exports, so a constraint never contradicts an override. On an OpenHands bump, regenerate the constraints with the script, drop each override the new lockfile already meets, and add one for each new finding. Qwen Code's lockfile raises `sharp` to 0.35.5 with an npm `overrides` entry the same way. The raised versions are newer than any OpenHands release was tested with, so the Dockerfile's assertions (OpenHands starts as uid 1000, `lmnr` accepts the SDK's tracing call, the CLI reports its version) and `mise run harness-conformance` inside the image are what show that it still works.
4. **Catalogue.** [`images.yaml`](../../ops/security/images.yaml) lists the image with `platforms: [linux/amd64]`, an enforced budget of zero Critical and zero High findings, and an OpenVEX file with no statements. An image without `platforms` still builds for `linux/amd64` only; a listed platform must be `linux/amd64` or `linux/arm64` and must include `linux/amd64`, the platform the gate scans.
5. **Release.** `scripts/release.sh` builds every image for its catalogued platforms. The release workflow registers QEMU so its amd64 runner can build an image that lists `linux/arm64`, and its timeout rises from 45 to 120 minutes for that emulated build. The agent image lists only `linux/amd64` for now: its first arm64 build, on Alpine, failed because `grpcio` 1.67.1 has no musl wheel for aarch64 and the build has no C++ compiler. On Wolfi a manylinux aarch64 wheel exists, but no arm64 build of the Wolfi image has run yet. The image is then gated, tagged, signed and attested exactly like `ploegd`, `claude-runner` and `ploeg-worker`, and its BuildKit metadata, OpenVEX and CVE budget verdict become release assets.
6. **Tags.** The image takes the Ploeg version: `0.x.y-rc.N` for each candidate, `0.x.y` for a stable release, and no `latest`. The private image's own version line (1.0.0 to 1.4.2) ends; "`agent-runner` 1.4.0" in older records names that line.
7. **Pull requests.** [image-security.yml](../../.github/workflows/image-security.yml) builds and gates the image for amd64 like every catalogued image, and a new `arm64-build` job builds each image that lists `linux/arm64` on GitHub's `ubuntu-24.04-arm` runner, so an arm64 failure shows on the pull request instead of in the release.
8. **Chart default: not yet.** The chart keeps rendering no agent image by default. The release stamps image digests into the chart when it packages it ([package-chart.sh](../../scripts/package-chart.sh)), and the quickstart's checkout leg resolves those digests from the newest published release. Until a release has published `agent-runner`, a chart default would have no digest to stamp and the quickstart's checkout leg would fail on every pull request. A follow-up after the first such release adds an `agentRunner.image` block stamped like `claudeRunner.image` and makes it the fallback after `executor.runnerImage`. The quickstart's "Use a real agent" step changes with it, so it names the image only once a release has it.
9. **Entrypoint.** Ploeg's worker does not use the image's entrypoint: the harness container runs the launcher the worker copies in ([managed workers](../ops/managed-workers.md)). The entrypoint and `litellm-key` serve a person who runs the image on its own, with a gateway key or a master key from which it mints and revokes a per-Run key.

### Consequences

* Good, because an installation can run a real harness from public, signed artifacts, and the harness versions Ploeg tests are the ones it ships.
* Good, because the image is gated, signed and attested by the same code as every other Ploeg image, and its absence from the catalogue would fail `go test ./internal/cvegate/`.
* Bad, because the image must meet the zero Critical, zero High budget before any release can publish anything. Its private predecessor was gated in a warning mode and last measured above zero. If the first `cve-budget (agent-runner)` run is over budget, every release stops until the findings are fixed, justified in OpenVEX, or the image leaves the catalogue. Accepted, because publishing an image over budget is what ADR 0145 rules out.
* Bad, because arm64 installations get no agent image yet.
* Bad, because once an image lists `linux/arm64`, its build runs under QEMU and lengthens every release, candidates included; native arm64 release builds are a re-evaluation trigger.
* Bad, because the image redistributes several hundred Python and npm packages that nobody has reviewed one by one. The [licence inventory](../cncf/license-inventory.yaml) records them as `exception-needed` until that review is done.
* Bad, because the image is not the byte-for-byte harness environment installations ran before: the C library, the Python and Node.js builds and about thirty Python packages differ. Accepted, because the copy as it was is over the budget ADR 0145 enforces; the Dockerfile's assertions and the conformance suite are the checks that it still behaves.
* Bad, because every override is a version OpenHands did not lock, and a future OpenHands release may need it dropped or raised. The overrides file is short and each entry names a package with an advisory, so the next bump reviews it in one place.
* Neutral: an arm64 variant would be built and signed but scanned only through the amd64 variant, because the gate scans the platform its runner pulls.

### Confirmation

* `go test ./internal/cvegate/` fails when a Dockerfile under `ops/` is not catalogued, when a catalogued image has no enforced budget or valid OpenVEX file, when a platform is unsupported or leaves out `linux/amd64`, when `scripts/release.sh` stops building with each image's catalogued platforms, and when an image lists `linux/arm64` while `release.yml` registers no QEMU.
* `image-security.yml` runs `cve-budget (agent-runner)` on every pull request, push and day, and `arm64-build` builds every image that lists `linux/arm64`.
* The Dockerfile fails its own build when OpenHands, installed with the overrides, does not start or is not executable as uid 1000, when OpenHands' tracing library lacks the call the SDK makes, when the public-skills patch no longer applies, when a CLI reports a version other than its pin, or when `litellm-key mint` succeeds without configuration.
* `go test ./internal/licenses/` fails when a base image the Dockerfile names has no inventory entry.

## Pros and Cons of the Options

### Keep publishing no agent image and document how to build one

* Good, because Ploeg carries no third-party payload and no extra CVE or licence work.
* Bad, because no installation can run a real harness without a build pipeline of its own, and the one that exists is private.

### Publish the image from its own workflow, on its own version line

* Good, because a harness bump would not wait for, or lengthen, a Ploeg release.
* Bad, because it is a second publish, sign and gate path next to the release, which ADR 0145 exists to prevent, and a consumer would have to match two version lines.

### Publish the copy on Alpine, as it was built before

* Good, because the C library and every native wheel stay the ones the copied image ran with.
* Bad, because its first gate run measured 19 Critical and 178 High findings: 166 in the OpenHands Python tree, 22 in Alpine packages and 9 in npm packages. Publishing it would need a warning-mode budget, which ADR 0145 rules out for a published image.
* Bad, because `grpcio` has no musl wheel for aarch64, so arm64 would need a compiler in the build.

## Re-evaluation triggers

* The first `cve-budget (agent-runner)` run reports a Critical or High finding that has no fixed version and no honest OpenVEX justification.
* Someone needs the image on arm64: list `linux/arm64` and let `arm64-build` prove the Wolfi build there.
* An OpenHands release locks every overridden package at or above its override, or an override breaks a harness in the conformance suite.
* An emulated arm64 build makes a release exceed 60 minutes, or GitHub offers arm64 runners to the release job's matrix without a second publish path.
* The licence review of the Python and npm trees finds a licence that is neither allowlisted nor coverable by an exception.
* A harness adapter needs a version the image does not carry, or the conformance suite fails against the image.
* The first release that publishes `agent-runner` exists, which unblocks the chart default of point 6.

## More Information

* [ops/docker/agent-runner/README.md](../../ops/docker/agent-runner/README.md) — what the image carries and how to bump each pin.
* [ADR 0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) — skills and toolchains are mounted, not baked, which is why removing the baked skills changes no Ploeg Run.
* [ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) — the harness container the image becomes.
