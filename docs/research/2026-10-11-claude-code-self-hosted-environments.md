# Claude Code self-hosted environments as a place to run a Run

Spike, 2026-10-11. Question: can Ploeg run a `claude-code` Run on a Team's Claude plan instead of on API spend through LiteLLM, without breaking Anthropic's terms or Ploeg's own boundaries? Verdict and decision: [ADR-0084](../adrs/0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md).

## What the product is

A self-hosted environment (public beta, Claude Team and Enterprise plans, off by default) runs Claude Code cloud sessions on machines the organisation operates. Anthropic keeps the control plane: the queue, the session stream, the transcript and model inference. A runner (`claude self-hosted-runner`, part of the normal `claude` binary from v2.1.224) polls the queue over outbound HTTPS, claims a session, clones the repository and starts a Claude Code child process. Nothing connects into the network. Sessions consume the organisation's Claude Code usage, the same way Anthropic-hosted cloud sessions do.

Sources: the Claude Code docs pages *Self-hosted environments*, *quickstart*, *Deploy to production*, *Customize sessions*, *Test end to end*, *Verify session identity* and *Legal and compliance*, read on 2026-10-11.

## The terms that bound the design

From *Legal and compliance*:

* OAuth sign-in from Free, Pro and Max plans is for ordinary use of Claude Code and Anthropic's own apps. Developers building products, including on the Agent SDK, use API keys.
* Third parties may not offer Claude.ai login in their own application, route requests through Free, Pro or Max credentials on behalf of their users, or "collect, store, or intermediate" Claude.ai credentials or session tokens.
* A product that hosts Claude Code must run the unmodified binary and may not pay for, resell or intermediate usage: each end user authenticates with their own key, plan or cloud-provider credential.
* An end user signing in to the unmodified binary with their own subscription is allowed, including where a platform hosts Claude Code.
* Advertised plan limits assume ordinary, individual usage.

Consequence for Ploeg: the Agent SDK on a plan login is out; a plan login stored in Ploeg's secrets and injected into worker pods is close to "store or intermediate"; a session that a Team member creates in their own organisation's environment, on a runner that organisation operates, is the supported path.

## What was run

One runner in a Docker container on a workstation, Claude Code 2.1.296, environment secret read from OpenBao into the container's environment (`SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET`), never written to disk.

| Step | Result |
| --- | --- |
| Register the runner | `Registered: runner_id=…` within a second |
| Dispatch from a CLI logged in to a personal Max organisation | Refused: `self-hosted runners are disabled by your organization's policy` |
| Dispatch with `claude -p "<prompt>" --environment <ccpool_…> --ref development --output-format json` from a Team-organisation login | Accepted; returns a session id and URL, exits without waiting |
| Clone a Forgejo repository (the checkout's `origin` was `ssh://git@forgejo-ssh…`) | The control plane accepted the non-GitHub remote; the runner rewrote it to `https://forgejo-ssh…`, which does not serve HTTPS, and the clone hung |
| Same with `--git-host-rewrite forgejo-ssh.webgrip.dev=forgejo.webgrip.dev` | Cloned at the requested ref onto a `claude/…` branch |
| Read the reply with a Stop hook writing `last_assistant_message` to a mounted directory | The reply arrived as a file keyed by session id; a prompt asking for one JSON object returned exactly that |
| Commit and push without credentials | Commit succeeded; push failed cleanly with `could not read Username` |
| A finished turn | The session keeps its runner slot for 15 minutes unless `--release-idle-session-min` is set; with `1` it released a minute after the turn |
| A session whose runner was replaced | Requeued and completed on the new runner |

Not run: a push with a minted credential, a `post-session` hook, the session JWT exchanged with a service, a runner on Kubernetes, more than one concurrent session per account.

## Findings that shape the decision

1. **Anthropic dispatches, not Ploeg.** Ploeg can create a session and read its result, but the queue, the lease on a runner and the transcript live at Anthropic.
2. **Dispatch needs a claude.ai login of a member of the Team's organisation.** API keys are refused for `--environment` dispatch. The session belongs to that member, and the runner locks itself to that account for its lifetime (`locked_account`).
3. **There is no metered spend to settle.** Usage counts against the plan's limits; LiteLLM never sees the traffic.
4. **Every session carries an Anthropic-signed JWT** (`CLAUDE_CODE_SESSION_ACCESS_TOKEN`, `iss` `ccr`, `aud` containing the environment id, `ccr:session_id`, `act.sub` the creator, ES256 against a public JWKS). Any process in the session can read it, so it proves the session, not the process.
5. **Forgejo works for checkout** with a host rewrite. The Anthropic git proxy is github.com only, so a Forgejo deployment always supplies its own push credential.
6. **Git credentials are the deployment's job.** The docs recommend minting a short-lived, least-scoped token per session from a wrapper script keyed on the session creator, with one session per container.
