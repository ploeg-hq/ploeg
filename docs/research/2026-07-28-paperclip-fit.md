# Paperclip × Ploeg — product-fit dossier

> Surveyed 2026-07-28 against a local clone of `paperclipai/paperclip`
> (shallow, depth 50) plus a manual read of this repo's architecture docs.
> Condensed verdict lives in [design.md §8](../design.md); the extracted
> design lessons are folded into backlog items #9, #10, #15, #16, #21, #44,
> #58, #60, #64, #86. This file is the full evidence trail.
>
> **Method caveat, stated up front.** Unlike the A2A and AHP sweeps, this was
> *not* a clean four-agent fan-out. One agent (local seam map of this repo)
> completed. Three paperclip-side agents were killed mid-dig by an org spend
> limit; their work was redone by hand against the clone. Consequences for
> confidence: file-level claims below were read directly and are solid;
> **contributor statistics come from the last 50 commits only** (shallow
> clone) and must not be read as project-lifetime attribution; and two
> questions were left unresolved (§10).

**Verdict: do not integrate, do not depend — mine it for design.** Paperclip is
a competing *board-first* control plane that owns the seam Ploeg deliberately
refuses (the tracker) and also claims the seam Ploeg exists to own (dispatch).
There is no integration seam between them today: Paperclip has no
tracker-provider interface to plug into, and its dispatch model (per-agent
heartbeats + issue-row checkout locks) is a second implementation of Ploeg's
job, not a layer above or below it. But it is MIT-licensed, five months old,
75k stars, and has *already shipped* most of Ploeg's phase-2/3 roadmap — which
makes its `doc/execution-semantics.md` the most valuable free design review
this backlog has received. Steal semantics; write no code against it.

Unlike A2A (which failed on *fit* despite maturity) and AHP (which failed on
*maturity*), Paperclip fails on **layer collision**: it is not too immature or
too foreign, it is a rival occupying the opposite pole of the same problem.

## 1. What Paperclip actually is

- **Self-description:** *"The app people use to manage AI agents for work…
  If OpenClaw is an employee, Paperclip is the company."* Open-source
  orchestration for teams of AI agents — org charts, budgets, governance,
  goal alignment. *"It looks like a task manager. Under the hood: org charts,
  budgets, governance, goal alignment, and agent coordination."*
- **Shape:** Node.js server + React UI, pnpm monorepo. Measured LOC (`.ts`
  + `.tsx`): server 427,217 · ui 360,728 · packages 200,502 · cli 35,071 —
  roughly 1M lines. For scale: Ploeg is 5,147 lines of Go.
- **License:** MIT (`Copyright (c) 2025 Paperclip AI`) — permissive, so
  copying *code* would be legally fine; the argument against is language and
  architecture, not licensing.
- **Storage:** PostgreSQL via Drizzle ORM, in three modes — embedded
  (auto-started, `~/.paperclip/instances/default/db/`), local Docker, or
  hosted (Supabase is the documented production path). `DATABASE_MIGRATION_URL`
  splits migration from runtime connections. (`doc/DATABASE.md`)
- **The four pillars** (their framing): Agentic Task Manager · Org Chart for
  Agents · Agent Employee Training (Skill Studio, evals) · Agentic OS
  (cross-provider runtime, sandboxing, MCP, SSO/RBAC, cost controls).

### The execution model — heartbeats, not events

*"The heartbeat is a protocol, not a runtime. Paperclip defines how to
initiate an agent's cycle."* (`doc/SPEC.md:187`) An agent is anything
callable: *"The minimum requirement to be a Paperclip agent: **be callable.**
… Paperclip can invoke you via command or webhook. No requirement to report
back — Paperclip infers basic status from process liveness when it can."*
(`doc/SPEC.md:113`)

Work reaches an agent through a **per-agent scheduled heartbeat** plus typed
event wakes (`issue_blockers_resolved`, one-shot monitors `issue_monitor_due`,
comment wakes, assignment wakes). The scheduler lives in
`server/src/services/heartbeat.ts` — **17,209 lines in one file**, which also
contains workspace preparation, budget gates, checkout, recovery, and
finalization.

This is precisely the model [design.md §1](../design.md) requirement 1
rejects (*"agents run when work is assigned, never on polling loops"*) — with
the important qualification that typed wakes make it substantially better than
naive polling. It is not "cron every 5 minutes and hope"; it is a scheduler
with event-driven fast paths bolted on. The token-burn objection is softened,
not eliminated.

### Concurrency — checkout locks, not leases

Not a claimable queue. Two lock columns on the issue row:

- `checkoutRunId` — *"who currently owns execution rights for the issue"*
- `executionRunId` — *"which run is actually live right now"*

Rules, verbatim from `doc/execution-semantics.md` §5: a run owns
`checkoutRunId` *"only while that run is non-terminal"*; on reaching
`succeeded|failed|cancelled|timed_out`, *"finalization must compare-and-clear
lock columns that still point at that run"* and *"must not clear a lock
already reacquired by a successor run"*; *"process-loss retry handoff must not
leave `checkoutRunId` pinned to the failed run when `executionRunId` moves to
the retry run."* Stale locks self-heal only when the owning run is terminal or
missing — *"Paperclip must not clear or adopt locks held by non-terminal
runs."* After stale cleanup a checkout `409` means a real live owner, and
*"agents must treat that 409 as an ownership conflict and stop rather than
retrying."* Implementation: `heartbeat.ts:14494-14565`, rows held under
`FOR UPDATE`; entry point `issuesSvc.checkout(...)` at `heartbeat.ts:11928`.

> **Red herring, recorded so the next reader doesn't repeat it:**
> `server/src/board-claim.ts` looks like the task-claim code and is not — it is
> the first-admin bootstrap ("claim this instance"), an in-memory challenge
> token with a 24h TTL. The real checkout is inside `heartbeat.ts`.

## 2. Project health

Measured via the GitHub API on 2026-07-28:

| Metric | Value |
|---|---|
| Created | **2026-03-02** (≈5 months old) |
| Stars / forks | **74,962 / 13,967** |
| Open issues | 4,958 |
| Watchers | 374 |
| Last push | 2026-07-28 (same day) |
| License | MIT |

- **Velocity:** PR numbers in the **#10,300s** five months in. Releases are
  weekly CalVer (`releases/` holds 22 notes; latest `v2026.722.0`);
  `server/package.json` says `0.3.1`.
- **Governance:** company-backed (Paperclip AI). Last-50-commit authorship:
  Dotta/cryppadotta 27, Nicky Leach (`nicky@paperclip.ing`) 14, Devin Foley 6,
  plus a bot and two one-off external commits. *This is a 50-commit window,
  not lifetime attribution* — but the release notes credit external
  contributors (`@nickyleach`, `@HKTITAN`, `@samrusani`, `@nosolosoft`), so
  there is a real if company-led contributor base.
- **Contribution posture:** *"if you want to work on roadmap-level core
  features, please coordinate with us first in Discord (`#dev`) before writing
  code"* (`ROADMAP.md`) — core direction is vendor-held; plugins are the
  sanctioned extension path.
- **Commercial trajectory:** a cloud tier is materializing — recent commits
  add *"computed owner instance-admin elevation for cloud-managed instances,
  behind platform floors"*, multi-tenant isolation with per-company JWT keys,
  and local→cloud upstream sync. Open-core drift is the thing to watch, not a
  present blocker (MIT is MIT for what has shipped).
- **Stability:** no API-stability promise; `doc/` is littered with in-flight
  plans and reversals. This is a project moving faster than its own docs.

## 3. Absence checks — zero contact with this stack

Every term below was grepped across their entire tree (`.ts`, `.tsx`, `.md`).
**Absences are the finding.**

| Term | Result |
|---|---|
| Vikunja | **0** |
| Forgejo | **0** |
| Gitea | **0** |
| KEDA | **0 real.** 99 case-insensitive file matches are all substring noise — `executionLockedAt` (131), `revokedAt` (120), `lockedAt` (54), `checkedAt` (33), `healthCheckedAt` (17) |
| ScaledJob | **0** |
| Helm | **0 real** — matched inside "overw**helm**ingly" |
| LiteLLM | **0 real** — one test fixture URL, `https://example.invalid/litellm`, in the Hermes adapter's model-detection test |
| OpenBao / Vault | **0** |
| Grafana | **0 real** — three mentions in `doc/plugins/PLUGIN_SPEC.md` as *hypothetical* plugin examples (`@paperclip/plugin-grafana`, "Grafana widgets") |
| OpenHands | **1 real** — `server/src/services/company-skills.ts:459` reads the `.openhands/skills` directory convention. Skill-path compatibility, not harness support |

**Conclusion:** the self-hosted tracker/forge/k8s-scheduler niche Ploeg serves
is entirely unserved by Paperclip, and vice versa. They are not competing for
the same operator today.

## 4. Layer map

| Seam | Ploeg | Paperclip | Relation |
|---|---|---|---|
| Board / source of truth | BYO tracker (Vikunja); *"not another kanban UI"* is non-goal #1 | **Is** the board. BYO-ticket-system (Jira/Linear/Asana as on-ramps) is an **unshipped ⚪ roadmap item**, framed as *"Paperclip owns execution, governance, and outcomes"* | **Competing worldviews** |
| Dispatch | Vikunja webhook → Postgres queue → `FOR UPDATE SKIP LOCKED` → TTL lease renewed by the Job | Per-agent heartbeat scheduler + typed wakes; issue-row checkout locks | **Same job, twice** — no integration seam |
| Executor | Run API ([contracts/executor.md](../contracts/executor.md)) + KEDA ScaledJob, scale-to-zero | Long-running Node server dispatches into sandboxes; `createJob` exists in the k8s provider (`job-orchestrator.ts`) but as sandbox plumbing, not queue-driven autoscaling | Different: theirs needs a always-on control plane |
| Worker ↔ harness | `harness.Adapter` (`pkg/harness/adapter.go`); ACP is bet #64 | Adapter registry (11+ built-ins) with a **native ACP engine** as its *richest* tier | **Convergent — they already shipped our bet** |
| Runtime / sandbox | The Job *is* the runtime (dind sidecar); agent-sandbox is #58 | 7 pluggable providers incl. a Kubernetes one that builds **`agents.x-k8s.io/v1alpha1 Sandbox` CRs** | **They are ahead; reference impl for #58** |
| LLM credential / spend | LiteLLM per-run budgeted key, alias `ploeg-<12hex>`, metered at the proxy, actively revoked | Own `cost_events` ledger (costCents/inputTokens/billingType) **parsed from adapter output**; budget policies scoped company/agent/project | **Different trust models** (see §7) |
| Human-in-the-loop | `needs_human` state + Vikunja re-assignment; no UI by design | Typed interactions, approvals, execution-policy participants, watchdogs, full UI | **They built the machinery behind our empty state** |

## 5. Can it be integrated? — No

1. **Both want to own dispatch.** Checkout locks + heartbeat scheduler vs
   leases + KEDA scaler is one responsibility implemented twice. Wiring them
   together means two claim authorities with no arbiter.
2. **There is nothing to plug into.** A Ploeg `TrackerProvider` needs outbound
   assignment webhooks; Paperclip's BYO-ticket-system is unshipped, so no
   event contract exists for `ParseWebhook` to consume.
3. **Ops-weight inversion.** Fronting a 5k-line Go dispatcher with a 1M-line
   Node control plane on a weekly breaking-release train inverts the entire
   thin-glue rationale ([design.md §10](../design.md) — *"designed so
   abandonment costs adopters little"*).

**The one honest future scenario:** Paperclip as the *human surface* —
replacing Vikunja and De Vloer as board and console, with Ploeg as the
Kubernetes execution muscle behind an `http`-adapter agent. **The named
tension:** their liveness contract assumes *they* track runs (active run,
queued wake, monitor); Ploeg's leases assume *Ploeg* does. One side's
run-tracking necessarily becomes a lie, and their recovery machinery would
fire against runs it cannot see. Dead until their BYO-ticket-system ships a
real event contract — and even then it is a board swap, not an integration.

## 6. Can it be cherry-picked? — Yes, as semantics

Nothing here is a code transplant (TypeScript → Go, and their abstractions
assume their data model). Every item is a *rule* worth adopting. All are now
annotated in [backlog.md](../backlog.md).

### Tier 1 — direct hits on open Ploeg pain

1. **Pre-dispatch configuration validation → #60, VIK-596.** From
   `execution-semantics.md` §5: *"Before a run is dispatched, required
   secret/env bindings are validated; missing bindings produce a surfaced
   configuration-incomplete blocker, not a dispatched run"* — and the rule
   behind it: *"A dispatched-then-failed run is the wrong shape for missing
   configuration."* This is exactly Ploeg's stuck-vs-failed inversion
   ([architecture.md §9.9](../architecture.md)). The worker already constructs
   its adapter before claiming; extend that to full config validation so a
   guaranteed-to-fail run never burns an attempt, mints a key, or parks an
   item in `needs_human`.
2. **Routable blocking → #86.** Entering `blocked` *requires* a machine-routable
   waiting path: a first-class blocker edge, a pending interaction naming a
   responder, or a structured unblock descriptor naming `{owner, action}`.
   *"Prose-only blocked — free-text that names an owner or action in a comment
   without any of the paths above — routes to nobody. It is rejected at the API
   or auto-classified as `needs_attention`, never silently accepted as a
   healthy waiting state."* Ploeg's `needs_human` today is free-text
   `stuck_reason` plus silence. Upgrade the OutcomeReport with an optional
   structured `{owner, action}` descriptor and #86's notification hook gains a
   routing target.
3. **The liveness contract + bounded recovery → #15.** Every non-terminal
   agent-owned item must have a live path, an explicit waiting path, or a
   recovery path; the health test is *"can the product answer 'what moves this
   forward next?' without requiring a human to reconstruct intent from the
   whole thread."* Recovery is bounded by a **recovery fingerprint**: exactly
   one automatic continuation per fingerprint, then visible escalation —
   *"unchanged killed/local-watcher evidence must not create an infinite
   wake/recovery loop."* New durable activity mints a new fingerprint and
   re-earns a retry. Sharper than Ploeg's bare `attempts` counter, because
   progress resets the budget while spinning does not.
   - Sub-rule worth its own line: *"Deliberate wait is not a lost run"* (§9.2)
     — a run parked waiting for review must be converted into a first-class
     dependency wait, not retried as a crash and escalated. They shipped the
     naive version first and had to fix it.
   - And: *"An unmanaged local process is not a durable action path"* — shell
     jobs, `nohup`, detached PTYs, adapter child processes. *"A PID, session
     id, log file, comment, or promise to check later is evidence only."*
4. **Budget gate placement → #44, #60.** Checked *before dispatch*
   (`getInvocationBlock`, `budgets.ts:718`, called at `heartbeat.ts:7805`) and
   *re-checked at invocation* (`heartbeat.ts:9257`, `:10736` →
   `cancelRunInternal(run.id, budgetBlock.reason)`), with `budget_blocked` as
   a distinct error code. For Ploeg: the budget check belongs *before Claim*,
   not inside the run.

### Tier 2 — reference implementations for planned work

5. **Kubernetes sandbox provider → #58, #71–74.** `sandbox-cr-builder.ts:43`
   emits `apiVersion: agents.x-k8s.io/v1alpha1, kind: Sandbox` — a production
   consumer of kubernetes-sigs/agent-sandbox. (Note the **v1alpha1** pin,
   older than backlog #58's assumed v1beta1/v0.5.x — verify the current API
   version before building.) The surrounding files are a checklist for our
   security backlog: `cilium-network-policy.ts`, `network-policy.ts`,
   `scoped-network-egress.ts`, `image-allowlist.ts`, `secret-manager.ts`,
   `pod-exec.ts`, `file-sync.ts` (tar-over-exec, batched into one exec per
   operation), `tenant-orchestrator.ts`. Their lease semantics match ours:
   *"a lease is resumable only while its workload resource [is alive]… if the
   pod backing a lease is gone or terminally failed, the lease can never be
   resumed"* → mark expired, fall back to a fresh acquire
   (`lease-lifecycle.ts`).
6. **Sandbox startup-latency discipline → cold-start work.** Recent commits
   are a masterclass in the problem Ploeg hit at ~2m45s: per-step round-trip
   and provider-latency attribution for sandbox startup (#10222), cached
   started-sandbox handle per lease (#10335), parallelized bridge setups
   (#10334), opt-in no-profile fast path for default-PATH execs (#10352).
   Measure per-step attribution first — the method, not the fixes.
7. **The acpx event vocabulary → #64.** Their ACP engine (`acpx@0.12.0`,
   vendored via `patches/`, `packages/adapter-utils/src/acpx-engine`, made the
   default in db migration `0136_acpx_default_engine_migration.sql`) emits
   JSONL per runtime moment: `acpx.session` (agent, mode, session identity),
   `acpx.status` (progress + context-window usage), `acpx.text_delta`,
   `acpx.tool_call` (title, call id, status updates that fold into one card),
   `acpx.result` (stop reason), `acpx.error` (code, message, **retryability**).
   That last field is the one Ploeg needs most — retryable-vs-not at the
   source kills the outcome-inference guesswork. They rank ACP as tier 1 of 3,
   above CLI-JSON parsing, above raw stdout.
8. **Watchdog stop-fingerprints → #10, future verifier.** Fingerprint a
   stopped subtree from durable state; suppress re-fire while unchanged; and
   critically: a *"live path restored"* claim earns only **bounded
   verification**, never permanent suppression — *"if the subtree is observed
   stopped again with a fingerprint equal to the one the watchdog claimed to
   have fixed, the restoration failed"*, re-fire with an incremented attempt
   count, escalate to a human after N (2–3). They also document the trap:
   a fingerprint computed from leaves alone makes a failed intermediate-node
   restoration byte-identical to a legitimate stop, *"which silences the
   watchdog forever."* Plus the **atomic recovery batch** — ≤3 mutations,
   applied all-or-nothing, aborted if the fingerprint changed mid-batch —
   because *"spending the only permitted write on an informational comment
   forfeits the state-restoring mutation the recovery actually needed."*
9. **Exact-once fan-out → #39/#43 (teams).** Their accepted-plan decomposition
   is keyed `(sourceIssueId, acceptedPlanRevisionId)` with a durable claim
   written *before* fan-out, durable partial progress during, and a durable
   result after: *"If a run creates some children and then dies, retries must
   continue from the same fingerprint and reuse the already-recorded partial
   result."* The pattern for any future multi-role team fan-out.
10. **Checkout finalization CAS rules → #16.** See §1 — the race checklist for
    the moment checkpoint-resume creates successor runs.

### Tier 3 — hard-won warnings (problem → rule)

11. **Untrusted content is a prompt-injection carrier *upward* → #9, #79.**
    Child→parent report comments are **off by default** for
    `low_trust_review` presets, *"whose input is untrusted content (diffs,
    external tickets) and whose report comment would be a prompt-injection
    carrier into higher-trust context."* The replacement is a system-attributed
    **stop-only relay**: fires on `blocked`/`cancelled` only, is a comment not
    a transition (*"depth-1 by construction"*), and dedupes per
    (child, target status) *"so status flapping cannot spam the parent."*
    Corroborating incident: commit `aed4478` re-scoped PR-gardening runs to
    *instance-authored PRs only*. When Ploeg's forge webhooks land, review text
    is untrusted input to the next run.
12. **Cheap-model recovery lane, and its reversal → #60.** Cheap profiles are
    permitted *only* for status-only bookkeeping, guarded with
    `allowDeliverableWork: false`, `allowDocumentUpdates: false`,
    `resumeRequiresNormalModel: true`, and *"cheap recovery hints must be
    scrubbed from copied retry, resume, child, and downstream source-work
    contexts."* Commit `1426494` then **disabled cheap model profiles by
    default** — they shipped it, learned, and defaulted it off.
13. **Migration authoring checklist → #21.** Their 0126 backfill *"looked for
    the next rows with an unindexed predicate, so PostgreSQL repeatedly scanned
    the same table and the migration became O(n²) as the table grew."*
    Resulting CI gate (`check:migrations`): index the batch predicate before
    the loop, bound batches by an indexed key (*"Do not use `OFFSET`
    pagination"*), `CREATE INDEX CONCURRENTLY` on large tables, split
    schema/index/backfill into separate phases.
14. **`heartbeat.ts` is 17,209 lines — the anti-pattern, free.** Checkout,
    budgets, workspaces, recovery, watchdogs, and finalization all accreted
    into one service. Ploeg's package-per-seam layout is the defense; the
    lesson is that this happens by *accretion under velocity*, not by decision.

## 7. Honest scorecard

### Where Ploeg is genuinely behind

Paperclip has **shipped**, in five months, most of what this repo has only
*designed*:

| Ploeg backlog item | Status here | Status there |
|---|---|---|
| Human-in-the-loop (#86, #45) | `needs_human` is a state with **nothing behind it** — no notification, no routing, no UI | Typed interactions, approvals, execution-policy participants, inbox, mobile |
| Checkpoint/resume (#16, #67, #70) | Checkpoints are **written and never read**; every run starts fresh | Session reuse across heartbeats, workspace/branch coherence checks |
| Teams with roles (#39–#45) | No roles, no `teams` table; a team is one worker + one model in Helm values | Org chart, delegation, courier pattern, subtree-scoped authorization |
| Budgets (#44) | Enforced only as a LiteLLM key ceiling; **never tracked** — `agent_runs.usage` is populated by one adapter and read by nothing | Per-scope policies, ledger, pre-dispatch + pre-invocation gates |
| Observability (#81–#84) | **No metrics, no OTel**; structured logs + an external Grafana join | Activity log, attribution, cost dashboards, output-silence watchdogs |
| Forge provider (#32) | Interface exists, **zero implementations**; README claims "two reference providers" — the forge one is aspirational | n/a (different problem) |
| Secrets (#75–#78) | Worker holds the **LiteLLM master key** and a long-lived PAT **in the clone URL** (`pkg/worker/git.go`) — exactly what #77 forbids | Per-agent scoped grants, run-bound API access, audited reads, strict mode |
| State machine (#11) | `work.CanTransition` **has no production caller**; SQL transitions directly | Enforced dispositions with documented legality |

**The strategic risk, stated plainly:** company-scale velocity beats spare
time. If the market's verdict is *"the board and the control plane should be
one product"*, Ploeg's audience narrows to operators who actively refuse that
bundle. That is a real audience — but it is smaller than the one
[design.md §10](../design.md) already flags as "real but narrow", and this
sweep should move the 2027-04 review gate's prior, not just sit in a table.

### Where Ploeg holds ground they structurally do not

Not consolation prizes — these are consequences of architecture they cannot
adopt without becoming a different product:

1. **Event-driven scale-to-zero.** Ploeg spawns a pod when a webhook arrives
   and runs nothing otherwise. Paperclip requires an always-on Node control
   plane whose scheduler wakes agents on a cadence. For a homelab cluster
   billed in watts and a factory that is idle most of the day, that is the
   difference between zero and continuous baseline cost.
2. **Tracker and forge neutrality for self-hosted stacks.** §3 is the proof:
   zero Vikunja, zero Forgejo, zero Gitea. Their BYO-ticket-system roadmap
   names Asana/Linear/Jira — the SaaS mainstream. Self-hosted operators have
   nothing to adopt there, which is the exact gap [design.md §1](../design.md)
   opens with.
3. **Trustworthy spend accounting.** This is the sharpest one. Their
   `cost_events` ledger is **parsed from adapter stdout** — a compromised,
   buggy, or lying agent skews the books, and their own budget gate then makes
   decisions from those numbers. Ploeg meters at a LiteLLM boundary the agent
   *cannot bypass*, with a per-run key the agent cannot mint, joined to ticket
   and commit by `ploeg-<12hex>`. For an autonomous factory, spend that the
   spender reports is not an audit trail.
4. **Crash-safety that does not depend on agent goodwill.** A dead pod stops
   renewing; the lease expires; the sweeper re-queues. No watchdog needs to
   reason about whether a silent process is healthy — Paperclip needed an
   entire silent-active-run watchdog subsystem (§12 of their doc) precisely
   because their liveness is inferred rather than mechanical.
5. **Ops surface proportional to the job.** 5,147 lines of Go, one binary, one
   chart, Postgres. Their `heartbeat.ts` alone is 3× Ploeg's entire codebase.
   When Ploeg is abandoned, an adopter keeps Postgres rows and git branches.

**The uncomfortable synthesis:** Ploeg's *architecture* is right for its niche
and its accounting model is genuinely more trustworthy — but Paperclip's
*execution semantics* are years ahead of Ploeg's, and that gap is a function
of engineering hours, not of design taste. The correct response is to take the
semantics for free (§6), not to widen scope trying to match their surface.

## 8. Recommendations

1. **Adopt nothing, depend on nothing, write no compatibility code.**
2. **Treat `doc/execution-semantics.md` as a design review** — Tier 1 items
   (§6.1–4) are the highest-value cheap fixes on the backlog right now.
3. **Raise confidence on #64 (ACP)** — a 75k-star project shipping ACP as its
   *richest* adapter tier, alongside the A2A sweep's OpenHands finding, is the
   third independent confirmation.
4. **Read their k8s provider before starting #58** — and re-check the
   `agents.x-k8s.io` API version, which they pin older than our backlog
   assumes.
5. **Before building any De Vloer approval/transcript surface, read their
   interaction model first** — that is a solved problem there and an empty
   state here.
6. **Do not widen scope to compete.** Every pillar they add (org charts,
   skills studio, evals, training) is a pillar Ploeg's non-goals already
   reject. Losing a feature race that was never entered is not a loss.

## 9. Re-evaluation triggers (any one reopens this)

- **Paperclip ships BYO-ticket-system with an outbound assignment-event
  contract** — the only development that creates a real integration seam, and
  the trigger to evaluate it as a human surface *above* Ploeg before building
  De Vloer equivalents.
- **Their Work Queues milestone (⚪) ships claimable-queue semantics** —
  direct collision with Ploeg's core; re-read their implementation.
- **acpx reaches a stable standalone 1.0** — build #64 against it rather than
  the raw protocol.
- **`agents.x-k8s.io` graduates past alpha** — their provider is among the
  largest known consumers; accelerate #58 and copy the security scaffolding.
- **A Vikunja, Forgejo, or KEDA integration appears in their tree** (all zero
  today) — would mean they are entering this niche deliberately.
- **Open-core drift**: if the cloud tier starts gating what is MIT today, the
  design-mine value degrades and this dossier's Tier-2/3 references should be
  captured locally while they remain readable.

## 10. Unresolved (honest gaps in this sweep)

1. **OpenClaw was not identified.** It anchors their positioning (*"If
   OpenClaw is an employee, Paperclip is the company"*) and ships as the
   `openclaw_gateway` adapter, but the agent tasked with identifying it died
   before reporting. Not speculated on here.
2. **Interaction API internals were not read.** §6.2's routable-blocking rule
   is quoted from the semantics doc, not verified against the implementation;
   the `request_confirmation` / `ask_user_questions` /
   `request_checkbox_confirmation` / `suggest_tasks` kinds are confirmed only
   as constants (`packages/shared/src/constants.ts:256`) and Zod schemas.
   Verify before implementing #86 against their model.
3. **Adoption evidence beyond GitHub metrics** (Discord size, production users
   other than the vendor, third-party adapters beyond the one npm Droid
   adapter) was not gathered — the ecosystem agent died first. Star count is
   not deployment count.
