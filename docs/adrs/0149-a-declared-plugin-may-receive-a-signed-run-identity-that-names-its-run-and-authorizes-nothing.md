---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A declared plugin may receive a signed Run identity that names its Run and authorizes nothing

## Context and Problem Statement

[ADR-0139](0139-a-team-declares-claude-code-plugins-pinned-to-a-commit-whose-hooks-run-guarded-with-only-their-declared-environment.md) lets a Team declare the Claude Code plugins every `claude-code` Run loads, and runs their hooks under a guard that hands each hook a minimal environment plus its declared env, and refuses every name Ploeg, the harness or the gateway reads. A plugin whose hooks report sessions to an outside service therefore cannot tell that it runs inside a Ploeg Run. That service sees one more stray session for every Run: it lists the Run twice, counts its cost twice (once from the session, once from the Run), and cannot attribute the Run's commits to anything.

How does a declared plugin learn, in a way the service it reports to can check, which Run its session belongs to, without the plugin receiving anything that lets it act in Ploeg?

## Decision Drivers

* The guard's boundary from ADR-0139 and [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) holds: no Ploeg credential, model key or forge token reaches a hook. What the hook gets must be useless against Ploeg itself.
* The service decides what to believe. It verifies the claim offline, with a key Ploeg publishes, not by trusting a variable a hook could have set.
* Facts come from ploegd's records, never from the worker that asks or the plugin that presents them.
* Reuse a standard. Ploeg already verifies ES256 JWTs against a JWK Set ([pkg/claudesession](../../pkg/claudesession/verifier.go), [pkg/mcpauth](../../pkg/mcpauth/keys.go)) and depends on `github.com/golang-jwt/jwt/v5`; a consumer in any language has a JOSE library.
* Ploeg knows no consumer: the token is generic, opted into per plugin, and names a Run, not a product.
* A plugin that did not opt in, or a deployment that configured nothing, sees exactly what it sees today.

## Considered Options

* **A short-lived ES256 JWT that ploegd signs per Run and audience, published keys at a JWK Set, handed to the hooks of a plugin that opted in**
* Pass the Run's facts as plain variables (`RUN_ID`, `WORK_ITEM_ID`, …)
* Reuse the worker's control capability
* Give the plugin an operator credential scoped to the Run
* Let the outside service find the Run by matching the session to the Run afterwards (branch, time, trace in commits)

## Decision Outcome

Chosen option: "**a short-lived ES256 JWT that ploegd signs per Run and audience**", because it is the only option whose claim a service can verify without asking Ploeg and without the token being worth anything inside Ploeg.

1. **Opt in per plugin.** A declaration may carry `runIdentity: {audience}` (`PLOEG_CLAUDE_PLUGINS`, chart `harness.claudeCode.plugins[].runIdentity`). The audience is required, so a token meant for one service is refused by every other: an https URL without credentials, query or fragment, or a URN, at most 256 characters. An object, not `runIdentity: true`, because a token without an audience can be replayed at any service that trusts Ploeg's key.
2. **ploegd signs; the worker only asks.** `POST /api/v1/runs/{token}/identity` with `{audiences}` (one to eight) and the Run's live control capability ([worker control](../contracts/worker-control.md)). ploegd reads the Run, its lease, its Shift and its Harness from its own tables and signs one token per audience. It answers 404 when it has no signing key and 409 when the Run is not running or a person's client carries it ([ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md)): that session is the person's own.
3. **The token.** A compact JWS, `alg: ES256`, `typ: run-identity+jwt` (explicit typing, RFC 8725), `kid` = the key's RFC 7638 thumbprint. Claims: `iss` (`PLOEG_RUN_IDENTITY_ISSUER`), `sub` = `run:<id>`, `aud`, `iat`, `nbf`, `exp`, a random `jti`, and `run`: `id`, `workItemId`, `shiftId`, `round`, `role`, `team`, `harness`, `branch` and `trace`, the `Agent-Trace-Id` trailer the Run writes on each commit and the alias of its model key, which is how a service attributes the Run's commits. Ids are the operator API's. `exp` is the Run's current lease deadline, at most 15 minutes after `iat`; a lease ending in under five seconds gets no token.
4. **A new key, asymmetric.** The worker control capability is an HMAC: publishing what verifies it would let anyone forge control tokens. ploegd gets its own P-256 key (`PLOEG_RUN_IDENTITY_SIGNING_KEY`, chart `runIdentity.signingKeySecret`) and publishes the public half, with any retired keys (`PLOEG_RUN_IDENTITY_RETIRED_KEYS`), at `GET /api/v1/run-identity/jwks`, unauthenticated, `application/jwk-set+json`, cacheable for five minutes. ES256 rather than EdDSA because it is what Ploeg already verifies and what every mainstream JOSE library supports. The chart refuses a plugin env that reads the signing key's Secret, as it refuses every Ploeg credential's.
5. **The environment variable is `AGENT_RUN_IDENTITY_TOKEN`.** The guard sets it for each hook of a plugin that opted in. Not `PLOEG_*`: ADR-0139 promises that no `PLOEG_*` name ever reaches a hook, and keeping that rule without an exception keeps it checkable. Not `CLAUDE*`: Claude Code owns that space and may start reading any name in it. The name says what it holds and that it concerns the agent's Run, not who issued it, so a plugin can read the same name under any orchestrator that adopts it. A declaration may not set it, so a plugin cannot be handed a forged one by its own configuration.
6. **Refresh.** The worker asks before the harness starts and again every third of the lease, the renewal cadence, so each token outlives the next refresh. It writes each plugin's token, mode 0600, to `run-identity.jwt` next to that plugin's guard specifications, by rename, and the guard reads it when each hook starts: every hook gets the current token, and a token that has expired is not passed at all. A transient failure keeps the last token until it expires; 404 or 409 removes the file and stops asking. The files are removed when the Run's execution ends. A deployment without a key, or a request that fails, costs the hooks their token, never the Run.
7. **Take-over and hand back.** A Run that yields to a person ([ADR-0142](0142-a-person-takes-over-a-running-run-at-its-next-safe-point-and-hands-it-back-on-the-same-branch.md)) stops running, so ploegd issues no further token for it and the last one expires with the lease it was signed for. `claude-code`, the only harness that loads plugins, does not yield today; this holds when it does. The person's own session while they hold the Work Item is not a Run and carries no Run identity. A hand back starts a new Run in the same Shift on the same branch, whose tokens name the new Run, so a service links the two by `run.shiftId`, `run.branch` and the `Ploeg-Yield` trailer.
8. **Reading more.** The token names a Run; it grants nothing. A service that wants the Run's cost or outcome reads `GET /api/v1/operator/runs/{run.id}` with its own operator credential and Team scope, unchanged; that response's `keyAlias` equals `run.trace`. Ploeg's Go module carries a verifier (`runidentity.ParseKeySet`, `runidentity.Verify`) that pins issuer and audience and refuses a token without them.

### Consequences

* Good, because a service a plugin reports to can fold the plugin's session into the Run it belongs to, count its cost once, and attribute the Run's commits by their trailer, without any change to the guard's credential boundary.
* Good, because the token is worthless inside Ploeg: no route accepts it, it carries no control capability, and it expires with the lease, five minutes with the chart's default `PLOEG_LEASE_TTL`.
* Good, because nothing changes for a plugin that did not opt in or a deployment that configured no key; the tests prove both.
* Bad, because the token is readable by the agent, as ADR-0139's declared env is: hooks run as the agent's user and the file sits in the Run's scratch directory. An agent that leaks it lets someone present this Run's identity to that one audience until the lease ends. Accepted: it names a Run the service would learn of anyway and authorizes nothing; a service that must bind a session to one presenter should accept the first session per Run and treat later ones as the same Run.
* Bad, because ploegd now holds one more key to rotate. Rotation is: generate a new key, move the old public key into `retiredKeysSecret`, deploy, and drop it after 15 minutes. The kid is the key's thumbprint, so nothing else needs renaming.
* Bad, because each worker asks ploegd once per third of the lease while plugins opted in. One request per Run for all audiences, the same cadence as the renewal.
* Neutral: the token reveals the Run's Team, Role and branch to the service its plugin reports to. A Team that declares the plugin and its audience chose to tell that service.

### Confirmation

* `go test ./pkg/runidentity/` proves a signed token verifies against the published key set and fails for an expired token, another audience, another issuer, an unknown key, a wrong signature, a missing `typ`, a lifetime over 15 minutes, a tampered payload and a verifier that pinned nothing; that a lease caps `exp`; that a retired key is published and still verifies its tokens; and that a P-384, RSA or doubled key and a malformed retired key are refused.
* `go test ./pkg/httpapi/` (`run_identity_test.go`) proves the facts in a token are ploegd's records of the claimed Run, that each audience gets its own token, that the route refuses bad audiences, an unknown Run, a lapsed lease and an oversized body, that it answers 404 without a key, that in managed mode only the Run's own capability and worker get one while the key set needs none, and that every body matches `run-api.v1.schema.json`.
* `go test ./pkg/harness/claudeplugins/` proves the opt-in parses and that a missing or plain-http audience, `runIdentity: true`, an unknown field and a declared `AGENT_RUN_IDENTITY_TOKEN` are refused; that every guarded hook of an opted-in plugin points at the one token file and others at none; and that the guard passes the current token, the refreshed one on the next hook, and nothing before the first, after expiry or without the opt-in, whatever the caller's environment holds.
* `go test ./pkg/worker/` (`run_identity_linux_test.go`, the binary-free tier) installs a fake plugin, runs its `SessionStart` hook through the real guard the way Claude Code would, and proves the hook receives a token that verifies against the issuer's keys without seeing the model key, while a plugin without the opt-in and a ploegd that issues none leave the hook without one; it also proves the refresh, that a transient failure keeps the last token, and that the files are gone once the Run ends or ploegd says it is no longer running.
* `go test ./cmd/ploegd/` proves no key issues none and half a configuration stops ploegd; `scripts/helm-golden.sh check` pins the rendered `runIdentity` env and plugin entry and refuses a plugin env that reads the signing key's Secret.
* `go test ./internal/ledger/` validates this record and its index row. CI runs all of them through `mise run verify`.

## Pros and Cons of the Options

### Pass the Run's facts as plain variables

* Good, because it is the least work for a plugin.
* Bad, because the service cannot tell a real Run from a hook, or anyone, that sets the same variables; a forged Run id would merge an unrelated session into a Run or inflate its cost.

### Reuse the worker's control capability

* Good, because it already names the Run, the worker and the Team.
* Bad, because it is a credential: it renews the lease, mints the model key and reports the outcome. Handing it to a hook breaks ADR-0139, and it is an HMAC no service could verify without the secret that also forges it.

### Give the plugin an operator credential scoped to the Run

* Good, because the service could read everything about the Run directly.
* Bad, because it is a credential the agent can read, it outlives what a hook needs, and the operator API has no per-Run scope; the service should use its own operator credential, which this decision leaves as it is.

### Match sessions to Runs afterwards

* Good, because the plugin needs nothing.
* Bad, because a match by branch and time is a guess that fails for concurrent Runs of one branch and for every session before its first commit, and the double count stands until the guess is made.

## Re-evaluation triggers

* A second harness loads Team plugins, or Claude Code gains a native way to pass an orchestrator's identity to hooks; then the variable or the guard's part changes.
* `claude-code` gains a yield (ADR-0142) or Runs start sharing a session across a hand back; then the take-over rules above are tested against a real yield.
* A service reports that a leaked token was presented by someone other than the Run, or asks for a sender-constrained token; then bind the token to a key the hook holds (DPoP, RFC 9449).
* A service needs a token after the Run ended (late session reports); then a longer-lived, separately audienced attestation is a new decision, not a longer `exp`.
* ploegd gains a general signing service or OIDC discovery; then the key set moves there and this route redirects.

## More Information

* Related: [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md) (the session tokens Ploeg verifies the same way), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) (operator reads stay attributed and scoped as before), [ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md), [ADR-0139](0139-a-team-declares-claude-code-plugins-pinned-to-a-commit-whose-hooks-run-guarded-with-only-their-declared-environment.md), [ADR-0142](0142-a-person-takes-over-a-running-run-at-its-next-safe-point-and-hands-it-back-on-the-same-branch.md).
* Contract: [run-api.v1.schema.json](../contracts/run-api.v1.schema.json) (`runIdentityRequest`, `runIdentityResponse`, `runIdentityClaims`, `runIdentityKeySet`) and [A Run identity for a declared plugin](../contracts/README.md#a-run-identity-for-a-declared-plugin).
* Standards: RFC 7515 (JWS), RFC 7517 (JWK Set), RFC 7519 (JWT), RFC 7638 (JWK thumbprint), RFC 8725 (JWT best current practice, explicit typing).
