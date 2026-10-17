---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-17
verified_by: "Read cmd/ploeg-mcp and pkg/mcpauth; go test ./cmd/ploeg-mcp ./pkg/mcpauth (2026-10-17, action tools); the MCP conformance suite 0.2.0-alpha.11 and the Inspector CLI --strict against the binary behind a token-injecting proxy. Not checked against a live identity provider or a running client."
---

# Serve Ploeg's MCP tools over HTTPS with a browser sign-in

**Goal:** people connect Claude Code, VS Code or Cursor to one HTTPS URL, sign in once in the browser, and read Ploeg through the same five tools as [the stdio server](ask-ploeg-from-an-ai-client.md), with no local binary, port-forward or token on their machine ([ADR-0081, amendment of 2026-10-13](../adrs/0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md#amendment-2026-10-13-a-remote-transport)).

`ploeg-mcp http` is a stateless Streamable HTTP server and an OAuth resource server. It checks the person's access token from your OpenID Connect provider, picks an Operator Consumer by the person's group, and calls ploegd with that consumer's token. The person's token never reaches ploegd.

## 1. Register a client at the identity provider

Create one public client (no secret, authorization code with PKCE S256) whose access tokens are signed JWTs. The steps below use Authentik's names; any provider works if its access tokens carry the claims in step 1.4.

1. Client id: `ploeg-mcp`. Client type: public.
2. Redirect URIs, one per client you support:

   | Client | Redirect URI |
   | --- | --- |
   | Claude Code | `http://localhost:<port>/callback`, any port, or the port you pass as `--callback-port` |
   | VS Code | `http://127.0.0.1:33418` and `https://vscode.dev/redirect` |
   | Cursor | `http://localhost:8787/callback` |

   In Authentik, a regular-expression redirect such as `^http://(localhost|127\.0\.0\.1):[0-9]+/callback$` covers Claude Code's changing port.
3. A scope `ploeg:read` whose mapping returns the person's groups, so a client that asks only for `ploeg:read` still gets them. In Authentik:

   ```python
   return {"groups": [group.name for group in request.user.ak_groups.all()]}
   ```

   Also attach `openid` and `offline_access`, so clients get a refresh token and do not open the browser every time a token expires.
4. Access token validity: 10 minutes. `ploeg-mcp` refuses a token that lives longer, unless you raise `PLOEG_MCP_MAX_TOKEN_LIFETIME`. Authentik's access tokens carry `iss`, `aud` and `azp` (the client id), `sub`, `iat`, `exp`, `scope` and, through the mapping above, `groups`.
5. Bind the application to a group, for example `ploeg-readers`, so only its members can sign in.

## 2. Give each group a read-only Operator Consumer

Follow [Ask Ploeg from an AI client, step 1](ask-ploeg-from-an-ai-client.md#1-give-it-a-read-only-operator-consumer) once per group: one consumer with `execute: false` and its own token, limited to the Teams that group may read. An existing reader consumer can serve the first group.

## 3. Run `ploeg-mcp http`

The Ploeg image ships the binary at `/usr/local/bin/ploeg-mcp`. Run it as its own Deployment next to ploegd with the argument `http` and these settings:

| Variable | Required | Meaning |
| --- | --- | --- |
| `PLOEG_URL` | yes | ploegd's base URL inside the cluster |
| `PLOEG_MCP_RESOURCE` | yes | The exact public URL clients connect to, such as `https://ploeg-mcp.example.com/mcp`. The MCP endpoint is served at its path, and the protected resource metadata names it as the resource |
| `PLOEG_MCP_ISSUER` | yes | The provider's issuer, matched byte for byte, such as `https://id.example.com/application/o/ploeg-mcp/` |
| `PLOEG_MCP_CLIENT_IDS` | yes | Comma-separated client ids whose tokens are accepted (`aud` and `azp`) |
| `PLOEG_MCP_GROUPS` | yes | JSON array of `{"group", "tokenEnv", "execute"}`. The first entry whose group the person is in decides the consumer; `tokenEnv` names the variable holding that consumer's token. `execute: true` also offers that group the action tools; its consumer then needs `execute: true` in ploegd too (see [Act on work](ask-ploeg-from-an-ai-client.md#act-on-work-over-https)) |
| `PLOEG_MCP_SUBJECT_NAMESPACE` | no | What person ids are derived under in place of the issuer: `oidc-` and the first 32 hex characters of the SHA-256 of `<namespace>\|<subject>`, with one trailing `/` removed. Set it to the issuer person ids were first recorded under when this provider application's issuer differs, or after the issuer changed, so a person keeps owning their executions ([steering](ask-ploeg-from-an-ai-client.md#steer-your-attended-executions)) |
| `PLOEG_MCP_JWKS_URL` | no | The provider's key set; default is the `jwks_uri` of the issuer's discovery document |
| `PLOEG_MCP_SCOPE` | no | The scope a token must carry; default `ploeg:read` |
| `PLOEG_MCP_REQUEST_SCOPES` | no | Space-separated scopes clients are told to request, such as `openid ploeg:read offline_access`; the required scope is always added |
| `PLOEG_MCP_MAX_TOKEN_LIFETIME` | no | Longest accepted `exp` minus `iat`; default `10m` |
| `PLOEG_MCP_LISTEN` | no | Listen address; default `:8090` |

For example:

```yaml
args: [http]
env:
  - {name: PLOEG_URL, value: "http://ploeg.ploeg.svc.cluster.local:8080"}
  - {name: PLOEG_MCP_RESOURCE, value: "https://ploeg-mcp.example.com/mcp"}
  - {name: PLOEG_MCP_ISSUER, value: "https://id.example.com/application/o/ploeg-mcp/"}
  - {name: PLOEG_MCP_CLIENT_IDS, value: ploeg-mcp}
  - {name: PLOEG_MCP_REQUEST_SCOPES, value: "openid ploeg:read offline_access"}
  - {name: PLOEG_MCP_GROUPS, value: '[{"group":"ploeg-readers","tokenEnv":"PLOEG_MCP_TOKEN_READERS"}]'}
  - name: PLOEG_MCP_TOKEN_READERS
    valueFrom: {secretKeyRef: {name: ploeg-mcp-reader, key: token}}
```

Probe `GET /healthz` for liveness and `GET /readyz` for readiness; readiness waits until the provider's keys have been fetched once. The pod needs egress to ploegd and to the provider's discovery and key URLs, and ingress from your gateway. Route the public host to the Service without stripping the `Authorization` header and without any authentication at the gateway: `ploeg-mcp` answers the 401 that starts the client's sign-in.

## 4. Connect a client

Each client finds the provider through `/.well-known/oauth-protected-resource/mcp`, opens the browser for sign-in, and stores and refreshes the token itself. Name the pre-registered client id, because the provider does not accept dynamic registration.

Claude Code:

```sh
claude mcp add --scope user --transport http --client-id ploeg-mcp ploeg https://ploeg-mcp.example.com/mcp
```

Then run `/mcp` in Claude Code, choose `ploeg` and **Authenticate**. If the provider lists exact redirect ports only, add `--callback-port <port>` and register `http://localhost:<port>/callback`.

VS Code, in the user `mcp.json` (command **MCP: Open User Configuration**):

```json
{
  "servers": {
    "ploeg": {
      "type": "http",
      "url": "https://ploeg-mcp.example.com/mcp",
      "oauth": { "clientId": "ploeg-mcp" }
    }
  }
}
```

Cursor, in `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "ploeg": {
      "url": "https://ploeg-mcp.example.com/mcp",
      "auth": { "CLIENT_ID": "ploeg-mcp" }
    }
  }
}
```

## Verify

```sh
curl -si -X POST https://ploeg-mcp.example.com/mcp | grep -i www-authenticate
curl -s https://ploeg-mcp.example.com/.well-known/oauth-protected-resource/mcp
```

The first answers 401 with `resource_metadata` and `scope`; the second names the issuer under `authorization_servers` and the exact URL as `resource`. After signing in, ask the client "what needs a person in Ploeg?".

## Symptom → cause → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| The client never opens a browser and says the server needs authentication | It cannot read the metadata or the provider's discovery | Check both URLs above from the client's machine; a browser-based client also needs CORS on the provider's discovery document |
| 401 `the token lives longer than 10m0s` | The provider issues longer access tokens | Set the access token validity to 10 minutes, or raise `PLOEG_MCP_MAX_TOKEN_LIFETIME` |
| 401 `the token has no azp or scope` | The client sent an ID token, or the provider puts no `scope` in access tokens | Use a client that sends the access token; check the provider's access token claims |
| 401 `the token is meant for another audience` | The client signed in with a client id missing from `PLOEG_MCP_CLIENT_IDS` | Pass `ploeg-mcp` as the client id, or add the id |
| 403 `insufficient_scope` | The token lacks `ploeg:read` | Attach the `ploeg:read` scope to the provider's client and sign in again |
| 403 "none of your groups gives access to Ploeg" | The token's `groups` hold none of `PLOEG_MCP_GROUPS` | Add the person to a mapped group, or check that the `ploeg:read` mapping returns groups |
| 503 "signing keys are unavailable" | `ploeg-mcp` cannot reach the provider's key set | Check egress to the provider and `PLOEG_MCP_JWKS_URL` |
| An action tool answers "This client cannot ask you to confirm" | The client does not declare elicitation, or speaks a protocol older than 2026-07-28, which cannot carry a confirmation over stateless HTTP | Use a client that supports elicitation on the 2026-07-28 protocol, or act through the operator API or a consumer application |
| An action tool answers "Ploeg refused (403 execution_forbidden)" | The group is marked `execute` in `PLOEG_MCP_GROUPS`, but its consumer is not `execute` in ploegd | Make the consumer execute-capable, or remove `execute` from the group |
| Every tool answers "Ploeg refused PLOEG_MCP_TOKEN" | The group's consumer token is not configured in ploegd | Check the token in the group's `tokenEnv` against the chart's `operator.consumers` |
