# Runner hook for self-hosted Claude environments

A Run can execute as a Claude Code session in a Team's self-hosted environment ([ADR-0084](../../docs/adrs/0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)). The runner belongs to the Team; Ploeg gives it no standing forge credential.

How a Run travels:

1. A worker with the `claude-environment` harness dispatches the session with `claude -p … --environment <ccpool_…>` and records it on `POST /api/v1/runs/{token}/claude-session`. Claude is told to commit, not push, and to end with the Run's outcome JSON.
2. `checkout` is the runner's checkout hook. The runner's own clone is anonymous and cannot reach a private repository, so the hook exchanges the session's token at `POST /api/v1/claude-sessions/forge-credential`, retrying until the worker has recorded the session, and clones the Run's repository at the session's ref into the checkout path. The token reaches git only through `GIT_ASKPASS`; the checkout's origin is the plain HTTPS remote. A reading Run gets no credential, so it cannot run against a private repository yet.
3. `claude-home/hooks/save-final-message` is a Claude Code Stop hook. It keeps the last final message in the checkout's git directory.
4. `post-session` is the runner's lifecycle hook. After the session ends it exchanges the session's token at `POST /api/v1/claude-sessions/forge-credential` for a push credential scoped to the Run, pushes the session's `HEAD` to the Run's Shift branch from a clean temporary repository so git config written by the session never sees the token, opens the pull request through the Forgejo API if none is open, and reports the end, the commit, the pull request and the final message on `POST /api/v1/claude-sessions/report`. A reading Run, a finished Run or a lapsed Lease gets no credential; the hook then pushes nothing and only reports.
5. The worker polls `GET /api/v1/runs/{token}/claude-session` until the report arrives, reads the outcome from the final message and continues as for any harness: it reads the pull request, verifies the pushed commit and scans it for leaked credentials.

## Runner settings

| Setting | Why |
| --- | --- |
| `--hooks-dir` pointing at a read-only copy of this directory | The session must not be able to change the hook. |
| `claude-home/` copied to the runner user's `~/.claude/` before the runner starts | The runner seeds every session from it, which installs the Stop hook. |
| `PLOEG_URL` in the runner's environment | Where the hooks exchange the session token. |
| `--capacity 1` | One session per container, so nothing the hook handles outlives its session. |
| `--release-idle-session-min 1` | Otherwise a finished session holds its slot for 15 minutes before the hook runs. |
| `--git-host-rewrite <ssh host>=<https host>` | When the forge's SSH and HTTPS hosts differ; the runner clones over HTTPS. |
| Never `--use-anthropic-git-proxy` | It serves github.com only. |

The Ploeg chart runs the runner with these settings when `claudeRunner.enabled` is true ([chart README](../helm/ploeg/README.md#ploeg-mcp-and-claude-runner)).

## On-demand runners

With `claudeRunner.mode: orchestrator` the chart runs `claude self-hosted-runner orchestrator` instead of a fleet, the only mode in a credential gateway namespace ([ADR-0197](../../docs/adrs/0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md) section 7). The orchestrator holds the environment secret and never runs a session. For each spawn request Anthropic sends it, [`spawn-runner`](spawn-runner), from `/opt/ploeg/claude-runner/orchestrator-hooks`, creates one runner Job from the chart's template: named after a hash of the order id, so a redelivered request finds its Job and starts no second runner, annotated with the session id, and carrying the request's single-use work order. It refuses a pre-warming request, which has no session, with exit 2. [`runner-from-work-order`](runner-from-work-order), at `/usr/local/bin/ploeg-claude-runner-from-work-order`, starts the runner: it moves the work order from the environment into a memory-backed file and runs `claude self-hosted-runner --environment-secret-file` with it, so no session inherits the order, and deletes the file once the runner answers `/healthz`, which it does only after it registered. The runner pod holds no Secret and no ServiceAccount token. [What this relies on in Anthropic's interface](../../docs/research/2026-11-18-claude-runner-orchestrator-interface.md).

In a credential gateway namespace the chart sets `PLOEG_CREDENTIAL_GATEWAY=on` and points `PLOEG_URL` at the gateway's session port, and both hooks work through the gateway with only the session token: `checkout` asks `GET /api/v1/claude-sessions/run` for the Run's repository and base branch, retrying while the routine's session is not recorded yet, and clones it from the gateway with the token as an `http.extraHeader` set through `GIT_CONFIG_*`, so the checkout's git config never holds it. `post-session` pushes `HEAD` to the Run's branch from a clean repository the same way, opens the pull request through the gateway's forge API (Forgejo only) and reports through the gateway to ploegd. The gateway attaches the Run's minted forge credential under the push fence; no forge token reaches the runner pod ([contract](../../docs/contracts/credential-gateway.md#session-grants)).

Each release publishes `ghcr.io/ploeg-hq/claude-runner:<version>` from [Dockerfile](Dockerfile): `git`, `curl`, `jq`, `checkout` and `post-session` in `/opt/ploeg/claude-runner/hooks` and `spawn-runner` in `/opt/ploeg/claude-runner/orchestrator-hooks` (root-owned, so a session cannot change them) and `claude-home/` as the `runner` user's `~/.claude`. It carries no Claude Code ([ADR-0141](../../docs/adrs/0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md)). [`install-claude-code`](install-claude-code), at `/usr/local/bin/ploeg-install-claude-code`, downloads the version pinned in [`claude-code.env`](claude-code.env) from Anthropic and verifies it against the GPG-signed release manifest. [`claude`](claude), at `/usr/local/bin/claude`, runs the installer and then the real binary. The chart installs into a read-only volume before the runner starts; everywhere else the wrapper installs on first use. Bump the pin's version and sha256 together, from the release's `manifest.json`. [Run Claude Code in the claude-runner](../../docs/how-to/run-claude-code-in-the-claude-runner.md) covers egress, versions and your own image. Run the runner with `--hooks-dir /opt/ploeg/claude-runner/hooks`. The same image serves as the dispatching worker's `harness.image`, and the chart uses it, by the release's digest, for a `claude-environment` team or Role that names no image. ploegd needs `PLOEG_CLAUDE_ENVIRONMENTS=true`, per-run push credentials (`PLOEG_FORGEJO_BOT_PASSWORD`) and `PLOEG_FORGEJO_URL`; see [the configuration reference](../../docs/reference/configuration.md).

The worker needs `claude` and `git` in its agent image, and a Team member's login. `claude setup-token` does not grant session control, and Anthropic caps the login that does at 30 days, so a person renews it. Two ways to give it to the worker:

- **`harness.claudeEnvironment.loginVolume`** (tested route): a PersistentVolumeClaim holding a Claude config directory. A person signs in once from a pod that mounts it, running as the worker's user, and the CLI refreshes the login in place on every dispatch:

  ```sh
  kubectl -n ploeg run claude-login --rm -it --restart=Never \
    --image=ghcr.io/ploeg-hq/claude-runner:<version> \
    --overrides='{"spec":{"securityContext":{"runAsUser":65532,"fsGroup":1000},"containers":[{"name":"claude-login","image":"ghcr.io/ploeg-hq/claude-runner:<version>","stdin":true,"tty":true,"args":["auth","login"],"env":[{"name":"CLAUDE_CONFIG_DIR","value":"/login"}],"volumeMounts":[{"name":"login","mountPath":"/login"}]}],"volumes":[{"name":"login","persistentVolumeClaim":{"claimName":"<claim>"}}]}}'
  ```

  Pick the organisation that owns the environment when the browser asks.
- **`harness.claudeEnvironment.loginSecret`**: a Secret with `refreshToken` and `scopes` that each Run exchanges with `claude auth login`. On 2026-10-11 that exchange did not finish without a browser on a workstation, so treat this route as unproven.

Run one `claude-environment` worker at a time per login volume (`maxReplicaCount: 1`): the CLI rewrites the login file when it refreshes.

Not built yet: the plan-paid cost status (these Runs still mint and settle an unused gateway key), and merge requests on GitLab without the credential gateway. Through the gateway, `post-session` opens the Run branch's merge request on GitLab and its pull request on Forgejo.
