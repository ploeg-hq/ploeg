---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-24
verified_by: "Built ops/claude-runner/Dockerfile and ran it as the runner (uid 1000, read-only /opt/claude-code, no network), as a worker (filtered environment, scratch HOME) and as the sign-in pod (uid 65532); ran scripts/test-claude-code-install.sh; rendered ops/helm/ploeg with ci/claude-runner-values.yaml"
---

# Run Claude Code in the claude-runner

`ghcr.io/ploeg-hq/claude-runner` carries no Claude Code. It carries an installer and a pinned version. The cluster downloads Anthropic's signed release when a pod needs it ([ADR-0141](../adrs/0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md)). Use this page when you upgrade from a release that shipped the binary, when the runner or a `claude-environment` worker fails with a `ploeg-install-claude-code:` message, when you want a different Claude Code version, or when your cluster can't reach Anthropic's download host.

## What happens at start

| Pod | Where Claude Code comes from | Download |
| --- | --- | --- |
| claude-runner Deployment (chart) | Init container `claude-code` installs into an emptyDir as uid 65532; the runner mounts it read-only at `/opt/claude-code` | Once per pod |
| `claude-environment` worker | The image's `claude` wrapper installs into the Run's cache directory on first use | Once per Run, about 257 MB |
| Sign-in pod (`claude auth login` on a login volume) | The same wrapper, into `/tmp` | Once per pod |

Every install verifies three things before the binary runs:

1. `manifest.json` is signed by Anthropic's release key, fingerprint `31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`.
2. The binary matches the sha256 the signed manifest lists for the platform.
3. The pin in `/opt/ploeg/claude-runner/claude-code.env` (or `claudeRunner.claudeCode.sha256`) agrees with the manifest.

The wrapper also sets `DISABLE_AUTOUPDATER=1` unless you set it yourself, so the pinned version is the one that runs.

## Upgrade from a release that shipped Claude Code

1. Let the runner pods and every `claude-environment` team's workers reach `downloads.claude.ai` on 443. The chart's own NetworkPolicies already allow the internet on 443 for both, so nothing changes there. If your CNI restricts egress by hostname, add the host to the policy that already lists `api.anthropic.com`. With Cilium:

   ```yaml
   - toFQDNs:
       - matchName: api.anthropic.com
       - matchName: downloads.claude.ai
     toPorts:
       - ports:
           - port: "443"
             protocol: TCP
   ```

2. Upgrade the chart. Keep `claudeRunner.image` as it is, including a pull-through proxy repository. The new image is smaller and holds no Claude Code.
3. Check it:

   ```sh
   kubectl -n ploeg logs deploy/claude-runner -c claude-code
   kubectl -n ploeg exec deploy/claude-runner -c runner -- claude --version
   ```

   The first command prints `installed /opt/claude-code/<version>/claude`, and the second prints the version.

## Run another Claude Code version

For the runner, set the version and, ideally, its linux-x64 checksum from that release's `manifest.json`:

```yaml
claudeRunner:
  claudeCode:
    version: 2.1.300
    sha256: <platforms.linux-x64.checksum from https://downloads.claude.ai/claude-code-releases/2.1.300/manifest.json>
```

Without `sha256`, the signed manifest alone decides. A `claude-environment` worker can't take these values, because the worker passes no environment through to the harness, so it uses the image's pin. To change both, build your own image (below) or wait for the Ploeg release that bumps the pin.

## Use your own image

Build your own image when the cluster can't reach `downloads.claude.ai`, or when a worker shouldn't download on every Run:

```dockerfile
FROM ghcr.io/ploeg-hq/claude-runner:<version>@<digest>
USER root
RUN PLOEG_CLAUDE_CODE_DIR=/opt/claude-code ploeg-install-claude-code
USER runner
```

Then point the chart at it, and skip the init container that would hide the baked binary:

```yaml
claudeRunner:
  image:
    repository: registry.example.com/team/claude-runner
    tag: <tag>
  claudeCode:
    install: false
executor:
  teams:
    - name: <team>
      harness:
        name: claude-environment
        image: registry.example.com/team/claude-runner:<tag>
```

The image is Wolfi, not Debian, since [ADR 0144](../adrs/0144-every-published-image-is-built-on-a-base-that-scans-clean-and-pulls-without-credentials.md): as root, add packages with `apk add --no-cache <package>`, not `apt-get`.

That image contains Anthropic's binary under Anthropic's terms. Keep it in a private registry and don't publish it.

To download from a mirror instead, set `PLOEG_CLAUDE_CODE_RELEASES_URL` and `PLOEG_CLAUDE_CODE_KEY_URL` in `claudeRunner.env`; the chart hands every `PLOEG_CLAUDE_CODE_*` entry there to the init container too. The mirror must serve the same paths as `https://downloads.claude.ai/claude-code-releases/<version>/` and `/keys/claude-code.asc`. Only the runner reads these variables; workers need your own image.

## Symptom, cause, fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| Init container `claude-code` fails with `download of https://downloads.claude.ai/... failed` | Egress to `downloads.claude.ai:443` is blocked | Allow it (step 1 above), or use your own image |
| `does not carry a good signature from the release signing key` | The manifest or signature didn't come from Anthropic, or a proxy rewrote it | Check the mirror or proxy; never override the fingerprint to make it pass |
| `the pinned sha256 ... disagrees with the signed manifest` | `claudeRunner.claudeCode.sha256` belongs to another version or platform | Copy `platforms.linux-x64.checksum` from that version's manifest |
| A `claude-environment` Run fails at dispatch with `ploeg-install-claude-code:` in its error | The worker can't reach `downloads.claude.ai` | Allow egress for that team's workers, or use your own image as their `harness.image` |
| `none of ... is writable` | The pod has no writable directory for the download | Mount one and set `PLOEG_CLAUDE_CODE_DIR`, or use your own image |
