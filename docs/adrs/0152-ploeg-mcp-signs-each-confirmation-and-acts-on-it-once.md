---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# ploeg-mcp signs each confirmation and acts on it once

## Context and Problem Statement

[ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) lets a person act on work through `ploeg-mcp` only after they confirm the exact effect through an MCP elicitation. Over stateless HTTP (MCP 2026-07-28) the confirmation is two tool calls: the first returns the effect as an input request plus an opaque `requestState`, and the second carries the person's answer and that state back. `ploeg-mcp` encoded the state as unsigned base64 JSON. A client, or an agent driving one, could write the state itself and send it with `accept: true` in a single call, so nothing showed the person anything ([threat model](../security/threat-model.md) F-08, High). How does `ploeg-mcp` know that the state it is handed is one it issued, for this call and this person, and that it has not been used before?

## Decision Drivers

* An action happens only on a confirmation `ploeg-mcp` itself issued, for the same tool, arguments, effect text and person, within its window.
* A confirmation is used once; a replay changes nothing.
* No hard-coded key: the key comes from a Secret, and `ploeg-mcp` fails closed when an `execute` group has none.
* `ploeg-mcp` stays stateless over HTTP and may run more than one replica, without a new store or a new ploegd route.
* The MCP specification has no standard for proving that a person, not the client, answered.

## Considered Options

* **An HMAC-signed state, checked and spent on the second call, with ploegd's command idempotency bounding a replay across replicas**
* Keep pending confirmations in ploegd's store and spend them there
* Encrypt the state (AEAD) instead of signing it
* Run ploeg-mcp as one replica only

## Decision Outcome

Chosen option: "**an HMAC-signed state**", because it closes the one-call bypass with no new state, and every action already reaches ploegd with an id that makes a replay act at most once.

**The state.** The first call returns `<payload>.<tag>`: the payload is base64url JSON of the tool name, a fresh confirmation id, the command id, the SHA-256 of the effect text, the SHA-256 of the arguments, the person id (`oidc-…`, ADR-0108) and an expiry ten minutes ahead; the tag is HMAC-SHA256 over a fixed context string and the payload, keyed by `PLOEG_MCP_CONFIRMATION_KEY`.

**The second call.** `ploeg-mcp` refuses, before calling ploegd and with "this confirmation does not belong to this call", a state whose tag does not verify under its key (forged, altered, unsigned, or signed with another key), or whose tool, argument digest or person differs from this call's. It refuses an expired state. It then spends the confirmation id: a second use answers "this confirmation was already used". Only then does it recompute the effect, compare it with the signed digest as before, and read the person's answer.

**The key.** At least 32 bytes from `PLOEG_MCP_CONFIRMATION_KEY`. A group marked `execute` in `PLOEG_MCP_GROUPS` without a key refuses to start; read-only groups need none. The chart reads it from a Secret named in `mcp.confirmationKeySecret` and does not generate one, so the key is the same on every replica and survives a re-render. Rotating it voids the confirmations still open, at most ten minutes of them; their clients are asked to call again.

**Replicas.** Each replica keeps the confirmation ids it has spent until they expire, capped at 100,000 and refusing further confirmations when full. A replay that reaches a different replica within the window passes that replica's spent set, so the guarantee across replicas comes from ploegd: the signed state fixes the command id, and submit, retry, stop and notes answer `replayed` for a repeated command id, a repeated approve or reject is refused as `not_proposed`, and an execution command carries the revision it expects. A replay on another replica therefore repeats the same command, which acts at most once. With one replica, the chart's default, a replay is refused outright.

**What it does not prove.** The signature proves that `ploeg-mcp` issued the question and that the answer belongs to it. It cannot prove that a person, rather than a client that answers its own elicitations, ticked the box; ADR-0108's trigger for a standard proof of human confirmation stays open.

### Consequences

* Good, because a client can no longer skip the question: every action needs a state only `ploeg-mcp` can make, bound to the arguments, effect and person, used once.
* Good, because `ploeg-mcp` stays stateless across requests and needs no new ploegd route or table.
* Good, because an `execute` deployment without a key fails at start, not at the first action.
* Bad, because an installation with an `execute` group must now create and mount a Secret before upgrading.
* Bad, because single use across replicas rests on ploegd's command idempotency rather than on the confirmation id; a future action without a command id or a state guard would need its own guard or a shared spent set.
* Bad, because a client that answers its own elicitations can still act without showing the person; only the protocol shape is enforced.

### Confirmation

`go test ./cmd/ploeg-mcp/ -run 'Confirm|Forge|Issued|Configuration'` covers it. Unit tests on the signer: `TestASealedConfirmationOpensUnchangedWithinItsWindow` (must pass), `TestAConfirmationSealedWithAnotherKeyIsRefused`, `TestAnAlteredOrUnsignedConfirmationIsRefused`, `TestAnExpiredConfirmationIsRefused` and `TestAConfirmationIsSpentOnce`. Against a real ploegd handler: `TestAClientCannotForgeTheConfirmationInOneCall` sends an unsigned state and one signed with a guessed key with `accept: true` and checks the Work Item did not change; `TestAnIssuedConfirmationActsOnceForItsActionArgumentsAndPerson` takes a state `ploeg-mcp` issued, refuses it for other arguments, another tool and another person, accepts it for the person's own call, and refuses its replay with one audit event recorded. `TestHTTPConfigurationIsRefusedWhenIncomplete` refuses an `execute` group without a key and a key shorter than 32 bytes; `TestExecuteGroupsLoadWithAConfirmationKey` loads one with a key. `scripts/helm-golden.sh check` pins the chart's `PLOEG_MCP_CONFIRMATION_KEY` reference.

## Pros and Cons of the Options

### Keep pending confirmations in ploegd's store and spend them there

* Good, because single use would hold across replicas for any action, with or without a command id.
* Bad, because it needs a new table, a new operator route and a ploegd round trip on every first call, for a guarantee the command ids already give.

### Encrypt the state (AEAD) instead of signing it

* Good, because the client would not see the digests or the person id inside.
* Bad, because the state holds only hashes, a random id and the caller's own person id; integrity is what F-08 lacks, and a MAC gives it with less to get wrong.

### Run ploeg-mcp as one replica only

* Good, because the in-memory spent set alone would make every replay fail.
* Bad, because it forbids scaling an otherwise stateless reader, and a restart would empty the set anyway.

## Re-evaluation triggers

* An action tool is added whose ploegd route has neither a command id nor a state precondition.
* ploeg-mcp runs more than one replica in a deployment and a replayed confirmation reaches ploegd twice in its audit log.
* The MCP specification adds a standard for proving a person confirmed an action, or for server-held request state.
* An adopter asks for key rotation without voiding open confirmations.

## More Information

* Fixes [threat model](../security/threat-model.md) finding F-08 ([#226](https://github.com/ploeg-hq/ploeg/issues/226)).
* Builds on [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md). [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md) lists "ploeg-mcp confirmations become signed records" as a trigger; this record signs the state between `ploeg-mcp`'s two calls only, and ploegd still receives the confirmation as `X-Ploeg-Confirmation` and checks its format, so 0150 is unchanged.
* Breaking change, deliberate: since PR #260 an install whose `PLOEG_MCP_GROUPS` marks a group `execute` must provide `mcp.confirmationKeySecret`, or ploeg-mcp refuses to start. It is recorded in [breaking-changes.yaml](../contracts/breaking-changes.yaml) against v0.2.0-rc.61. To upgrade, create a Secret holding at least 32 random bytes (such as `openssl rand -base64 48`) and set `mcp.confirmationKeySecret: {name: <secret>, key: <key>}` before you roll out the new release, or remove `execute` from every group.
