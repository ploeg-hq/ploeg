# Runner hook for self-hosted Claude environments

A Run can execute as a Claude Code session in a Team's self-hosted environment ([ADR-0084](../../docs/adrs/0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)). The runner belongs to the Team; Ploeg gives it no standing forge credential.

How a Run travels:

1. A worker with the `claude-environment` harness dispatches the session with `claude -p … --environment <ccpool_…>` and records it on `POST /api/v1/runs/{token}/claude-session`. Claude is told to commit, not push, and to end with the Run's outcome JSON.
2. `claude-home/hooks/save-final-message` is a Claude Code Stop hook. It keeps the last final message in the checkout's git directory.
3. `post-session` is the runner's lifecycle hook. After the session ends it exchanges the session's token at `POST /api/v1/claude-sessions/forge-credential` for a push credential scoped to the Run, pushes the session's `HEAD` to the Run's Shift branch from a clean temporary repository so git config written by the session never sees the token, opens the pull request through the Forgejo API if none is open, and reports the end, the commit, the pull request and the final message on `POST /api/v1/claude-sessions/report`. A reading Run, a finished Run or a lapsed Lease gets no credential; the hook then pushes nothing and only reports.
4. The worker polls `GET /api/v1/runs/{token}/claude-session` until the report arrives, reads the outcome from the final message and continues as for any harness: it reads the pull request, verifies the pushed commit and scans it for leaked credentials.

## Runner settings

| Setting | Why |
| --- | --- |
| `--hooks-dir` pointing at a read-only copy of this directory | The session must not be able to change the hook. |
| `claude-home/` copied to the runner user's `~/.claude/` before the runner starts | The runner seeds every session from it, which installs the Stop hook. |
| `PLOEG_URL` in the runner's environment | Where the hook exchanges the session token. |
| `--capacity 1` | One session per container, so nothing the hook handles outlives its session. |
| `--release-idle-session-min 1` | Otherwise a finished session holds its slot for 15 minutes before the hook runs. |
| `--git-host-rewrite <ssh host>=<https host>` | When the forge's SSH and HTTPS hosts differ; the runner clones over HTTPS. |
| Never `--use-anthropic-git-proxy` | It serves github.com only. |

Each release publishes `ghcr.io/ploeg-hq/claude-runner:<version>` from [Dockerfile](Dockerfile): Claude Code pinned by version and checksum, `git`, `curl`, `jq`, `post-session` in `/opt/ploeg/claude-runner/hooks` (root-owned, so a session cannot change it) and `claude-home/` as the `runner` user's `~/.claude`. Run the runner with `--hooks-dir /opt/ploeg/claude-runner/hooks`. The same image serves as the dispatching worker's `harness.image`. ploegd needs `PLOEG_CLAUDE_ENVIRONMENTS=true`, per-run push credentials (`PLOEG_FORGEJO_BOT_PASSWORD`) and `PLOEG_FORGEJO_URL`; see [the configuration reference](../../docs/reference/configuration.md).

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

Not built yet: the plan-paid cost status (these Runs still mint and settle an unused gateway key), and pull requests on GitLab.
