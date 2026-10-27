---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An Operator Consumer proves the person it acts for with a token exchanged for Ploeg's audience

## Context and Problem Statement

[ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) made every operator mutation name its person, but the person is whatever the Operator Consumer writes in `X-Ploeg-Actor` or `X-Ploeg-Acting-User`. ploegd checks the format and nothing else, so anyone holding a consumer's static token can act as, or claim client-carried Runs for, any person inside that consumer's Teams ([threat model](../security/threat-model.md) F-07). ADR-0108 accepted that as a known weakness and named its trigger: "a consumer needs Ploeg to verify the person itself instead of trusting the consumer". `ploeg-mcp` is the clearest case: it verifies the person's access token and then calls ploegd with a token its whole group shares, re-asserting the person in a header.

Consumers already sign people in at an OpenID Connect identity provider. RFC 8693 token exchange lets a consumer trade the person's token there for a token issued for another audience on the person's behalf, optionally naming the party that acts for them in an `act` claim. How does ploegd accept such a token, which consumer is then the caller, and how does an audit record tell a proven person from an asserted one?

## Decision Drivers

* A person named on an operator mutation is proven by the identity provider, not by the consumer's word.
* The standard way: an access token for Ploeg's audience is the bearer credential (RFC 6750, RFC 8693, RFC 9068), so a consumer needs no Ploeg-specific signing.
* The consumer stays known and scoped: Teams, `execute`, `verify` and `maxBudgetUsd` still come from the consumer's configuration.
* One person id across every consumer, the one ADR-0108 already derives from an OpenID Connect subject.
* Existing consumers keep working for one release while they adopt it, and every audit record says which kind of attribution it holds.
* Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)); the rule is for any Operator Consumer acting for a person.
* Reuse the token verification `ploeg-mcp` already has in `pkg/mcpauth` instead of writing a second verifier.

## Considered Options

* **The exchanged person token is the bearer and its `azp` names the consumer; a consumer may also send it next to its own static token**
* Only a dedicated header next to the consumer's static token
* Ploeg signs its own person assertions for consumers
* Mutual TLS per consumer, with the person still in a header

## Decision Outcome

Chosen option: "**the exchanged person token is the bearer**", because it is the standard shape, proves the person and the consumer in one credential, and leaves room for a consumer that must keep its static token as the caller.

### How a person token is carried

A person token belongs to one Operator Consumer, found in one of two ways:

* **By its audience (preferred).** Each consumer may declare `audiences`: the audience its person tokens are exchanged for, which no other consumer shares (one identity provider target per consumer). A token whose `aud` names one consumer's audience belongs to that consumer. Its `azp` (or `client_id`) must be present and is either that audience, which is how an identity provider that issues the token on the target's own client fills it, or one of the consumer's `clientIds`. A token whose `aud` names the audiences of two consumers is refused.
* **By its client.** A token whose `aud` names no consumer's audience must name Ploeg's shared audience, and belongs to the consumer whose `clientIds` list its `azp` (or `client_id`). This serves identity providers that name the requesting client in `azp`.

It is carried in one of two ways:

* **As the bearer.** `Authorization: Bearer <person token>`. ploegd verifies it, and the consumer it belongs to is the caller. A client listed by more than one consumer is ambiguous and refused.
* **Next to the consumer's own credential.** `Authorization: Bearer <static token>` plus `X-Ploeg-Person-Token: <person token>`. The static token names the caller, and the person token must belong to *that* consumer: its `aud` names one of that consumer's `audiences`, or, without a consumer audience, its `azp` is one of that consumer's `clientIds`; otherwise 401. The client form serves a consumer that maps people to several Operator Consumers with different scopes behind one identity provider client.

### What ploegd verifies

A person token is a JWT the configured issuer signed with an asymmetric algorithm (RS256 or ES256), checked against the issuer's keys (its discovery document, or `PLOEG_OPERATOR_PERSON_JWKS_URL`). `iss` must equal the issuer byte for byte (one issuer for every consumer; with authentik, the targets use the global issuer mode), `aud` must name a consumer's audience or Ploeg's shared audience, `exp` and `iat` must be present and hold within 30 seconds of skew, the lifetime from `iat` to `exp` is at most five minutes (configurable up to an hour), and `sub` is present. A scope value is not required. An ID token never passes: a client id equal to any audience is refused at start, so a token whose `azp` is a client id was not issued to its own audience, and a token whose `azp` equals its consumer audience must carry a `scope` claim, even an empty one, which access tokens have and ID tokens lack. An `act` claim, when present, must carry a `sub`.

A presented person token that fails any check answers 401 `invalid_token` with an RFC 6750 challenge, and nothing changes. Unreachable keys answer 503.

### Who may act through a consumer

A verified person token proves a person; it does not say that this person may use the consumer. Without more, whoever the identity provider issues a token for a consumer's audience or client acts with that consumer's Teams, `execute`, `verify` and budget, recorded `proven` ([threat model](../security/threat-model.md) F-47). So a consumer may name `requireClaims`, a map from claim name to the values it accepts, for example `{"groups": ["ploeg-operators"]}`:

* After the token verifies and its consumer is found, every named claim must hold one of its values: a string equal to one, or an array of strings that contains one. `scope` is split on spaces first. Matching is exact and case-sensitive.
* A token that lacks one answers 403 `person_claim_required`, naming the claim and not its values, whether it came as the bearer or in `X-Ploeg-Person-Token`, and nothing changes.
* `requireClaims` on a consumer that names neither `audiences` nor `clientIds` refuses to start; so does a claim with no value, an empty or duplicated value, or more than ten claims or twenty values per claim.
* ploegd warns at start for each consumer with `execute` that names `audiences` or `clientIds` and no `requireClaims`. Under profile P1 the chart lists such a consumer as a departure from chart 0.3.0, prereleases included, and warns in its install notes before that.

The installer still owns the identity provider's policy: who may obtain a token for the consumer's audience or client, and who is in the group `requireClaims` names. Ploeg checks the claim; it cannot check that the claim means what the installer intends.

### The person and the audit record

* **The person** is `operatorclient.PersonID(<namespace>, sub)`, the id ADR-0108 already uses: `oidc-` and the first 32 hex characters of the SHA-256 of `<namespace>|<sub>`. The namespace is the issuer, or `PLOEG_OPERATOR_PERSON_SUBJECT_NAMESPACE`, so that person ids survive a change of issuer and agree with the ids a consumer derives from its own issuer.
* **Proven or asserted.** Every operator mutation's `detail.attribution` gains `proof`: `proven` when a verified person token named the person, `asserted` when only `X-Ploeg-Actor` did. A proven attribution also records `act`, the token's `act.sub`, the party acting for the person. Events recorded before this decision carry no `proof`.
* **One person per request.** With a proven person, ploegd sets `X-Ploeg-Actor` to that person for every route, so routes that read the header see the proven id. An `X-Ploeg-Actor` or `X-Ploeg-Acting-User` naming anyone else answers 403 `actor_conflict`, and nothing changes.
* **Headers stay asserted for one release.** A consumer that sends only `X-Ploeg-Actor` keeps working and is recorded as `asserted`. Under [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) a later decision may refuse asserted attribution on mutations after a deprecated minor; this one does not.

### Configuration

ploegd reads `PLOEG_OPERATOR_PERSON_ISSUER`, `PLOEG_OPERATOR_PERSON_AUDIENCE`, `PLOEG_OPERATOR_PERSON_JWKS_URL`, `PLOEG_OPERATOR_PERSON_MAX_LIFETIME` (default `5m`) and `PLOEG_OPERATOR_PERSON_SUBJECT_NAMESPACE`, and each `PLOEG_OPERATOR_CONSUMERS` entry may list `audiences`, `clientIds` and `requireClaims`. `PLOEG_OPERATOR_PERSON_AUDIENCE` is needed only when a consumer lists `clientIds`. The chart forwards `operator.personTokens` and each consumer's `audiences` and `clientIds`. ploegd refuses to start on `audiences` or `clientIds` without an issuer, an issuer without any consumer `audiences` or `clientIds`, `clientIds` without Ploeg's audience, an audience listed by two consumers, an audience equal to any consumer's client id, and an audience equal to Ploeg's shared audience.

### `ploeg-mcp`

In HTTP mode, when `PLOEG_MCP_EXCHANGE_TOKEN_URL` is set, an action exchanges the caller's verified token at the identity provider after the person confirms (RFC 8693, `audience` the acting consumer's own audience, or Ploeg's shared audience for an identity provider that names ploeg-mcp in `azp`, authenticated as ploeg-mcp's own confidential client) and calls ploegd with the exchanged token as the bearer. Reads keep the group's consumer token. Without the setting nothing changes. The Operator Consumer that lists that audience in `audiences`, or ploeg-mcp's client id in `clientIds`, is then the caller for every action, whatever the person's group; a deployment whose groups map to consumers with different scopes keeps the static tokens or runs one ploeg-mcp per scope.

### Consequences

* Good, because a stolen consumer token alone no longer names a person once its consumer sends person tokens, and the audit record says which records still rest on a consumer's word.
* Good, because the identity provider's `act` claim reaches the audit record unchanged, ready to be carried further by a later Run token.
* Good, because one verifier serves `ploeg-mcp` and ploegd.
* Good, because a person token issued for one consumer's audience cannot be replayed as another consumer: its `aud` names its consumer, so presenting it as the bearer makes that consumer the caller, and presenting it next to another consumer's credential is refused.
* Bad, because a consumer that still sends headers keeps F-07's exposure until headers are refused; this decision narrows F-07 and does not close it.
* Bad, because a person token outlives the revocation of the token it came from by up to its lifetime; five minutes bounds that.
* Bad, because person ids from a consumer and from ploegd agree only when both use the same subject namespace; the configuration reference says so.
* Good, because `requireClaims` lets the installer name, in Ploeg's own configuration, which people may act through a consumer, instead of leaving it to the identity provider's policy for that audience or client alone.
* Bad, because a claim is only as narrow as the identity provider's group or role behind it; P1 makes `requireClaims` required on an executing bearer consumer but cannot see who is in the group.

### Confirmation

`go test ./pkg/httpapi/ -run 'Person|Proven|ActClaim|HeadersAlone|Refused|Audience|ClientIDTokens'` verifies against an in-test issuer: a valid token as bearer and next to the static token, the `act` claim recorded, header attribution marked `asserted`, and refusals for a wrong audience, an expired token, a lifetime over the bound, an unknown `azp`, a bad signature, another issuer, an `azp` of another consumer than the static token's, and a conflicting `X-Ploeg-Actor`. With consumer audiences it verifies that a token shaped as authentik issues it (`aud` and `azp` the consumer's audience, the global issuer, an empty `scope`) makes that consumer the caller and records `act`, that the same token next to another consumer's credential is refused, that tokens naming two consumers' audiences, an ID token's shape, or another consumer's audience or client in `azp` are refused, that `clientIds` tokens still work beside audiences, and that an audience listed twice, equal to a client id or to Ploeg's audience refuses to start. `go test ./pkg/mcpauth/ -run OwnAudience` covers the verifier's self-issued audiences. `TestAProvenNoteRecordsTheProvenPersonInTheAudit` checks the stored audit row and the event list. `go test ./pkg/mcpauth/` covers the exchanged-token verifier and the exchanger against a fake token endpoint, and `go test ./cmd/ploeg-mcp/ -run Exchange` drives an action through the exchange into a real ploegd handler. `go test ./pkg/config/` renders the chart's person token values, consumer audiences and `requireClaims`. `go test ./pkg/httpapi/ -run 'RequiredGroup|RequiredClaims|RequiresItsClaims|RequireClaimsConfiguration|AnyPersonExecute'` verifies that a valid person token without the required group is answered 403 on the notes route with no audit row, and the same token with the group is recorded `proven`; that the check holds for the bearer, the header and an audience consumer, for string, array and `scope` claims; and that bad `requireClaims` refuse to start. `go test ./pkg/mcpauth/ -run EveryPayloadClaim` covers the claims the verifier hands on. The `BEARER_VALUES` cases in `scripts/helm-golden.sh` render the consumer under 0.2 with the warning, and refuse it under P1 for a copy of the chart at 0.3.0-rc.1 and 0.3.0. Reviewers check that no new Ploeg text names a consumer.

## Pros and Cons of the Options

### Only a dedicated header next to the consumer's static token

* Good, because the consumer's scope is always explicit.
* Bad, because it invents a Ploeg-only carriage for a credential the standards carry as the bearer, and every consumer keeps a long-lived static token on every call.

### Ploeg signs its own person assertions

* Good, because Ploeg controls the format.
* Bad, because Ploeg would first have to verify the person itself, which is the identity provider's job, and every consumer would need a second sign-in flow.

### Mutual TLS per consumer, with the person still in a header

* Good, because it binds the consumer's credential to a key.
* Bad, because the person stays asserted, which is the weakness to remove.

## Re-evaluation triggers

* Every known consumer sends person tokens for mutations: decide when asserted attribution is refused.
* A Run token shaped like an OAuth Transaction Token lands and needs this token's `act` nested inside it.
* An identity provider in use cannot put `azp` or `client_id` in exchanged tokens, cannot issue a token for a per-consumer audience, or cannot keep their lifetime at five minutes or less.
* A deployment needs consumers behind different issuers.
* A deployment needs one identity provider client to act as several consumers in bearer mode.
* `ploeg-mcp` confirmations become signed records (F-08), which changes what a proven action from it means.
* A deployment needs a claim matched other than by exact string, such as a nested claim or a prefix, or an identity provider in use cannot put groups or roles in an exchanged token.

## More Information

* Amends [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md): its accepted cost "ploegd trusts the Operator Consumer to name the person truthfully" now holds only for asserted attribution.
* Narrows [threat model](../security/threat-model.md) F-07.
* Standards: RFC 8693 (token exchange, `act`), RFC 6750 (bearer tokens), RFC 9068 (JWT access tokens, `client_id`), RFC 7519 (JWT).
* 2026-10-24 — Amended: consumers may declare `audiences`. authentik 2026.8.5 issues an RFC 8693 exchanged token on the `audience` target's provider, so `aud` and `azp` are both the target's client id and `iss` is the target's issuer, or the global issuer in global issuer mode; the token never names the requesting client (`create_token_exchange_response` in `authentik/providers/oauth2/views/token.py` and `IDToken.new` and `to_access_token` in `id_token.py`, tag `version/2026.8.5`). Choosing the consumer by `azp` in `clientIds` cannot work there, and one shared Ploeg audience would let any consumer replay a person token issued for another. A per-consumer audience names the consumer in `aud`, stops that replay, and needs no change from an identity provider that names the client; the `clientIds` path stays for those.
* 2026-10-28 — Amended: a consumer may name `requireClaims` (threat model F-47, [#357](https://github.com/ploeg-hq/ploeg/issues/357)). The 2026-10-27 security review found that a person token alone carried its consumer's whole principal, and that only the identity provider's policy for the audience or client decided who could obtain one; the verifier reads no group or role. A token without a required claim value now answers 403 `person_claim_required`. Profile P1 requires `requireClaims` on a consumer with `execute` that accepts person tokens as the bearer from chart 0.3.0, and ploegd and the install notes warn before then, so an installer has the 0.2 line to add the claim.
