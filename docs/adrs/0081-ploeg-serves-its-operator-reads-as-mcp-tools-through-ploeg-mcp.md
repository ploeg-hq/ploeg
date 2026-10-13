---
status: accepted
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# Ploeg serves its operator reads as MCP tools through a separate ploeg-mcp command

## Context and Problem Statement

People work in AI clients that speak the Model Context Protocol. From there they could reach the tracker but not Ploeg: which Work Items wait for a person and why, what a Shift spent, what a Run's verdict was. The operator API already serves all of it, but only to a consumer that writes its own HTTP client. The owner accepted an MCP server on the operator API on 2026-09-30, in the consumer's decision record, and asked on 2026-10-10 for it to be built. Where does that server live, and what may it do?

## Decision Drivers

* A person asks "what is waiting for me and why" from their own client, without a browser.
* Ploeg names none of its consumers ([ADR 0069](0069-ploeg-names-none-of-its-consumers.md)); the server is one more Operator Consumer.
* ploegd's attack surface and its authority over budgets do not grow.
* Runs get no Ploeg MCP server; they reach tools only through the gateway ([ADR 0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md)).

## Considered Options

* A separate `ploeg-mcp` command that reads the operator API with its own consumer token
* MCP routes inside ploegd
* No server: document raw operator API calls

## Decision Outcome

Chosen option: "A separate `ploeg-mcp` command", because it adds no route, no listener and no authority to ploegd, and a deployment that does not want it never runs it.

* `cmd/ploeg-mcp` serves MCP over stdio with `github.com/modelcontextprotocol/go-sdk`. It reads `PLOEG_URL` and `PLOEG_MCP_TOKEN` and refuses to start without them.
* It calls the operator API through `pkg/operatorclient`, which sends the bearer token, refuses redirects, bounds each call to 5 seconds and 16 MiB, and never puts the token in an error.
* It has five read-only tools: `ploeg_overview`, `ploeg_find_work`, `ploeg_get_work`, `ploeg_recent_runs` and `ploeg_changes_since`. Each has a title, `readOnlyHint`, an output schema, and returns its answer as structured content and text.
* Text from trackers, forges and agents sits under an `untrusted` key; the server's instructions tell the client to treat it as data. Amounts are rounded to cents.
* An operator answer of 401, 403, 404 or a timeout becomes a tool error without the token, and the server keeps running.
* The consumer that `ploeg-mcp` uses should have `execute: false`. Write tools (propose, approve, cancel) and a remote transport are not part of this decision.
* The Ploeg image ships the binary at `/usr/local/bin/ploeg-mcp`; the chart is unchanged.

### Consequences

* Good, because any MCP client reads Ploeg's state with one local command and one token.
* Good, because ploegd is unchanged and the reader is an ordinary, revocable Operator Consumer.
* Bad, because the module gains the MCP Go SDK and its dependencies; only `cmd/ploeg-mcp` imports them.
* Bad, because a laptop needs a route to ploegd, a port-forward until a remote transport is decided.

### Confirmation

`go test ./cmd/ploeg-mcp/` drives every tool through in-memory MCP transports against the real operator handler and PostgreSQL, including the 401, 404 and timeout paths and the token never appearing in a result. `go test ./pkg/operatorclient/` tests the client against the same handler. `go test ./internal/boundary/` keeps consumer names out of both.

## Amendment 2026-10-13: a remote transport

The second re-evaluation trigger fired: the owner wants Ploeg's tools in Claude Code, VS Code and Cursor on machines with no route to ploegd, without a local binary, a port-forward or a pasted token. The owner decided on 2026-10-13 that the server is reachable on the LAN and VPN only, that it validates the sign-in itself rather than behind a gateway policy, that it gets a dedicated client at the identity provider, that a person reads as the Operator Consumer their identity-provider group maps to, and that it stays read-only.

* `ploeg-mcp http` serves stateless Streamable HTTP with JSON responses (`StreamableHTTPOptions{Stateless: true, JSONResponse: true}`). The go-sdk answers both `server/discover` clients of MCP 2026-07-28 and `initialize` clients of 2025-11-25 on the one endpoint. Running `ploeg-mcp` without arguments is the stdio server, unchanged.
* It is an OAuth 2.1 resource server for one OpenID Connect issuer, in `pkg/mcpauth`. It verifies the JWT access token's signature against the issuer's published keys, matches `iss` byte for byte, requires `aud` and `azp` to be one of the configured client ids, requires `exp`, `iat` and `sub`, tolerates at most 60 seconds of clock skew, refuses a token living longer than 10 minutes, and refuses a token without `azp` or `scope`, which is how an ID token presented as an access token fails. Only asymmetric algorithms are accepted. An unknown key id refetches the key set at most once per 30 seconds.
* It publishes RFC 9728 protected resource metadata at `/.well-known/oauth-protected-resource` and at the path-inserted form for its endpoint, naming the issuer as the only authorization server and the exact public URL as the resource, with CORS. It writes the RFC 6750 challenges itself: 401 with `resource_metadata` and `scope`, `error="invalid_token"` for a refused token, and 403 `error="insufficient_scope"` when the required scope (default `ploeg:read`) is missing. `auth.RequireBearerToken` is not used because its 403 carries no `error` parameter.
* The caller's token never leaves `ploeg-mcp`. `PLOEG_MCP_GROUPS` maps identity-provider groups to Operator Consumer tokens; the first configured group the person is in decides the consumer whose token calls ploegd. A person in no configured group gets 403 naming the groups that give access. Each mapped consumer should have `execute: false`.
* The person's identity is logged but not sent to ploegd; operator calls are still attributed to the consumer. Naming the acting person to ploegd waits for [ADR 0089](0089-operator-consumers-read-knowledge-and-write-notes-for-a-named-person-within-scopes-ploegd-checks.md).

### Consequences of the amendment

* Good, because a person signs in once in the browser and every client on the LAN or VPN uses Ploeg's tools without a binary, a token on disk or a route to ploegd.
* Good, because ploegd is still unchanged: `ploeg-mcp` remains an ordinary consumer of the operator API, now one per group.
* Bad, because `ploeg-mcp` becomes a network service that checks tokens, which the stdio command never did; a mistake in `pkg/mcpauth` is an access-control mistake.
* Bad, because the access token's lifetime and its group claim are configured at the identity provider; a provider issuing hour-long tokens is refused until it issues 10-minute ones or `PLOEG_MCP_MAX_TOKEN_LIFETIME` is raised.

### Confirmation of the amendment

`go test ./pkg/mcpauth/` signs tokens with RSA and EC keys against a fake key set and refuses ID tokens, other issuers and audiences, a foreign `azp`, missing claims, long lifetimes, expiry beyond the skew and forged algorithms; it pins the 401, 403 and 503 answers and the key refetch limit. `go test ./cmd/ploeg-mcp/` drives the HTTP server with the go-sdk client and with raw 2025-11-25 requests, and asserts that ploegd receives the group's consumer token and never the caller's.

### Re-evaluation triggers of the amendment

* The server must be reachable from outside the LAN and VPN, such as by Claude.ai or ChatGPT connectors.
* The identity provider supports RFC 8707 resource indicators, so `aud` can name the resource instead of a client id.
* A person must read as themselves rather than as their group's consumer.

## Pros and Cons of the Options

### MCP routes inside ploegd

* Good, because no extra process.
* Bad, because ploegd would carry a protocol, session handling and a new public surface next to budget authority, for every deployment.

### No server

* Good, because nothing to build.
* Bad, because every client and every person writes their own operator calls and token handling.

## Re-evaluation triggers

* A person asks to propose, approve or cancel work from an AI client.
* Someone needs Ploeg's tools from a machine without a route to ploegd.
* The MCP Go SDK publishes a major version or the stdio transport is deprecated.

## More Information

* How-to: [Ask Ploeg from an AI client over MCP](../how-to/ask-ploeg-from-an-ai-client.md).
* 2026-10-13 — Amended: `ploeg-mcp http` serves stateless Streamable HTTP as an OAuth resource server and maps a person's groups to Operator Consumers, because the trigger "someone needs Ploeg's tools from a machine without a route to ploegd" fired.
* The consumer's record of 2026-09-30 named the tools after the consumer; this record names them after Ploeg because of ADR 0069.
