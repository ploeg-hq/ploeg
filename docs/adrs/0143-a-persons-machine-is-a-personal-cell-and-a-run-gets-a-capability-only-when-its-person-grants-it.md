---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A person's own machine is a personal cell that runs only that person's Runs, and a Run gets a named capability only when the person grants it

## Context and Problem Statement

Every Run executes in a cluster today: a worker pod under the strictest isolation class the cell can honour ([ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md)), behind a default-deny network policy ([ADR-0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md)), with its harness in a container that holds no credential ([ADR-0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md)). That is right for most work and wrong for some. A Run cannot reach what only a person's machine has: a secret the person keeps locally, a Docker engine, a GPU, a browser with the person's test accounts, a company VPN, or MCP servers the person installed. The only way to give a Run more today is a Team-wide setting that every Run of the Team then has.

The owner asked for agents that can run on a person's own machine "whenever you want, maybe to give it more rights". [ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md) already lets a person's own client carry a `claude-environment` Run: the person claims it and dispatches it with their own Claude login, and the session then runs in the Team's environment, not on the machine.

How does a person register their own machine as a place where their Team's Runs may execute, when does Ploeg route a Run there, what happens when the machine sleeps, and how does a Run ask for one more right without the person handing over the machine?

## Decision Drivers

* **Capacity pulls, authority never dials in** ([ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md)). A laptop sits behind NAT and sleeps. It must work without an inbound port.
* **A machine executes only for its own person.** A Run that acts for one person never runs on another person's machine, and never on another person's Claude seat ([ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md)).
* **Ploeg stores and relays no Claude login**, on the machine or in ploegd. Anthropic's terms are met under every reading, as ADR-0112 requires.
* **The run protocol stays the run protocol.** Claim, renew, checkpoint and outcome ([executor contract](../contracts/executor.md)), the branch fence, the leak watch and verification ([worker control](../contracts/worker-control.md)) hold on a laptop as in a pod.
* **A right is named, granted once by the person, scoped to one Run and audited** ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)). Some rights are never grantable.
* **Spend stays authorized spend** ([ADR-0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)). A Run on a laptop holds the same budget as one in a pod, or says plainly that a person's own seat pays.
* **Ploeg knows no consumer and no vendor** ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)). People reach Ploeg through operator consumers.

## Considered Options

* **A personal cell: the machine runs `ploeg-worker` and pulls Runs that act for its owner, like a cell of ADR-0107**
* A wider client carrier: the person's own tool claims the Run and runs the harness itself, then reports the outcome
* A tunnel to the machine: the Run stays in a pod and reaches the laptop's Docker, MCP servers and network through a connection the laptop opens
* A full `ploeg-cell` chart on a local Kubernetes (k3s, kind) on the machine
* No laptop: add the missing capabilities to the cluster instead (GPU nodes, secrets in the vault, more egress)

## Decision Outcome

Chosen option: "**a personal cell**", because it is ADR-0107's cell with one owner: the machine pulls, ploegd compares strings, and the worker that enforces the branch fence, the leak watch and verification is the same binary. It is also the only option that reaches what only the machine has (its VPN, its browser, its locally installed MCP servers) without opening the machine to the cluster.

### 1. Registration and identity

* **Enrolment.** `ploeg-worker cell enrol --hub <url>` generates an Ed25519 key pair on the machine and asks ploegd for an enrolment ticket: `POST /api/v1/cells/personal/enrolments` with the public key, a machine label and the capabilities the machine offers. ploegd answers a ticket id, a short user code (RFC 8628 alphabet) and a ten-minute expiry. The machine prints the code.
* **Approval is a person's operator action.** The person approves the ticket through an operator consumer, which shows the code, the machine label and what it offers: `POST /api/v1/operator/cells/personal/enrolments/{id}/approve` with `X-Ploeg-Acting-User`, recorded with person, channel and confirmation (ADR-0108). Only the acting person can approve, and the approved cell belongs to them. An administrator can deny or revoke, never approve for someone else.
* **Short-lived capability.** The machine proves possession of its key for each capability: it signs a ploegd challenge and receives a cell capability for at most one hour, scoped to (cell, owner) and the worker-control audience, with key generations so one cell rotates alone. Every request after that carries the capability and, like a DPoP proof (RFC 9449), a signature over the method, path and time, so a copied capability is useless off the machine.
* **Revocation.** The owner or an administrator revokes a cell with `DELETE /api/v1/operator/cells/personal/{id}`. ploegd refuses new capabilities at once, every renew of a Run that cell holds answers 404 (the worker kills the harness, as the executor contract says), and each such Run's forge credential and model key are blocked as at settle. A capability already issued dies within the hour. A cell also lapses after 30 days without a capability exchange, and when its owner is no longer known to any operator consumer.
* **Storage on the machine.** The private key sits in the operating system's key store (macOS Keychain, Windows DPAPI, the Secret Service on Linux), or in a 0600 file when none is available, which the cell logs at start.

### 2. What a personal cell advertises and where a Run goes

* **Opaque capability strings**, as ADR-0107's cells already advertise: `docker`, `gpu:<kind>`, `browser`, `vpn:<name>`, `mcp:<server>`, `secret:<name>`, `os:<os>/<arch>`. They are labels, not grants: a `secret:` label says the machine can provide a secret under that name, never its value.
* **The class `personal`.** A personal cell advertises the isolation class `personal`. It sits outside ADR-0106's ladder: a floor comparison never selects it and never rejects it. A Run lands on a personal cell only when its Team's manifest allows the placement ([ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) `may`, action `place`, resource `personal`, `allow` or `ask`), and the Run's label says `personal` wherever it is shown. A Team that says nothing never runs on a laptop.
* **Only the owner's Runs.** Demand and reservation (ADR-0072) answer a personal cell only with Runs whose acting person is the cell's owner: an attended Run's `X-Ploeg-Acting-User`, or tracker work's matched tracker user (ADR-0095, ADR-0112).
* **When a Run goes there.** A Run is placed on its person's machine in three cases, and in no other:
  1. **It needs what only the machine has.** A Role declares `requires: [docker, vpn:corp]`. Any cell that advertises every string qualifies, cluster cells included, so a GPU node in the cluster serves `gpu:` before a laptop does.
  2. **The person asks.** An operator admission carries `placement: personal` (optionally a cell id), or the person moves a pending Run to their machine with `POST /api/v1/operator/runs/{id}/placement`. Both are operator actions.
  3. **The Team prefers it.** `placement.prefer: personal` with `fallback: cluster | wait` and `fallbackAfter`. A Run that prefers a laptop and finds none online waits up to `fallbackAfter` (default 10 minutes), then goes to the cluster or keeps waiting.
* **Not reachable is a wait, not an error.** A Run that requires a capability no online cell offers waits as `needs_human` with the reason `machine_offline` and the cell it needs, the same way ADR-0106 makes an unmet floor visible.

### 3. How a Run is offered and claimed

This generalises ADR-0112's client carrier. A Run gets a `carrier`:

| `carrier` | Who claims | Where the harness runs | Who pays the model |
| --- | --- | --- | --- |
| `worker` (today's default) | a cell's worker pod | the cell | the Team's budget, per-Run key |
| `client` (ADR-0112) | the person's client through the operator API | the Team's Claude environment | the person's own seat |
| `personal` (new) | the person's personal cell | the person's machine | the Team's budget, or the person's own seat (section 5) |

A personal cell long-polls `GET /api/v1/executor/demand` with its capability, reserves with `POST /api/v1/runs/reserve`, and starts one `ploeg-worker` per reservation. That worker claims with the cell capability and runs the ordinary run protocol: renew at a third of the lease, checkpoint, exactly one outcome. Its worker identity is `cell:<owner>/<cell-id>`. One spawn is one auditable Run, and a cell never retries a Run itself. A cluster worker never claims a `personal` Run, and a personal cell never claims a `worker` Run.

`maxRunning` per personal cell defaults to 2. The Team cap and the cell cap are both enforced in the claim transaction, as ADR-0107 does for cells.

**On the machine.** The worker runs as the person's user. The harness runs in its own rootless container (Podman or Docker in rootless mode), the shape of ADR-0138 with a container runtime in place of a pod: the worker process keeps the credentials and both loopback proxies, the harness gets placeholders and a network that reaches only the worker's proxies and the local egress proxy. The local egress proxy renders the same allowlist ADR-0137 derives from the URLs (ploegd, the model gateway, the forge), by host name, which a laptop can do and a NetworkPolicy cannot. A person may set `harness.local: native` on their cell to run the harness as a plain process instead, for a machine without a container runtime. The Run then records `native` in its label and its outcome, and a Team may refuse native execution with `may` (`place`, `personal:native`, `deny`).

A Team's pinned Claude Code plugins ([ADR-0139](0139-a-team-declares-claude-code-plugins-pinned-to-a-commit-whose-hooks-run-guarded-with-only-their-declared-environment.md)) load exactly as in a pod. The person's own plugins, settings and MCP servers do not load; one of them reaches a Run only as a granted capability (section 6).

### 4. Leases, sleep and going offline

A laptop sleeps, loses Wi-Fi and is closed mid-Run. The design tells three cases apart:

* **Yield.** When the cell knows it is going away (the person runs `ploeg-worker cell pause`, the operating system announces sleep or shutdown, the battery falls under a threshold), the worker stops the harness, pushes a checkpoint ref for a writing Run ([ADR-0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md)), and reports outcome `yielded`. ploegd requeues the Run without charging the attempt budget, keeps its Shift, and routes the retry by section 2: back to the same machine when it needs that machine, to the cluster when its Team allows the fallback. The cell may inhibit sleep while a Run is running (`systemd-inhibit`, `caffeinate`, `SetThreadExecutionState`) when the person turns `inhibitSleep` on. It is off by default.
* **Lapse.** When renewals simply stop, the Lease runs out. A `personal` Run's Lease carries `personal.leaseGrace` (default 15 minutes) on top of its TTL, so a short sleep or a train tunnel does not end it. After the grace, the sweeper ends the Run `lost` with failure reason `infra_personal_offline`, which counts against the infra retry budget ([ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)), never the agent's. Its model key and forge credential are blocked at once. The retry starts from the last checkpoint and is routed by section 2.
* **Wake after loss.** A worker that wakes and gets 404 on renew kills the harness and never pushes again; its forge credential is already blocked. It keeps the workspace for the person to inspect and removes it after `personal.keepLostWorkspaces` (default 7 days).

A Run never resumes on a machine after its Lease is gone. A paused attended Run ([ADR-0101](0101-a-paused-run-keeps-its-pod-and-session-for-a-bounded-time-and-is-never-retried-automatically.md)) is the exception that already exists: its Lease is held while it is paused, on a laptop as in a pod.

### 5. Budget and settlement

* **On a Team key (the default).** Exactly as in a pod: the admission reserves the authorization, the worker mints one per-Run key through `POST /api/v1/runs/{token}/llm/credential`, the harness gets only a placeholder behind the worker's model proxy, and the settlement sweep reconciles from the gateway's spend log ([worker control](../contracts/worker-control.md#accounting-states)). The key is valid only for the Run and blocked at settle or revocation. The machine's electricity and hardware are the person's and are not metered.
* **On the person's own seat.** A Run whose Role binds the `claude-code` harness may set `seat: person`. The harness then runs the unmodified `claude` binary with the person's own Claude Code configuration directory, which the cell mounts into the harness container read-write and never opens, copies or sends. No authorization is reserved and no key is minted, as for an ADR-0112 client-carried Run, and the Run's spend reads "paid by the person's seat", never zero. This is allowed only under the conditions in section 7.
* **Nothing else changes.** Shift pools, Role caps and the periodic allowance hold for laptop Runs as for pod Runs. A capability grant never changes a budget; raising a budget is its own operator action.

### 6. Capabilities on request

A Run that hits a wall can ask for one more right, by name.

* **The request.** The harness asks through the existing interaction relay ([ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)) with a new request kind, `capability`, carrying exactly one item from a fixed vocabulary and a one-line reason:

  | Capability | What the cell does when it is granted |
  | --- | --- |
  | `egress:<host>[:<port>]` | adds one host name, never a wildcard or an address range, to the Run's local egress allowlist |
  | `secret:<name>` | puts the value the person stored under that name on the machine into the harness's environment or a 0400 file, and adds it to the leak watch |
  | `docker` | starts a rootless engine inside the harness's own sandbox, never the host's socket |
  | `gpu:<kind>` | passes the device into the harness container |
  | `mcp:<server>` | starts one MCP server the person listed on the machine, with the environment the person gave it, and adds it to the Run's MCP config |
  | `browser` | starts a fresh browser profile with remote debugging inside the sandbox; the person's own profile, cookies and password store never |
  | `vpn:<name>` | routes the Run's egress through a VPN interface the person named |
  | `path:<dir>:ro` or `:rw` | mounts one directory the person listed on the machine |

* **Who decides.** The Team's manifest decides first: `may` with action `capability` and a pattern (`docker`, `egress:*.internal.example`, `secret:STAGING_*`) and effect `allow`, `ask` or `deny`. `deny` cannot be widened by anyone. `allow` grants without asking. `ask`, the default, relays the request to the person the Run acts for, through any operator consumer, which shows the Run, the capability, the reason and what granting means. An unanswered request expires by the Team's answer timeout and is refused.
* **The person approves once, for that Run.** `POST /api/v1/operator/runs/{id}/capabilities/{request}/grant` or `/refuse`, an operator action with person, channel and confirmation (ADR-0108). ploegd signs the grant for that cell and that Run; the cell checks the signature, the Run and its own identity before it applies anything. A grant ends with the Run: it does not carry to a retry, another Round or another Work Item. The person may tick "for the rest of this Shift", which lets later Runs of the same Shift ask again and be answered at once by the recorded grant; each use is still logged.
* **The machine's own word.** The person's cell configuration lists what the machine will ever offer (`offers:`) and what it never will. A grant for something the cell does not offer is refused by the cell, whatever ploegd signed.
* **Audit.** Every request, grant, refusal, expiry and use is a Run event with the person, the channel, the capability and the reason. Secret values and file contents are never in an event, a transcript or a log; the transcript ([ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)) redacts granted secret values as it redacts credentials. The cell also keeps a local, append-only log of what it applied.
* **Never grantable.** No Team setting, no person and no administrator can grant these. The cell refuses them before ploegd is asked, and ploegd refuses to sign them:
  1. a Claude login, a claude.ai session token or the person's Claude configuration directory as a value (only the seat mode of section 5 uses that directory, and never as a capability);
  2. any Ploeg credential: the cell key or capability, a bootstrap token, another Run's token or control capability, the gateway's master key, a signing key;
  3. forge authority beyond the Run's branch fence: pushing another branch or a tag, merging, deleting branches, settings, protection rules, keys, members or another repository;
  4. another person's secret, cell, seat or session;
  5. the host: a privileged container, the host's Docker or Podman socket, the host network namespace, host PID, `/`, the person's whole home directory, `~/.ssh`, an SSH or GPG agent socket, the operating system's key store, a browser's real profile;
  6. wildcards and ranges: `egress:*`, an IP range, the cloud metadata address (169.254.169.254 and its IPv6 form), the machine's LAN, loopback services other than the worker's own proxies;
  7. switching off a control: the leak watch, the transcript, the branch fence, verification, the budget or the Lease;
  8. a longer life: a grant that outlives its Run, or a capability that renews itself.

### 7. Anthropic's terms, and what needs an API key

Anthropic's [legal and compliance page](https://code.claude.com/docs/en/legal-and-compliance), read on 2026-10-24, says:

* OAuth sign-in "is intended exclusively for purchasers of Claude Free, Pro, Max, Team, and Enterprise subscription plans and is designed to support ordinary use of Claude Code and other native Anthropic applications".
* Developers building products or services, including with the Agent SDK, "should use API key authentication"; Anthropic does not permit them "to route requests through Free, Pro, or Max plan credentials on behalf of their users", and they "may not collect, store, or intermediate Claude.ai credentials or session tokens".
* This does not prevent "an end user from signing in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code".
* Running Claude Code in a product or service needs the Commercial Terms, the binary must not be modified, and "each end user must authenticate with their own Anthropic API key, Claude subscription plan credentials, or 3P inference provider credential".
* "Advertised usage limits for Pro and Max plans assume ordinary, individual usage of Claude Code and the Agent SDK."

The [Consumer Terms](https://www.anthropic.com/legal/consumer-terms) (Free, Pro, Max; dated 2025-10-08) forbid sharing "Account login information" or making the account "available to anyone else", and accessing the services "through automated or non-human means, whether through a bot, script, or otherwise" except with an API key "or where we otherwise explicitly permit it". Anthropic's own documentation permits scripted use of a person's own subscription in Claude Code: `claude setup-token` makes a token "for CI pipelines, scripts, or other environments where interactive browser login isn't available", and background and Remote Control sessions run unattended on a person's login ([authentication](https://code.claude.com/docs/en/authentication)). The [Commercial Terms](https://www.anthropic.com/legal/commercial-terms) (Team, Enterprise, API) make the customer responsible for all activity under its account.

What follows for a personal cell:

| | Allowed? |
| --- | --- |
| A Run on its owner's machine, acting for its owner, with the unmodified `claude` binary and the owner's own login, which the owner started or asked for (attended, `placement: personal`, or a Run the owner moved to the machine) | **Allowed.** It is the end user's own sign-in to the unmodified binary, for their own work. Ploeg never touches the login. |
| The same with the owner's long-lived `claude setup-token` token, kept by the owner in the machine's secret store and read by the `claude` binary | **Allowed, with care.** Anthropic documents the token for scripts. The cell treats it as the owner's configuration, not as a capability, and never sends it anywhere. |
| A Run on the owner's seat that acts for anyone else, or that another person's assignment started | **Not allowed.** That makes the account available to someone else (Consumer Terms) and is not the end user's own credential (Commercial conditions). It needs an API key. |
| The cell draining a queue of tracker work on the owner's Pro or Max seat, on a timer, with nobody starting it | **Not settled.** Ordinary individual use is the stated basis of Pro and Max limits, and automated access is allowed only where Anthropic permits it. This is the batch question ADR-0112 put to Anthropic on 2026-10-18. Until it is answered, `seat: person` applies only to Runs the owner started or asked for, and tracker work on a laptop runs on a Team key. |
| Team or Enterprise seats | **Allowed on the same lines**, under the Commercial Terms; the organisation's administrator can also restrict Claude Code by managed settings, which the cell does not override. |
| Ploeg storing, copying, relaying or reading the login, a session token or the configuration directory | **Not allowed.** ADR-0112 forbids it; the cell mounts the directory into the harness container and never opens it. |
| Any harness Ploeg builds on the Agent SDK, any OpenCode, Goose or Qwen Code Run, and any unattended work | **Needs an API key**: the Team's per-Run gateway key, as today. |

### Consequences

* Good, because a Run can finally use what only a person's machine has, and the person decides each extra right as it is needed, for one Run, with a record of who granted what.
* Good, because a personal cell is ADR-0107's cell with an owner, so demand, reservation, the run protocol, the branch fence, the leak watch and settlement need no second design.
* Good, because a laptop that sleeps loses nothing: a yield is free, a lapse costs an infra retry and resumes from a checkpoint.
* Good, because work a person starts can run on their own Claude seat on their own machine, inside Anthropic's terms, with no login in Ploeg.
* Bad, because the person's machine is a weaker boundary than a Kata pod. The rootless container narrows what the harness sees; it does not stop a person, or malware on their machine, from reading the Run's per-Run key and forge token in the worker's memory. Accepted: both are scoped to that person's own Run, branch-fenced and blocked at settle, and the person could already do that work by hand. A Team that cannot accept it leaves `place: personal` out.
* Bad, because ploegd's executor routes must be reachable from the internet over TLS for a laptop outside the estate. They already are for remote cells under ADR-0107; a deployment that keeps ploegd private needs a VPN for its laptops.
* Bad, because every capability in the vocabulary is code on three operating systems. The first increment ships `egress`, `secret` and `docker` on Linux and macOS only.
* Bad, because the seat mode cannot verify whose login is in the mounted directory. The cell checks nothing in it by design. Claude Code's own `/status` reports the account in its telemetry ([ADR-0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md)), which an operator consumer can show next to the Run.
* Neutral: a personal cell can run on a desktop, a workstation under a desk or a home server as well as a laptop. "Laptop" is the common case, not a limit.

### Confirmation

* `go test ./pkg/httpapi/`:
  * enrolment needs the acting person's approval, an administrator cannot approve for someone else, and a revoked cell's next renew answers 404;
  * demand and reservation for a personal cell answer only Runs whose acting person is its owner, and only for Teams whose `may` allows `place: personal`;
  * a cluster worker never claims a `personal` Run and a personal cell never claims a `worker` Run;
  * a Run that `requires` a capability no online cell offers waits `needs_human` with `machine_offline`;
  * a `yielded` outcome requeues without charging the attempt budget, and a lapse past `personal.leaseGrace` ends `lost` with `infra_personal_offline` and charges the infra budget;
  * a capability grant is refused for every entry of the never-grantable list, is signed for one cell and one Run, and does not carry to a retry.
* `go test ./pkg/store/`: no column or migration holds a Claude credential or a granted secret's value; ADR-0112's test stays green.
* `go test ./pkg/worker/`: the cell refuses a grant it does not offer, a grant signed for another cell or Run, and every never-grantable capability; a granted secret is in the leak watch; the harness container has no route except the worker's proxies and the egress proxy.
* `go test ./internal/ledger/` keeps this record's Confirmation and index row.
* A qualification record in `docs/research/` cites, on one Linux and one macOS machine: a Run completed on a personal cell with a Team key; one yielded on sleep and finished on the cluster; one that asked for `secret:` and `egress:`, was granted, used both, and whose transcript shows neither value.

## Pros and Cons of the Options

### A wider client carrier

The person's own tool claims the Run, runs the harness however it likes, and reports the outcome, as ADR-0112's "register a session it already runs" does.

* Good, because it is little new code in ploegd: one more `carrier: client` case.
* Bad, because ploegd would trust the client's outcome. The branch fence, the leak watch, verification and the transcript live in `ploeg-worker`; a client that runs its own harness has none of them unless it re-implements them.
* Bad, because spend on a Team key would be reported by the client, not observed by the gateway account.

### A tunnel to the machine

The Run stays in a pod; the laptop opens a connection and serves Docker, MCP servers and its network through it.

* Good, because the Run keeps the pod's isolation class.
* Bad, because a pod then reaches into a person's machine, which is authority dialling in by another name, and a compromised Run gets a path into the person's LAN and VPN.
* Bad, because a browser, a GPU or a VPN do not tunnel well, and every call pays the round trip.

### A full `ploeg-cell` chart on a local Kubernetes

* Good, because nothing new is built: the person installs ADR-0107's cell chart on k3s or kind.
* Bad, because a laptop then runs a Kubernetes control plane for a handful of Runs, which costs battery and memory most people will not give, and Docker Desktop's licence is a cost on its own for larger companies.
* Bad, because the cell would still be one person's, so it needs every ownership rule of this record anyway. It stays possible for a workstation: a person can point a local cell chart at their own personal-cell identity.

### No laptop: add the capabilities to the cluster

* Good, because the cluster's isolation stays the only kind.
* Bad, because a company VPN, a person's locally installed MCP servers and their test browser cannot move to the cluster.
* Neutral: GPUs, Docker-in-sandbox and secrets in the vault can, and section 2 prefers a cluster cell that offers them.

## Re-evaluation triggers

* Anthropic answers the 2026-10-18 question of ADR-0112, on batch starts or on automated use of a person's own seat; section 7's "not settled" row then moves.
* Anthropic offers delegated or session-only authorization a cell could hold without a login.
* A rootless runtime on macOS (Apple's container framework, Podman machine) or Windows changes what the harness container can isolate.
* A person's cell is involved in a credential leak, or ten personal-cell Runs in a month lapse without yielding.
* A Team asks for a capability outside the vocabulary three times; then the vocabulary, not a free-form grant, grows.

## More Information

* Phased plan, each phase releasable alone:
  1. **Identity.** Enrolment, approval, the cell key and capability, revocation, the `personal` class and `may: place`, Team keys only, native harness only. An operator consumer needs: an approval page for the code, and a list of a person's cells with revoke.
  2. **Containers and placement.** The rootless harness container, the local egress proxy, `requires`, `placement: personal` on admission and the move route, `machine_offline`. An operator consumer needs: a placement choice when a person starts work, and the waiting reason.
  3. **Sleep.** `yielded`, `personal.leaseGrace`, `infra_personal_offline`, `inhibitSleep`. An operator consumer needs: to show a yielded or offline Run as waiting for its machine.
  4. **Capabilities.** The `capability` request kind, grants, the vocabulary's first three entries, the never-grantable list, audit events. An operator consumer needs: to show a capability request to its person and send the grant or refusal with their confirmation.
  5. **Seat mode.** `seat: person` for Runs the owner started, after a review of Anthropic's answer if one has come.
* Related: [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md), [ADR-0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md), [ADR-0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md), [ADR-0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md), [ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md), [ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md), [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md), [ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md), [ADR-0101](0101-a-paused-run-keeps-its-pod-and-session-for-a-bounded-time-and-is-never-retried-automatically.md), [ADR-0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md), [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md), [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md), [ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md), [ADR-0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md), [ADR-0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md), [ADR-0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md), [ADR-0139](0139-a-team-declares-claude-code-plugins-pinned-to-a-commit-whose-hooks-run-guarded-with-only-their-declared-environment.md) (proposed in pull request #234).
* 2026-10-24 — [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) amended: a Run runs at the class it declares, not the strictest class a cell can honour, as the Context above still says.
* Questions for the owner, each a decision:
  1. **Reachability.** (a) ploegd's executor and cell routes on a public TLS host name, (b) laptops only over a VPN or tailnet, (c) an operator consumer relays cell traffic. Recommended: (a), the routes ADR-0107 already needs, rate-limited per cell.
  2. **Harness on the machine.** (a) rootless container always, (b) container by default with `native` as the person's opt-in, (c) native only. Recommended: (b).
  3. **Grant scope.** (a) one Run only, (b) one Run with "for the rest of this Shift", (c) standing grants per person and Team. Recommended: (b); (c) is what `may: allow` in the manifest is for.
  4. **Seat mode.** (a) never, Team keys only, (b) Runs the owner started or asked for, (c) also tracker work the owner assigned. Recommended: (b) now, (c) only after Anthropic answers.
  5. **Offline fallback.** (a) a Run that does not need the machine goes to the cluster after `fallbackAfter`, (b) it waits for the machine. Recommended: (a) as the default, (b) per Team.
  6. **Lease grace.** 5, 15 or 30 minutes. Recommended: 15.
  7. **First operating systems.** (a) Linux and macOS, (b) all three including Windows through WSL. Recommended: (a).
