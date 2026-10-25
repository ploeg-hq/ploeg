---
status: proposed
date: 2026-10-25
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A person confirms a ploeg-mcp action by signing in afresh at a ploeg-mcp page

## Context and Problem Statement

[ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) lets a person submit, approve, reject, retry, stop, note and steer work through `ploeg-mcp`, and only after they confirm the exact effect. The confirmation is an MCP form elicitation: `ploeg-mcp` asks, the client shows a checkbox, the client answers. [ADR-0152](0152-ploeg-mcp-signs-each-confirmation-and-acts-on-it-once.md) signed the state between the two calls, so a client can no longer invent a confirmation or replay one. It cannot tell who ticked the box. An agent driving an MCP client, or a client that answers its own elicitations, receives the question and answers `accept` without any person seeing it ([threat model](../security/threat-model.md) T-OC-2, residual of F-08). That residual is why `execute` groups sit outside the supported profile P1.

The owner approved building a proof of human presence on 2026-10-25. How does `ploeg-mcp` know that a person, not the client, approved this action, for these arguments, now?

## Decision Drivers

* The proof must come from something the MCP client cannot produce: the person's own fresh authentication at the identity provider, in a browser the client does not drive.
* The person sees the action, its arguments and their own identity before they approve, on a page Ploeg serves, not in text the client renders.
* Standard protocols only, issuer-agnostic: OpenID Connect authorization code with PKCE, `prompt=login`, `max_age`, `acr`/`amr`, `auth_time`; MCP URL-mode elicitation.
* It works with the reference deployment's identity provider (authentik) without paid features, and with any OpenID Connect provider.
* Each approval stays bound to one tool, one argument digest, one person and an expiry, and is used once, reusing ADR-0152's sealing and spent set.
* No break for an existing `execute` deployment without the deprecation path of [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md).

## Considered Options

* **URL elicitation to a ploeg-mcp page that requires a fresh OpenID Connect login (step-up), optionally with a required phishing-resistant method**
* Keep the signed form confirmation only (ADR-0152)
* OpenID Connect Client-Initiated Backchannel Authentication (CIBA)
* Serve the page from ploegd instead of ploeg-mcp
* RFC 8693 delegation with an `act` claim for the agent

## Decision Outcome

Chosen option: "**URL elicitation to a ploeg-mcp page that requires a fresh OpenID Connect login**", because it is the only option whose proof the MCP client cannot fabricate, it uses protocols the reference identity provider implements today, and the MCP specification describes this very pattern for interactions that must not pass through the client.

**The flow.** With `PLOEG_MCP_CONFIRMATION_MODE=login`, an action's first call does not ask the client yes or no. It seals the confirmation state as ADR-0152 does and returns one input request of mode `url` (MCP 2025-11-25 URL-mode elicitation, carried in the 2026-07-28 multi-round-trip result) whose URL is `<origin of PLOEG_MCP_RESOURCE>/confirmations/<random page token>`. The page shows the effect text, the tool, the arguments as the client sent them, the person's username and person id, and the expiry, with "Sign in to confirm" and "Decline". Signing in sends the browser to the issuer's authorization endpoint with `response_type=code`, PKCE `S256`, a fresh `state` and `nonce`, `prompt=login`, `max_age=0` and, when configured, `acr_values`. The callback redeems the code with ploeg-mcp's confidential client and verifies the ID token (signature from the issuer's keys, `iss`, `aud`, `azp`, `exp`, `iat`, `sub`, `nonce`, `auth_time` present). Only then is the confirmation approved, when all of these hold:

* `sub`, under the person namespace, is the person the action is for (ADR-0108's person id);
* `auth_time` is no earlier than 30 seconds before the sign-in started on this page, and no older than `PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE` (default 2 minutes), so an identity provider that ignores `prompt=login` still fails;
* `acr` is one of `PLOEG_MCP_CONFIRMATION_ACR_VALUES`, when set;
* `amr` contains every value of `PLOEG_MCP_CONFIRMATION_AMR`, when set;
* the callback arrives in the browser that started the sign-in (an HttpOnly, SameSite=Lax cookie whose hash is stored with the attempt), so an agent that starts the sign-in itself and forwards the identity provider's link to the person gets nothing.

**The re-call.** The client re-calls with the sealed state and the URL elicitation's `accept` (the person agreed to open the page). ploeg-mcp verifies the state as ADR-0152 does (signature, expiry, tool, argument digest, person), then waits up to 45 seconds for the page. Approved: it spends the confirmation id, removes the page, checks the effect text still hashes to the signed digest, and acts. Still open: it returns the same URL request again, so the client keeps the person on the page and nothing acts; a client that answers its own elicitations loops until its round-trip limit and never acts. Declined, or the elicitation declined or cancelled: "Not done". Expired or unknown (another replica, a restart): refused, call again.

**Where the page lives.** ploeg-mcp, not ploegd. ploeg-mcp is the component that already serves people over HTTP, holds the person's verified identity and the issuer configuration, and holds the confirmation state; ploegd serves Operator Consumers and workers, has no browser-facing routes, and would need a new table and route (ADR-0152 rejected that for the same reason). The page runs on PLOEG_MCP_RESOURCE's host, which must be `https` outside loopback.

**One replica.** Open pages live in ploeg-mcp's memory (at most 10,000, refused beyond). The browser and the MCP client are different connections, so with several replicas the page and the re-call may reach different processes; the chart refuses `login` mode with `mcp.replicas` above 1. A restart drops open pages; their clients are told to call again.

**Configuration.** `PLOEG_MCP_CONFIRMATION_MODE` (`login` or `elicitation`), `PLOEG_MCP_CONFIRMATION_ISSUER` (default `PLOEG_MCP_ISSUER`), `PLOEG_MCP_CONFIRMATION_CLIENT_ID` (a confidential client, never one of `PLOEG_MCP_CLIENT_IDS`), `PLOEG_MCP_CONFIRMATION_CLIENT_SECRET` (chart: `mcp.confirmationClientSecret`, an existing Secret), `PLOEG_MCP_CONFIRMATION_ACR_VALUES`, `PLOEG_MCP_CONFIRMATION_AMR`, `PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE` (10 seconds to 10 minutes) and `PLOEG_MCP_CONFIRMATION_TTL` (one minute to one hour, default ten minutes, for both modes). ploeg-mcp refuses to start in `login` mode without the client id or secret, with an unknown mode, or on a plain-http public host. In `login` mode a client that does not declare URL elicitation is refused for actions and can still read.

**Compatibility.** Requiring `login` whenever an `execute` group exists would stop every existing `execute` deployment at start: a removed behaviour, which [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) allows only after one deprecated stable minor. So this release adds `login` without breaking anything: an unset mode with an `execute` group keeps ADR-0152's client-answered confirmation and logs a `DEPRECATED` warning at start naming both values; read-only deployments see nothing. The next minor makes an unset mode with an `execute` group refuse to start, as its own `feat!` with an entry in `docs/contracts/breaking-changes.yaml`. `elicitation` stays an explicit choice after that, for a deployment whose people use clients without URL elicitation or without a browser, because it still closes F-08 and the operator then knowingly runs outside P1. No breaking change is recorded now.

### Consequences

* Good, because an action now needs the person's own fresh sign-in, which an agent or a client that answers itself cannot produce; T-OC-2's residual is closed for deployments in `login` mode.
* Good, because the person sees the action, arguments and their identity on a Ploeg page, not in text the client controls.
* Good, because a required `amr` or `acr` lets an operator demand a phishing-resistant, user-verified method (WebAuthn with user verification), so a stolen password alone cannot confirm.
* Good, because nothing changes for existing deployments until they choose, and the warning tells them to.
* Bad, because `login` mode holds state in memory and runs as one replica; a restart voids open confirmations.
* Bad, because each action costs the person a browser round trip and a sign-in, and needs an MCP client with URL elicitation.
* Bad, because the proof is only as strong as the identity provider's sign-in: without a required `amr`/`acr`, a password the agent has read somewhere still signs in. The how-to tells authentik operators to require WebAuthn with user verification.
* Bad, because ploegd still receives the confirmation as the `X-Ploeg-Confirmation` format check and does not learn that a sign-in happened; the proof is recorded in ploeg-mcp's log (`confirmation signed`, with `auth_time`, `acr`, `amr`).
* Neutral: authentik puts the same `acr` in every ID token and does not honour `acr_values`, so with authentik the method is required through `amr` (`mfa` after an authenticator validation stage, `user` after a passwordless WebAuthn login).

### Confirmation

`go test ./cmd/ploeg-mcp/ ./pkg/mcpauth/` covers it against a fake OpenID Connect issuer (httptest) and a real ploegd handler:

* must pass: `TestAPersonWhoSignsInAfreshAtThePageConfirmsTheAction` (the page shows the action, arguments and person, the authorization request carries `prompt=login`, `max_age=0`, PKCE `S256`, `acr_values`, and the re-call acts once with the person's attribution) and `TestAnMCPClientThatOpensThePageActsAfterThePersonSignsIn` (the SDK's own multi-round-trip client);
* refused: `TestASignInAsAnotherPersonConfirmsNothing` (`sub`), `TestAStaleSignInConfirmsNothing` (`auth_time` before the sign-in started; older than the maximum age), `TestASignInWithoutTheRequiredMethodConfirmsNothing` (`amr`, `acr`), `TestASignInStartedInAnotherBrowserConfirmsNothing`, `TestAnExpiredPageConfirmsAndActsNever`, `TestAPageConfirmationActsOnce` (replay), `TestADeclinedPageChangesNothing`, `TestAClientThatSkipsThePageNeverActs` (re-calls with `accept` or a ticked form, and a client answering its own elicitations), `TestAClientWithoutURLElicitationIsRefusedInLoginMode`; ID token checks in `TestIDTokensThatMustNotPassAreRefused`;
* configuration: `TestLoginModeConfigurationIsRefusedWhenIncomplete`, `TestAnUnsetModeKeepsClientConfirmationsAndWarnsOnlyForExecuteGroups`;
* each check was mutation-tested on 2026-10-25: removing the `sub`, `auth_time` (both halves), `acr`, `amr`, page expiry (re-call and page), browser binding, the wait-for-the-page branch, the decline branch, URL capability, page removal after use, ID token `nonce`, `aud`, `azp`, `sub`, `iat`, `prompt=login` or `max_age=0` fails at least one test. In `login` mode a replay is refused both by the spent set and by the page's removal; removing either alone is caught (the spent set by ADR-0152's test, the removal by `TestAPageConfirmationActsOnce`), and removing both fails both;
* `scripts/helm-golden.sh check` pins `PLOEG_MCP_CONFIRMATION_CLIENT_SECRET` from `mcp.confirmationClientSecret` and refuses `login` with two replicas (`reject-mcp-login-replicas-values.yaml`).

Not verified: no real authentik and no real MCP client (Claude Code, VS Code, Cursor) has run this flow.

## Pros and Cons of the Options

### Keep the signed form confirmation only (ADR-0152)

* Good, because it needs no browser, no second client and no state.
* Bad, because the client answers its own question; nothing shows a person was there. Kept only as the explicit `elicitation` choice.

### OpenID Connect Client-Initiated Backchannel Authentication (CIBA)

* Good, because the identity provider itself asks the person on their own device, with a binding message, and the client never sees the interaction.
* Bad, because the reference identity provider does not implement it: authentik's OAuth2 provider (source at `7ff197b`, 2026-10-25) advertises the authorization code, refresh token, implicit, client credentials, password and device code grants (`authentik/providers/oauth2/views/provider.py`), its token endpoint also handles token exchange (`views/token.py`), and nowhere in the provider is there a `backchannel_authentication_endpoint`, a `bc-authorize` route or the `urn:openid:params:grant-type:ciba` grant. CIBA support is found in commercial providers, not in the self-hosted one Ploeg's reference deployment uses. Choosing it would make Ploeg's human proof depend on a provider most adopters do not run.

### Serve the page from ploegd instead of ploeg-mcp

* Good, because ploegd has a database, so pages could survive restarts and replicas.
* Bad, because ploegd serves consumers and workers, has no person-facing HTTP, no issuer client and no confirmation state; it would need a new route, a table and a ploeg-mcp round trip on every call, which ADR-0152 already declined.

### RFC 8693 delegation with an `act` claim for the agent

* Good, because authentik 2026.8.0 and later issue an exchanged token that names the user as subject and the acting party in `act` ([authentik token exchange](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/token_exchange/)), which ploegd already records ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)). It tells who acts for whom.
* Bad, because it proves a delegation exists, not that the person approved this action now; an agent holding the delegation acts without asking. authentik's actors are its agent accounts, an Enterprise feature. Complementary, not a replacement.

## Re-evaluation triggers

* The MCP specification standardizes proof that a person answered an elicitation, or server-held request state that would let several replicas share pages.
* authentik, or another identity provider an adopter names, ships CIBA.
* An adopter needs more than one ploeg-mcp replica with `login` mode, or loses open confirmations to restarts often enough to report it.
* A widely used MCP client that adopters rely on lacks URL elicitation a stable minor after this ships.
* The next minor is prepared: make an unset mode with an `execute` group refuse to start (recorded break).
* ploegd needs to know that a sign-in confirmed an action, for example to refuse unconfirmed MCP actions itself.

## More Information

* MCP 2025-11-25, client/elicitation: URL mode is for "sensitive interactions that must not pass through the MCP client"; "Data (other than the URL itself) is not exposed to the client"; the server "MUST verify the identity of the user who opens the URL", for example "by comparing the authoritative subject (`sub` claim) from the MCP server's authorization server to the subject" of the browser session; the URL must not be pre-authenticated or carry the user's personal data. This design compares `sub` after a fresh login rather than trusting a session cookie, and its URL holds only a random token. The same section says a server "MUST NOT rely on URL mode elicitation to authorize users for themselves"; this page is not the MCP authorization, which stays the bearer token of [ADR-0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md), but a confirmation added on top of it.
* MCP Go SDK v1.8.0 (`go.mod`): `ElicitParams` carries `Mode: "url"`, `URL` and `ElicitationID`, `ElicitationCapabilities.URL` declares support, and the client's multi-round-trip middleware retries a call at most ten times.
* authentik (source at `7ff197b`): `prompt=login` forces re-authentication and `max_age` is honoured (`authentik/providers/oauth2/views/authorize.py`); the ID token's `auth_time` is the login event's time, `amr` holds `pwd`, `mfa` (after an authenticator validation stage), `user` (passwordless WebAuthn) or `sc`, and `acr` is always `goauthentik.io/providers/oauth2/default` (`authentik/providers/oauth2/id_token.py`, `authentik/common/oauth/constants.py`); the WebAuthn validation stage's user verification is configurable (`webauthn_user_verification`, default `preferred`).
* Builds on ADR-0108 and ADR-0152; closes the T-OC-2 residual for `login` deployments. Whether `execute` groups join the supported profile P1 once this ships and is configured is the owner's decision in the threat model.
* 2026-10-25: proposed with the implementation (`cmd/ploeg-mcp/pages.go`, `pkg/mcpauth/login.go`).
