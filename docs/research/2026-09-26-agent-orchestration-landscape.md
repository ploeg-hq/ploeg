# The 20k-star agent-orchestration landscape × Ploeg — fit dossier

> Surveyed 2026-09-26 via seven parallel research agents (tracker-to-PR rivals,
> board control planes, the parallel-agent workbench layer, runtimes and
> sandboxes, coding harnesses, spec/skill methods and personal-agent platforms,
> and a read-only seam map of this repository), most working from shallow
> clones of each project's default branch rather than READMEs. Star counts were
> read from the GitHub API on the survey date. The GitHub API rate-limited
> several agents part-way; where that happened, contributor figures come from
> `git shortlog` over the clone and are marked as such. This file is the full
> evidence trail. It records findings and proposed changes; it changes no
> decision by itself.

**Verdict: nothing in this landscape removes Ploeg's reason to exist, but two
of the four differentiators in
[ADR-0009](../adrs/0009-paperclip-mine-for-design-never-integrate.md) are no
longer exclusive, and the niche has narrowed to a sharper claim.** No project
among ~80 surveyed combines (1) tracker *and* forge neutrality that reaches a
self-hosted Vikunja/Forgejo stack, (2) webhook-triggered, scale-to-zero
Kubernetes Jobs per Run, (3) a durable Postgres lease with TTL, and (4) per-Run
credentials minted at a proxy the agent cannot bypass, authorized then settled.
But Postgres leases now exist elsewhere (yc-software/qm, Multica, the
agent-orchestrator cloud edition), a Go + Postgres + Forgejo-aware twin exists
(Multica), and a proxy-enforced per-run spend cap exists on GitHub (gh-aw). What
remains Ploeg's alone is the *combination* on a self-hosted stack, and the
authorize-then-settle spend model. The closest challenger found is
Databricks' Omnigent (§5), which already runs sessions as Kubernetes Jobs behind
a credential proxy with spend policy, but has no tracker, no forge abstraction
and no work lease. The layers above and below Ploeg have
meanwhile standardized in ways that make it cheaper to be thin: ACP at the
harness seam, `agents.x-k8s.io` v1beta1 at the runtime seam, Agent Skills and
AGENTS.md inside the Run, and A2A 1.0 on the north-facing side.

## 1. What changed since the July survey

The July sweep behind [ADR-0005](../adrs/0005-build-a-dedicated-dispatch-plane.md)
and [ADR-0009](../adrs/0009-paperclip-mine-for-design-never-integrate.md) saw a
market of small board-orchestrators and one large control plane. Eight weeks
later:

- **The tracker-to-PR pattern went mainstream.**
  [openai/symphony](https://github.com/openai/symphony) (27.4k★, created
  2026-02-26) publishes it as a language-agnostic
  [SPEC.md](https://github.com/openai/symphony/blob/main/SPEC.md) and invites
  readers to "Tell your favorite coding agent to build Symphony in a programming
  language of your choice". At least eighteen ports exist; only one
  ([OasAIStudio/symphony-ts](https://github.com/OasAIStudio/symphony-ts), 424★)
  has more than 16 stars.
- **A second Go + Postgres control plane appeared.**
  [multica-ai/multica](https://github.com/multica-ai/multica) (51.4k★, created
  2026-01-13) is architecturally the closest thing to Ploeg found in either
  survey.
- **The workbench layer exploded.** Parallel-agent desktops and multiplexers
  now hold six repositories above 20k★, led by
  [stablyai/orca](https://github.com/stablyai/orca) (78.5k★) and
  [herdrdev/herdr](https://github.com/herdrdev/herdr) (40.8k★).
- **The runtime seam graduated.**
  [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)
  shipped v1.0.0 on 2026-08-28, moved to `v1beta1` and removed `v1alpha1`.
  ADR-0005's trigger "`agents.x-k8s.io` graduates past alpha → accelerate
  backlog #58" has fired.
- **kagent changed layers.** Its main branch (v1.0.0-alpha4, 2026-09-25)
  replaces the Deployment runtime with Agent Substrate, adds `Harness` CRDs for
  Claude Code and Codex, and adds cron `ScheduledRuns` guarded by database
  execution leases.
- **Daytona closed its source** in June 2026; the public repository is a
  README-only tombstone.
- **The personal-agent platforms learned to dispatch coding work.**
  [openclaw/openclaw](https://github.com/openclaw/openclaw) (390.5k★) ships a
  `gh-issues` skill that fetches issues, spawns background fix agents and opens
  PRs. OpenClaw, Hermes Agent and ZeroClaw all ship A2A 1.0 as both client and
  server.

## 2. Layer map

One row per seam of Ploeg, following the "three meanings of scheduling" in
[the components guide](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/landscape/components.md). "Nobody"
marks a seam no surveyed project claims.

| Seam | Ploeg today | What the landscape offers | Status |
| --- | --- | --- | --- |
| Work intake (what is requested) | External tracker via `TrackerProvider`: Vikunja, ClickUp ([provider.go](../../pkg/provider/provider.go)) | Symphony's read-only adapter kernel (Linear, GitHub, GitLab, Jira, Asana); beads; Paperclip and Multica *are* the tracker | Ploeg is the only one that keeps the tracker external **and** reaches Vikunja |
| Work decomposition above the ticket | None (non-goal) | OpenSpec, spec-kit, BMAD, mattpocock `triage`/`to-tickets`, gstack | Complementary; all emit artifacts a Work Item can reference |
| Admission, claim, lease, budget | Postgres lease + TTL, authorized-then-settled budgets ([ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md)) | qm and Multica have TTL leases; gh-aw has a proxy cap; nobody has authorize-then-settle | Ploeg's core; partially duplicated |
| Worker count (how many) | KEDA ScaledJob on queue depth | agent-sandbox `SandboxWarmPool` scale subresource (KEDA-drivable) | Complementary |
| Placement and isolation (where) | Kubernetes Job, hardened pod | agent-sandbox v1.0.x, Agent Substrate, OpenSandbox, OpenShell | Below Ploeg; standardizing on `agents.x-k8s.io` |
| Harness | ACP, claude-code, openhands, exec adapters ([pkg/harness](../../pkg/harness)) | ACP v1 in 40+ agents; native in Goose, Gemini, Qwen, Cline, Grok Build, deepseek-harness | Below Ploeg; ACP bet confirmed |
| Model route and metering | Per-Run LiteLLM virtual key ([ADR-0008](../adrs/0008-litellm-is-the-credential-and-metering-seam.md)) | gh-aw api-proxy (GitHub only), LangSmith gateway (workspace-level), Kortix llm-gateway | Ploeg only one with per-Run keys + authorize/settle |
| Forge delivery | `ForgeProvider`: Forgejo, GitLab; per-Run push tokens | Multica mirrors Forgejo/Gitea/GitLab PR state; Archon has a community Gitea adapter; everyone else is GitHub-first | Rare; Ploeg and Multica only |
| Human workbench | Vloer | orca, herdr, cmux, AionUi, agent-orchestrator, t3code, happy | See §5 |
| North-facing client API | Operator API only; A2A facade on watchlist ([ADR-0007](../adrs/0007-a2a-adopt-nothing-watchlist-a-facade.md)) | OpenClaw, Hermes, ZeroClaw speak A2A 1.0 both ways | **Nobody** exposes a governed ticket-to-PR dispatcher as an A2A agent |
| Repo-owned workflow policy | AGENTS.md read in the worker prompt ([task.go](../../pkg/worker/task.go)) | Symphony `WORKFLOW.md` (one owner + ports), AGENTS.md (Linux Foundation), Agent Skills | AGENTS.md and Skills are multi-vendor; `WORKFLOW.md` is not |

## 3. Direct rivals: tracker issue → isolated run → PR

| Project | Stars | Trigger | Trackers / forges | Claim and recovery | Isolation | Spend | Undercuts Ploeg? |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [openai/symphony](https://github.com/openai/symphony) | 27.4k | Poll (30 s default) | Linear, GitHub, GitLab, Jira, Asana (pluggable since 2026-07-17); no forge abstraction | In-memory `claimed` set; "No running sessions are assumed recoverable" (SPEC §14.3) | Host directory per issue, optional SSH workers | Token counting only | Partly: neutrality only |
| [github/gh-aw](https://github.com/github/gh-aw) | 5.2k | GitHub Actions events | GitHub, GHES only | Actions concurrency groups | Actions job + egress firewall | **Per-run hard cap at a proxy** (`max-ai-credits`) | Partly: spend, but GitHub-bound |
| [yc-software/qm](https://github.com/yc-software/qm) | 15.3k | Slack, web, cron, webhooks | None (chat-ops, not tickets) | **Postgres `lease_token` + `lease_expires_at` + `SKIP LOCKED` + reaper** | Durable sandbox per scope | Check-then-record USD windows | Partly: leases |
| [Untrivial-ai/agent-orchestrator](https://github.com/Untrivial-ai/agent-orchestrator) | 12.4k | Human; opt-in tracker polling; cloud webhooks | GitHub, self-managed GitLab | Cloud edition: Postgres leases with epoch fencing | Worktree + tmux; cloud Docker/Coder | Post-hoc pricing from the LiteLLM price file | Partly: leases (cloud) |
| [langchain-ai/open-swe](https://github.com/langchain-ai/open-swe) | 10.8k | GitHub, Slack, Linear webhooks | GitHub only | LangGraph checkpoints + stale-run sweep | Persistent sandbox per thread (LangSmith, Daytona, Modal, E2B) | Workspace-level gateway policy | Partly |
| [gastownhall/gastown](https://github.com/gastownhall/gastown) + [gascity](https://github.com/gastownhall/gascity) | 18.2k / 1.3k | Human via the Mayor agent | beads (syncs to Linear, Jira, GitHub, GitLab, ADO, Notion) | Assignee claim, no TTL, liveness from tmux | Worktree + tmux; Gas City adds a Kubernetes pod provider | None | No |
| [cyrusagents/cyrus](https://github.com/cyrusagents/cyrus) | 0.8k | Assignment webhooks | Linear, GitHub, GitLab | None (one process) | Worktree on host | Display only | No |

Absences confirmed by grep across all of them: **zero Vikunja, zero Forgejo,
zero Gitea** (except a 0★ Symphony port,
[Typeve/symphony-go](https://github.com/Typeve/symphony-go)), **nobody mints a
per-Run model key**, and **nobody runs a scale-to-zero Kubernetes Job per Run**.

**Symphony in detail**, because it is the one most likely to be put next to
Ploeg in a conversation:

- `WORKFLOW.md` is YAML front matter (`tracker`, `polling`, `workspace`,
  `hooks`, `agent`, `codex`) plus a strict Liquid prompt template over `issue`
  and `attempt`. It is a repository contract for Symphony, not a proposed
  cross-tool standard: the core schema hard-codes a `codex` key, and SPEC §18.1
  makes a Codex app-server client REQUIRED for conformance. No harness reads
  `WORKFLOW.md` natively.
- Its tracker contract is deliberately read-only ("Do not add generic
  comment/state/attachment CRUD", §11); the agent writes to the tracker through
  host-side tools that hold the adapter credential. Ploeg's `TrackerProvider`
  writes back itself (`Comment`, `SetStatus`). Neither is wrong; Symphony's
  shape keeps credentials out of the agent, which is the same property Ploeg
  gets from its forge broker.
- It is contributed essentially by one OpenAI engineer (34 of 47 commits; every
  commit in the last 90 days by one author) and labelled "a low-key engineering
  preview for testing in trusted environments".
- Paperclip's draft `docs/specs/external-task-protocol.md` defines a
  "Symphony-compatible task source" profile. That is the only cross-product
  adoption found.

## 4. Control planes that own the board

**Paperclip** ([paperclipai/paperclip](https://github.com/paperclipai/paperclip),
85.4k★, MIT). Re-verified against main at `7f3c06d` (2026-09-25) and the
July tag `v2026.722.0`:

| ADR-0009 claim (2026-07-29) | Now | Evidence |
| --- | --- | --- |
| No tracker-provider interface; BYO ticket system unshipped | Still true | `ROADMAP.md` still lists "Bring-your-own-ticket-system" as not started |
| "Integration is not available even in principle" | **Was already false in July** | Built-in `http` adapter (`server/src/adapters/http/execute.ts`), plugin `webhooks.receive` + `issues.create`, and `POST /companies/:companyId/cost-events` all existed at `v2026.722.0` |
| Zero Vikunja/Forgejo/Gitea/KEDA/LiteLLM contact | Still true | grep; forge support is GitHub-only and deepening |
| Dispatch via per-agent scheduled heartbeats | **Overstated** | Timer heartbeats default off (`heartbeat.ts`, `enabled: … false`, same at the July tag); the main path is an assignment wake. The accurate contrast is an always-on control plane with a 30 s scheduler tick versus Ploeg's scale-to-zero execution |
| `checkoutRunId` issue-row locks | Still true | `server/src/services/issues.ts`; `heartbeat.ts` has grown from 17,107 to 29,750 lines |
| Spend ledger parsed from adapter output | Still true | `costs.createEvent` from adapter results; agents may self-report costs |
| Builds `agents.x-k8s.io/v1alpha1` Sandbox CRs | Still true, now **stale upstream** | Kubernetes sandbox provider plugin; `v1alpha1` was removed in agent-sandbox v1.0.0 |

New since July: an experimental native runner protocol (PRP v1,
`packages/paperclip-runner/`, disabled by default) with a narrow
`ControlPlanePort` "through which a runner opens a run, appends ordered events,
and submits a terminal structured result". Self-hosted instances now broker
managed GitHub connector events through `https://my.paperclip.app` by default.
None of ADR-0009's re-evaluation triggers has fired (BYO tracker not started,
Work Queues not started, acpx at 0.19.3 rather than 1.0).

**Multica** ([multica-ai/multica](https://github.com/multica-ai/multica),
51.4k★, created 2026-01-13):

- **License:** "Multica License" = Apache-2.0 plus a Part I that controls on
  conflict: no hosted or embedded service to third parties without a commercial
  licence, UI branding must stay, and "The producer can adjust this Multica
  License". Internal use within one organization is permitted. Source-available,
  not OSI.
- **Architecture:** Go backend (sqlc over Postgres), TypeScript web, desktop and
  mobile, and a Go daemon on the user's machine that detects installed CLIs (26
  harnesses, ACP for several).
- **Dispatch:** assignment enqueues an `agent_task_queue` row; the server pushes
  a WebSocket wake and the daemon claims with `FOR UPDATE SKIP LOCKED`, setting
  `prepare_lease_expires_at` and renewing every 15 s until start. Once a task is
  running, liveness is a daemon-wide heartbeat, not a per-task lease.
  `FailStaleTasks` and `RecoverOrphanedTasksForRuntime` recover.
- **Forges:** an inbound `vcs.Provider` for Forgejo, Gitea and GitLab that
  mirrors PR and CI state onto issues (self-host only, off by default). Agents
  open PRs themselves from the daemon host.
- **Absent:** Kubernetes runtime, KEDA, external tracker import, LiteLLM, any
  spend budget or cap (usage is parsed from CLI output, priced client-side).

Multica is the first project to occupy "Go + Postgres lease + self-hosted
Forgejo" that ADR-0009 treated as Ploeg-only. What still separates Ploeg:
the tracker stays external, execution scales to zero on the cluster, and spend
is authorized and metered at a proxy.

**Also checked:** [coleam00/Archon](https://github.com/coleam00/Archon) (23.6k★)
is a YAML workflow engine for coding agents with an in-memory conversation
lock; it competes with Ploeg's Role/Shift sequencing, not dispatch, and its
forge plugin protocol with applied/refused/unverified/unknown outcomes is a
design reference. [BloopAI/vibe-kanban](https://github.com/BloopAI/vibe-kanban)
is sunsetting under community maintenance.
[kortix-ai/suna](https://github.com/kortix-ai/suna) (ELv2) is the only other
project that meters at its own gateway. [MetaGPT](https://github.com/FoundationAgents/MetaGPT)
is dormant.

## 5. The workbench layer

This layer overlaps Vloer, not Ploeg, but it decides whether Ploeg-run Runs can
be watched and steered from the tools developers already use.

| Project | Stars | Local / remote | Attaches to agents via | Could front a Ploeg-run Run |
| --- | --- | --- | --- | --- |
| [stablyai/orca](https://github.com/stablyai/orca) | 78.5k | Electron + detached PTY daemon; SSH relay; `orca serve` / `orcad` over WebSocket, paired and end-to-end encrypted | PTY + hooks (16 agents), Codex app-server, Claude Agent SDK, OpenCode plugin | Only as the process owner: run `orcad` in the pod and let it launch the harness |
| [herdrdev/herdr](https://github.com/herdrdev/herdr) | 40.8k | Local server/client; remote over OpenSSH only | PTY + screen-scraped state manifests (22 agents) + hooks | Weak: keystroke steering through `remote-api-bridge` |
| [manaflow-ai/cmux](https://github.com/manaflow-ai/cmux) | 27.4k | macOS app; Rust `cmux-tui` daemon; paid cloud VMs | PTY + hooks; an `agent-chat` sidecar drives ACP, Codex app-server, `claude -p` | Possible via `cmux relay` over stdio (untested); macOS-only UI |
| [pingdotgg/t3code](https://github.com/pingdotgg/t3code) | 23.6k | Client/server; `t3 serve` | Claude Agent SDK, Codex app-server, OpenCode SDK, ACP | Yes, if T3 owns execution in the pod |
| [slopus/happy](https://github.com/slopus/happy) | 23.9k | Daemon dials a self-hostable relay server | Claude CLI/SDK, Codex app-server, ACP | Yes, outbound only; Happy still owns the process |
| [getpaseo/paseo](https://github.com/getpaseo/paseo) | 18.6k | Daemon image on :6767, Bearer auth or relay | Claude Agent SDK, Codex app-server, OpenCode SDK, ACP | Best mechanical fit; Paseo still owns the agent |
| [openchamber/openchamber](https://github.com/openchamber/openchamber) | 10.6k | Server in front of OpenCode | OpenCode server HTTP API only | **Yes: attaches to an externally started `opencode serve`** |
| oh-my-claudecode, oh-my-codex | 39.4k / 33.4k | Local only (tmux) | Harness hooks, `send-keys` | No |
| [warpdotdev/warp](https://github.com/warpdotdev/warp) | ~65k | Open-source client; proprietary Oz platform | PTY for third-party CLIs; own harness server-side | Only by replacing Ploeg with Oz |

Findings:

- **None of these can attach to an agent process another engine started.**
  Every one either spawns and owns the harness or scrapes a PTY it owns. The
  only exception is OpenChamber for OpenCode, because OpenCode exposes a
  server. Every other remote path installs the tool's daemon in the pod and hands
  it process authority, which is exactly what Ploeg must not give away.
- **There is no attach protocol to converge on.** The structured tier (Orca,
  T3 Code, Paseo, Happy, cmux) uses a per-harness adapter stack: Codex
  app-server, Claude Agent SDK, OpenCode SDK and ACP for the long tail.
  Client↔host protocols are all bespoke. **AHP has zero adoption across the
  eleven tools verified**, so Vloer's AHP host
  ([ADR-0012](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/vloer/docs/adrs/0012-agent-host-protocol-host.md)) has
  no client ecosystem in this layer. A2A: zero.
- **None has budget authority.** Spend figures are transcript-derived
  "API-equivalent" estimates or plan windows, with no enforcement. Paseo's
  owner/operator/viewer grants and T3's scoped sessions come closest to an
  authority model.
- **Warp's proprietary Oz platform is architecturally the closest analogue to
  Ploeg + Vloer as a whole**: an `oz-agent-worker` Helm chart that runs each task
  as a Kubernetes Job, teammates attaching to running sessions, tracker
  integrations and per-team credit caps. It is SaaS-controlled; self-hosted
  workers are Enterprise-only and transcripts pass through Warp's control plane.
- **Also checked:** [superset-sh/superset](https://github.com/superset-sh/superset)
  (14.7k) is Elastic License 2.0 with a hosted relay; remote access is a paid
  tier. [winfunc/opcode](https://github.com/winfunc/opcode) (22.4k, AGPL-3.0)
  has been effectively dormant since October 2025.
  [iOfficeAI/AionUi](https://github.com/iOfficeAI/AionUi) (33.1k) reaches
  Claude Code and Codex through ACP adapters and has no PR flow or cost
  tracking.

### Omnigent: the one that reaches into Ploeg's layer

[omnigent-ai/omnigent](https://github.com/omnigent-ai/omnigent) (10.2k★,
created 2026-06-11, Apache-2.0, "Copyright (2026) Databricks, Inc.", alpha)
calls itself "the open-source meta-harness for all your AI agents". It is filed
under the workbench layer but reaches further down than any other project here:

- **Execution on Kubernetes.** A `batch/v1` Job per managed host whose
  container command is `omnigent host`, with the launch token in a per-Job
  Secret, `automountServiceAccountToken: false`, non-root and all capabilities
  dropped. A second launcher creates `agents.x-k8s.io/v1beta1` Sandboxes with
  warm pools, where expiry suspends rather than deletes.
- **Harnesses.** Claude Agent SDK, Codex app-server, the OpenCode server API,
  a generic ACP client (Goose, Qwen, Grok Build, Devin) and tmux bridges for
  native TUIs.
- **Credentials.** A credential proxy keeps "Real, long-lived secrets … in the
  parent process… nothing credential-shaped enters the sandbox".
- **Policy and spend.** ALLOW/DENY/ASK policies at server, agent and session
  level, including `cost_budget` (explicitly "a downgrade gate, not a hard
  stop") and `user_daily_cost_budget`.
- **Team.** OIDC, invite-only sign-up, shared and co-driven sessions, Postgres
  backing. Databricks sells a managed edition.
- **Absent.** Any tracker (work arrives from the UI, a Slack thread or a
  schedule), a forge abstraction (GitHub App tokens only), a durable work
  lease, an authorize-then-settle budget, per-Run gateway keys, AHP and A2A.

Omnigent is the only open project that combines Kubernetes Jobs per session,
agent-sandbox, a credential proxy and spend policy. It is session-first rather
than ticket-first, and its budget downgrades models instead of refusing spend.
It is Databricks-backed and moving fast, so it is the project to watch.

## 6. Runtimes and sandboxes below Ploeg

| Project | Relation | Integration shape | Verdict |
| --- | --- | --- | --- |
| [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) v1.0.4 (SIG Apps, Apache-2.0) | Below | Executor creates `extensions.agents.x-k8s.io/v1beta1` `SandboxClaim` → `SandboxWarmPool` → `SandboxTemplate` (RuntimeClass Kata or gVisor, managed NetworkPolicy) through `sigs.k8s.io/agent-sandbox/clients/k8s`; KEDA scales the warm pool's scale subresource | **Best runtime under Ploeg** |
| [kagent-dev/kagent](https://github.com/kagent-dev/kagent) v0.10.2 / v1.0.0-alpha4 (CNCF Sandbox) | Beside, partially competing | A2A peer at most | Not a runtime to embed; still no tracker, forge or spend |
| [google/ax](https://github.com/google/ax) + [agent-substrate](https://github.com/agent-substrate/substrate) | Below, overlapping ambition | Substrate `CreateActor` directly | Pre-1.0, breaking changes promised, heavy node install; revisit after CNCF donation and a v1 |
| [opensandbox-group/OpenSandbox](https://github.com/opensandbox-group/OpenSandbox) 1.1.0 | Below | OpenAPI lifecycle server, can back onto agent-sandbox | Heavier than raw agent-sandbox |
| [NVIDIA/OpenShell](https://github.com/NVIDIA/OpenShell) v0.1.1 | Beside | Policy and credential layer that itself creates `agents.x-k8s.io` Sandboxes | Overlaps the LiteLLM credential seam; too young |
| [e2b-dev/E2B](https://github.com/e2b-dev/E2B), [TencentCloud/CubeSandbox](https://github.com/TencentCloud/CubeSandbox) | Below | HTTP; no Go SDK (E2B), untagged (Cube) | Kubernetes paths are evaluation-only or preview |
| [daytonaio/daytona](https://github.com/daytonaio/daytona) | Below | None | Disqualified: control plane closed since June 2026 |
| [conductor-oss/conductor](https://github.com/conductor-oss/conductor), Temporal | Competing substrate | Would replace Ploeg's store and leases | Same class as the rejected Argo option |

Three independent projects (OpenShell's Kubernetes driver, OpenSandbox's
provider, kagent v0.10) now build on `agents.x-k8s.io`. Vloer already has a
`SandboxWorkspaces` provisioner against `v1beta1`
([sandbox.ts](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/vloer/src/runtime/sandbox.ts)); Ploeg's backlog #58 still
says "pin v1beta1/v0.5.x" and points at Paperclip's now-invalid `v1alpha1`
builder. Two constraints to design around: `SandboxClaim.spec.env` accepts only
literal values (never pass a per-Run key there), and a warm pod starts before
its Run exists, so the worker must wait for the `agents.x-k8s.io/claim-uid`
label before claiming from ploegd.

## 7. Harnesses below Ploeg

ACP wire v1 is stable (schema package v1.9.1, 2026-09-18); the v2 draft
(2026-07-20) is still unstable. Since spring, ACP stabilized `usage_update`,
request cancellation, session resume and close, and elicitation. The live
registry lists 41 agents. Ploeg's ACP bet in backlog #64 is confirmed.

| Harness | ACP | Base URL covers every model call | Notes |
| --- | --- | --- | --- |
| opencode | native | yes | Current default |
| goose | native | yes (native LiteLLM provider; disable session naming) | Env-only configuration; the `profiles.go` note that it "wants a config.yaml" is out of date |
| qwen-code | native | yes (turn off usage statistics) | Matches Ploeg's existing `OPENAI_*` env |
| codex | adapter (codex-acp) | yes, but Responses API only | LiteLLM must serve `/v1/responses`; per-Run `CODEX_HOME` |
| grok-build | native | partial until `session_summary` and `web_search` are remapped | External contributions not accepted |
| gemini-cli | native | partial (hard-coded flash models for compression) | Gemini wire format only |
| claude-code | adapter (claude-agent-acp) | yes for model calls; telemetry and server-side web search leave | Proprietary |
| deepseek-harness | native | partial by default; session-log upload on by default | Pre-1.0 developer preview |
| OpenHands | native | yes via SDK | Now an orchestrator itself (Agent Canvas, automation service, Kubernetes `agent_sandbox` workspace) |
| crush, claw-code, aider | none | — | Not candidates |

Recommended next profiles after OpenCode, cheapest first: **Qwen Code**, then
**Goose**, then **Codex via codex-acp**.

## 8. Methods, skills and personal agents

**Inputs above Ploeg.** OpenSpec (already used in
[apps/ploeg/openspec](../../openspec)) is the cleanest fit: it has a JSON agent
contract (`openspec instructions apply --json`, `openspec validate --json`) and
"never commits, branches, pushes", so it cannot compete. BMAD's
`bmad-build-auto` is explicitly a single iteration that "never picks the next
ticket itself … A human or an orchestrator … owns backlog policy and dispatch",
which is the role Ploeg plays. spec-kit, Ralph's `prd.json` and mattpocock's
`AGENT-BRIEF.md` are importable formats; none has cross-vendor adoption as a
work-item interchange.

**Inside the Run.** Two conventions now have genuine multi-vendor adoption:
AGENTS.md (Linux Foundation, about 20 harnesses; Ploeg already reads it) and
Agent Skills `SKILL.md` (40+ shipped clients, including every harness Ploeg
runs). Skills are not hooks, so a Ploeg-owned skill set in the worker image does
not conflict with [ADR-0030](../adrs/0030-target-repository-instructions-rank-below-the-delivery-contract.md).

**Competing local dispatchers.** A dozen methods ship their own loop: spec-kit
`specify workflow run`, gsd-pi, bmad-loop, Gas Town, task-master `loop`,
`ralph.sh`, ECC 2.0, compound-engineering `lfg`, mattpocock/sandcastle,
OpenClaw `gh-issues`, Hermes Kanban and ZeroClaw's `claude_code_runner`. All are
single-operator and run on one host. None has Kubernetes Job workers, Forgejo PR
delivery, Vikunja, per-Run budgets or an authorization boundary between tenants.

**Clients north of Ploeg.** OpenClaw, Hermes Agent and ZeroClaw ship A2A 1.0 as
both server and client; OpenClaw accepts text-only tasks without streaming or
cancellation. ZeroClaw also has a Git channel for Forgejo issues and PRs, so it
can already talk to a Ploeg-watched issue with no new protocol. ADR-0007's
facade would let all three submit governed work at once.

## 9. Does any of it remove Ploeg's viability?

No, for a reason that is structural rather than a feature gap: every large
project in this survey is built for one operator on one host or for GitHub. The
things Ploeg does are the things that only matter when several people share a
cluster, a budget and a self-hosted forge: admission before spend, a lease that
survives a dead pod, a key the agent cannot exfiltrate beyond its Run, and a
tracker the organization already owns.

What the survey does change:

1. **Drop "event-driven" as a headline differentiator against Paperclip.** The
   heartbeat claim was overstated even in July. Say "execution scales to zero;
   the control plane stays small" instead.
2. **Stop claiming the lease as unique.** qm, Multica and the agent-orchestrator
   cloud edition have equivalent semantics. The distinguishing property is that
   the lease sits under admission and budget, not the lease itself.
3. **The strongest single differentiator is spend.** Authorized-then-settled
   budgets with per-Run virtual keys at a proxy exist nowhere else; gh-aw's cap
   is the nearest and is GitHub-only and self-described as not an
   authentication boundary against code inside the container.
4. **Tracker neutrality needs a GitHub and Linear story to be credible.** Ploeg
   has no GitHub or Linear `TrackerProvider` and no GitHub `ForgeProvider`.
   Symphony's five adapters make "neutral but only Vikunja and ClickUp" look
   narrow. Vloer can already read GitHub issues
   ([tasks.ts](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/vloer/src/tasks.ts)); Ploeg cannot ingest them.
5. **The biggest risk is not a competitor but a pattern.** Symphony shows the
   pattern is small enough to be regenerated by an agent from a spec. Ploeg's
   defence is the part a spec does not carry: the recovery, admission and
   settlement semantics tested in [pkg/store](../../pkg/store) and
   [pkg/shiftengine](../../pkg/shiftengine).

## 10. Integration shapes, cheapest first

1. **Record the agent-sandbox trigger and re-pin backlog #58** to `v1beta1` at
   v1.0.x, reusing Vloer's provisioner design. KEDA drives the warm pool.
2. **Add ACP profiles** for Qwen Code and Goose in
   [profiles.go](../../pkg/harness/adapters/acp/profiles.go), then Codex via
   codex-acp once LiteLLM serves `/v1/responses`.
3. **Mount a Ploeg-owned Agent Skills set** in the worker image, for example
   verification and review skills.
4. **OpenSpec as a first-class Work Item input:** a Work Item that names an
   OpenSpec change runs `openspec instructions apply --json`; a reviewer Role
   gates on `openspec validate --json`.
5. **A GitHub `TrackerProvider` and `ForgeProvider`**, reusing the per-Run token
   design with GitHub App tokens (backlog #75 already anticipates this).
6. **A `WORKFLOW.md` crosswalk**, not conformance: document how Symphony's
   `tracker`, `hooks`, `agent.max_turns` and prompt body map onto Ploeg Roles,
   and optionally read that front-matter subset. Strict Symphony conformance is
   impossible (Codex app-server required).
7. **The A2A facade from ADR-0007** once one of its triggers fires; OpenClaw,
   Hermes and ZeroClaw are now ready clients.

Never: run Ploeg as a Paperclip or Multica adapter (two run-trackers, and
Multica's licence restricts redistribution); adopt Symphony's in-memory claim
model; embed kagent or Agent Substrate as a dependency before v1; pass per-Run
credentials through `SandboxClaim.spec.env`.

## 11. Proposed record changes

These need the owner and are not made by this dossier:

- **ADR-0005:** note that the "graduates past alpha" trigger fired on
  2026-08-28; revise the kagent row to "beside, partially competing
  (ScheduledRuns with leases, Claude Code and Codex harnesses); still no
  tracker, forge or spend".
- **ADR-0009:** correct "per-agent heartbeats" and "not available even in
  principle"; add Multica as the second self-hosted-forge-aware control plane.
  Both belong to the 2026-10-31 quarterly re-scan in the
  [review calendar](../adrs/README.md).
- **Backlog #58:** re-pin to `v1beta1`, v1.0.x.
- **Tracker tickets:** Qwen Code and Goose ACP profiles; GitHub tracker and
  forge providers; OpenSpec Work Item input.

## 12. Re-evaluation triggers

Any one of these reopens this dossier:

- Symphony's SPEC moves past "Draft v1", gains a maintainer outside OpenAI, or
  drops the REQUIRED Codex app-server client from §18.1.
- Multica publishes its daemon protocol as a stable API, relicenses under an
  OSI licence, or adds a Kubernetes runtime.
- Paperclip ships its bring-your-own-ticket-system roadmap item or enables PRP
  v1 by default.
- gh-aw targets Forgejo Actions, or any project ships per-Run virtual model
  keys.
- kagent v1.0.0 goes GA with `ScheduledRuns`, or adds a tracker trigger.
- Agent Substrate completes its CNCF donation and tags v1.
- ACP v2 leaves draft.
- Omnigent adds a tracker trigger, a forge abstraction beyond GitHub, or a
  hard (refusing) budget.
