---
status: accepted
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A claude-code Run can execute in its Team's self-hosted Claude environment, paid by the Team's plan

## Context and Problem Statement

Every Run today calls its model through a LiteLLM key Ploeg mints and settles ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)), so a Team pays API prices. A Team on a Claude Team or Enterprise plan already pays for Claude Code usage it does not use at night. Anthropic forbids reaching that plan through the Agent SDK or through plan credentials a product stores or intermediates, but it offers self-hosted environments: Claude Code cloud sessions that a member of the organisation creates and that run on the organisation's own runners. The 2026-10-11 spike ran such a session against a Forgejo repository and read its result back. How does a `claude-code` Run use a Team's plan without Ploeg holding a Claude login, and what happens to Ploeg's authorization, push rights and outcome contracts?

## Decision Drivers

* Anthropic's terms: the binary runs unmodified, each organisation pays for its own usage, and no product stores or intermediates Claude.ai credentials or session tokens.
* Ploeg still admits, authorizes and records every Run ([ADR-0024](0024-operator-work-uses-one-execution-authority.md)), and its spend reports never invent a number.
* Push rights stay minted per Run and die with the Lease ([ADR-0013](0013-push-rights-are-minted-per-run.md)).
* A reading Run still returns its outcome through the drop box shape ([ADR-0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md)).
* It works with Forgejo, which the Anthropic git proxy does not serve.

## Considered Options

* A Run executes as a cloud session in the Team's self-hosted environment, dispatched by Ploeg with the Team's own dispatch login
* Sign the existing `claude-code` adapter in to a plan inside Ploeg's worker pods
* Call Claude through the Agent SDK with a plan login
* Keep every Run on API spend through LiteLLM

## Decision Outcome

Chosen option: "A Run executes as a cloud session in the Team's self-hosted environment", because it is the path Anthropic supports for running Claude Code on infrastructure an organisation controls, it keeps Claude logins out of Ploeg's custody beyond one dispatch credential the Team provides for itself, and it leaves admission, Leases, push rights and the outcome contract with Ploeg.

**An execution target, not a new harness.** A Team's `claude-code` Role can name the execution target `claude-environment` with an environment id (`ccpool_…`). The harness is still Claude Code; what changes is where it runs and who pays. The default stays the worker pod and LiteLLM.

**Ploeg dispatches; Anthropic queues; the Team's runner executes.** For such a Run the worker prepares the TaskSpec as usual, then creates the session with `claude -p <prompt> --environment <id> --ref <base> --output-format json` from a clone at the Run's base commit, records the returned session id on the Run, and waits for the outcome or the Run's deadline. It starts no Claude process and mints no LiteLLM key.

**The dispatch login is the Team's, one per Team, never pooled.** Dispatch needs a claude.ai login of a member of the Team's organisation; API keys are refused. The Team creates it through Anthropic's own sign-in (`claude auth login`) and stores its refresh token as its own secret; Anthropic caps the session-control scope at 30 days, so a person renews it; Ploeg reads it only to dispatch and never offers a Claude sign-in. One Team's login never dispatches another Team's Runs, and a hosted deployment never supplies a login of its own. Sessions belong to that member and count against that seat's limits.

**Push rights are exchanged for the session's identity.** The runner gets no standing forge credential. A `post-session` hook on the runner presents the session's `CLAUDE_CODE_SESSION_ACCESS_TOKEN` to Ploeg; Ploeg verifies it against Anthropic's JWKS (`iss` `ccr`, `aud` the Run's environment id, `ccr:role` `session_worker`, `ccr:session_id` the Run's recorded session) and returns the Run's minted push token, which the hook uses to push the `claude/…` branch and which dies with the Lease. A reading Run gets no push token.

**The outcome travels the same way.** The runner's Stop hook writes the final reply in the drop box shape, and the `post-session` hook posts it to Ploeg with the same session token. Ploeg validates it exactly as it validates a drop box file from a worker pod.

**The Budget is time and turns, and spend is reported as plan-paid.** A Run in an environment has no money to authorize. Admission holds no Shift-pool money for it; the Run carries a deadline and the runner's `--kill-session-after-min` and `--release-idle-session-min` bound it. Its settlement records cost status `plan` and no amount, so reports show "paid by the Claude plan", never `0.00` as if it were measured.

**Forgejo needs a host rewrite.** The runner starts with `--git-host-rewrite <ssh host>=<https host>` when the forge's SSH and HTTPS hosts differ, and never with `--use-anthropic-git-proxy`.

### Consequences

* Good, because a Team's idle plan usage can build its Work Items, and an API Budget is spent only when the Team chooses it.
* Good, because Ploeg holds no plan login beyond the Team's own dispatch credential, and no forge credential lives on the runner.
* Bad, because prompts, tool results and transcripts go to Anthropic and stay there; only checkouts and build output stay on the Team's machines.
* Bad, because Ploeg cannot stop a session mid-turn; it relies on the runner's kill and idle limits and on refusing the push exchange after the Lease ends.
* Bad, because the session token is readable by anything in the session, so the agent itself can perform the push exchange. The token it gets is the same per-Run, per-branch token a worker pod already holds, so the blast radius equals ADR-0013's.
* Bad, because spend for these Runs is not measured. A Team that needs a money ceiling keeps the Role on LiteLLM.
* Bad, because it depends on a beta product whose flags and limits may change.

### Confirmation

Nothing implements this record yet. The implementation lands with:

* `go test ./pkg/worker/` proves that a Role with the `claude-environment` target dispatches with the Team's login and the Run's base ref, records the session id, mints no LiteLLM key and holds no Shift-pool money, against an `httptest` fake of the dispatch result.
* `go test ./pkg/httpapi/` proves the push-token exchange refuses a token with the wrong issuer, audience, role, session id, an expired token, a reading Run, and a Run whose Lease has ended.
* The spike's runner recipe becomes an end-to-end test that dispatches to a test environment and checks the outcome, run on demand like the live-provider tests.

## Pros and Cons of the Options

### Sign the existing `claude-code` adapter in to a plan inside Ploeg's worker pods

* Good, because it is a small change: the adapter stops setting `ANTHROPIC_API_KEY` and the CLI falls back to its login.
* Bad, because Ploeg would store a plan token and inject it into every Run, which is the storing and intermediating Anthropic forbids, and hosted Teams would be routed through credentials Ploeg holds.

### Call Claude through the Agent SDK with a plan login

* Good, because Ploeg would control the agent loop directly.
* Bad, because Anthropic's terms require API keys for the Agent SDK in products.

### Keep every Run on API spend through LiteLLM

* Good, because spend stays measured and capped per Run, and nothing new is built.
* Bad, because a Team with a Claude plan pays twice for the same capacity.

## Re-evaluation triggers

* Self-hosted environments leave beta, or Anthropic changes the dispatch, runner or session-token contract.
* Anthropic's terms change on plan logins, hosted Claude Code or session tokens.
* Anthropic exposes a session's token usage to the organisation through an API, so these Runs can report measured usage.
* Anthropic offers dispatch by an organisation's service identity, so sessions no longer belong to one member's seat.
* A Team asks for this target from a Claude Pro or Max plan, which self-hosted environments do not serve.

## More Information

* 2026-10-11 — The owner asked whether Ploeg can run Claude Code subagents on self-hosted runners that use a subscription; the spike's evidence and the terms it relies on are in [docs/research/2026-10-11-claude-code-self-hosted-environments.md](../research/2026-10-11-claude-code-self-hosted-environments.md).
* 2026-10-11 — The owner accepted this record and chose: the push-token exchange is built before any Run pushes from a runner; sessions are dispatched with the owner's own Team seat while the work is the owner's own; the runner stays on a workstation until the exchange exists, then moves to the homelab cluster.
* 2026-10-11 — Correction while building the worker side: `claude setup-token` mints an inference-only token, which cannot create sessions. Dispatch needs a `claude auth login` refresh token, valid for at most 30 days.
* 2026-10-13 — Correction before the first homelab Run: the runner clones anonymously, so a private repository fails at setup. A `checkout` hook now exchanges the session token the same way and clones with the Run's credential; the token never lands in the checkout. A reading Run still gets no credential, so it cannot use a private repository yet.
* Related: [ADR-0006](0006-ahp-is-the-wrong-layer.md) (AHP stays above Ploeg; this target does not use it), [ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md).
