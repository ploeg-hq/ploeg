---
status: proposed
date: 2026-10-25
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# ploeg-mcp asks a client on the previous protocol to confirm in a session

## Context and Problem Statement

[ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) requires a person's confirmation for every `ploeg-mcp` action and, because `ploeg-mcp http` was stateless ([ADR-0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md)), refused actions from any client older than MCP 2026-07-28. Claude Code 2.1.302 and 2.1.310 first send `server/discover` on 2026-07-28 with their capabilities in each request's `_meta`; when that probe fails or does not answer within five seconds, they fall back to `initialize` on 2025-11-25 and declare elicitation once, for the session. On 2026-10-25 a person signed in to the reference deployment, the first authenticated `server/discover` did not answer within those five seconds, Claude Code fell back, and every action was refused with "This client cannot ask you to confirm", although the client declared form and URL elicitation. A stateless server cannot remember capabilities declared at `initialize` (go-sdk v1.8.0 synthesizes empty initialize parameters for every request) and cannot send a request to the client. How does `ploeg-mcp` confirm actions for such a client without weakening the confirmation?

## Decision Drivers

* The confirmation stays mandatory, signed and single-use (ADR-0152), and in `login` mode needs the person's fresh sign-in (ADR-0153).
* A client that declares elicitation should be able to act whichever revision it negotiated; widely used clients fall back to 2025-11-25.
* A call acts for the person who sent that HTTP request, with that request's token, never with a session's first token.
* No new store and no change to 2026-07-28 clients.

## Considered Options

* **Keep a session for clients on an earlier revision, and ask them with `elicitation/create` in it**
* Keep refusing; tell people to use a 2026-07-28 client
* Make the whole server stateful
* Trust capabilities a client repeats in each request's `_meta` on 2025-11-25

## Decision Outcome

Chosen option: "**keep a session for clients on an earlier revision**", because it is the only way a 2025-11-25 client's declared capabilities last and the server can ask it anything, and it leaves the 2026-07-28 path as it is.

**Routing.** A request whose `Mcp-Protocol-Version` is 2026-07-28 or later goes to the stateless handler, as before. An `initialize`, and any request carrying `Mcp-Session-Id`, goes to a go-sdk handler that keeps sessions (SSE responses, 30 idle minutes). A request on an earlier revision with no session and no `initialize` is still served statelessly, so a client that ignored the session id keeps reading.

**Who a session answers.** After the guard and the group mapping, `ploeg-mcp` hands the SDK a token info whose user is the person id and group; the SDK refuses a request in a session from any other person or group with 403. Each call reads its person and token from its own HTTP request (`RequestExtra`), so a token exchange for an action uses the current token.

**Confirming.** The action builds the same signed input requests as for 2026-07-28. In a session on an earlier revision it does not return them: it sends each as `elicitation/create` on the call's stream and runs the same checks on the answers, at most ten rounds. In `login` mode, after the person agrees to open the page, the call waits for the page until the confirmation expires instead of 45 seconds, so the person is asked once. A request in a session may therefore stay open for the confirmation window plus a minute; `ploeg-mcp` extends that request's write deadline accordingly.

**Refusal.** A client that declares no elicitation, in a session or not, is refused for actions as before; in `login` mode one without URL elicitation is refused as before.

### Consequences

* Good, because Claude Code, and any client that settles on 2025-11-25, can act after its person confirms, in both modes.
* Good, because the signed, single-use state and the page's checks are the same code on both paths.
* Bad, because sessions live in one process's memory: more than one replica needs routing on `Mcp-Session-Id`, and a restart makes clients initialize again. `login` mode already runs one replica.
* Bad, because a request in a session can hold a connection for up to the confirmation window plus a minute; the gateway route needs at least that timeout.
* Bad, because ADR-0108's sentence "An older client is refused for actions and can still read" and ADR-0081's "stateless Streamable HTTP" no longer describe ploeg-mcp; those accepted records are not edited, this record narrows them.
* Neutral: what made the first authenticated `server/discover` slow on 2026-10-25 is not established; the guard fetched the issuer's keys synchronously (about 1.5 s) and the pod requests 10m CPU.

### Confirmation

`go test ./cmd/ploeg-mcp/` against a real ploegd handler and a fake issuer, with the go-sdk client negotiating 2025-11-25:

* `TestAClientOnThePreviousProtocolConfirmsInItsSession` (asked once, acts once, attribution carries the confirmation), `TestADeclinedConfirmationInASessionChangesNothing`, `TestAClientWithoutElicitationIsRefusedInItsSession`;
* `TestASessionAnswersOnlyThePersonAndGroupThatOpenedIt` (another person or another group gets 403);
* `TestAPreviousProtocolClientConfirmsAtThePageInItsSession` (one page, signed in after the client accepted) and `TestAPreviousProtocolClientThatSkipsThePageNeverActs`;
* `TestA20251125ClientGetsASessionAndStillWorksWithoutOne`;
* the 2026-07-28 tests of ADR-0152 and ADR-0153 pass unchanged.

On 2026-10-25 every new test except the refusal failed on the code before this change, removing the group from the session's user failed the session test, and removing the wait-until-expiry in a session failed the page test.

## Pros and Cons of the Options

### Keep refusing

* Good, because nothing changes.
* Bad, because the client most people use to reach ploeg-mcp cannot act whenever its probe falls back, and the message blames a client that does declare elicitation.

### Make the whole server stateful

* Good, because one handler.
* Bad, because go-sdk v1.8.0 rejects 2026-07-28 requests on a stateful handler, and 2026-07-28 clients would lose request-scoped cancellation.

### Trust `_meta` capabilities on 2025-11-25

* Good, because no session.
* Bad, because a 2025-11-25 client does not send them, and a stateless server still cannot send `elicitation/create`.

## Re-evaluation triggers

* go-sdk serves 2026-07-28 and earlier revisions with sessions on one handler, or drops earlier revisions.
* Claude Code, VS Code and Cursor all stay on 2026-07-28 after a failed or slow `server/discover`.
* An adopter runs ploeg-mcp with more than one replica in `elicitation` mode and loses sessions.
* The MCP specification removes sessions from the revisions ploeg-mcp still serves.

## More Information

* Evidence, 2026-10-25: a logging go-sdk v1.8.0 server (stateless, JSON responses, as deployed) received from Claude Code 2.1.310 `server/discover` and `tools/call` on 2026-07-28 with `elicitation: {form: {}, url: {}}` in each request's `_meta`, and Claude Code answered an `input_required` result by calling again with `inputResponses`. A logging server that answered `server/discover` with an error got `initialize` on 2025-11-25 with the same capabilities, a session, and answered `elicitation/create` (form and URL) on the call's SSE stream. In `claude -p` both kinds were answered `cancel` without asking. The reference deployment's gateway log showed Claude Code 2.1.302's first authenticated `server/discover` disconnected after 4,999 ms, then `initialize`, then `GET` (405) for the session stream, then the refused action.
* Builds on ADR-0108, ADR-0152 and ADR-0153; narrows ADR-0081's "stateless" and ADR-0108's refusal of older clients.
* 2026-10-25: proposed with the implementation (`cmd/ploeg-mcp/http.go`, `cmd/ploeg-mcp/actions.go`).
