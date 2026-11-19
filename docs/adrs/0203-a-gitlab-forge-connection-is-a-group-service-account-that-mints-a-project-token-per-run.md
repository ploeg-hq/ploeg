---
status: proposed
date: 2026-11-19
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-19
---

# A GitLab forge connection is a group service account that mints a project token per Run

## Context and Problem Statement

[ADR-0013](0013-push-rights-are-minted-per-run.md) tier 2 gives every writing Run a forge credential minted for it alone and revoked when it settles, so holding the Lease and being able to push are one fact. Forgejo mints a repository-restricted token with the bot's password ([ADR-0172](0172-the-forgejo-broker-runs-only-on-forgejo-15-and-refuses-a-token-the-forge-did-not-restrict-to-the-runs-repository.md)), GitHub an installation token pinned to one repository ([ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md)). GitLab mints nothing: a static GitLab forge hands every writing Run the shared `PLOEG_GITLAB_TOKEN`, so [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md) lets GitLab be only the sole forge of its release, and `connectionBroker` refuses every runtime GitLab connection with `ErrConnectionCannotMint`, so [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md) registration answers `422 capability_missing` for GitLab. A runtime GitLab connection ([ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md)) is a person's OAuth token: it acts as that person.

How does a GitLab installation get per-Run push rights that a person can set up through the operator API, without a person's credential doing Ploeg's work and without anyone handling a long-lived token?

## Decision Drivers

* **One project, one Run, revoked on settlement.** The ADR-0013 property, with GitLab's own expiry as the bound when a revocation is lost.
* **No person's identity.** A Run must not push, comment or open a merge request as the person who connected GitLab.
* **No long-lived token in a person's hands** ([ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md)). What a person types must die once Ploeg has used it.
* **Self-serve.** A group Owner connects GitLab through the operator API; no installer edits Helm values or seeds a secret.
* **GitLab's real API, as its source has it,** on gitlab.com and self-managed.

## Considered Options

* (a) The person's OAuth token mints and pushes
* (b) A service account's token shared by every Run
* (c) A group service account whose token mints a project access token per Run
* (d) A fine-grained personal access token per Run on the service account
* (e) The OpenBao GitLab secrets engine mints the tokens

## Decision Outcome

Chosen option: "**(c) a group service account whose token mints a project access token per Run**", because a project access token reaches one project at one role, is created and revoked through the API, carries the Run in its name and ends by itself at a date GitLab enforces, and the service account that creates it is nobody's person.

1. **The connection method `gitlab_service_account`.** `POST /api/v1/operator/connection-flows` with `port: forge`, `kind: gitlab` and `method: gitlab_service_account` starts it; `connections.allow[].methods` allows it per instance, for `kind: gitlab` on the forge port only. `POST /connection-flows/complete` takes `state`, `token` (a personal access token with scope `api` of an Owner of the top-level group, ideally one day old) and `group` (the full path of the group, a subgroup allowed, whose projects Ploeg may reach).
2. **The bootstrap.** With the Owner's token Ploeg reads `group` and its top-level group, finds the service account `ploeg-<id, connection or label>` among `GET /groups/:top/service_accounts` or creates it with `POST /groups/:top/service_accounts`, adds it to `group` at Maintainer (`POST /groups/:id/members`, access level 40; an existing membership below Maintainer is raised), and creates its token with `POST /groups/:top/service_accounts/:user/personal_access_tokens`, scopes `api` and `self_rotate`, for `connections.instances.<id>.serviceAccountTokenDays` days (default 364). The account lives in the top-level group, so it can be invited to any group under it and stays invitable under SSO enforcement. It reaches exactly the projects of `group`.
3. **The store, the probe, the revocation.** The service account's token and a webhook signing token go to the secret store under the connection's reference in one write; no row, log, response or event holds either. Ploeg probes the new token (`GET /user` must be the service account, `GET /personal_access_tokens/self` must hold `api`, and the account must reach a project as Maintainer for `canMint`), records the connection, and finally revokes the Owner's token with `DELETE /personal_access_tokens/self` using that token, whether the flow succeeded or not. A failed revocation is the warning `owner_token_not_revoked` in the answer and the audit row `connection.owner_token_not_revoked`, never silent.
4. **Compensation.** A bootstrap that fails part-way undoes what it made: the token it created, the account when it created it (which takes its membership with it), otherwise the membership it added or raised. The same undo runs when the probe or the row write fails, and when the connection already exists (`409 already_connected`), so a second flow for the same account leaves the account and its membership and keeps no second token.
5. **The per-Run mint, `forgebroker.GitLab`.** `POST /projects/:id/access_tokens` by the project id a runtime target stored (by URL-encoded path only for a static target, which stores none), named `ploeg-run-<first 12 characters of the Run token>`. A writing Run gets access level 30 (Developer) with `api` and `write_repository`; a reading Run 20 (Reporter) with `read_api` and `read_repository`. `expires_at` is the UTC date two days after the mint. The credential id is `gitlab:<project>/<token id>`; `DELETE /projects/:id/access_tokens/:token_id` revokes it and a 404 is done. The sweep lists the active tokens of every project the connection's live targets and its own mints name before it asks which Runs hold a Lease, and revokes only `ploeg-run-*` tokens of Runs that hold none. A 400, 403 or 404 on the mint says what the project needs: the account as Maintainer, project access tokens allowed by the top-level group, and Premium on gitlab.com.
6. **Which connections mint.** A `gitlab_service_account` connection, and a `token` connection (a service account's token an administrator pasted) whose probe found `api` and Maintainer reach. A GitLab OAuth connection acts as its person and is refused with a reason that names `gitlab_service_account`. Registration admits a GitLab connection whose last probe reported `canMint`, and requires Maintainer on the target project, the least role that may create a project access token.
7. **The static forge.** `PLOEG_GITLAB_MINT_PER_RUN=on` (chart `executor.gitlab.mintPerRun`) mints the same way with `PLOEG_GITLAB_TOKEN`, which must then be a service account's token; its sweep lists the static GitLab targets. With it GitLab may serve beside another forge; without it the chart and ploegd keep refusing that.
8. **Webhooks.** Registering a runtime GitLab target first registers a project webhook at `PLOEG_PUBLIC_WEBHOOK_BASE/webhooks/forge/gitlab?connection=<id>` for merge request, note, pipeline and push events, updating a hook already at that URL; removing the target deletes it, and one that could not be deleted is named in the removal's audit row. On GitLab 19 or later the secret is the hook's `signing_token` (`whsec_` and 32 random bytes), and a delivery must carry `webhook-signature: v1,<base64 HMAC-SHA256 of "<webhook-id>.<webhook-timestamp>.<body>">` within five minutes; on an older GitLab the same secret is the hook's `X-Gitlab-Token`, compared in constant time. The GitLab version is read from `GET /version` when the hook is written. A delivery that names a connection is verified with that connection's secret only, never with the static `PLOEG_GITLAB_SECRET`, and one without a connection keeps the static check, so static installs work as before. Its events reach the static GitLab forge when one is configured, as ADR-0195's runtime App deliveries reach the static GitHub forge; without one they are verified and acknowledged.
9. **Rotation.** The hourly connection pass, and any credential read, rotates the service account's token with `POST /personal_access_tokens/self/rotate` and an explicit `expires_at` when fewer than 30 days remain, under the row's lock, writing the new token as one new version with every other stored value, the webhook secret included. GitLab revokes the old token when it answers; a new token Ploeg cannot store after three tries makes the connection `invalid` (`refresh_failed`), to be reconnected.
10. **Removal.** Removing the connection revokes its token with itself; the service account and its membership are named under `leftAtVendor`, because deleting them needs an Owner.
11. **Ploeg's own actors.** A project access token's bot user carries the token's name. A forge event whose user is a project bot (`project_<id>_bot…`) named `ploeg-run-…` is attributed to that name, and `isForgeBot` treats `ploeg-run-` as Ploeg's own, so a Run's comment never sends work back as a person's review.

### Consequences

* Good, because every GitLab Run holds a token that reaches its own project at its own role, and a lost revocation ends at 00:00 UTC two days after the mint at the latest: between 24 and 48 hours.
* Good, because no Run acts as a person, and the token a person typed is revoked before the answer.
* Good, because GitLab can be a self-serve forge with runtime targets and can serve beside Forgejo and GitHub.
* Good, because the token list at GitLab is the record of what was minted, as on Forgejo, so a ploegd that died between mint and record leaves nothing the sweep cannot find on a project it knows.
* Bad, because each project access token creates a bot user; GitLab deletes it with the token, keeping its contributions under a ghost user, and a project accumulates bots while Runs are live.
* Bad, because a merge request's author is a different bot per Run, so "authored by Ploeg" is a name prefix, not one account.
* Bad, because gitlab.com allows project access tokens only on Premium or Ultimate, a top-level group can switch them off, and service accounts depend on GitLab's tier; a Free gitlab.com group cannot mint.
* Bad, because a Developer token cannot merge into a protected default branch; Ploeg does not merge on GitLab today ([ADR-0188](0188-ploeg-merges-a-work-items-pull-request-at-a-named-head-for-a-proven-person-with-a-merge-identity-no-run-holds.md) is proposed).
* Bad, because the service account is a Maintainer of every project of its group: what a compromised ploegd holds reaches that group.
* Neutral: a static GitLab sweep lists only static targets and the projects this ploegd minted on; a token from an earlier process on a project no target names any more ends at its expiry.

### Confirmation

* `go test ./pkg/provider/gitlab/`: `TestABootstrapMakesAServiceAccountInTheTopGroupAMaintainerOfTheGroupWithItsOwnToken`, `TestASecondBootstrapReusesTheAccountAndItsMembership`, `TestABootstrapThatFailsAfterCreatingTheAccountDeletesIt`, `TestABootstrapNamesTheStepGitLabRefused`, `TestAServiceAccountTokenRotatesAndRevokesItselfAndItsProbeCanMint`, `TestAProjectHookIsSignedOnGitLab19AndUpdatedInPlace`, `TestARuntimeDeliveryIsProvedWithItsConnectionsSecret`.
* `go test ./pkg/forgebroker/`: `TestAGitLabMintIsAProjectTokenOfTheRunsProjectByIdWithTheRunsRole`, `TestAGitLabMintRefusalSaysWhatTheProjectNeeds`, `TestAGitLabRevokeIsIdempotent`, `TestAGitLabSweepRevokesOnlyOrphansAndListsBeforeItReadsLeases`, `TestARouterSendsAGitLabForgesMintAndRevocationToTheGitLabBroker`.
* `go test ./pkg/httpapi/`: `TestAGitLabServiceAccountConnectionKeepsOnlyItsOwnTokenAndRevokesTheOwners` (stored only in the store, the Owner's token revoked, rotation near expiry, removal revokes, no token in a row, response or log), `TestASecondServiceAccountFlowReusesTheAccountAndKeepsNoSecondToken`, `TestAServiceAccountBootstrapThatFailsUndoesItsAccountAndStoresNothing`, `TestAnOwnerTokenThatCannotBeRevokedIsAWarningAndAnAuditRow`, `TestAServiceAccountFlowRefusesAMissingGroupOrAnotherMethodsBody`, `TestAGitLabServiceAccountRegistersATargetWithItsWebhookAndMintsByProjectID`, `TestAGitLabTargetIsRefusedForAPersonsConnectionADeveloperOrNoWebhookBase`.
* `go test ./cmd/ploegd/`: `TestAGitLabForgeThatMintsPerRunMayServeBesideAnotherForge`, `TestAGitLabOAuthConnectionMintsNothing`, `TestTwoForgesRefuseToStartWhenOneCannotMint`.
* `scripts/helm-golden.sh check`: the `executor-gitlab` golden renders `PLOEG_GITLAB_MINT_PER_RUN`, and `reject-two-forges-with-gitlab` still fails without `mintPerRun`.
* Not yet confirmed: a mint, rotation and signed hook against a live GitLab. Until a dated record in `docs/research/` shows them, GitLab stays unsupported in the [support matrix](../reference/support-matrix.md).
* `go test ./internal/ledger/` validates this record.

## Pros and Cons of the Options

### (a) The person's OAuth token

* Good, because it exists already and needs nothing new at GitLab.
* Bad, because every Run acts as the person: their name on every push and comment, their whole reach as the blast radius.
* Bad, because GitLab's project access token API expects a personal access token, and an OAuth token's lifetime is the person's session.

### (b) A service account's token shared by every Run

* Good, because it is one token to rotate and no bot users accumulate.
* Bad, because it is the shared credential ADR-0013 removes: a zombie writer keeps pushing after its Lease lapsed, and a reader can push.

### (d) A fine-grained personal access token per Run

* Good, because one account would author every merge request.
* Bad, because GitLab offers no API to create a fine-grained, project-restricted personal access token for a group service account that is generally available on gitlab.com and self-managed alike today; the group service account token API takes the legacy scopes only.

### (e) The OpenBao GitLab secrets engine

* Good, because rotation and leases would live in the store.
* Bad, because it ties GitLab minting to one store while ADR-0184 keeps the store a seam, and it still needs the same project access token API and an administrator's token in the store.

## Re-evaluation triggers

* GitLab makes fine-grained, project-restricted tokens for group service accounts generally available on gitlab.com and self-managed: re-evaluate (d).
* A Run needs to merge on GitLab, or ADR-0188 gains a GitLab merger.
* A project's bot users or token count hits a GitLab limit, or project access tokens become available on Free gitlab.com groups or are withdrawn.
* GitLab changes how a project access token's expiry is enforced, or stops revoking a rotated token at once.
* A live GitLab qualification shows the signed webhook format or `GET /version` differs from what this record states.

## More Information

* Amends [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md) (GitLab may be one of several forges when it mints), [ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md) (a GitLab connection that can mint registers targets, with a project webhook) and [ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) (a fifth method), each through its More Information.
* GitLab source read on 2026-11-19 at gitlab-org/gitlab `master`: `lib/api/resource_access_tokens.rb` (create, list with `state=active`, revoke; `access_level` defaults to 40, so Ploeg always sends it), `app/models/concerns/expirable.rb` (a token expires when `expires_at <= now`, so a token dated today is dead when made and one dated tomorrow can live for seconds; Ploeg dates it two days ahead), `app/models/personal_access_token.rb` (365 days at most when expiry is enforced, against the instance's own date, hence 364 by default), `lib/api/group_service_accounts.rb` (create, list, delete, token create with legacy scopes, and rotate), `lib/api/personal_access_tokens/self_information.rb` (`DELETE /personal_access_tokens/self` with any scope, for personal access tokens only), `lib/api/personal_access_tokens/self_rotation.rb` (`api` or `self_rotate`; without `expires_at` the new token lives a week), `lib/api/project_hooks.rb` and `app/services/web_hook_service.rb` (`signing_token` since GitLab 19.0, `webhook-id`, `webhook-timestamp` and `webhook-signature`).
* Code: [service_account.go](../../pkg/provider/gitlab/service_account.go), [hooks.go](../../pkg/provider/gitlab/hooks.go), [forgebroker/gitlab.go](../../pkg/forgebroker/gitlab.go), [connection/service_account.go](../../pkg/connection/service_account.go), [worktarget/hooks.go](../../pkg/worktarget/hooks.go), migration `0099_gitlab_service_account_connections.sql`.
* How-tos: [let operators connect trackers and forges](../how-to/let-operators-connect-trackers-and-forges.md), [register Work Targets at runtime](../how-to/register-work-targets-at-runtime.md).
* 2026-11-19: proposed with its implementation in one pull request. The brief asked for `expires_at` tomorrow; the source shows a token dated tomorrow may live seconds when minted just before midnight UTC, so the record dates it two days ahead.
* 2026-11-19: section 8 amended. In a release with no static GitLab, every GitLab instance `connections.allow` names on the forge port becomes a forge provider under its instance id that acts on each project with the credential of the connection its active runtime target was granted through, read from the secret store per request; a project no active target names, or a target on an OAuth connection, is refused before any request. With exactly one such instance it also serves `/webhooks/forge/gitlab`, so a runtime project's deliveries, verified with the connection's secret, are acted on as a static GitLab's are (pull request facts, review settlement, follow-ups), and the Run's card is posted through the connection. With several such instances their deliveries are still only verified and acknowledged. A release with a static GitLab behaves as before.
