---
status: accepted
date: 2026-10-25
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# The claude-runner image carries no Claude Code and installs Anthropic's signed release at start

## Context and Problem Statement

Every release published `ghcr.io/ploeg-hq/claude-runner` with the Claude Code binary inside it. The Dockerfile downloaded the binary from `downloads.claude.ai` and checked its sha256. The binary is closed ("© Anthropic PBC. All rights reserved"), has no source, and comes from a vendor's website rather than a package manager. The CNCF container image guidance (B)(1) asks a project image to hold only the project's own build output and standard package manager content, and (B)(2) sends a binary without source to Legal Committee and Governing Board review ([licence inventory](../cncf/license-inventory.md), [issue #9](https://github.com/ploeg-hq/ploeg/issues/9)). The rights holder decided that Ploeg stops redistributing it.

The image serves three uses ([ADR 0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)): the runner Deployment (`claude self-hosted-runner`), the dispatching worker of a `claude-environment` team (`claude -p … --environment`, `claude auth login`), and the one-off pod in which a person signs in to a login volume. How does each get Claude Code when no image Ploeg publishes contains it?

## Decision Drivers

* **No Anthropic executable in any Ploeg artifact**, the image layers included.
* **Unmodified and from the vendor.** Anthropic's terms require the binary to run unmodified ([ADR 0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)). The cluster should fetch it from Anthropic's own distribution.
* **Verified before it runs.** Anthropic signs each release's `manifest.json` with a GPG key (fingerprint `31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`), and the manifest lists every platform binary's sha256 ([binary integrity](https://code.claude.com/docs/en/setup#binary-integrity-and-code-signing)). The old build checked only a sha256 pin.
* **Least friction for an installer.** An existing installation should keep working after an upgrade with at most an egress rule, not a new image pipeline.
* **A session cannot change the binary the runner executes next.** The hooks are root-owned for this reason; the binary deserves the same protection.
* **The worker's harness environment is an allowlist** ([environment.go](../../pkg/worker/environment.go)), so a setting the dispatcher must honour cannot travel as an environment variable.

## Considered Options

* Install at start: the image carries an installer and a pin; the chart installs into a volume before the runner starts, and a `claude` wrapper installs on first use where no volume exists
* The operator builds the image: Ploeg publishes a base without Claude Code, and every installer builds and hosts `FROM` it with Claude Code added
* Install from Anthropic's signed apt repository at start
* Keep shipping the binary and request a Governing Board exception

## Decision Outcome

Chosen option: "Install at start". It is the only option that needs no image of the installer's own and still keeps Anthropic's executable out of every Ploeg artifact.

1. **Image.** `ghcr.io/ploeg-hq/claude-runner` keeps Debian, git, curl, jq, ssh and the hooks. It adds `ploeg-install-claude-code` and the pin `/opt/ploeg/claude-runner/claude-code.env`, which holds `CLAUDE_CODE_VERSION` and `CLAUDE_CODE_SHA256` (2.1.296 at this record). `/usr/local/bin/claude` becomes a wrapper: it calls the installer, sets `DISABLE_AUTOUPDATER=1` unless the operator set it, and execs the real binary. `ENTRYPOINT ["claude"]` and every argument stay as they were.
2. **Installer.** It downloads `manifest.json`, `manifest.json.sig` and the release key from `downloads.claude.ai`. It verifies the signature with `gpgv`, which the Debian base already carries, and requires the signer to be the pinned fingerprint. It checks the manifest's version and the binary's sha256 against the signed manifest. When a pin applies, the manifest must also agree with it. Only then does it move the binary into place. A version already installed is reused without the network. It tries `/opt/claude-code`, then `$XDG_CACHE_HOME`, then `$TMPDIR`, or only `PLOEG_CLAUDE_CODE_DIR` when that is set. Every URL and the fingerprint can be overridden, so a mirror works.
3. **Runner Deployment (chart).** `claudeRunner.claudeCode.install: true` (the default) renders an init container from the same image. It runs as uid 65532 and installs into an emptyDir, which the runner (uid 1000) mounts read-only at `/opt/claude-code`, so a session can neither replace nor patch the binary. `claudeRunner.claudeCode.version` and `.sha256` override the image's pin for the runner. Set `install: false` when `claudeRunner.image` is an image of your own that already carries Claude Code there.
4. **Dispatching worker and sign-in pod.** No chart change. `/opt/claude-code` is root-owned and empty in the image, so the wrapper installs into the Run's own cache directory on first use and verifies the build the same way. That costs one 257 MB download per Run (about 9 s on a home connection, measured 2026-10-24). No agent runs in that pod ([ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) keeps `claude-environment` in one container), so a writable cache gives nothing an attacker could use.
5. **Your own image is the escape hatch.** For an air-gapped cluster, or to drop the per-Run download, build `FROM` the Ploeg image and run `ploeg-install-claude-code` as root with `PLOEG_CLAUDE_CODE_DIR=/opt/claude-code`. Point `claudeRunner.image` and the team's `harness.image` at the result, and set `claudeRunner.claudeCode.install: false`. That image holds Anthropic's binary, so it is the operator's to host and never Ploeg's to publish.
6. **Egress.** The chart's NetworkPolicies already let the runner and a `claude-environment` team's workers reach the internet on 443 ([ADR 0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md)). An installer that narrows egress by hostname must add `downloads.claude.ai:443` for the runner pods and those workers.

### Consequences

* Good, because no Ploeg artifact contains an Anthropic executable, so the inventory records Claude Code as `user-fetched` and needs no exception.
* Good, because the installed binary is now checked against Anthropic's signature, not just a hash someone copied into a Dockerfile.
* Good, because an operator can move the runner to a newer Claude Code with a value instead of waiting for a Ploeg release.
* Good, because the image shrinks by the binary, from 441 MB to 178 MB (measured with `docker image ls` on 2026-10-24).
* Bad, because a runner pod cannot start while `downloads.claude.ai` is unreachable. Accepted: it cannot reach `api.anthropic.com` either in most such outages, and the cache survives container restarts within the pod.
* Bad, because a `claude-environment` worker downloads Claude Code on every Run. Accepted for now, because one team's dispatches are infrequent. An init container in the worker pod template would remove it, and the worker and harness container split is changing that template, so it waits.
* Bad, because the change is breaking for an installer whose egress names hosts. It ships as `feat!` with a `BREAKING CHANGE` footer and an entry in [breaking-changes.yaml](../contracts/breaking-changes.yaml) ([ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)). There is no deprecation window, because the old form is the redistribution this record ends.
* Neutral: the version pin still has to be bumped by hand together with its sha256, from the release's `manifest.json`, as before.

### Confirmation

* `scripts/test-claude-code-install.sh` (in `mise run verify`) serves a fixture release from a throwaway key over local HTTP. It checks that the wrapper runs the pinned build, that a cached build runs with the server down, that the operator's `DISABLE_AUTOUPDATER` wins, and that an override version installs from the signed manifest. It checks refusals for a tampered binary, a foreign signature, a manifest changed after signing, a pin that disagrees with the manifest, a signer other than the pinned fingerprint, a path-like version and an unwritable directory. Removing any one of those checks from the installer fails the test.
* `scripts/license-check.sh` (in `mise run verify`) fails when `ops/claude-runner/Dockerfile` fetches Claude Code at build time or stops copying the installer. Run against the previous Dockerfile, it fails.
* `sh scripts/helm-golden.sh check` pins the init container, the uid 65532 install and the read-only mount in `ops/helm/ploeg/ci/golden/claude-runner.yaml`.
* `go test ./internal/licenses/` keeps the inventory's Claude Code entry `user-fetched` and out of every artifact's `shipped_in`.

## Pros and Cons of the Options

### The operator builds the image

* Good, because nothing is downloaded at run time.
* Bad, because every installer needs a build pipeline, a registry and an update routine just to start the runner, and an image built that way is still a redistribution if it is ever published. This record keeps it as the opt-in escape hatch of point 5.

### Install from Anthropic's signed apt repository at start

* Good, because apt verifies the repository signature itself.
* Bad, because `apt-get install` needs root, which neither the restricted runner pod nor the worker has. It would also give up the exact version pin, since the repository serves a channel.

### Keep shipping the binary and request an exception

* Good, because nothing changes for installers.
* Bad, because it needs Anthropic's written permission for free redistribution plus Legal Committee and Governing Board review, for a binary that has a supported install path anyway.

## Re-evaluation triggers

* Anthropic publishes Claude Code under a licence that allows redistribution, or in a package registry CNCF guidance accepts for images.
* Anthropic changes the manifest format, the signing key or the release bucket path.
* The worker pod template gains an init container slot for the harness image, which would remove the per-Run download.
* An installer reports that a runner failed to start because `downloads.claude.ai` was unreachable.

## More Information

* Evidence: the manifest, signature and key for 2.1.296 were fetched and verified on 2026-10-24. The signer is `31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`, and the linux-x64 checksum `24972e3b…d0de` equals the Dockerfile's previous pin. The image built from this change printed `2.1.296 (Claude Code)` on first start.
* How-to: [Run Claude Code in the claude-runner](../how-to/run-claude-code-in-the-claude-runner.md).
* Related: [ADR 0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md), [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md), [ADR 0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md), [ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md).
* 2026-10-24: proposed with the change.
* 2026-10-25: accepted by the owner.
