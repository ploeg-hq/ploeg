---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-11-19
verified_by: "Read pkg/connection, pkg/httpapi/operator_connections.go, pkg/provider/clickup/handover.go and cmd/ploegd/runtime_connections.go; go test ./pkg/connection/ ./pkg/provider/clickup/; go test ./pkg/httpapi/ -run 'Connection|ClickUp|GitLab|GitHubApp|Reconnect|HandOver|ServiceAccount|OwnerToken'"
---

# Let operators connect trackers and forges

Use this when an operator consumer should connect a tracker or forge on a person's behalf: ClickUp or GitLab or Forgejo through OAuth, GitHub through an App Ploeg creates from a manifest, GitLab through a group service account Ploeg makes from an Owner's short-lived token, or a pasted token where no OAuth application exists. Ploeg starts every flow and issues its `state`; the consumer hosts the vendor's callback and relays the code to Ploeg in a request body ([ADR-0195](../adrs/0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md)). Every secret goes to the secret store, never to a row, log, response or event ([ADR-0184](../adrs/0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md)).

## Before you start

- [Configure a connection secret store](configure-a-connection-secret-store.md). Without one, every change answers `503 secret_store_unavailable`.
- Person tokens must be configured: every change needs a proven person, in every attribution mode ([ADR-0150](../adrs/0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)).

## 1. Opt in and name what may be connected

Add a `connections` section to the configuration file:

```yaml
connections:
  enabled: true
  max: 20
  requireClaims:
    groups: [ploeg-admins]
  stepUp:
    acrValues: [mfa]
    maxAge: 900
  allow:
    - {port: tracker, kind: clickup, instance: clickup, methods: [oauth]}
    - {port: forge, kind: github, instance: github, methods: [github_app_manifest, github_app_installation]}
    - {port: forge, kind: gitlab, instance: gitlab, methods: [oauth, gitlab_service_account]}
  instances:
    clickup:
      kind: clickup
      oauth:
        clientId: "ABC123"
        clientSecretEnv: PLOEG_CLICKUP_OAUTH_CLIENT_SECRET
        redirectUris: ["https://console.example.com/connections/callback"]
    github:
      kind: github
      url: https://github.com
    gitlab:
      kind: gitlab
      url: https://gitlab.example.com
      oauth:
        clientId: "gitlab-app-id"
        clientSecretEnv: PLOEG_GITLAB_OAUTH_CLIENT_SECRET
        redirectUris: ["https://console.example.com/connections/callback"]
        scopes: [api]
```

Every vendor URL comes from this file, never from a request. An instance with `oauth` needs an `https://` `url`, because the token and revoke URLs derived from it carry the client secret; `authorizeUrl` and `tokenUrl` must be https too. For an in-cluster instance reached deliberately over plain HTTP, set `allowInsecureHttp: true` on that instance to accept an `http://` `url`. An instance without `oauth` keeps accepting either scheme. The client secret is read from the environment variable `clientSecretEnv` names; seed it once per vendor. For GitHub, set `PLOEG_PUBLIC_WEBHOOK_BASE` to the HTTPS base vendors reach ploegd at: a manifest App's webhook goes to `<base>/webhooks/forge/github`, and its deliveries are verified with that App's own secret from the store.

## 2. Grant the consumer

In `PLOEG_OPERATOR_CONSUMERS`, give the consumer `"manageConnections": true` and its callbacks in `"connectionRedirectUris"` (exact HTTPS URLs, at most 10). An OAuth `redirectUri` must be in both this list and the instance's `redirectUris`.

## 3. What the consumer does

1. `POST /api/v1/operator/connection-flows` with `port`, `kind`, `instance`, `method` and `redirectUri`. The answer carries `flow.state` once and `next.authorizeUrl` (OAuth), `next.manifest` and `next.postUrl` (GitHub manifest) or `next.installUrl` (GitHub installation).
2. Send the person there. The vendor returns them to the consumer's callback with a code and the `state`; keep that page out of access logs and send it with `Referrer-Policy: no-referrer`.
3. `POST /api/v1/operator/connection-flows/complete` with `state` and `code` (or `installationId` and `setupAction` from GitHub's setup redirect, or `token`, with `group` for `gitlab_service_account`). A manifest completion answers with the new connection app and a follow-on installation flow.

### A GitLab service account

`gitlab_service_account` makes the GitLab connection that mints a project access token per Run ([ADR-0203](../adrs/0203-a-gitlab-forge-connection-is-a-group-service-account-that-mints-a-project-token-per-run.md)). The group's GitLab must offer service accounts (check the tier: GitLab gates them by plan), its top-level group must allow project access tokens, and gitlab.com allows those only on Premium or Ultimate. On gitlab.com the Owner must also be identity-verified to create a service account.

1. The person creates a personal access token with scope `api` and an expiry of one day, as an Owner of the top-level group. Nobody else ever sees it: the consumer relays it once.
2. `POST /api/v1/operator/connection-flows` with `port: forge`, `kind: gitlab`, `instance`, `method: gitlab_service_account` and an optional `label` or `id`; there is no `redirectUri` and no `next` step.
3. `POST /api/v1/operator/connection-flows/complete` with `state`, `token` and `group`, the full path of the group (a subgroup is allowed) whose projects Ploeg may work in.

Ploeg then reuses or creates the service account `ploeg-<id or label>` in the top-level group, makes it a Maintainer of `group` alone, creates its token with scopes `api` and `self_rotate` for 364 days (`connections.instances.<id>.serviceAccountTokenDays`, at most 365), keeps that token and a webhook signing token in the secret store only, probes the token as the service account, and revokes the Owner's token with `DELETE /api/v4/personal_access_tokens/self`, also when the flow fails. A failure part-way removes what the flow made: the token, the account when it created it, otherwise the membership it added. The answer carries the connection and `serviceAccount` (`username`, `group`, `topGroup`, `created`, `tokenExpiresAt`), never a token. When the Owner's token could not be revoked the connection is still made, the answer carries the warning `owner_token_not_revoked` and the audit log `connection.owner_token_not_revoked`: revoke it at GitLab. The hourly pass rotates the service account's token with itself when fewer than 30 days remain. Removing the connection revokes its token; the service account and its membership stay at GitLab and are named under `leftAtVendor`, because deleting them needs an Owner.

A release whose forges are all runtime connections needs no static forge block: with `targetRegistration.enabled` and a `port: forge` entry here, the chart runs the executor without `executor.<forge>.url` or any forge Secret, and a GitLab service account connection mints each Run's token, posts its card and takes its project's webhooks ([a release without a static forge](register-work-targets-at-runtime.md#a-release-without-a-static-forge)).

A `token` connection for GitLab takes a service account's token an administrator made by hand; it is accepted only for an instance without `oauth`, and it mints per Run when its probe finds `api` scope and Maintainer reach. Ploeg does not rotate or revoke a pasted token.

Read connections with `GET /api/v1/operator/tracker-connections` and `/forge-connections`, probe one with `POST .../{id}/probe`, reconnect by starting a flow with `connection: <id>`, and remove one with `DELETE .../{id}`. The full contract is in [the operator API schema](../contracts/operator-api.v1.schema.json).

## Hand work over by assigning the connection's own account

A tracker connection made from a dedicated tracker member can be the hand-over agent itself ([ADR-0195 section 9](../adrs/0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md)). Send `handOverAgent: true` when you start the flow, or `PATCH .../tracker-connections/{id}` with it later. Only ClickUp recognises such an assignment today; every other kind answers `422 capability_missing`.

For a ClickUp connection with `handOverAgent: true`:

- The account is the one the probe read through `GET /user`, by its immutable user id (the connection's `account.id`). A reconnect to another account is refused, so the agent never changes under a connection.
- Assigning that account to a task in a List bound ([ADR-0186](../adrs/0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md)) through this same connection admits the task to the binding's Team, through the same dedupe, routing, gates and budget as an assignment to a static agent. Removing the account withdraws it the same way.
- An assignment to anyone else on that List is ignored, unless it is another of Ploeg's own accounts ([ADR-0205](../adrs/0205-only-an-assignment-of-ploegs-own-tracker-account-hands-work-over.md)). A person assigning themselves or a colleague starts nothing.
- The account hands nothing over on a List any static route names, also when a binding for another Team routes it: static routing wins.
- Ploeg reads and writes a task in a List bound through a store-backed connection (mirror, comments, statuses, board reads) with that connection's token from the secret store, not with `PLOEG_CLICKUP_TOKEN`.
- Ploeg registers a missing List webhook with the connection's token, in the Workspace that holds the List, subscribed to `taskAssigneeUpdated` among the dispatch events. Its deliveries arrive on `/webhooks/tracker/clickup` and are verified with that webhook's own signing secret from the secret store; a delivery naming a webhook no live binding registered is refused like a bad signature. Without a static ClickUp tracker (no `PLOEG_CLICKUP_SECRET` or `PLOEG_CLICKUP_TOKEN`), ploegd still serves that route whenever `connections.allow` names a ClickUp tracker kind, and accepts only those deliveries. With a static ClickUp, static deliveries keep verifying with `PLOEG_CLICKUP_SECRET` and static routing wins.

## Check it

- `ploeg_connections{kind,state}` counts live connections; alert on `invalid` and `token_missing`.
- The audit log holds `connection.*` and `connection_app.*` events with the proven person as actor.
- An hourly pass probes every connection, refreshes expiring GitLab and Forgejo OAuth tokens, rotates a GitLab service account's token in its last 30 days and deletes store paths nothing references.
