---
status: proposed
date: 2026-10-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A call without a live person is attributed to its Run or a proven workload, and an administrator overrides as themselves

## Context and Problem Statement

Under `PLOEG_OPERATOR_ATTRIBUTION=proven` ([ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)), the chart default from 0.3.0, every operator request other than `GET` or `HEAD` needs a verified person token ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)). Four kinds of call that Operator Consumers make today have no live person behind them:

* **(a) Lease renewal.** The `heartbeat` command on `POST /api/v1/operator/executions/{execution}/commands` keeps a running session's lease alive after the person who started it has signed out. Nobody can mint a five-minute person token for a person who is not there.
* **(b) Keyed pull request comments.** The consumer writes `PUT` and `DELETE /api/v1/operator/work-items/{id}/pull-request-comments/{key}` as its own actor, not for a person.
* **(c) Delivery verification.** A consumer with `verify` sends `POST /api/v1/operator/executions/{execution}/delivery/verification`, and the publication status beside it, on its own schedule.
* **(d) An administrator acting on another person's session.** The administrator's proven token names the administrator. Naming the owner in `X-Ploeg-Actor` answers 403 `actor_conflict`, and without it the owner lookup finds nothing.

ADR-0171 names two re-evaluation triggers for this: "decide how a machine caller is attributed" and "a deployment needs a change route that asserted attribution may reach under `proven`". Until this is decided, a deployment that needs any of (a) to (d) has to stay on `asserted`, which keeps [threat model](../security/threat-model.md) F-07 open on every route. How is a call without a live person attributed, proven and recorded, without reopening F-07?

This record is a proposal. The owner has not chosen between the options below; the proposed option is the research recommendation.

## Decision Drivers

* Every operator change is attributed to a principal that something other than the consumer's word proves: a person's identity provider, Ploeg's own Run token, or the platform that runs the workload.
* A record never names a person as the one who acted when that person did not act. A person shown on a machine's call is context, never the subject.
* No credential outlives the call it serves, and none lets one principal become another (no impersonation).
* A route list, not a blanket exemption: a route accepts a principal other than a person only when this decision names it, so a route added later stays under the person rule ([ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md) clause 2).
* Standard shapes: RFC 8693 for `act`, the OAuth Transaction Tokens draft for a Run's principal, Kubernetes bound service account tokens and RFC 7523 client assertions for workloads, RFC 9470 for step-up.
* It works with the identity provider in use today (authentik 2026.8.5) and with any OpenID Connect provider; nothing needs a feature that provider lacks.
* One release of compatibility ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)): an `asserted` deployment that configures nothing new behaves as before.
* Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **Attribution kinds `person`, `run` and `workload` (with `grant` later); the administrator overrides a session as themselves**
* The session owner's offline refresh token, kept by the consumer
* Exempt the four kinds of route from `proven`
* Delegation at the identity provider: `may_act`, or owned actors that exchange for the person
* A signed, bounded grant at hand-over (kept as a later `grant` kind)
* An asynchronous owner approval, CIBA-like (kept as a later option)
* Keep such deployments on `asserted`

## Decision Outcome

Proposed option: "**attribution kinds `person`, `run` and `workload`, and the administrator overrides as themselves**", because the party that can prove each kind proves it (the identity provider for a person, ploegd for a Run, the platform for a workload), no kind lets a caller become a person who is not there, and each route accepts only the kinds it lists.

### The kinds

Every attributed operator change records `detail.attribution.kind`:

| Kind | Who acted | Proven by | Carried as |
| --- | --- | --- | --- |
| `person` | a person, live | the identity provider (ADR-0150) | an exchanged person token |
| `run` | a Run, for the person proven or named at its admission | ploegd's own signature (ADR-0156) | the Run's transaction token, bound to `req_wl` |
| `workload` | an Operator Consumer's workload, as itself | the platform that runs it, or the identity provider through a federated client | a projected service account token, or a `client_credentials` token federated from one |
| `grant` (later) | a Run or workload inside a bound the person signed at hand-over | ploegd's signature over the grant | a grant id beside a `run` or `workload` credential |

Rows written before this decision carry no `kind` and read as `person`. `proof` keeps its ADR-0150 meaning for `person` (`proven` or `asserted`), takes the transaction token's `subjectProof` for `run`, and is `workload` for `workload`.

### (a) Lease renewal is a `run` call

* Renewing a running Run's lease moves to the Run route `POST /api/v1/runs/{token}/renew` under the Run's transaction token, where client-carried Runs already renew ([ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md), ADR-0156). For an operator execution, ploegd answers a transaction token with the execution's start, `req_wl` = `consumer:<name>`, as a client-carried claim answers one with `client:<person>`. The executor's own lifecycle commands (`start`, `heartbeat`, `report`, `close`), which only the admitting consumer may send today, move to the Run routes with it.
* The call is recorded as the person proven at admission, the token's `sub` and `subjectProof`, and authenticated by the token's signature and its `req_wl` binding to `X-Ploeg-Worker-ID`. No person token is asked for, so a person who signed out does not stop their Run.
* The operator route keeps accepting `heartbeat` under `asserted` for one deprecated minor, and refuses it under `proven` as it does today (`person_token_required`).

### (b) and (c) are `workload` calls

A consumer may prove its own workload, apart from any person:

* **A projected Kubernetes service account token.** `aud` is Ploeg's workload audience (default `ploeg`) and the lifetime is at most one hour (`expirationSeconds` ≤ 3600; the kubelet rotates it). ploegd verifies it by a TokenReview with `spec.audiences: [<audience>]`, which also fails once the pod the token is bound to is gone, or, without access to the cluster's API, against the issuer's JWKS from service account issuer discovery. The subject is `system:serviceaccount:<namespace>:<name>`.
* **Or an identity provider `client_credentials` token federated from that service account token.** The workload presents its service account JWT as an RFC 7523 §2.2 client assertion (`client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer`) and receives an access token from the person token issuer. ploegd verifies it with the ADR-0150 verifier and classifies it as a workload because its `azp` (or `client_id`) is in a consumer's `workload.clientIds`. ploegd never derives a person from its `sub`.
* **Machine scopes.** Each consumer names `machineScopes`. Only the non-`GET` routes those scopes map to accept a workload principal; every other change still needs a person. The vocabulary is closed, and an unknown scope refuses to start:

  | Scope | Routes | Also needs |
  | --- | --- | --- |
  | `comment.keyed` | `PUT`, `DELETE /api/v1/operator/work-items/{id}/pull-request-comments/{key}` | the Work Item's Team in scope |
  | `verify` | `POST /api/v1/operator/executions/{execution}/delivery/verification` | `verify: true` |
  | `publication.status` | `POST /api/v1/operator/executions/{execution}/delivery/publication/{operation}/status` | `execute` or `verify` |
  | `commit-status` | a keyed commit status route, once one exists; until then the scope refuses to start | the Work Item's Team in scope |
  | `person.disable` | `POST /api/v1/operator/people/{person}/disable` and `/enable` (below) | `teams: ["*"]` |

  A machine scope never widens a consumer: the route still checks Teams, `execute`, `verify` and `readContent` as it does for a person.
* **No person on a workload call.** A workload request that names anyone in `X-Ploeg-Actor` or `X-Ploeg-Acting-User` answers 403 `actor_conflict`. A route finds its record by id inside the consumer's Teams and reads the person from the Work Item, never from the request.
* **The record.** `principal: consumer:<name>`, `proof: workload`, with the Work Item's person as `context.person`, never as `sub` or `actor`.

### (d) An administrator overrides as themselves

* A Team may grant `override` on its sessions (`teams.<name>.override`): the claims a person token must carry to hold it (`requireClaims`, matched as ADR-0150 matches them) and the step-up it needs (`acrValues`, `amr`, `maxAge`).
* The administrator sends their own proven person token and names the session's owner in `X-Ploeg-Override-For: <person id>`. `X-Ploeg-Actor` may still name only the proven person, so ADR-0150's rule is unchanged. The owner lookup uses the named owner instead of answering `actor_conflict`.
* Step-up: the token's `acr` is one of `acrValues`, its `amr` contains every `amr` value, and its `auth_time` is within `maxAge`, the checks [ADR-0153](0153-a-person-confirms-a-ploeg-mcp-action-by-signing-in-afresh-at-a-ploeg-mcp-page.md) already makes. A token without them answers 401 with an RFC 9470 `insufficient_user_authentication` challenge carrying `acr_values` and `max_age`, so the consumer can send the administrator to sign in again.
* `override` covers stopping and steering: the person commands on a session (`message`, `pause`, `resume`, `cancel`, `take-control`, `handback`), and cancel, requeue, notes and take-over on its Work Item. It never answers an approval, a question or a confirmation in the owner's name. Those stay the owner's, so nobody gives a required approval by override.
* The record: `sub` is the administrator, `target` the owner, and `override` holds `{acr, amr, authTime}`. The operator event feed tells the owner (`operator.override`).

### Disabling a person

* `POST /api/v1/operator/people/{person}/disable`, sent by a workload principal with machine scope `person.disable` (typically the installation's identity lifecycle job), records the person as disabled. ploegd then cancels the person's running Runs (`cancel_requested`, as a cancel does), revokes their capability grants ([ADR-0143](0143-a-persons-machine-is-a-personal-cell-and-a-run-gets-a-capability-only-when-its-person-grants-it.md)) and any later `grant`, refuses new admissions in their name (409 `person_disabled`), and stops signing transaction tokens with them as `sub`, so a Run that missed the cancel stops at its next renewal. A Team whose sponsor is disabled runs nothing until it names another, as a Team without a sponsor does (ADR-0156).
* `/enable` lets new work in again and restarts nothing.
* A person signing out stops nothing. Being offboarded does.

### Later options

* **`grant`.** At hand-over, the person signs a bounded grant shaped like RFC 9396 `authorization_details` (`type`, `actions`, `locations` as repository and branch, `datatypes`, plus a budget and an expiry). Ploeg stores and signs it, and a `run` or `workload` call inside the bound records `kind: grant` with the grant id. It serves needs that Team scope and machine scopes cannot express.
* **Asynchronous owner approval.** When a call needs the owner and the owner is away, ploegd asks the owner out of band, CIBA-like, and the owner's sign-in yields a single-use signed approval record that the call presents once. It waits until an identity provider in use ships CIBA or ploeg-mcp confirmations become signed records.

### Audit shape

```json
{"kind": "person",   "consumer": "c", "actor": "oidc-1f…", "proof": "proven", "act": "c-client"}
{"kind": "person",   "consumer": "c", "actor": "oidc-9a…", "proof": "proven", "target": "oidc-1f…",
 "override": {"acr": "urn:example:acr:mfa", "amr": ["pwd", "hwk"], "authTime": "2026-10-30T09:12:03Z"}}
{"kind": "run",      "consumer": "c", "actor": "oidc-1f…", "proof": "proven", "run": 812, "agent": "team/role", "reqWl": "consumer:c"}
{"kind": "workload", "consumer": "c", "principal": "consumer:c", "proof": "workload", "scope": "comment.keyed",
 "workload": {"via": "kubernetes", "sub": "system:serviceaccount:ns:sa"}, "context": {"person": "oidc-1f…", "personProof": "proven"}}
```

A refusal stays `operator.attribution_refused` (ADR-0171), with `reason` one of `person_token_required`, `workload_scope_required` (a workload outside its machine scopes), `step_up_required` or `override_forbidden`.

### Configuration

* ploegd: `PLOEG_OPERATOR_WORKLOAD_VERIFIER` (`tokenreview` or `jwks`; unset accepts no service account token), `PLOEG_OPERATOR_WORKLOAD_ISSUER`, `PLOEG_OPERATOR_WORKLOAD_AUDIENCE` (default `ploeg`), `PLOEG_OPERATOR_WORKLOAD_JWKS_URL` (default from discovery) and `PLOEG_OPERATOR_WORKLOAD_MAX_LIFETIME` (default and maximum `1h`).
* Each `PLOEG_OPERATOR_CONSUMERS` entry may name `workload: {serviceAccounts: ["<namespace>/<name>"], clientIds: [...]}` and `machineScopes: [...]`.
* Each Team may name `override: {requireClaims, acrValues, amr, maxAge}`.
* Start refusals: `machineScopes` without `workload`; a `workload.clientIds` value that is also a consumer's `clientIds` or `audiences`; a service account named by two consumers; an unknown scope; `serviceAccounts` without a verifier; `override` without `requireClaims` or without any step-up value; `person.disable` on a consumer without `teams: ["*"]`.
* The chart forwards these and, for `tokenreview`, binds ploegd's service account to `system:auth-delegator` only when a verifier is set.

### Support matrix

| Call | `asserted` (today) | `proven` (today) | `proven`, this decision |
| --- | --- | --- | --- |
| A live person's change | person token `proven`, or header `asserted` | person token only | unchanged |
| (a) Lease renewal after sign-out | operator `heartbeat`, `asserted` | refused | `run` on `/runs/{token}/renew` |
| (b) Keyed pull request comment | header, `asserted` | refused | `workload` with `comment.keyed` |
| (c) Delivery verification | header, `asserted` | refused | `workload` with `verify` |
| (d) Administrator on another's session | `actor_conflict`, or the owner asserted in a header | `actor_conflict` | `person` with `override` and step-up |
| A person is offboarded | no contract | no contract | `workload` with `person.disable` |
| Any other change without a person | header, `asserted` | refused | refused |
| Reads | unchanged | unchanged | unchanged |

| Workload proof | Needs |
| --- | --- |
| Service account token, TokenReview | Kubernetes with bound service account tokens; ploegd's service account may create TokenReviews |
| Service account token, JWKS | service account issuer discovery reachable from ploegd; no check that the pod still exists |
| `client_credentials` federated from the service account token | an identity provider that accepts a federated JWT as an RFC 7523 client assertion; authentik documents projected service account tokens for this flow |

### Migration

* An `asserted` deployment that configures nothing new is unchanged, including the operator `heartbeat`, with ADR-0171's `Warning` header on each header-only change.
* A `proven` deployment that configures nothing new is unchanged: (b), (c) and the operator `heartbeat` stay refused, and (d) stays `actor_conflict`.
* A consumer moves one call at a time: renewals to the Run route, then workload credentials and `machineScopes`, then the deployment sets `proven`. Every addition is a new field, header, code or route, so `go test ./internal/compat/` finds no break. [breaking-changes.yaml](../contracts/breaking-changes.yaml) gains an entry only when the operator `heartbeat` is removed, a minor after its deprecation.

### Consequences

* Good, because a deployment that needs machine calls can set `proven` and close F-07 on every route outside a machine scope.
* Good, because no record says a person acted when a machine did: a workload call names the consumer, and the person appears only as context.
* Good, because a Run outlives its person's sign-in but not their offboarding, and offboarding has one contract.
* Good, because an administrator's intervention names both the administrator and the owner, and needs a fresh, strong sign-in.
* Bad, because consumers carry a second credential, the workload token, beside person tokens, and the chart gains a TokenReview binding.
* Bad, because a stolen workload token can act inside its machine scopes for up to an hour; the scopes, TokenReview's pod binding and Team scope bound it.
* Bad, because the JWKS verifier cannot tell that a token's pod is gone; TokenReview is the default wherever ploegd can reach the API.
* Bad, because `override` adds a path around ownership. It is per Team, claim-gated, stepped up and visible to the owner.
* Neutral: operator executions are being retired ([ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)). Once none is open, (a) concerns only client-carried and worker Runs, which already renew on the Run route.

### Confirmation

The record stays proposed until these tests exist and pass; each is named so the implementing change can be checked against it.

* `go test ./pkg/httpapi/ -run 'TestARunRenewsItsLeaseAfterItsPersonSignedOut|TestARunTokenForAnotherWorkerCannotRenew|TestAnExecutionStartAnswersATransactionTokenForItsConsumer|TestTheOperatorHeartbeatIsRefusedUnderProven'`
* `go test ./pkg/httpapi/ -run 'TestAWorkloadTokenWritesAKeyedCommentUnderProven|TestAWorkloadVerifiesADeliveryUnderProven|TestAWorkloadOutsideItsMachineScopesIsRefused|TestAWorkloadNamingAPersonIsRefused|TestAWorkloadTokenLongerThanAnHourIsRefused|TestTokenReviewRefusesATokenWhosePodIsGone|TestAClientCredentialsTokenIsAWorkloadNeverAPerson'`, with TokenReview, the JWKS and the token endpoint faked by `httptest`.
* `go test ./pkg/httpapi/ -run 'TestAnAdministratorOverridesASessionAsThemselves|TestAnOverrideWithoutStepUpAnswersInsufficientUserAuthentication|TestAnOverrideCannotApproveInAnotherPersonsName|TestAnOverrideOutsideTheTeamIsActorConflict'`
* `go test ./pkg/httpapi/ -run 'TestDisablingAPersonCancelsTheirRunsAndRefusesTheirRenewals|TestSigningOutDoesNotStopARun'`
* `TestEveryAttributionRecordsItsKind` checks the stored rows of each kind and each refusal against PostgreSQL.
* `go test ./pkg/config/ -run TestMachineScopesAndOverrideConfiguration` and `go test ./cmd/ploegd/ -run 'Workload|Override'` check the parsing and every start refusal above.
* `scripts/helm-golden.sh check` renders the workload values, and the `system:auth-delegator` binding only when a verifier is set.
* `go test ./internal/boundary/` keeps consumer names out of the text, and `go test ./internal/ledger/` checks this record's shape.

## Pros and Cons of the Options

### The session owner's offline refresh token

* Good, because every call is still a person token and ploegd changes nothing.
* Bad, because the consumer can then impersonate the person whenever it likes, and the record says the person acted when nobody did.
* Bad, because it keeps a long-lived credential per person at the consumer, and an offline token survives the person's logout, the opposite of the revocation contract.

### Exempt the four kinds of route from `proven`

* Good, because it is one list in the middleware.
* Bad, because the exempt routes accept a consumer's static token and an asserted header again, which reopens F-07 on them, and every later exemption widens it.

### Delegation at the identity provider: `may_act`, or owned actors

* Good, because RFC 8693 §4.4 `may_act` is the standard statement that one party may act for another, and the identity provider would own the policy.
* Bad, because the identity provider in use neither issues nor evaluates `may_act`, and its owned actors (authentik 2026.8.5, `post_init_token_exchange_actor` in `authentik/providers/oauth2/token/token_exchange.py`) still need the person's own `subject_token` at each exchange, so they do not help while the person is away. It would also tie the design to one provider's model.

### A signed, bounded grant at hand-over

* Good, because it carries the person's intent, bounded, to a later call, in the RFC 9396 shape.
* Bad, because the four cases do not need it: (a) is the Run itself, and (b) and (c) are the consumer's own acts. It is kept as the `grant` kind for the first need that Team and machine scopes cannot express.

### An asynchronous owner approval

* Good, because the owner decides even when away from the consumer.
* Bad, because it blocks the call on the owner's answer, which (a) to (c) cannot wait for, and it needs CIBA, which the identity provider in use does not ship. It is kept as a later option.

### Keep such deployments on `asserted`

* Good, because nothing changes.
* Bad, because one machine call keeps F-07 open on every route of the deployment.

## Re-evaluation triggers

* A consumer needs a non-`GET` route without a person that is outside the machine scope vocabulary.
* The identity provider in use issues or evaluates `may_act`, or lets an actor exchange for a person without that person's `subject_token`.
* The identity provider in use ships CIBA, or ploeg-mcp confirmations become signed records: decide the asynchronous owner approval.
* A hand-over needs a bound that Team scope and machine scopes cannot express: decide the `grant` kind.
* The identity provider in use sends OpenID Shared Signals (CAEP or RISC) account or session events: decide whether they disable a person instead of a consumer call.
* A consumer must prove a workload that runs outside Kubernetes and cannot obtain `client_credentials` from the identity provider.
* One Team sees more than ten overrides in a month, or an owner reports an override they did not expect.
* The transaction tokens draft changes `sub`, `act` or `req_wl`, or becomes an RFC.
* No operator execution is open: drop the operator `heartbeat` and the execution's transaction token.

## More Information

* Answers [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)'s triggers "decide how a machine caller is attributed" and "a change route that asserted attribution may reach under `proven`", and amends its clause 2: a non-`GET` request needs a verified person token, or a workload principal on a route in its consumer's machine scopes. Amends [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md) (`kind` beside `proof`) and uses [ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md)'s transaction token for `run`. Related: [ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md), [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md) (projected service account tokens as a workload's proof), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) (a Team grants `override`), [ADR-0143](0143-a-persons-machine-is-a-personal-cell-and-a-run-gets-a-capability-only-when-its-person-grants-it.md), [ADR-0153](0153-a-person-confirms-a-ploeg-mcp-action-by-signing-in-afresh-at-a-ploeg-mcp-page.md), [ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md).
* Threat model: [F-07, T-PD-3, T-PD-5 and TB5](../security/threat-model.md).
* Code it builds on: `operatorAuth`, `operatorCaller` and `personTokenOwner` in [operator.go](../../pkg/httpapi/operator.go); `operatorAttribution` and `refuseUnprovenChange` in [attribution.go](../../pkg/httpapi/attribution.go); [txntoken.go](../../pkg/txntoken/txntoken.go); `handleExecutionCommand` in [operator_execution.go](../../pkg/httpapi/operator_execution.go).
* Standards and precedent:
  * [RFC 8693](https://www.rfc-editor.org/rfc/rfc8693) §4.1 (`act`) and §4.4 (`may_act`).
  * [OAuth Transaction Tokens, draft-ietf-oauth-transaction-tokens-11](https://datatracker.ietf.org/doc/draft-ietf-oauth-transaction-tokens/11/).
  * [RFC 9396](https://www.rfc-editor.org/rfc/rfc9396), Rich Authorization Requests (`authorization_details`).
  * [OpenID AuthZEN Authorization API 1.0](https://openid.net/specs/authorization-api-1_0.html): subject, action, resource and context, the shape a workload call takes here, with the consumer as the subject and the person as context.
  * [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523) §2.2 (JWT client assertion), [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749) §4.4 (client credentials), [RFC 9470](https://www.rfc-editor.org/rfc/rfc9470) (step-up challenge), [RFC 8176](https://www.rfc-editor.org/rfc/rfc8176) (`amr` values).
  * Kubernetes [service account tokens](https://kubernetes.io/docs/reference/access-authn-authz/authentication/), [token projection and issuer discovery](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/) and [TokenReview](https://kubernetes.io/docs/reference/kubernetes-api/authentication-resources/token-review-v1/).
  * authentik [client credentials](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/client_credentials/) with a federated JWT, including projected Kubernetes service account tokens.
  * [OpenID CIBA Core 1.0](https://openid.net/specs/openid-client-initiated-backchannel-authentication-core-1_0.html) and the [Shared Signals Framework](https://openid.net/specs/openid-sharedsignals-framework-1_0.html).
  * GitHub Apps' [installation access token and user access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/about-authentication-with-a-github-app): an app authenticates as an installation "when you want to attribute app activity to the app", and on behalf of a user when the activity is the user's. The `workload` and `person` kinds draw the same line.
