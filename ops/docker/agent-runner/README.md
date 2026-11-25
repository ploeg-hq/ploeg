# Agent image

Each release publishes `ghcr.io/ploeg-hq/agent-runner:<version>` for `linux/amd64` from [Dockerfile](Dockerfile), signed and CVE-gated like every Ploeg image ([ADR-0200](../../../docs/adrs/0200-ploeg-publishes-an-agent-image-with-the-harnesses-it-runs-from-its-own-release.md)). It is the image a Team's `harness.image` can name for the `openhands` harness and the `acp` profiles `openhands`, `opencode`, `qwen-code` and `goose`. The chart does not default to it yet; set `executor.harness.image` to `ghcr.io/ploeg-hq/agent-runner:<version>@<digest>`, with the digest from the release's `signed-artifacts.txt`.

## What it carries

| Tool | Version | Installed from |
| --- | --- | --- |
| OpenHands CLI (`openhands`) | 1.16.0 | PyPI with uv, constrained by [openhands-1.16.0.constraints.txt](openhands-1.16.0.constraints.txt) and [openhands-overrides.txt](openhands-overrides.txt) |
| OpenCode (`opencode`) | 1.19.6 | npm, [tools/opencode](tools/opencode/package-lock.json) |
| Qwen Code (`qwen`) | 0.28.0 | npm, [tools/qwen-code](tools/qwen-code/package-lock.json) |
| goose (`goose`) | 1.60.1 | the GitHub release's musl archive, checked against a pinned sha256 |
| moon (`moon`, `moonx`) | 2.6.5 | npm, [tools/moon](tools/moon/package-lock.json) |
| openspec (`openspec`) | 2.1.0 | npm, [tools/openspec](tools/openspec/package-lock.json) |
| Python, PyYAML | 3.12, 6.0.3 | Wolfi `python-3.12`, uv |
| Node.js | 24 | Wolfi `nodejs-24` |
| git, bash | Wolfi | apk |
| uv | 0.13.9 | `ghcr.io/astral-sh/uv` |

Every version above was the newest stable release on 2026-11-18.

The image is built on the digest-pinned `cgr.dev/chainguard/wolfi-base` the other Ploeg images use, so its C library is glibc. Like those images, it pins no apk version: `apk upgrade` and `apk add` take the current Wolfi package at every build, because Wolfi withdraws old versions ([ADR-0144](../../../docs/adrs/0144-every-published-image-is-built-on-a-base-that-scans-clean-and-pulls-without-credentials.md)). The daily `image-security` run rebuilds and scans the image, so a new finding in a Wolfi package turns it red, and the BuildKit SBOM of each release records what shipped. npm is used only to install the tools during the build and is not in the image. Wolfi's npm 12 runs no package's install script unless it is allowed, so the build runs `opencode-ai`'s `postinstall.mjs` itself, which puts the platform's `opencode` binary in place. Qwen Code's optional `@qwen-code/audio-capture` install script, which only builds a microphone backend, does not run.

No Claude Code ([ADR-0141](../../../docs/adrs/0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md)) and no language toolchain: a Run gets its toolchains mounted ([give Runs a toolchain and checks](../../../docs/how-to/give-runs-a-toolchain-and-checks.md)). The image has no `USER`; the chart runs the harness container as a non-root user, and the build checks that OpenHands runs as uid 1000.

## How Ploeg runs it

The worker never uses the image's entrypoint. The harness container runs the launcher that the worker's init container copies in, and the launcher starts the harness binary from `PATH` ([managed workers](../../../docs/ops/managed-workers.md)). Everything below the entrypoint matters only when a person runs the image on its own.

## Running it on its own

`docker-entrypoint.sh` runs `openhands --override-with-envs` with the arguments it is given, against an OpenAI-compatible gateway:

| Variable | Default | Meaning |
| --- | --- | --- |
| `LLM_BASE_URL` | none, required | The gateway, for example `https://litellm.example/v1`. |
| `LLM_MODEL` | `litellm_proxy/deepseek-chat` | The model OpenHands asks for. |
| `LLM_API_KEY` | none | A key the caller owns. When set, nothing is minted. |
| `LITELLM_MASTER_KEY` | none | Without `LLM_API_KEY`, `litellm-key` mints a per-Run key with it and revokes the key on exit. |
| `LITELLM_ADMIN_URL` | `LLM_BASE_URL` without `/v1` | The LiteLLM admin API. |
| `LITELLM_KEY_BUDGET`, `LITELLM_KEY_DURATION`, `LITELLM_KEY_MODELS` | `5`, `2h`, the model | The minted key's budget in USD, lifetime and model scope. |
| `LLM_TRACE_ID` | generated | Sent as `x-litellm-trace-id` on every call and used as the minted key's alias. |
| `OPENHANDS_SKILLS_PROFILE`, `OPENHANDS_SKILLS_REPO`, `OPENHANDS_SKILLS_REF` | none, none, `main` | Comma-separated skills cloned from `skills/<name>` of the repository into OpenHands' installed skills. Nothing is added without a repository. |

`litellm-key` reads every input from the environment, so no key appears in a process's arguments. The entrypoint runs OpenHands in the background and forwards `TERM` and `INT` to it, so the agent stops promptly and the key is still revoked when it exits; a `SIGKILL` skips the revoke and the key's lifetime ends it.

## What the build checks

The Dockerfile fails rather than publish an image that would fail a Run:

- OpenHands must start and be executable as uid 1000, and its `lmnr` must accept the `rollout_entrypoint` argument the SDK's tracing passes.
- The OpenHands CLI loads public skills from GitHub at every start and has no setting to stop it. The build changes `load_public_skills=True` to `False` in its `agent_store.py` and fails if that line moves, so a Run without GitHub egress does not wait for the clone to time out. `LITELLM_LOCAL_MODEL_COST_MAP=True` stops LiteLLM fetching its cost map at start for the same reason.
- OpenHands sends a model its earlier reasoning only when the model is on a fixed list, and SDK 1.21.0 lists no Qwen. Qwen 3.6 and later keep reasoning across tool calls (`preserve_thinking`), so without it the model reads empty thoughts and works out the same things again. The build adds `qwen3.6`, `qwen3.7` and `qwen3.8` to the list, as [OpenHands pull request 5435](https://github.com/OpenHands/software-agent-sdk/pull/5435) does, and fails if the list moves, if Qwen 3.8's reasoning is not serialized, or if an older Qwen's is. Drop the step when the pinned release carries it.
- `openhands --version` takes minutes to import at a worker's CPU limit, so the build writes it to `/etc/openhands-version` and the entrypoint reads that.
- The image Qwen Code was proven in had no `node-pty`, because its prebuilds crash on musl. The build still removes them, so Qwen Code runs its shell without a PTY as before, and fails if either still resolves. `qwen` and `opencode` must report their pinned versions, and `goose` its pinned release.
- `litellm-key mint` must fail when nothing is configured, so it can never hand an agent an empty key.

## Keeping it current

Nothing in the image should wait for a person to notice a new release:

- **Base image, uv, PyYAML, OpenCode, Qwen Code, moon and openspec.** Renovate proposes them: the `FROM` digests through its Dockerfile manager, `PYYAML_VERSION` through the `# renovate:` line above it, and each `tools/<tool>/package.json` with its lockfile through the npm manager. Python and Node.js follow Wolfi's `python-3.12` and `nodejs-24` packages on every build.
- **Overrides.** Renovate also proposes every line of [openhands-overrides.txt](openhands-overrides.txt). The file raises OpenHands dependencies above the CLI release's lockfile: each package that had a Critical or High advisory, at its newest release OpenHands still imports, plus the packages those need newer or bring in. uv installs each at that version whatever OpenHands declares (ADR-0200). Qwen Code's `package.json` raises `sharp` the same way through npm `overrides`. Drop an override once OpenHands' own lockfile reaches its version.
- **OpenHands and goose.** Renovate cannot regenerate the constraints file or the goose checksums, so the [Agent image pins workflow](../../../.github/workflows/agent-image-pins.yml) runs [update-agent-runner-pins.sh](../../../scripts/update-agent-runner-pins.sh) every day. When PyPI has a newer `openhands` or `aaif-goose/goose` a newer release, it regenerates the constraints with [openhands-constraints.sh](openhands-constraints.sh), or downloads both goose archives and writes their sha256, and opens a pull request. It opens that pull request with a token of the release consumer GitHub App (`RELEASE_CONSUMER_GITHUB_APP_ID`, `RELEASE_CONSUMER_GITHUB_APP_PRIVATE_KEY`, the App `release-consumers.yml` uses), limited to this repository with contents and pull request write, because a pull request opened with the workflow's own token would start no CI. The App must be installed on `ploeg-hq/ploeg`; without the secrets the workflow does nothing.

By hand, the same steps are `bash scripts/update-agent-runner-pins.sh` (it needs `uv`, `jq` and `sha256sum`). To see a Python finding before CI does, resolve the set with `uv pip compile` (`--python-platform x86_64-manylinux_2_28`, `-c` the constraints, `--override` the overrides) into a `requirements.txt` and run `grype dir:` on its directory. Before merging a harness version or an override change, run `mise run harness-conformance` inside the image ([managed workers](../../../docs/ops/managed-workers.md)).

## Building it locally

```sh
docker buildx build --file ops/docker/agent-runner/Dockerfile --platform linux/amd64 --tag agent-runner:dev .
```

Run it from the repository root: the Dockerfile copies `LICENSE` and `NOTICE` from there.
