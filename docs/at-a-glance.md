---
type: explanation
audience: [buyer, owner, operator]
owner: ploeg
last_verified: 2026-10-30
verified_by: "Every bullet checked against reference/features.md at 9c70ae5 for its status and against reference/support-matrix.md for any support claim; novelty claims checked against research/2026-09-26-agent-orchestration-landscape.md and cncf/ecosystem-fit.md; standards checked by grep over pkg, cmd and ops"
---

# Ploeg at a glance

For platform engineers who want to run AI coding agents on their own Kubernetes, with budgets, isolation and an audit trail.

Ploeg is a self-hosted service that takes a ticket from your tracker, runs a coding agent on it inside your cluster, and hands back a pull request for a person to review. It decides whether the work may run, pays for it from a budget you set, and keeps a record of everything that happened.

Ploeg is experimental: it is at 0.x, has no stable release yet, and the [support matrix](reference/support-matrix.md) names exactly which paths are supported and tested.

- **Spend you authorize up front.** Every Run gets a budget before it starts, and its cost settles afterwards from the model gateway's own records.
- **Agents that never hold your keys.** Model and push credentials are minted per Run and kept outside the agent's container.
- **Your cluster, your tracker, your forge.** It installs with Helm on Kubernetes and works from the tracker and the code host you already use.

A few words used below, in Ploeg's own terms ([glossary](domain/glossary.md)): a **Work Item** is one unit of work, usually a ticket. A **Run** is one agent working on it once. A **Role** is what that Run does (write code, review, plan). A **Shift** is the whole attempt at a Work Item, made of one or more **Rounds** of Runs. A **Team** is a configured group of Roles with its own budget and limits. A **Harness** is the agent tool a Run drives, such as OpenHands or OpenCode.

Each item carries a status:

- `Available`: in the current release, on by default or turned on in configuration.
- `Experimental`: in the source, but outside the supported profile or not exercised by any test or live record.
- `Planned`: a proposed decision with no code yet.

Each item links to its entry in the engineering list, [Ploeg features by bounded context](reference/features.md), where the code and decision records behind it are cited.

## In ten lines

- **A budget before every Run.** Ploeg authorizes a spending limit before a Run starts and settles what it actually cost from the model gateway's spend logs. `Available` · [features](reference/features.md#hold-and-budget)
- **One capped model key per Run.** Each Run gets its own gateway key, limited to its budget and revoked when the Run ends. `Available` · [features](reference/features.md#inference-account-and-cost-status)
- **The agent never sees the credentials.** Loopback proxies hold the model key and the push token, so the agent's container holds only placeholders. `Available` · [features](reference/features.md#credential-isolation-in-the-agent-container)
- **Pushes only to its own branch.** The push proxy refuses pushes to any other branch and refuses branch deletions. `Available` · [features](reference/features.md#credential-isolation-in-the-agent-container)
- **Spend caps per Team.** A Team stops claiming new work once its daily, weekly or monthly cap would be passed. `Available` · [features](reference/features.md#teams-plans-and-shifts)
- **Ticket in, pull request out.** Assign a GitHub Issues, Vikunja or ClickUp ticket and a Run opens a pull request on GitHub or Forgejo. `Available` · [features](reference/features.md#tracker-intake-and-write-back)
- **A second agent reviews the first.** A Plan can add reviewing Roles on a different model, and their requested changes send the work back for a fix Round. `Available` · [features](reference/features.md#teams-plans-and-shifts)
- **Ready means CI passed on the forge.** A pull request counts as ready only when your forge's CI passed on its head, and a person still merges it. `Available` · [features](reference/features.md#forge-ci-readiness-and-review-watch)
- **An audit row for every change.** Each state change commits its audit record in the same database transaction, and each Run keeps a redacted transcript. `Available` · [features](reference/features.md#ploegd-process-and-store-plumbing)
- **Installs as a signed Helm chart.** Signed images and chart, scale-to-zero workers through KEDA, and default-deny network policies for every pod. `Available` · [features](reference/features.md#deployment-helm-chart)

## Integrations

**Works with trackers**

- **GitHub Issues as the work queue.** A `ploeg:<team>` label or an assignee hands an issue to a Team, and comments and closing are written back. Qualified live against github.com. `Available` · [features](reference/features.md#tracker-intake-and-write-back)
- **Vikunja and ClickUp boards.** Assigning a ticket starts the work, and statuses and comments flow back; tested in CI against fakes, not yet against a live instance. `Available` · [features](reference/features.md#tracker-intake-and-write-back)
- **Your board's statuses mapped to delivery gates.** Board columns map to development, test, acceptance and done, and every move is recorded. `Available` · [features](reference/features.md#tracker-intake-and-write-back)
- **Work without a tracker.** An operator console of your choice can submit work through the published operator API, with no ticket needed. `Available` · [features](reference/features.md#intake-routing-rules-and-admission)
- **Forgejo or GitLab issues as a tracker.** Not implemented; Forgejo and GitLab work as forges only today. `Planned` · [features](reference/features.md#tracker-intake-and-write-back)

**Works with forges (code hosts)**

- **GitHub through a GitHub App.** Each Run pushes and opens its pull request with an installation token minted for it alone; qualified live against github.com. `Available` · [features](reference/features.md#push-credential)
- **Forgejo 15 or later.** Each Run gets a repository-scoped token minted for it; tested in CI. `Available` · [features](reference/features.md#push-credential)
- **GitLab.** Implemented as a forge, but outside the supported profile because it has no per-Run token broker yet. `Experimental` · [features](reference/features.md#forge-intake-facts-and-write-back)

**Works with agent tools (Harnesses)**

- **OpenHands through ACP.** Qualified live with both credential proxies on. `Available` · [features](reference/features.md#choosing-a-harness)
- **OpenHands image and any binary of your own.** The `openhands` image and the generic `exec` adapter are in the supported profile; `exec` runs any program that reads the published Task Spec. `Available` · [features](reference/features.md#choosing-a-harness)
- **OpenCode, Qwen Code, Goose, Claude Code, and Codex or Gemini CLI through ACP.** Adapters are in the source and run in the scheduled attack drill, but are outside the supported profile. `Experimental` · [features](reference/features.md#choosing-a-harness)

**Works with model providers**

- **Any provider behind LiteLLM.** Every model call goes through a LiteLLM gateway (1.103.0 or later), so any provider LiteLLM supports can sit behind it. `Available` · [features](reference/features.md#executors-and-sandboxes)
- **A second gateway, such as Agent Router.** Proposed as a gateway contract with two implementations. `Planned` · [features](reference/features.md#inference-account-and-cost-status)

**Works with operator tools and identity**

- **Any operator console, through the operator API.** A versioned, published API with JSON schemas and a Go client covers every read and action. `Available` · [features](reference/features.md#published-language-for-operator-consumers)
- **AI clients over MCP.** `ploeg-mcp` serves Ploeg's reads as MCP tools to a person's own AI client; read-only use is supported. `Available` · [ask Ploeg from an AI client](how-to/ask-ploeg-from-an-ai-client.md)
- **Your identity provider.** `ploeg-mcp` runs as an OAuth 2.1 resource server and maps your IdP groups to access; the operator API can require a person's IdP token on every change. `Available` · [features](reference/features.md#operator-consumers)
- **Prometheus and OpenTelemetry.** Prometheus metrics and alert rules for Ploeg itself, and each Harness's own OpenTelemetry export pointed at your collector. `Available` · [features](reference/features.md#http-hardening-health-and-metrics)

## You stay in control

- **Budgets at three levels.** A Shift has a pool, each Role a per-Run cap, and each Team a period spend cap, with a chart default of 20 USD a day. `Available` · [features](reference/features.md#teams-plans-and-shifts)
- **Concurrency limits per Team.** `maxRunning` caps how many Runs a Team has going at once. `Available` · [features](reference/features.md#teams-plans-and-shifts)
- **Stop at any time.** A person can withdraw a Work Item, cancel its pending Runs and stop the running ones; unassigning the ticket does the same. `Available` · [features](reference/features.md#intake-routing-rules-and-admission)
- **Take over and hand back.** A person asks a running Run to stop at a safe point, works on the branch, then hands it back to a new Shift. Works with ACP Harnesses. `Available` · [features](reference/features.md#teams-plans-and-shifts)
- **Agents ask, people answer.** A Run that needs a decision asks a person and the Work Item waits; the answer reaches the next Run. `Available` · [features](reference/features.md#blackboard-and-briefing)
- **Steer with a note.** A person's instruction on a live Work Item reaches the next Run, and a stopped Work Item can restart from a chosen Round. `Available` · [features](reference/features.md#blackboard-and-briefing)
- **Agent-proposed work waits for approval.** Work Items an agent creates are proposed until a person approves them, within limits on count, depth and budget. `Available` · [features](reference/features.md#follow-ups-and-run-created-work-items)
- **A cheap premise check before writing.** An optional zero-cost classifier asks whether a ticket is ready, needs a person's answer or is too big for one pull request. `Available` · [features](reference/features.md#intake-routing-rules-and-admission)
- **Routing you define.** Rules map each board and label to an allowed repository and Team; a ticket no rule covers is refused with a comment saying how to fix it. `Available` · [features](reference/features.md#intake-routing-rules-and-admission)
- **Confirmed actions from AI clients.** Actions through `ploeg-mcp` ask the person to confirm the exact effect, optionally with a fresh sign-in; action groups are outside the supported profile today. `Experimental` · [serve ploeg-mcp over HTTPS](how-to/serve-ploeg-mcp-over-https.md)

## Security and isolation

- **A supported deployment profile the chart enforces.** With `supportProfile: P1`, the default, the chart refuses values that leave the documented profile. `Available` · [features](reference/features.md#deployment-helm-chart)
- **Credentials minted per Run.** Model keys and forge push tokens are minted for one Run, refreshed while it works and revoked when it ends; sweeps revoke any that outlive their Run. `Available` · [features](reference/features.md#push-credential)
- **Credentials kept out of the agent's container.** The Harness runs in its own container and no credential crosses into it; qualified live against a hostile Harness for one Harness and one Role. `Available` · [features](reference/features.md#credential-isolation-in-the-agent-container)
- **Leak watch with a canary.** Each Run carries a random canary credential; if it or a real credential shows up in pushed commits, the Run stops and its pull requests are closed. `Available` · [features](reference/features.md#credential-isolation-in-the-agent-container)
- **Worker pods locked down.** A worker pod holds no Kubernetes token and reaches no metadata endpoint, API server or unapproved service; qualified live on one cluster with Cilium. `Available` · [features](reference/features.md#executors-and-sandboxes)
- **Default-deny network policies.** The chart renders a policy for every pod it creates; your CNI must enforce them. `Available` · [network policies](ops/network.md)
- **VM isolation per Run.** The `sandbox` executor runs each Run in its own Kata Containers VM; supported with guest seccomp on, though no probe has yet passed in that configuration. `Available` · [features](reference/features.md#executors-and-sandboxes)
- **The repository cannot steer the agent.** Ticket and forge text is framed as untrusted data, the repository's own hooks, plugins and MCP servers are switched off, and a reviewer reads the base branch's instructions. `Available` · [features](reference/features.md#defending-against-the-repository-under-change)
- **Hostile-repository drill on a schedule.** Every Harness runs against a hostile repository in a recurring CI job. `Available` · [features](reference/features.md#defending-against-the-repository-under-change)
- **Verifiable releases.** Images and the chart are signed keyless with cosign and carry an SBOM, build provenance, an OpenVEX document and a CVE budget verdict anyone can check without an account. `Available` · [verify a release](ops/verify-a-release.md)
- **A published threat model.** STRIDE per element, attack trees and open findings, plus a CNCF-style security self-assessment. `Available` · [threat model](security/threat-model.md)
- **TLS served by Ploeg itself.** Today the server speaks plain HTTP and the profile requires CNI or mesh encryption. `Planned` · [features](reference/features.md#http-hardening-health-and-metrics)

## Cost and spend you can see

- **Authorized, held, then settled.** The amount a Run may spend is held from the pool when it starts, and the real cost settles later from the gateway. `Available` · [features](reference/features.md#hold-and-budget)
- **Settlement that matches the gateway.** Live accounts settled within 0.0001 USD of the gateway's spend logs over four weeks on one deployment; the one exception settled at the higher spend Ploeg observed. `Available` · [accounting invariants](reference/accounting-invariants.md)
- **Every figure says how sure it is.** Each cost is marked observed, provisional, settled or unknown, and unknown spend stays open until it is reconciled. `Available` · [features](reference/features.md#inference-account-and-cost-status)
- **A usage report on each pull request.** One comment per pull request lists each Run's models, tokens, verdict, outcome, duration and cost state, updated after late settlement. `Available` · [features](reference/features.md#blackboard-and-briefing)
- **Spend per Team in metrics and alerts.** Team period spend against its cap, settled spend and a spend-spike alert. `Available` · [features](reference/features.md#deployment-helm-chart)
- **A spent shared budget parks the work.** When a gateway budget wider than the Run's own runs out, the Work Item waits instead of retrying. `Available` · [features](reference/features.md#teams-plans-and-shifts)

## Visibility and evidence

- **Run transcripts.** Messages, tool calls, plans and stages are kept per Run, with the Run's secrets and common token shapes masked. `Available` · [features](reference/features.md#run-transcript)
- **Same-transaction audit.** Every change to work state writes its audit record in the same database transaction. `Available` · [features](reference/features.md#ploegd-process-and-store-plumbing)
- **Who did what.** Every change through the operator API carries the actor, the person it acts for and the confirmed text, and the chart defaults to requiring a proven person. `Available` · [features](reference/features.md#operator-consumers)
- **Delivery read from the forge.** Whether a Run opened or updated a pull request is read from the forge, never taken from the agent's own report. `Available` · [features](reference/features.md#run-orchestration-in-the-worker)
- **Briefings you can replay.** Each Run's briefing records the versions it was built from, so it can be rebuilt to the same digest. `Available` · [features](reference/features.md#blackboard-and-briefing)
- **Findings on the pull request.** Each Round's review findings are posted to the pull request thread, and the ticket gets a summary when the Shift ends. `Available` · [features](reference/features.md#blackboard-and-briefing)
- **Delivery facts for your dashboards.** Pull request, CI, review, revert and deploy facts per Work Item, read through the operator API or a read-only database role. `Available` · [features](reference/features.md#forge-intake-facts-and-write-back)
- **Clear retention rules.** A page lists what Ploeg keeps about work, Runs, transcripts and spend, and the settings that keep prompts and code out of storage. `Available` · [data retention](ops/data-retention.md)

## Built for operators

- **Helm chart with a strict schema.** A signed OCI chart whose values schema refuses unsafe or unknown settings. `Available` · [features](reference/features.md#deployment-helm-chart)
- **Runs on Kubernetes you already have.** Kubernetes 1.30 and 1.37 are tested in CI, 1.31 to 1.36 are supported, and PostgreSQL 17 and 18 are tested. No CRDs; all state lives in PostgreSQL. `Available` · [support matrix](reference/support-matrix.md)
- **Workers scale to zero.** KEDA starts workers from the queue depth, and idle queue checks make no model calls; a CronJob executor works without KEDA. `Available` · [features](reference/features.md#executors-and-sandboxes)
- **Recovers without the dying pod's help.** Expired Leases (the exclusive right to write a branch) and Runs are reclaimed by background sweeps, and their keys revoked. `Available` · [features](reference/features.md#claim-lease-and-recovery)
- **Restore and upgrade drills in CI.** Database restore from backup and database upgrade from earlier releases run as CI drills. `Available` · [support matrix](reference/support-matrix.md)
- **Chart upgrade and rollback between releases.** Promised by the compatibility policy, not yet exercised by any job. `Experimental` · [compatibility policy](ops/compatibility.md)
- **Metrics and alerts with runbooks.** Prometheus metrics, a ServiceMonitor and 15 alert rules, each linked to a runbook. `Available` · [features](reference/features.md#deployment-helm-chart)
- **Runbooks for the bad days.** Drain workers, recover a stuck Lease or Run, recover from a failed upgrade, restore after a database outage, rotate credentials. `Available` · [operator runbooks](how-to/index.md)
- **A first Run in minutes, for free.** The quickstart installs the signed chart on a local kind cluster and completes one Run against a mock model that makes no provider calls and spends nothing. `Available` · [quickstart](quickstart.md)

## Open, self-hosted, no lock-in

- **Apache-2.0 code, CC BY 4.0 docs.** The API specifications are Apache-2.0 too. `Available` · [features](reference/features.md#release-and-supply-chain)
- **Self-hosted.** Everything runs in your cluster, and all of Ploeg's state lives in your PostgreSQL. `Available` · [architecture](architecture.md)
- **Open standards it implements.** MCP (revision 2026-07-28) for AI clients, the Agent Client Protocol v1 for agents, OAuth 2.1 resource server metadata (RFC 9728) and token exchange (RFC 8693), OpenTelemetry export from Harnesses, Prometheus metrics, OpenVEX, and the Open Knowledge Format for knowledge bundles. `Available` · [features](reference/features.md#published-language-for-operator-consumers)
- **Published, versioned contracts.** The operator API, the Run API, the executor contract and the Harness Task Spec and Outcome Report are published as schemas, and a CI check refuses a breaking change that names no decision record. `Available` · [contracts](contracts/README.md)
- **Bring your own agent.** The `exec` adapter runs any binary that reads the Task Spec and writes an Outcome Report. `Available` · [features](reference/features.md#the-contract-task-spec-in-outcome-report-out)
- **Open governance.** Public governance, maintainers file, contributor ladder and DCO sign-off; preparing for a CNCF Sandbox application, not yet filed. `Available` · [features](reference/features.md#release-and-supply-chain)

## What's new here

The claims below were checked against two dated comparisons: a survey of about 80 agent-orchestration projects on 2026-09-26 ([landscape](research/2026-09-26-agent-orchestration-landscape.md)) and the CNCF neighbours on 2026-10-27 ([ecosystem fit](cncf/ecosystem-fit.md)). Projects in this space ship weekly; read those pages for what others do better.

- **Authorize, then settle.** A Run's budget is held before it starts and settled from the gateway's logs afterwards, never below what Ploeg saw spent. Why it matters: an agent cannot run up a bill nobody approved. Evidence: neither comparison found authorize-then-settle budgets elsewhere. `Available` · [features](reference/features.md#hold-and-budget)
- **Per-Run model keys at a proxy the agent cannot bypass.** Why it matters: a leaked key is worth one Run's budget and dies with the Run. Evidence: the 2026-09-26 survey found no other project minting a per-Run model key on that date. `Available` · [features](reference/features.md#inference-account-and-cost-status)
- **One executor contract for every launcher.** KEDA, a CronJob or agent-sandbox can launch the same Run, and the launcher never decides who owns the work. Why it matters: you pick the isolation and scaling you trust without changing how work is owned. Evidence: the 2026-10-27 comparison did not find this elsewhere. `Available` · [features](reference/features.md#executors-and-sandboxes)
- **The combination on a self-hosted stack.** An external tracker and forge, a scale-to-zero Kubernetes Job per Run, a durable lease, and per-Run credentials with a budget. Why it matters: it fits teams that keep their tracker and code host in-house. Evidence: no project among the ~80 surveyed combined all four; each part on its own exists elsewhere. `Available` · [architecture](architecture.md)
- **Reviewers on a different model, plus a shadow reviewer.** A reviewer can be forced onto a different model or Harness from the writer, and a shadow reviewer records its verdict without acting. Why it matters: you can compare review quality before trusting a new setup. Built in. `Available` · [features](reference/features.md#teams-plans-and-shifts)
- **A canary credential in every Run.** Built in, and paired with closing any pull request that leaked. Why it matters: a leak is caught at push time, not after merge. `Available` · [features](reference/features.md#credential-isolation-in-the-agent-container)
- **A premise check that costs nothing by construction.** It reads the model's price before each call and refuses a reply charged to another model. Why it matters: triage runs on every ticket without a bill. Built in. `Available` · [features](reference/features.md#intake-routing-rules-and-admission)
- **Consumer contracts replayed on every change.** Each operator console records what it relies on, and every Ploeg change replays those expectations in CI. Why it matters: upgrades do not silently break your tooling. Built in. `Available` · [features](reference/features.md#published-language-for-operator-consumers)

## Coming next

Each item below is a proposed decision with no code yet.

- **TLS in ploegd.** The server serves TLS itself, so the profile no longer needs CNI or mesh encryption. `Planned` · [features](reference/features.md#http-hardening-health-and-metrics)
- **Connections stored in your secret store.** Ploeg runs the OAuth exchange with a tracker or forge itself and keeps the credential in OpenBao or Vault. `Planned` · [features](reference/features.md#ports-conformance-and-connection-secrets)
- **A second model gateway.** A gateway contract with LiteLLM and Envoy AI Gateway (now Agent Router) as implementations. `Planned` · [features](reference/features.md#inference-account-and-cost-status)
- **Tenants and tenant export.** Work, Shifts and Runs take a tenant from configuration, and a tenant can be exported as one checksummed archive. `Planned` · [features](reference/features.md#proposed-in-dispatch)
- **Isolation floor per Run.** A Run declares the strictest isolation it needs and runs only where that can be honoured. `Planned` · [features](reference/features.md#proposed-in-execution)
- **Bake-off.** One Work Item runs on several configurations, a judge recommends, and a person picks. `Planned` · [features](reference/features.md#proposed-in-dispatch)
- **A walkthrough of each change.** A writing Run shows its change in steps a person can repeat. `Planned` · [features](reference/features.md#proposed-in-harness)
- **Preview environments.** A delivered pull request gets a preview environment through one provision, status and teardown contract. `Planned` · [features](reference/features.md#forge-intake-facts-and-write-back)
- **Team manifests.** A Team becomes its own manifest of charter, Roles, permissions, intake and skills. `Planned` · [features](reference/features.md#proposed-in-dispatch)
- **The first stable release, v0.2.0.** A soaked release candidate promoted to `main` once its release gates pass. `Planned` · [support matrix](reference/support-matrix.md#release-lines)
