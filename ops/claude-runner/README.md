# Runner hook for self-hosted Claude environments

A Run can execute as a Claude Code session in a Team's self-hosted environment ([ADR-0084](../../docs/adrs/0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)). The runner belongs to the Team; Ploeg gives it no standing forge credential.

`post-session` is the runner's lifecycle hook. After the session ends it exchanges the session's token at `POST /api/v1/claude-sessions/forge-credential` for a push credential scoped to the Run, and pushes the session's `HEAD` to the Run's Shift branch from a clean temporary repository, so git config written by the session never sees the token. A reading Run, a finished Run or a lapsed Lease gets no credential, and the hook pushes nothing.

## Runner settings

| Setting | Why |
| --- | --- |
| `--hooks-dir` pointing at a read-only copy of this directory | The session must not be able to change the hook. |
| `PLOEG_URL` in the runner's environment | Where the hook exchanges the session token. |
| `--capacity 1` | One session per container, so nothing the hook handles outlives its session. |
| `--release-idle-session-min 1` | Otherwise a finished session holds its slot for 15 minutes before the hook runs. |
| `--git-host-rewrite <ssh host>=<https host>` | When the forge's SSH and HTTPS hosts differ; the runner clones over HTTPS. |
| Never `--use-anthropic-git-proxy` | It serves github.com only. |

The image needs `git`, `curl` and `jq`. ploegd needs `PLOEG_CLAUDE_ENVIRONMENTS=true`, per-run push credentials (`PLOEG_FORGEJO_BOT_PASSWORD`) and `PLOEG_FORGEJO_URL`; see [the configuration reference](../../docs/reference/configuration.md).

Not built yet: the worker dispatching the session and recording it on `POST /api/v1/runs/{token}/claude-session`, and the outcome path.
