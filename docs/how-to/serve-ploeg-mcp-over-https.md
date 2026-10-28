---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-28
verified_by: "Read cmd/ploeg-mcp and pkg/mcpauth; go test ./cmd/ploeg-mcp ./pkg/mcpauth (2026-10-17, action tools; 2026-10-25, login confirmations against a fake OpenID Connect issuer); authentik's OAuth2 provider source read at 7ff197b for prompt, max_age, acr and amr; the MCP conformance suite 0.2.0-alpha.11 and the Inspector CLI --strict against the binary behind a token-injecting proxy. Not checked against a live identity provider or a running client."
---

# Serve Ploeg's MCP tools over HTTPS with a browser sign-in

**Goal:** people connect Claude Code, VS Code or Cursor to one HTTPS URL, sign in once in the browser, and read Ploeg through the same five tools as [the stdio server](ask-ploeg-from-an-ai-client.md), with no local binary, port-forward or token on their machine ([ADR-0081, amendment of 2026-10-13](../adrs/0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md#amendment-2026-10-13-a-remote-transport)).

`ploeg-mcp http` is a Streamable HTTP server and an OAuth resource server. It serves MCP 2026-07-28 requests statelessly; a client on an earlier revision (2025-11-25 or before, which is also what Claude Code falls back to when its `server/discover` probe fails or times out) gets a session when it initializes, held in ploeg-mcp's memory for 30 idle minutes and answering only the person and group that opened it, so that ploeg-mcp can ask it to confirm an action ([ADR-0157](../adrs/0157-ploeg-mcp-asks-a-client-on-the-previous-protocol-to-confirm-in-a-session.md)). Such a session lives on one replica: with more than one, route on `Mcp-Session-Id` or run one; a lost session makes the client initialize again. It checks the person's access token from your OpenID Connect provider, picks an Operator Consumer by the person's group, and calls ploegd with that consumer's token. The person's token never reaches ploegd.

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
6. The issuer, its key set and its token endpoint must be `https` URLs. `ploeg-mcp` refuses an `http` one unless `PLOEG_MCP_ALLOW_LOOPBACK_HTTP=true` and the host is `localhost` or a loopback address, a local-development setting outside profile P1.

### Audience: the resource or the client id

By default `ploeg-mcp` accepts a token only when its `aud` names `PLOEG_MCP_RESOURCE` (scheme and host in any case, the rest exactly, without an added trailing slash), and `azp` is one of `PLOEG_MCP_CLIENT_IDS` ([ADR-0174](../adrs/0174-ploeg-mcp-accepts-a-token-only-for-its-own-resource-from-an-https-issuer.md)). That is what the MCP authorization specification asks of a server: the client sends the RFC 8707 `resource` parameter with the URL the protected resource metadata names, the provider puts it in `aud`, and the server checks it (RFC 9068 section 4). A token the same client obtained for another resource is then refused.

A provider that ignores the `resource` parameter puts its client id in `aud`. Authentik does (its OAuth2 provider sets `aud` to the client id). For such a provider set `PLOEG_MCP_TOKEN_AUDIENCE=client-id`: `aud` and `azp` must then name a client id in `PLOEG_MCP_CLIENT_IDS`, and `ploeg-mcp` logs a warning at start. Give `ploeg-mcp` a client that no other resource uses, because any token issued to that client is accepted (threat model T-OC-1).
5. Bind the application to a group, for example `ploeg-readers`, so only its members can sign in.

## 2. Give each group a read-only Operator Consumer

Follow [Ask Ploeg from an AI client, step 1](ask-ploeg-from-an-ai-client.md#1-give-it-a-read-only-operator-consumer) once per group: one consumer with `execute: false` and its own token, limited to the Teams that group may read. An existing reader consumer can serve the first group.

## 3. Run `ploeg-mcp http`

The Ploeg image ships the binary at `/usr/local/bin/ploeg-mcp`. The Ploeg chart runs it as its own Deployment next to ploegd with `mcp.enabled: true`, sets `PLOEG_URL` and `PLOEG_MCP_LISTEN`, and takes the other settings from `mcp.env`, `mcp.extraEnv` and `mcp.envFrom` ([chart README](../../ops/helm/ploeg/README.md#ploeg-mcp-and-claude-runner)). Outside the chart, run it with the argument `http` and these settings:

| Variable | Required | Meaning |
| --- | --- | --- |
| `PLOEG_URL` | yes | ploegd's base URL inside the cluster |
| `PLOEG_MCP_RESOURCE` | yes | The exact public URL clients connect to, such as `https://ploeg-mcp.example.com/mcp`. The MCP endpoint is served at its path, and the protected resource metadata names it as the resource |
| `PLOEG_MCP_ISSUER` | yes | The provider's issuer, matched byte for byte, such as `https://id.example.com/application/o/ploeg-mcp/` |
| `PLOEG_MCP_CLIENT_IDS` | yes | Comma-separated client ids whose tokens are accepted: `azp` must name one, and with `PLOEG_MCP_TOKEN_AUDIENCE=client-id` so must `aud` |
| `PLOEG_MCP_TOKEN_AUDIENCE` | no | `resource` (default): a token's `aud` must name `PLOEG_MCP_RESOURCE`, scheme and host in any case (RFC 8707). `client-id`: `aud` names a client id instead, for a provider without resource indicators such as Authentik; logs a warning at start |
| `PLOEG_MCP_ALLOW_LOOPBACK_HTTP` | no | `true` accepts an `http` issuer, key set, confirmation issuer and exchange token endpoint on `localhost` or a loopback address, for local development. Default `false`: every one must be `https`. Outside profile P1; the chart refuses it unless `supportProfile: unsupported` |
| `PLOEG_MCP_GROUPS` | yes | JSON array of `{"group", "tokenEnv", "execute"}`. The first entry whose group the person is in decides the consumer; `tokenEnv` names the variable holding that consumer's token. `execute: true` also offers that group the action tools; its consumer then needs `execute: true` in ploegd too (see [Act on work](ask-ploeg-from-an-ai-client.md#act-on-work-over-https)) |
| `PLOEG_MCP_CONFIRMATION_KEY` | with an `execute` group | The key ploeg-mcp signs each person's confirmation state with (HMAC-SHA256, [ADR-0152](../adrs/0152-ploeg-mcp-signs-each-confirmation-and-acts-on-it-once.md)), at least 32 bytes, such as `openssl rand -base64 48`. ploeg-mcp refuses to start when a group is marked `execute` without it. Keep it in a Secret (the chart's `mcp.confirmationKeySecret`); every replica must read the same key. Changing it voids confirmations still open, which ask to be called again |
| `PLOEG_MCP_CONFIRMATION_MODE` | with an `execute` group | `login`: a person confirms each action by signing in afresh at ploeg-mcp's confirmation page ([ADR-0153](../adrs/0153-a-person-confirms-a-ploeg-mcp-action-by-signing-in-afresh-at-a-ploeg-mcp-page.md), section 5 below). `elicitation`: the client shows a checkbox and answers it, which a client can do without the person ([ADR-0152](../adrs/0152-ploeg-mcp-signs-each-confirmation-and-acts-on-it-once.md)). Unset with an `execute` group behaves as `elicitation` and logs a `DEPRECATED` warning at start; from the next minor it refuses to start |
| `PLOEG_MCP_CONFIRMATION_CLIENT_ID` | with `login` | ploeg-mcp's own confidential client at the provider, for the confirmation sign-in; not one of `PLOEG_MCP_CLIENT_IDS` |
| `PLOEG_MCP_CONFIRMATION_CLIENT_SECRET` | with `login` | That client's secret; the chart sets it from `mcp.confirmationClientSecret` |
| `PLOEG_MCP_CONFIRMATION_ISSUER` | no | The issuer of the confirmation client; default `PLOEG_MCP_ISSUER`. Its `sub` for a person must equal the access token's, because the person id is derived from it under the same namespace |
| `PLOEG_MCP_CONFIRMATION_AMR` | no | Space-separated `amr` values the confirmation's ID token must all carry, such as `mfa` |
| `PLOEG_MCP_CONFIRMATION_ACR_VALUES` | no | Space-separated `acr` values: sent as `acr_values`, and the ID token's `acr` must be one of them. authentik ignores `acr_values`; use `PLOEG_MCP_CONFIRMATION_AMR` there |
| `PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE` | no | How old the sign-in (`auth_time`) may be when the page receives it, 10s to 10m; default `2m` |
| `PLOEG_MCP_CONFIRMATION_TTL` | no | How long a confirmation stays open, in both modes, 1m to 1h; default `10m` |
| `PLOEG_MCP_SUBJECT_NAMESPACE` | no | What person ids are derived under in place of the issuer: `oidc-` and the first 32 hex characters of the SHA-256 of `<namespace>\|<subject>`, with one trailing `/` removed. Set it to the issuer person ids were first recorded under when this provider application's issuer differs, or after the issuer changed, so a person keeps owning their executions ([steering](ask-ploeg-from-an-ai-client.md#steer-your-attended-executions)) |
| `PLOEG_MCP_JWKS_URL` | no | The provider's key set; default is the `jwks_uri` of the issuer's discovery document |
| `PLOEG_MCP_SCOPE` | no | The scope a token must carry; default `ploeg:read` |
| `PLOEG_MCP_REQUEST_SCOPES` | no | Space-separated scopes clients are told to request, such as `openid ploeg:read offline_access`; the required scope is always added |
| `PLOEG_MCP_MAX_TOKEN_LIFETIME` | no | Longest accepted `exp` minus `iat`; default `10m` |
| `PLOEG_MCP_LISTEN` | no | Listen address; default `:8090` |
| `PLOEG_MCP_EXCHANGE_TOKEN_URL` | no | The provider's token endpoint. When set, an action exchanges the person's token there for one issued for Ploeg's audience (RFC 8693) after the person confirms, and calls ploegd with it, so ploegd records the person as proven ([ADR-0150](../adrs/0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)). Reads keep the group's token. Unset keeps actions on the group's token |
| `PLOEG_MCP_EXCHANGE_CLIENT_ID` | with the token URL | `ploeg-mcp`'s own confidential client id at the provider, allowed to exchange tokens for Ploeg's audience |
| `PLOEG_MCP_EXCHANGE_CLIENT_SECRET` | with the token URL | That client's secret; put it in a Secret and pass it with `mcp.extraEnv` |
| `PLOEG_MCP_EXCHANGE_AUDIENCE` | with the token URL | The audience of the Operator Consumer ploeg-mcp acts as: one of that consumer's `audiences` in ploegd's `PLOEG_OPERATOR_CONSUMERS`, or ploegd's `PLOEG_OPERATOR_PERSON_AUDIENCE` when the identity provider names ploeg-mcp's client in `azp` |
| `PLOEG_MCP_EXCHANGE_SCOPE` | no | Scope to request for the exchanged token |

With the exchange on, the Operator Consumer whose `audiences` in ploegd's `PLOEG_OPERATOR_CONSUMERS` list `PLOEG_MCP_EXCHANGE_AUDIENCE`, or, for Ploeg's shared audience, whose `clientIds` list the exchanged token's `azp` (ploeg-mcp's client id), is the caller for every action. authentik issues an exchanged token on the target's own client, so `azp` names the target rather than ploeg-mcp: there, give ploeg-mcp its own target and list it in its consumer's `audiences`. That consumer is the caller whichever group the person is in, so give that consumer `execute: true` and the Teams every acting group may act on. Set `PLOEG_MCP_SUBJECT_NAMESPACE` and ploegd's `PLOEG_OPERATOR_PERSON_SUBJECT_NAMESPACE` to the same value, so the person id ploeg-mcp reads with matches the one ploegd proves.

In the chart's values:

```yaml
mcp:
  enabled: true
  env:
    PLOEG_MCP_RESOURCE: https://ploeg-mcp.example.com/mcp
    PLOEG_MCP_ISSUER: https://id.example.com/application/o/ploeg-mcp/
    PLOEG_MCP_CLIENT_IDS: ploeg-mcp
    PLOEG_MCP_TOKEN_AUDIENCE: client-id
    PLOEG_MCP_REQUEST_SCOPES: openid ploeg:read offline_access
    PLOEG_MCP_GROUPS: '[{"group":"ploeg-readers","tokenEnv":"PLOEG_MCP_TOKEN_READERS"}]'
  extraEnv:
    - name: PLOEG_MCP_TOKEN_READERS
      valueFrom: {secretKeyRef: {name: ploeg-mcp-reader, key: token}}
  httpRoute:
    enabled: true
    hostnames: [ploeg-mcp.example.com]
    parentRefs: [{name: internal-gateway, namespace: gateway, sectionName: https}]
```

The same as a container spec of your own:

```yaml
args: [http]
env:
  - {name: PLOEG_URL, value: "http://ploeg.ploeg.svc.cluster.local:8080"}
  - {name: PLOEG_MCP_RESOURCE, value: "https://ploeg-mcp.example.com/mcp"}
  - {name: PLOEG_MCP_ISSUER, value: "https://id.example.com/application/o/ploeg-mcp/"}
  - {name: PLOEG_MCP_CLIENT_IDS, value: ploeg-mcp}
  - {name: PLOEG_MCP_TOKEN_AUDIENCE, value: client-id}
  - {name: PLOEG_MCP_REQUEST_SCOPES, value: "openid ploeg:read offline_access"}
  - {name: PLOEG_MCP_GROUPS, value: '[{"group":"ploeg-readers","tokenEnv":"PLOEG_MCP_TOKEN_READERS"}]'}
  - name: PLOEG_MCP_TOKEN_READERS
    valueFrom: {secretKeyRef: {name: ploeg-mcp-reader, key: token}}
```

Probe `GET /healthz` for liveness and `GET /readyz` for readiness; readiness waits until the provider's keys have been fetched once. The pod needs egress to ploegd and to the provider's discovery and key URLs, and ingress from your gateway. Route the public host to the Service without stripping the `Authorization` header and without any authentication at the gateway: `ploeg-mcp` answers the 401 that starts the client's sign-in.

## 4. Require a fresh sign-in for each action

With an `execute` group, set `PLOEG_MCP_CONFIRMATION_MODE: login`. An action then answers the client with a link (an MCP URL elicitation) to `https://<PLOEG_MCP_RESOURCE's host>/confirmations/<token>`. The page shows the action, its arguments and the person; **Sign in to confirm** sends the browser to the provider with `prompt=login` and `max_age=0`, and ploeg-mcp acts only when the person signed in afresh, as the person the action is for, with the required method, in the browser that opened the page. A client that does not declare URL elicitation can still read but cannot act. Open confirmations live in ploeg-mcp's memory, so run one replica (the chart refuses `login` with `mcp.replicas` above 1); a restart asks clients to call again.

Register a second client at the provider, confidential this time. In authentik:

1. **Provider.** An OAuth2/OpenID provider, client type *Confidential*, client id such as `ploeg-mcp-confirm`, redirect URI `https://ploeg-mcp.example.com/confirmations/callback` (strict, the exact host of `PLOEG_MCP_RESOURCE`), scopes `openid` and `profile`, signing key set (RS256 or ES256). Use the same *Subject mode* as the `ploeg-mcp` provider, so `sub` is the same for a person in both; if its issuer differs, `PLOEG_MCP_SUBJECT_NAMESPACE` must already be set (see the variable above), because person ids are derived from it.
2. **Application.** Bind the provider to an application, and the application to the groups that may act.
3. **Authentication flow.** Give the provider an authentication flow that ends in an **Authenticator Validation** stage restricted to the WebAuthn device class, with *WebAuthn user verification* set to **required**, and no "not configured" fallback that skips it. authentik honours `prompt=login` and `max_age` by sending the person through that flow again even with a session; the ID token's `auth_time` is the time of that login, and its `amr` holds `mfa` after an authenticator validation stage (`user` after a passwordless WebAuthn login). Set `PLOEG_MCP_CONFIRMATION_AMR: mfa` (or `user` for a passwordless flow). authentik's `amr` does not name the device class, so the stage's restriction to WebAuthn is what makes the sign-in phishing-resistant; keep it.
4. **Secret.** Put the client secret in a Secret, for example `ploeg-mcp-confirmation-client` with key `client-secret`, and point `mcp.confirmationClientSecret` at it.

Another provider works the same way: a confidential client, the callback above, and, if it supports them, `acr_values` for a phishing-resistant method (`PLOEG_MCP_CONFIRMATION_ACR_VALUES`) or the `amr` values its WebAuthn login emits.

```yaml
mcp:
  env:
    PLOEG_MCP_CONFIRMATION_MODE: login
    PLOEG_MCP_CONFIRMATION_CLIENT_ID: ploeg-mcp-confirm
    PLOEG_MCP_CONFIRMATION_AMR: mfa
  confirmationKeySecret: {name: ploeg-mcp-confirmation}
  confirmationClientSecret: {name: ploeg-mcp-confirmation-client}
```

The gateway route must send `/confirmations/` on the public host to ploeg-mcp as well (the chart's `mcp.httpRoute` sends every path), and the pod needs egress to the provider's token endpoint.

## 5. Connect a client

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
| 401 `the token is meant for another audience` | With `PLOEG_MCP_TOKEN_AUDIENCE=client-id`, the client signed in with a client id missing from `PLOEG_MCP_CLIENT_IDS` | Pass `ploeg-mcp` as the client id, or add the id |
| 401 `the token's aud does not name this resource` | The provider ignored the RFC 8707 `resource` parameter and put the client id in `aud`, or the client asked for another resource | Configure the provider to issue tokens for `PLOEG_MCP_RESOURCE`; for a provider that cannot, such as Authentik, set `PLOEG_MCP_TOKEN_AUDIENCE=client-id` |
| ploeg-mcp refuses to start: `issuer must be https` | `PLOEG_MCP_ISSUER`, `PLOEG_MCP_JWKS_URL`, the confirmation issuer or the exchange token URL is `http` | Use the provider's `https` URL; only for local development against a loopback provider set `PLOEG_MCP_ALLOW_LOOPBACK_HTTP=true` |
| 403 `insufficient_scope` | The token lacks `ploeg:read` | Attach the `ploeg:read` scope to the provider's client and sign in again |
| 403 "none of your groups gives access to Ploeg" | The token's `groups` hold none of `PLOEG_MCP_GROUPS` | Add the person to a mapped group, or check that the `ploeg:read` mapping returns groups |
| 503 "signing keys are unavailable" | `ploeg-mcp` cannot reach the provider's key set | Check egress to the provider and `PLOEG_MCP_JWKS_URL` |
| An action tool answers "This client cannot ask you to confirm" | The client does not declare elicitation, or it calls on a protocol older than 2026-07-28 without the session it initialized | Use a client that declares elicitation and keeps its `Mcp-Session-Id`, or act through the operator API or a consumer application |
| ploeg-mcp logs "DEPRECATED: a group in PLOEG_MCP_GROUPS is marked execute and PLOEG_MCP_CONFIRMATION_MODE is unset" | An `execute` group runs on client-answered confirmations by default | Set up section 4 and `PLOEG_MCP_CONFIRMATION_MODE: login`, or set `elicitation` to keep the current behaviour knowingly |
| An action tool answers "This client cannot open a confirmation page" | `login` mode, and the client does not declare URL elicitation | Use a client that declares URL elicitation (2025-11-25 or later), or act through the operator API or a consumer application |
| A client on 2025-11-25 loses its session, or an action's confirmation is cut off after about a minute | ploeg-mcp restarted, the session sat idle 30 minutes, another replica answered, or a proxy in front closes long requests | The client initializes again on its own. A request in a session may stay open for the confirmation window plus a minute (`PLOEG_MCP_CONFIRMATION_TTL`, default ten minutes): give the gateway route at least that timeout |
| The confirmation page says "someone other than the person this action is for" | The browser signed in as another account, or the two providers give a person different `sub` values | Sign in as the person the client is signed in as; give both providers the same subject mode |
| The page says "did not sign you in afresh" | The provider reused a session despite `prompt=login`, or the callback came more than `PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE` after the sign-in | Check the provider honours `prompt=login`; check the clocks of ploeg-mcp and the provider |
| The page says "did not use the method this Ploeg requires" | The ID token's `amr` lacks a value of `PLOEG_MCP_CONFIRMATION_AMR`, or its `acr` is not in `PLOEG_MCP_CONFIRMATION_ACR_VALUES` | Make the provider's flow end in the WebAuthn validation stage; in authentik, require `amr` values rather than `acr` |
| The page says "was not started in this browser" | The sign-in link was opened in another browser than the confirmation page, for example a link an agent passed on | Open the confirmation link itself and sign in from there |
| An action tool answers "no longer open on ploeg-mcp" | The confirmation expired, ploeg-mcp restarted, or another replica issued it | Call the tool again; run one replica in `login` mode |
| ploeg-mcp exits with "a group in PLOEG_MCP_GROUPS is marked execute, so PLOEG_MCP_CONFIRMATION_KEY must hold the key" | An `execute` group has no confirmation key | Create a Secret with at least 32 random bytes and set `mcp.confirmationKeySecret`, or remove `execute` from the group |
| An action tool answers "this confirmation does not belong to this call" or "was already used" | The client sent a confirmation ploeg-mcp did not issue for this person, tool and arguments, or one already answered; or replicas read different keys | Call the tool again. If it repeats on every call, check that every replica reads the same `PLOEG_MCP_CONFIRMATION_KEY` |
| An action tool answers "Ploeg refused (403 execution_forbidden)" | The group is marked `execute` in `PLOEG_MCP_GROUPS`, but its consumer is not `execute` in ploegd | Make the consumer execute-capable, or remove `execute` from the group |
| Every tool answers "Ploeg refused PLOEG_MCP_TOKEN" | The group's consumer token is not configured in ploegd | Check the token in the group's `tokenEnv` against the chart's `operator.consumers` |
