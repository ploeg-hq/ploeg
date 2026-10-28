---
status: proposed
date: 2026-10-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# The Forgejo broker runs only on Forgejo 15 and refuses a token the forge did not restrict to the Run's repository

## Context and Problem Statement

[ADR-0013](0013-push-rights-are-minted-per-run.md) mints each writing Run a Forgejo token with `write:repository` and asks the forge to restrict it to the Run's repository. Forgejo added that restriction in 15.0.0: `CreateAccessTokenOption` gained `repositories` and the created `AccessToken` reports them back ([`modules/structs/user_app.go`](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.10/modules/structs/user_app.go) lines 19 to 21 and 34 to 35). Forgejo 14's handler has no such field ([`routers/api/v1/user/app.go` at v14.0.0](https://codeberg.org/forgejo/forgejo/src/tag/v14.0.0/routers/api/v1/user/app.go)), so it ignores the request's `repositories` and creates a token for every repository the bot can reach, with no error. The broker read only `id`, `name` and `sha1` from the answer, so on an older forge it handed every writing Run the bot's whole reach. This is finding F-10 ([#298](https://github.com/ploeg-hq/ploeg/issues/298)). How does the broker know the token it hands out is the one it asked for, and what does ploegd do on a forge that cannot give it?

## Decision Drivers

* The check must hold for every token, not only for the forge ploegd met at start: a forge can be downgraded or replaced behind the same URL.
* A token wider than its Run must never reach a worker, even for the seconds before a sweep.
* An installer on an old forge must learn why at rollout, with the version named, not from Runs failing one by one.
* No new configuration: the check must not need a second repository named per installation.
* The shared-token fallback stays an explicit choice (`botPasswordSecret` unset), never something ploegd picks silently when per-Run tokens were asked for.

## Considered Options

* **Read the restriction from the mint answer, revoke and refuse a token that does not name exactly the Run's repository, and refuse the broker at start below Forgejo 15**
* Probe a second repository with each new token and refuse it when the probe succeeds
* Check the version at start only
* Fall back to the shared token on a forge older than 15

## Decision Outcome

Chosen option: "**Read the restriction from the mint answer, revoke and refuse a token that does not name exactly the Run's repository, and refuse the broker at start below Forgejo 15**", because Forgejo 15 returns the restriction it stored in the same answer that carries the token ([`routers/api/v1/user/app.go`](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.10/routers/api/v1/user/app.go) lines 178 to 214 and 237 to 244), so the broker can check every token at no extra request, and an absent field is exactly what an older forge sends.

1. `Forgejo.Mint` decodes the answer's `repositories`. Unless it lists exactly one repository whose owner and name equal the request's (ignoring case, as Forgejo does), Mint deletes the token by its name and returns `ErrUnrestrictedToken`, naming the token and what it reaches but never the secret. A failed delete is joined to the error; the Run is released, so its Lease is gone and the forge sweep deletes the token by its `ploeg-run-` name.
2. The claim treats that error like any failed mint ([`forge_credential.go`](../../pkg/httpapi/forge_credential.go)): no token reaches the worker, and the Run ends `failed` with `infra_node`.
3. When `PLOEG_FORGEJO_BOT_PASSWORD` is set, ploegd reads `GET /api/v1/version` at start. A version whose major is below 15, or one that does not read as a Forgejo version (Gitea answers `1.x`), stops ploegd with an error naming the URL and the version found. A version it cannot read at all (the forge unreachable or erroring) stops it too. On success ploegd logs `per-run forge credentials enabled` with `forgejo_version`.

### Consequences

* Good, because every handed-out token was confirmed by the forge to be restricted to the Run's repository, whatever happened to the forge after ploegd started.
* Good, because an installer on Forgejo 14 or older learns at rollout, with the version in the error, instead of running with tokens wider than ADR-0013 promises.
* Good, because it costs one request per ploegd start and none per Run.
* Bad, because ploegd with per-Run tokens configured no longer starts while the forge is unreachable; Kubernetes restarts it until the forge answers. Accepted, because ploegd already needs the forge to route Work Targets at start, and starting with an unchecked broker is the state F-10 describes.
* Bad, because the check trusts the forge's report of its own restriction. A forge that reported a restriction it does not enforce would pass. The opt-in live qualification test, given a second repository, proves enforcement on a real forge; the report is what the mint can check on every Run.
* Neutral: a deployment that leaves `botPasswordSecret` unset is unchanged, and the GitHub broker ([ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md)) is not affected.

### Confirmation

* `go test ./pkg/forgebroker/ -run 'Mint|RequireRepositoryTokens'` runs against a fake Forgejo tokens API: a token answered with no `repositories` field, an empty list, another repository, another owner's repository of the same name, or the Run's repository plus one more is refused with `ErrUnrestrictedToken` and deleted by name, and the error never carries the secret; a token restricted to the Run's repository passes in any letter case and is not deleted; a refused delete is joined to the refusal and still hands out nothing. Versions `14.0.2+gitea-1.22.0`, `11.0.6+gitea-1.22.0`, `1.22.0`, `dev` and the empty string are refused, naming the version; `15.0.0`, `15.0.10` and `16.1.0` pass.
* `go test ./cmd/ploegd/ -run ForgeCredentials` requires ploegd's start to fail on a forge reporting 14.0.2, naming that version and returning no broker, and to log `forgejo_version` on 15.0.10.
* `go test ./pkg/httpapi/ -run TestClaim_ATokenTheForgeDidNotRestrictIsRevokedAndTheRunReleased` requires a claim against a forge that ignores the restriction to answer 204 with no token, leave no token alive at the forge and end the Run `failed` with `infra_node`.
* `TestQualificationARevokedForgejoRunTokenIsRefusedAtOnce` ([credential flow](../security/credential-flow.md#qualification-against-a-live-forge)) mints through the broker against a live forge, so the check runs there too, and with `PLOEG_FORGEJO_E2E_OTHER_REPO` it requires the other repository to refuse the token.

## Pros and Cons of the Options

### Probe a second repository with each new token

* Good, because it tests enforcement, not the forge's report of it.
* Bad, because it needs a second repository the bot can read, named per installation, and one more request on every claim.
* Bad, because a 403 or 404 there also comes from that repository being moved, made private or deleted, so a pass can be a false one.

### Check the version at start only

* Good, because it is one request per start.
* Bad, because a forge replaced or downgraded behind the same URL after start would mint unrestricted tokens unnoticed.

### Fall back to the shared token on a forge older than 15

* Good, because ploegd keeps running.
* Bad, because the installer asked for per-Run tokens; silently giving every writing Run the long-lived shared token is the zombie writer ADR-0013 closes.

## Re-evaluation triggers

* Forgejo changes the `AccessToken` answer so that `repositories` is no longer reported, or reports it in another shape.
* Forgejo adds token expiry to the tokens API, which changes what the broker sends and checks.
* Ploeg supports a Forgejo older than 15 or Gitea as a forge for per-Run tokens.
* An installation needs ploegd to start while its forge is unreachable.

## More Information

* Finding F-10 in the [threat model](../security/threat-model.md#findings); issue [#298](https://github.com/ploeg-hq/ploeg/issues/298).
* [Credential flow](../security/credential-flow.md) for the mint, the claim and the live qualification.
* [ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md).
