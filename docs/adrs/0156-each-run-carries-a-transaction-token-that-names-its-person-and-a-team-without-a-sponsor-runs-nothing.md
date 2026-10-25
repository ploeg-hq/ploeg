---
status: proposed
date: 2026-10-25
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Each Run carries a transaction token that names its person, and a Team without a sponsor runs nothing

## Context and Problem Statement

A worker proves itself to ploegd in two steps today ([worker control](../contracts/worker-control.md)): a static bootstrap token per Team and Role at the claim, then an HMAC capability bound to the Run, the worker id, the Team and the Role, valid for 24 hours. The capability says which Run a request is about. It says nothing about the person the Run acts for, so no record ploegd writes about a Run's own requests names one. [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md) now proves the person at the operator API, and its re-evaluation triggers name the next step: "a Run token shaped like an OAuth Transaction Token lands and needs this token's `act` nested inside it". The capability is also a shared secret: publishing what verifies it would let anyone forge one, and it outlives the Run's lease by up to a day.

Some work reaches Ploeg with no person behind it: a tracker task assigned by a user the configuration does not map, a scheduled or follow-up Run. The owner decided on 2026-10-24 that such a Run acts for its Team's sponsor, a person, never a bot; that the owner sponsors every Team until a second person joins; that a Run with neither a person nor a sponsor is refused; and that when the identity provider issued an actor for the person (`act`), Ploeg nests it rather than replacing it.

What does ploegd sign for each Run, what does a worker present, how does the HMAC capability retire without breaking a running release, and where does a Team name its sponsor?

## Decision Drivers

* Every record Ploeg writes about a Run names the person it acts for and the agent acting for them, and says whether the person was proven or only asserted.
* A credential a verifier can check without holding Ploeg's secret: an asymmetric signature and a published key set, with rotation.
* The standard shape where one exists: OAuth Transaction Tokens (draft-ietf-oauth-transaction-tokens-11) for a token that carries a principal and immutable context through one trust domain, RFC 8693 for `act`.
* No longer than the Run: the credential expires with the Run's lease.
* One release of compatibility ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)): a worker of the previous release keeps working, and nothing breaks on a deployment that configures nothing new.
* Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **ploegd signs an ES256 transaction token at the claim and at every renewal; the worker presents it on every Run route; the HMAC capability is issued beside it for one minor and can be turned off**
* Keep the HMAC capability and add the person to its payload
* Sign a long-lived token once at the claim, valid until a fixed Run deadline
* Issue the token only from a separate token service the worker calls

## Decision Outcome

Chosen option: "**ploegd signs an ES256 transaction token at the claim and at every renewal**", because it gives every Run request a verifiable person and agent, uses the draft's shape and RFC 8693's `act`, lives no longer than the lease, and retires the HMAC capability on the schedule ADR-0134 sets.

### The token

A compact JWS, `alg: ES256`, `typ: txntoken+jwt` (the draft's required type), `kid` = the key's RFC 7638 thumbprint. Claims:

| Claim | Value |
| --- | --- |
| `iss` | `PLOEG_TRANSACTION_TOKEN_ISSUER`, the URL ploegd is reached at |
| `aud` | Ploeg's trust domain, `PLOEG_TRANSACTION_TOKEN_AUDIENCE` (an http(s) URL or a `urn:`); empty is the issuer |
| `sub` | the person the Run acts for, as Ploeg's person id |
| `act` | `{sub: "<team>/<role>", run: "<run id>"}`, and inside it `{sub: <the identity provider's act.sub>}` when the person token that handed the work over carried one |
| `txn` | the Run's id |
| `scope` | `ploeg.run.write` or `ploeg.run.read` |
| `req_wl` | the worker id that claimed the Run, or `client:<person>` for a client-carried Run ([ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md)) |
| `tctx` | `workItem`, `shift`, `team`, `role`, `repository` (`<forge>/<owner>/<repo>`), `branch`, `allowance` (the Run's authorized USD), `isolation` (absent until ploegd records the class of [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md)), `subjectProof` |
| `iat`, `nbf`, `jti` | issue time, the same, 16 random bytes |
| `exp` | the Run's lease deadline |

Every value comes from ploegd's own records, never from the request. The Run token, the opaque identifier in the Run routes' path, is never a claim: it is itself a credential.

### Who `sub` is

1. The person the Work Item acts for (`work_items.acts_for`) when there is one. Migration `0076` records how Ploeg knows them: `proven` when a person token named them at an operator admission (ADR-0150), `asserted` when an Operator Consumer's header, `X-Ploeg-Acting-User`, or the tracker's `people` mapping did. Rows older than the migration read as `asserted`. The identity provider's `act.sub` is kept beside a proven person and nested in the token.
2. Otherwise the Team's sponsor, `teams.<name>.sponsor` in the config file, with `subjectProof: sponsor`.
3. Otherwise no token. ploegd never signs one without a person.

### A Team names its sponsor

`teams.<name>.sponsor` is a person id in the same form as the tracker `people` mapping, validated when ploegd starts. It amends [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md): the manifest's crew gains the person who answers for it. While ploegd signs transaction tokens, a Team without a sponsor runs nothing, which is the owner's rule and makes step 3 above unreachable in practice: a worker's claim for it answers 403 with the reason and the key to set, an operator admission into it answers 409 `team_without_sponsor`, and a client-carried claim the same. ploegd logs each such Team at start, and the chart refuses to render a deployment whose `transactionToken.signingKeySecret` is set while an `executor.teams[]` Team has no `config.teams.<name>.sponsor`. Without a signing key nothing changes, so a deployment that upgrades without configuring one keeps running.

### Issue, refresh and verify

* **Claim.** After a managed claim succeeds, ploegd signs the token for the claiming worker and answers it as `transactionToken`, beside `controlToken` while the HMAC capability is still issued. A claim whose token cannot be signed answers 503.
* **Renew.** Every successful renewal answers a fresh `transactionToken` that expires at the renewed deadline. The worker stores it and presents it from then on. The worker renews at a quarter of the lease, so a token is replaced long before it expires.
* **Verify.** A Run route accepts a three-segment bearer as a transaction token: ES256 against ploegd's current or retired keys, `typ`, `iss`, `aud`, `exp` (five seconds of leeway), then `txn` equal to the id of the Run the path names, `req_wl` equal to `X-Ploeg-Worker-ID`, and `tctx.team` and `tctx.role` equal to the Run's. The Run must still be running and before its deadline, except for the existing outcome replay of a finished Run, which now has until the token's `exp`.
* **Publication.** `GET /.well-known/jwks.json` serves the current key and the retired ones (`PLOEG_TRANSACTION_TOKEN_RETIRED_KEYS`), unauthenticated, `application/jwk-set+json`, cacheable for five minutes, so another service in the trust domain can verify a Run's attribution on its own. It answers 404 when ploegd signs none.
* **Its own key.** `PLOEG_TRANSACTION_TOKEN_SIGNING_KEY`, a PEM P-256 key in its own Secret (chart `transactionToken.signingKeySecret`), not the Run identity's key of [ADR-0149](0149-a-declared-plugin-may-receive-a-signed-run-identity-that-names-its-run-and-authorizes-nothing.md): that key signs tokens for any audience a plugin declares, and keeping the keys apart means a plugin's token can never be replayed as a worker credential however a verifier is configured. The chart refuses a plugin env that reads either Secret. ES256 for the reason ADR-0149 gives; the key parsing and key set code is now shared in `pkg/signingkeys`.

### The record a Run writes

Every audit row written while serving a Run route under a verified transaction token carries `detail.runActor`: `{person, proof, agent, act, run}`, taken from the token. That covers every row a worker's requests write, such as `checkpoint.written` and the outcome's row, which the end-to-end test checks. Rows written under the deprecated HMAC capability carry none.

### The HMAC capability retires

`PLOEG_WORKER_HMAC_CAPABILITY` (chart `executor.workerAuth.hmacCapability`) is `issue` by default: ploegd still signs the HMAC capability at the claim and accepts it on Run routes, so a worker of the previous release, which knows only `controlToken`, keeps working. It is deprecated from this release. `off` issues and accepts the transaction token only; ploegd refuses it without a transaction token key, in legacy worker mode, or with `PLOEG_WORKER_SIGNING_KEY` still set. Under ADR-0134 the default flips to `off`, and the HMAC capability and `PLOEG_WORKER_SIGNING_KEY` are removed, no earlier than the next minor; that removal is its own `feat!` with an entry in `docs/contracts/breaking-changes.yaml`.

### What this is not

The draft says a transaction token must not be used to authenticate a workload (§13.12). Here it authenticates nothing alone: it is accepted only with the worker id it names, the same binding the HMAC capability had, and the workload is proven at the claim by its bootstrap token. Replacing the bootstrap token with the pod's projected service account token is the next step ([ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md) names it the "second step"). The token is not an access token for anything outside Ploeg; a broker that later mints downstream credentials reads it to decide what to mint.

### Consequences

* Good, because every Run request and the audit rows it writes name a person and the agent acting for them, and say whether the person was proven, asserted or the Team's sponsor.
* Good, because a verifier needs only the published key set; nobody outside ploegd holds anything that forges a worker credential, which the HMAC key could not offer.
* Good, because a stolen token lives at most one lease, against 24 hours for the capability.
* Good, because the identity provider's actor survives into every Run as a nested `act`, as RFC 8693 chains actors.
* Bad, because ploegd signs once per renewal per Run: one ECDSA signature every few minutes per running Run, negligible next to the database writes of a renewal.
* Bad, because the outcome replay of a finished Run now has until the token's `exp` instead of 24 hours. A worker replays immediately after a lost answer, well inside a lease; a replay after that fails and the sweeper settles the Run as before.
* Bad, because until the default flips a deployment carries two worker credentials and two keys. The boot log says so each start.
* Bad, because a Team without a sponsor stops when a deployment turns transaction tokens on. The chart and the boot log say which Team and which key to set.
* Neutral: `tctx.isolation` stays absent until ploegd records the class a Run executes at.

### Confirmation

* `go test ./pkg/txntoken/` proves the claims (`typ`, `alg`, `kid`, `iss`, `aud`, `sub`, `txn`, `scope`, `req_wl`, `act`, `tctx`), verification against the published key set, `exp` at the lease deadline and refusal after it, the nested identity provider actor, refusal to sign without a person, an unmarked proof or a worker, refusal of another key, trust domain or issuer, a tampered subject, a missing `typ` or `act`, an unpinned verifier and the HMAC capability's shape, and that a retired key is published and still verifies.
* `go test ./pkg/httpapi/ -run 'TestAClaimedRun|TestAHandedOver|TestAnAsserted|TestATeamWithout|TestTheTransactionTokenBinds|TestRetiringThe|TestTheKeySet|TestAWorkerUses'` proves, against a real store: the sponsor as `sub` with `subjectProof: sponsor`; a proven person with the nested actor; an asserted person; that a Team without a sponsor claims nothing and gets a readable 403, and claims as before without a key; refusal of another worker, another key, another Run and a lapsed Run; a renewal's fresh token expiring at the renewed deadline; that `off` issues no HMAC capability and refuses one while the default still accepts it; the 404 key set without a key; and `ploeg-worker`'s own client claiming, renewing, checkpointing and reporting with the token, switching to the renewed one, with every audit row of its requests naming the person and agent.
* `go test ./pkg/store/ -run TestAnAdmissionRecords` proves the proof and actor an admission records; `go test ./pkg/config/ -run TestATeamNamesItsSponsor` the sponsor key; `go test ./cmd/ploegd/ -run 'TransactionToken|HMAC'` the boot refusals.
* `scripts/helm-golden.sh check` pins the rendered `transactionToken` env and `hmacCapability: "off"`, and refuses `off` without a key, a sponsorless Team with a key, and a plugin env reading the key's Secret.
* Reviewers check that no new Ploeg text names a consumer.

## Pros and Cons of the Options

### Keep the HMAC capability and add the person to its payload

* Good, because it is one field more in a token every worker already handles.
* Bad, because nothing outside ploegd can verify it without the key that forges it, and it keeps the 24-hour lifetime.

### Sign a long-lived token once at the claim

* Good, because the worker never replaces its credential.
* Bad, because a Run has no fixed end: its lease is renewed until it finishes, so a token valid "until the deadline" either expires under a live Run or outlives a dead one.

### A separate token service the worker calls

* Good, because it is the draft's architecture: a Transaction Token Service.
* Bad, because ploegd already is the only party that knows the Run's facts and already answers the claim and every renewal; a separate call adds a round trip and a failure mode for nothing. A service can be split out later behind the same key set.

## Re-evaluation triggers

* Every worker in use presents the transaction token: flip the default to `off`, then remove the HMAC capability a minor later.
* Workers prove themselves with projected service account tokens: drop the bootstrap token and reconsider `req_wl`.
* A broker mints downstream credentials from the token, or a service outside ploegd verifies it: revisit the claims it needs and whether `aud` stays one trust domain.
* The identity provider issues chained `act` (a delegation chain deeper than one): nest it whole instead of its `sub`.
* The transaction tokens draft changes a required claim or the `typ`, or becomes an RFC.
* ploegd records the isolation class a Run executes at: fill `tctx.isolation`.

## More Information

* Amends [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) (a Team names its sponsor) and the worker credential of [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md) ("a bootstrap token exchanged for an HMAC capability" becomes "for a transaction token"); answers ADR-0150's trigger about a Run token nesting its `act`. Related: [ADR-0025](0025-management-authority-stays-in-the-control-plane.md), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md), [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md), [ADR-0143](0143-a-persons-machine-is-a-personal-cell-and-a-run-gets-a-capability-only-when-its-person-grants-it.md) (a personal cell's Runs act for its owner, which this token now states), [ADR-0149](0149-a-declared-plugin-may-receive-a-signed-run-identity-that-names-its-run-and-authorizes-nothing.md).
* Contract: [run-api.v1.schema.json](../contracts/run-api.v1.schema.json) (`claimResponse.transactionToken`, `renewResponse.transactionToken`, `transactionTokenClaims`, `transactionTokenActor`, `transactionTokenKeySet`), [operator-api.v1.schema.json](../contracts/operator-api.v1.schema.json) (the client claim's `credential.transactionToken`) and [worker control](../contracts/worker-control.md).
* Threat model: [AS2, AS8, AS16, EP4 and F-07](../security/threat-model.md).
* Standards: draft-ietf-oauth-transaction-tokens-11, RFC 8693 §4.1 (`act`), RFC 7515, RFC 7517, RFC 7519, RFC 7638, RFC 8725.
