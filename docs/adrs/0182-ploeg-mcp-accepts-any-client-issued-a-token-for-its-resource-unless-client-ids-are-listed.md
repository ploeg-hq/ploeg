---
status: proposed
date: 2026-10-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# ploeg-mcp accepts any client issued a token for its resource, unless client ids are listed

## Context and Problem Statement

ploeg-mcp refuses to start without `PLOEG_MCP_CLIENT_IDS`, and accepts a token only when its `azp` names one of those pre-registered client ids, also under the resource audience of [ADR-0174](0174-ploeg-mcp-accepts-a-token-only-for-its-own-resource-from-an-https-issuer.md). The [MCP specification, revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/changelog), deprecates Dynamic Client Registration in favour of Client ID Metadata Documents: a client names itself by an `https` URL that serves its metadata, and the authorization server fetches and validates that document. Such a client has no id an operator can list in advance. Which clients does ploeg-mcp accept?

## Decision Drivers

* [Client Registration, revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration.md): clients and authorization servers should support Client ID Metadata Documents; the `client_id` URL must use `https` and contain a path; Dynamic Client Registration is deprecated.
* [draft-ietf-oauth-client-id-metadata-document-00](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-client-id-metadata-document-00) section 3: a client identifier URL has an `https` scheme and a path, no `.` or `..` segments, no fragment, no username or password.
* [Authorization, Token Handling](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization): a server must accept only tokens issued for it as the audience (RFC 8707). Under ADR-0174's `resource` default, `aud` already proves that; the client allowlist adds nothing the specification asks for.
* RFC 9068 section 2.2 names the client in `client_id`; Authentik puts it in `azp`.
* The confirmation sign-in client of [ADR-0153](0153-a-person-confirms-a-ploeg-mcp-action-by-signing-in-afresh-at-a-ploeg-mcp-page.md) must never pass as an MCP client.

## Considered Options

* **Under the `resource` audience, an empty `PLOEG_MCP_CLIENT_IDS` accepts any client; a listed one, opaque or a validated metadata URL, still restricts**
* Keep `PLOEG_MCP_CLIENT_IDS` required and let operators list metadata URLs
* Fetch and validate each client's metadata document in ploeg-mcp

## Decision Outcome

Chosen option: "**an empty list accepts any client under the `resource` audience**", because the token's `aud` already binds it to this server, the identity provider is the party that registers and vets clients, and a client using a metadata document then works with no operator step.

* `mcpauth.Config.Audiences` may be empty when `Resource` is set: any client the issuer issued a token for the resource passes. Under `Resource` the client is `azp`, or `client_id` when the token has no `azp`, and must still be present, with a `scope` claim. `RefusedClients` lists ids never accepted; ploeg-mcp puts `PLOEG_MCP_CONFIRMATION_CLIENT_ID` there.
* `mcpauth.CheckClientID` accepts an opaque id, or a metadata URL that is `https` (lowercase), has a host and a path, and has no credentials, fragment or `.` and `..` segments, plain or percent-encoded. ploeg-mcp stops at start on any other entry in `PLOEG_MCP_CLIENT_IDS`. A listed id compares byte for byte.
* `PLOEG_MCP_TOKEN_AUDIENCE=client-id` keeps requiring `PLOEG_MCP_CLIENT_IDS`, because `aud` must then name a listed client.
* The issuer still compares byte for byte, and the protected resource metadata names `PLOEG_MCP_ISSUER` exactly as configured, as RFC 9207 and the authorization specification's Authorization Response Validation expect of the issuer a client records.

### Consequences

* Good, because a client with a Client ID Metadata Document connects without an operator adding its id.
* Good, because an operator who wants a fixed set of clients keeps it, now including metadata URLs.
* Bad, because with an empty list a client the provider trusts but the operator did not intend can reach the tools. Accepted: the person still signs in, still needs a mapped group, and actions still need their confirmation; restrict at the provider or list the clients.
* Neutral, because a token whose only client claim is `client_id` now passes under `Resource` when its client is accepted.

### Confirmation

`go test ./pkg/mcpauth/ -run 'ClientID|AnyClient|Issuer'` checks the client id rules, that an empty list under a resource accepts `azp` or `client_id` of any client but a refused one and refuses a token without a client, a scope or this resource, and that the issuer is compared exactly. `go test ./cmd/ploeg-mcp/ -run 'AnyClient|ExactlyAs|Incomplete|MetadataDocument'` drives the HTTP server with an empty list, a listed metadata URL matched byte for byte, invalid URLs refused at start, `client-id` without ids refused, and the metadata naming the issuer as configured.

## Pros and Cons of the Options

### Keep `PLOEG_MCP_CLIENT_IDS` required and let operators list metadata URLs

* Good, because nothing reaches the tools that the operator did not name.
* Bad, because every new client needs a configuration change and a restart, which is the friction metadata documents remove.

### Fetch and validate each client's metadata document in ploeg-mcp

* Good, because ploeg-mcp could apply its own trust policy to client domains.
* Bad, because that is the authorization server's job in the specification, and it adds outbound fetches, caching and SSRF exposure to a resource server.

## Re-evaluation triggers

* The MCP authorization specification asks a resource server to restrict or verify clients.
* A deployment's identity provider issues tokens for the resource to clients its operator does not vet.
* Authentik supports Client ID Metadata Documents, or the resource audience, so the documented setup can drop the list.
* A client ploeg-mcp supports sends a metadata URL that differs from the token's client claim only by normalisation.

## More Information

* How-to: [Serve Ploeg's MCP tools over HTTPS](../how-to/serve-ploeg-mcp-over-https.md#audience-the-resource-or-the-client-id).
* Builds on [ADR-0174](0174-ploeg-mcp-accepts-a-token-only-for-its-own-resource-from-an-https-issuer.md), whose resource audience it relies on.
