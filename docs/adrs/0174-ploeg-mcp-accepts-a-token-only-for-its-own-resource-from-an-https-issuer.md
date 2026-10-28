---
status: proposed
date: 2026-10-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# ploeg-mcp accepts a token only for its own resource, from an https issuer

## Context and Problem Statement

ploeg-mcp accepted any access token whose `aud` and `azp` named a configured client id ([ADR-0081, amendment of 2026-10-13](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md#amendment-2026-10-13-a-remote-transport)). A token issued to that client for another resource passed, and so did a token from an `http` issuer, whose discovery document, key set and token endpoint any party on the path could replace (finding F-14, threat T-OC-1, issue [#301](https://github.com/ploeg-hq/ploeg/issues/301)). The verifier in `pkg/mcpauth` also serves ploegd's person tokens ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)) and the confirmation sign-in ([ADR-0153](0153-a-person-confirms-a-ploeg-mcp-action-by-signing-in-afresh-at-a-ploeg-mcp-page.md)). How does ploeg-mcp make sure a token was issued for it, by the issuer it names?

## Decision Drivers

* The [MCP authorization specification, revision 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization), requires a server to "validate that access tokens were issued specifically for them as the intended audience, according to RFC 8707 Section 2" (Access Token Usage, Token Handling) and to "reject tokens that do not include them in the audience claim" (Security Considerations, Access Token Privilege Restriction). Its clients must send the `resource` parameter with the server's canonical URI, and a server should accept uppercase scheme and host components (Resource Parameter Implementation, Canonical Server URI).
* RFC 9068 section 4: a resource server must check that `aud` contains a resource indicator value it expects for itself. RFC 9728 section 2 names that value the protected resource's `resource`, which ploeg-mcp already publishes as `PLOEG_MCP_RESOURCE`.
* OpenID Connect Discovery 1.0 section 3 and RFC 8414 section 2 make the issuer an `https` URL. The MCP specification's Communication Security section, after OAuth 2.1 (draft-ietf-oauth-v2-1-13, section 1.5), says every authorization server endpoint must be served over HTTPS, and lets only redirect URIs use `localhost`.
* Authentik, the provider the how-to documents, ignores the `resource` parameter and sets `aud` to the client id (`authentik/providers/oauth2/id_token.py` line 95 at `9bbdfd9ee`, which `views/token.py` uses for the access token JWT). A deployment on it must keep working, on a choice it states.
* Local development and the tests run fake providers on `httptest` servers, which speak `http` on a loopback address.

## Considered Options

* **Require `aud` to name the resource by default, with an explicit `client-id` setting; require `https` everywhere unless a local-development setting allows `http` on a loopback host**
* Check the resource only when an operator opts in
* Accept either the resource or a client id in `aud`
* Refuse `http` with no exception

## Decision Outcome

Chosen option: "**resource by default with an explicit `client-id` setting, and `https` unless a loopback development setting**", because it is what the specification asks of an MCP server, an operator on a provider without resource indicators chooses the weaker check knowingly and is warned of it at every start, and `http` remains only where nobody else is on the path.

* `mcpauth.Config.Resource`: when set, `aud` must contain it, and `Audiences` are only the client ids `azp` must be one of. Scheme and host compare without case; path, port and a trailing slash compare exactly. A token whose `aud` is a client id alone, another resource, or the resource with another path, port, scheme or trailing slash is refused with a reason that names the RFC 8707 `resource` parameter. `Resource` cannot be combined with `AuthorizedParties` or `SelfIssuedAudiences`, whose meanings are about audiences other than this server.
* ploeg-mcp sets `Resource` to `PLOEG_MCP_RESOURCE` unless `PLOEG_MCP_TOKEN_AUDIENCE=client-id`, which keeps the previous check and logs a warning at start naming T-OC-1. Any other value stops ploeg-mcp at start.
* `mcpauth.Config`, `LoginConfig` and `ExchangeConfig` refuse an `http` issuer, JWKS URL, discovered `jwks_uri`, discovered authorization or token endpoint, and exchange token URL, unless `AllowLoopbackHTTP` is set and the host is `localhost` or a loopback address. ploeg-mcp sets it from `PLOEG_MCP_ALLOW_LOOPBACK_HTTP=true` and logs a warning; the chart treats that value, in `mcp.env` or `mcp.extraEnv`, as a departure from profile P1. ploegd's person tokens never set it, so their issuer must be `https`.

### Consequences

* Good, because a token issued to ploeg-mcp's client for another resource no longer reaches the tools, on a provider that implements RFC 8707.
* Good, because the issuer's keys and the exchange's client secret no longer travel over `http` outside a developer's own machine.
* Bad, because an upgrade on a provider without resource indicators, Authentik included, refuses every token until `PLOEG_MCP_TOKEN_AUDIENCE=client-id` is set. Accepted: the refusal says what to set, the how-to and its symptom table say it, and a silent default to the weaker check is what F-14 recorded.
* Bad, because with `client-id` T-OC-1 stays open: any token issued to that client is accepted. Accepted while Authentik cannot do better; the how-to tells the operator to give ploeg-mcp a client no other resource uses.
* Bad, because a ploegd whose `PLOEG_OPERATOR_PERSON_ISSUER` is `http` stops at boot. Accepted: no supported deployment runs one, and the same tokens carry a person's authority.

### Confirmation

`go test ./pkg/mcpauth/ -run 'HTTP|Discovery|Resource'` refuses `http` issuers, key sets, discovered `jwks_uri`, confirmation issuers and exchange token endpoints without the setting and on a non-loopback host with it, verifies a token from an `https` issuer, and refuses every token not for the configured resource while passing one for it. `go test ./cmd/ploeg-mcp/ -run 'Audience|Loopback'` drives the HTTP server: a token whose `aud` is the client id is refused by default and accepted with `client-id`, and each setting's value and warning are checked. `scripts/helm-golden.sh` (in `mise run verify`) has `leaves_p1` cases for `PLOEG_MCP_ALLOW_LOOPBACK_HTTP=true` in `mcp.env` and `mcp.extraEnv`, and a `renders_p1` case for `false`.

## Pros and Cons of the Options

### Check the resource only when an operator opts in

* Good, because no deployment changes on upgrade.
* Bad, because the default stays below the MCP specification and F-14 stays open for every deployment that does not read the release notes.

### Accept either the resource or a client id in `aud`

* Good, because Authentik and RFC 8707 providers both work with no setting.
* Bad, because a token whose `aud` is the client id still passes, which is the hole F-14 names.

### Refuse `http` with no exception

* Good, because there is no setting to misuse.
* Bad, because the tests and local development need a provider on a loopback `httptest` server, and a test-only path in production code is harder to see than a named setting the chart refuses.

## Re-evaluation triggers

* Authentik puts the RFC 8707 `resource` parameter in `aud` (a release note, or `resource` handled in `authentik/providers/oauth2/views/token.py`); then `client-id` can leave the documented setup.
* The MCP authorization specification changes how a server identifies itself in `aud`, for example by allowing a normalized match.
* A client ploeg-mcp supports sends a `resource` that differs from `PLOEG_MCP_RESOURCE` by a trailing slash or the case of its path.
* A deployment needs ploeg-mcp to accept tokens for more than one resource identifier.

## More Information

* Finding F-14 and threat T-OC-1 in the [threat model](../security/threat-model.md#findings); issue [#301](https://github.com/ploeg-hq/ploeg/issues/301).
* How-to: [Serve Ploeg's MCP tools over HTTPS](../how-to/serve-ploeg-mcp-over-https.md#audience-the-resource-or-the-client-id).
* Narrows the token check of [ADR-0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md)'s amendment of 2026-10-13, whose trigger "the identity provider supports RFC 8707 resource indicators" this record answers for providers that do.
