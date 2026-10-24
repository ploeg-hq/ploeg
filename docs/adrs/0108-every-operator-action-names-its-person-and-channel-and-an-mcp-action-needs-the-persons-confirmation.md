---
status: accepted
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# Every operator action names its person and channel, and an MCP action needs the person's confirmation

## Context and Problem Statement

[ADR 0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md) made `ploeg-mcp` read-only. Its HTTP mode already verifies the caller's access token and maps the caller's groups to an Operator Consumer, but it calls ploegd as that consumer, so Ploeg cannot tell which person acted. Operator mutations take the person from `X-Ploeg-Actor` and `X-Ploeg-Acting-User`, and each route records it its own way. No action records the channel it came through (a browser, an editor, an AI client) or whether a person confirmed it.

On 2026-10-17 the owner decided that Ploeg is the core and stays open: a person may assign, approve, reject, retry, stop and steer work through `ploeg-mcp` as well as through a consumer such as a web application, and auditability is essential. How does Ploeg attribute an action, and when may an AI client act?

## Decision Drivers

* Every change to work can be traced to one person, the channel they used, and what they agreed to.
* A model never acts on its own: a person confirms each change through their own client.
* Ploeg stays usable without any particular consumer; `ploeg-mcp` is a first-class way in.
* One audit record shape, whichever route or channel made the change.

## Considered Options

* Attribute every operator action uniformly, and let `ploeg-mcp` act over HTTP as the verified person with their confirmation
* Keep `ploeg-mcp` read-only and route all actions through a consumer application
* Let `ploeg-mcp` act as its Operator Consumer without a person

## Decision Outcome

Chosen option: "Attribute every operator action uniformly, and let `ploeg-mcp` act over HTTP as the verified person with their confirmation", because it keeps Ploeg open to any client while every change still names a person and their consent.

**Attribution.** Every operator mutation records one audit shape: the Operator Consumer, the person (`X-Ploeg-Actor`, and `X-Ploeg-Acting-User` when a consumer acts on a person's behalf), the channel (new `X-Ploeg-Via`, a short label such as `mcp:Claude Code 2.1.296` or `browser`), and the confirmation (new `X-Ploeg-Confirmation`, the client's confirmation id and the text the person agreed to, hashed). The operator API's event list returns these fields and can filter by person.

**`ploeg-mcp` acts over HTTP only.** In HTTP mode the person comes from the verified access token (subject and username), the channel from the client's `initialize` information, and the consumer from the person's groups. Each action tool needs an execute-capable consumer for the person's group. Stdio mode has no verified person and stays read-only.

**Actions, slice 1.** Submit (assign) a Work Item, approve and reject a proposal, retry (requeue, optionally from a Round), stop (cancel), and steer: a note for the next Run of tracker work, or a message, pause, resume or cancel for an attended execution. These use the existing operator routes; no new engine behaviour.

**Confirmation.** Every action tool that changes state asks the person through an MCP elicitation that states the exact effect and any cost before it calls ploegd. A declined or unanswered confirmation changes nothing. A client without elicitation is refused and told where else to act. Each action carries a command id, so a repeated call never acts twice.

### Consequences

* Good, because every change to work names a person, a channel and the agreed text, in one shape.
* Good, because any MCP client can drive Ploeg without a particular consumer.
* Bad, because ploegd trusts the Operator Consumer to name the person truthfully; a consumer's token must be guarded like an admin credential, and the person is only as verified as the consumer that names them.
* Bad, because a client without elicitation cannot act through `ploeg-mcp`.

### Confirmation

Tests in `pkg/httpapi` assert the audit shape (consumer, person, channel, confirmation) for every operator mutation and the event filter by person. Tests in `cmd/ploeg-mcp` drive each action tool over HTTP: an accepted confirmation calls ploegd with the person and channel, a declined one calls nothing, stdio lists no action tools, and a repeated command id acts once.

## Pros and Cons of the Options

### Keep `ploeg-mcp` read-only and route all actions through a consumer application

* Good, because one consumer owns every action path.
* Bad, because Ploeg would depend on one consumer to be operable, against the owner's decision that Ploeg is the open core.

### Let `ploeg-mcp` act as its Operator Consumer without a person

* Good, because nothing new to verify.
* Bad, because no action could be traced to a person.

## Re-evaluation triggers

* The MCP specification adds a standard for proving a person confirmed an action (beyond a client-rendered form).
* A second deployment needs per-person authorization finer than group-to-consumer mapping.
* A consumer needs Ploeg to verify the person itself instead of trusting the consumer.

## More Information

* Owner's words, 2026-10-17: Ploeg "should be open. It's the core of this all"; auditability is "super important"; people get "true freedom" to assign, approve, retry, cancel and steer work.
* Builds on [ADR 0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md) (read tools) and [ADR 0069](0069-ploeg-names-none-of-its-consumers.md) (consumers stay unnamed).
* 2026-10-17 — Amended by the owner after slice 1 (#147):
  * **Actions over stateless HTTP need an MCP 2026-07-28 client.** Its confirmation travels as a multi-round-trip input request inside the tool result; `ploeg-mcp` recomputes the effect text on the second call and acts only if it hashes to what the person saw. An older client is refused for actions and can still read.
  * **A person steers any attended execution they own, whichever Operator Consumer admitted it.** This needs one person identifier across consumers: Ploeg's person id is `oidc-` followed by the first 32 hex characters of the SHA-256 of `<issuer>|<subject>`, the form consumers already send as `X-Ploeg-Actor`. `ploeg-mcp` derives it from the verified token's issuer and subject. ploegd accepts a person's execution command (message, pause, resume, cancel, take control, hand back) from any execute-capable consumer whose Teams include the execution's Team when `X-Ploeg-Actor` names the execution's owner; executor commands (start, heartbeat, report, close, credential) stay with the admitting consumer. Built in a following slice.
  * **Accepted behaviour changes of slice 1:** a malformed or repeated attribution header answers 400 before anything changes, and a repeated approve or reject answers 409 `not_proposed`; each still acts once.
* 2026-10-24 — Amended by [0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md): an Operator Consumer acting for a person may prove the person with that person's token, exchanged at the identity provider for Ploeg's audience. Such attribution is recorded as `proven`, header-only attribution as `asserted`, and the trust in the consumer that this record accepts now applies to asserted attribution only.
