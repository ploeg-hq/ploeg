# Tracker agent identity: which trackers let an agent be assigned, and how it reports back, 2026-11-01

Status: research record, 2026-11-01. This is the evidence behind [ADR-0187](../adrs/0187-a-team-is-assignable-in-a-tracker-as-a-delegate-and-reports-its-work-as-a-session-of-activities.md).

**Question.** Can a tracker present a Ploeg Team as something a person assigns, without a billable seat, and give Ploeg a place on the item to report its work while it happens? Which trackers offer that today, through what path, and is there a standard Ploeg could adopt instead of a capability of its own?

**Answer.** One tracker has an open, free path (Linear). Three have agent features that are reserved for partners or their own agents (Jira, GitHub, Asana). One has a single partner beta (ClickUp). One assigns a flow to a service account (GitLab). One has an assignable agent in beta (Plane). There is no cross-tracker standard for assigning an agent or for its session; A2A v1.0 is the only protocol candidate for the session half, and the identity work at the W3C and the IETF covers who an agent is and who it acts for, not how a tracker hands it an item. So Ploeg defines a capability of its own, shaped by Linear's model, and emulates it where a tracker has nothing.

## Landscape

| Tracker | Agent path | Open to any agent | Assignable | Reports back as | Cost | Source |
| --- | --- | --- | --- | --- | --- | --- |
| **Linear** | Agent Sessions (developer preview). OAuth app installed with `actor=app`, scopes `app:assignable` (the app is a *delegate*, assignable beside the human assignee) and `app:mentionable`. Webhook `AgentSessionEvent` with `created` and `prompted`. | Yes | Yes, as delegate; the human stays assignee | Typed activities `thought`, `action`, `response`, `error`, `elicitation`; `agentSessionUpdate` sets `externalUrls`; session state `pending`, `active`, `awaitingInput`, `complete`, `stale`, `error` derived from the last activity; webhook answered within 5 s, first activity or external URL within 10 s or the session shows unresponsive; a stop signal | Free; agents are not billable users | [agents](https://linear.app/developers/agents), [agent interaction](https://linear.app/developers/agent-interaction) |
| **Jira Cloud** | Partner agents (Rovo) are GA on work items: assign or mention an agent, it works and comments. "Third-party agents via an external MCP server" is documented as a path, but what an agent Atlassian did not partner with gets (assignability, mentions, comments as itself) is unverified. Rovo usage is metered in credits from 2026-12-03. | Partners yes; third party unverified | Partners yes | Comments and work item updates | Credits from 2026-12-03 | [Collaborate on work items with AI agents](https://support.atlassian.com/jira-software-cloud/docs/collaborate-on-work-items-with-ai-agents/) |
| **GitHub Issues** | Third-party coding agents (Claude, Codex) on issues and pull requests, public preview; the agent is a partner integration, not an app anyone can register for the role. | No, partner only | Yes, partner agents | Pull requests, comments, session logs in GitHub's own UI | Copilot licensing | [About third-party coding agents](https://docs.github.com/en/copilot/concepts/agents/about-third-party-coding-agents) |
| **Asana** | AI Teammates, Asana's own; the API's assignee field accepts an Agent GID, so a task can be assigned to an agent, but only Asana's. | No | Yes, Asana's agents | Comments and task fields | Asana AI pricing | [Upcoming AI Teammates and agent API support](https://forum.asana.com/t/upcoming-ai-teammates-and-agent-api-support/1142767) |
| **ClickUp** | External AI agents: a beta with Cursor only. No app actor, no session, no open registration. | No | No | Comments (as a member) | Seats for members | [Use external AI Agents](https://help.clickup.com/hc/en-us/articles/33572865526807-Use-external-AI-Agents) |
| **GitLab** | Duo Agent Platform flows: a flow runs as a service account and starts on an Assign trigger (assign the service account to an issue or merge request). Premium or Ultimate. | Flows yes, with a service account | The service account, not an agent | Comments and merge requests as the service account | Premium or Ultimate, Duo add-on | [2026-10-10 GitLab native agent work](2026-10-10-gitlab-native-agent-work.md) |
| **Plane** | Agents (beta): assignable agents plus an Agent Development Kit for building one. | Yes, beta | Yes | Comments and activity | Plan-dependent | Plane documentation (beta) |

No tracker in the table speaks a shared protocol for any of this. Each has its own webhook, its own write path and its own idea of what the agent is (delegate, partner, teammate, member, service account).

## Standards

| Candidate | What it covers | Verdict for ADR-0187 |
| --- | --- | --- |
| **A2A v1.0** (Linux Foundation, Agentic AI Foundation) | Task lifecycle between agents: `SUBMITTED`, `WORKING`, `INPUT_REQUIRED`, `AUTH_REQUIRED`, `COMPLETED`, `FAILED`, `CANCELED`, `REJECTED`; messages, artifacts, push notifications; an Agent Card for discovery. No notion of a delegate on a tracker item. | The only protocol candidate for the session half, and a close match for it. No tracker speaks it, so it is not the wire protocol; Ploeg's session states map one to one onto its task states so an A2A adapter stays possible ([ADR-0007](../adrs/0007-a2a-adopt-nothing-watchlist-a-facade.md)). [Specification](https://a2a-protocol.org/latest/specification/). |
| **W3C AI Agent Protocol Community Group** | Agent identity, discovery and delegation of authority between agents and services. | Identity and delegation only; nothing about assignment or a session on an item. Watch. |
| **IETF individual drafts** on agent identity and delegated authorization | Who an agent is, on whose behalf it acts, how a token says so. | Same: identity and delegation. Relevant to [ADR-0150](../adrs/0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md) and [ADR-0156](../adrs/0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md), not to this record. |
| **MCP** | Tools and resources a model calls. Jira's third-party path names an external MCP server as the way in. | A transport a tracker may use to reach an agent, not a model of assignment or a session. |

## What this means for the capability

* The interface is shaped by Linear, the one open model: delegate distinct from assignee, a session opened by assignment or mention, typed activities, external URLs, a stop, and timing bounds (5 s, 10 s).
* Every other tracker in use today is an emulation: "who" from a field on the item, "go" from one Ploeg member's assignment, activities as comments, the pull request as a comment with a link, stop as unassignment or a status move. ClickUp is the first implementation by the owner's decision; Linear is second as the proof the shape holds; Jira waits for its third-party path to be verified; GitLab is parked.
* The internal model stays A2A-compatible without naming A2A, so ADR-0007's facade is one more adapter of the same capability.

## Unknowns, to verify before each adapter

* **Jira**: whether an agent Atlassian did not partner with can be assigned or mentioned through the external MCP server path, and whether it posts as itself; what Rovo credits cost such an agent from 2026-12-03.
* **ClickUp**: whether the public API lets an integration create a dropdown custom field and write its options, or only read fields and set a task's value. The emulation reads the field either way; creation is an "if" in ADR-0187.
* **Linear**: whether one OAuth app can present several delegates, or an installation with several Teams needs one app per Team; how the developer preview changes before general availability.
* **Plane**: whether its Agents beta exposes an open webhook and write path comparable to Linear's.
