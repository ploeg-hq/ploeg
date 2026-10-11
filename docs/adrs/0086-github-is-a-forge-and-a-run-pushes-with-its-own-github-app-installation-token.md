---
status: proposed
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# GitHub is a forge, and a Run pushes with its own GitHub App installation token

## Context and Problem Statement

Ploeg delivers pull requests on Forgejo and GitLab only. A team whose code lives on GitHub (issue #44) cannot use it, even with the GitHub Issues tracker of [ADR-0085](0085-a-team-label-on-an-issue-in-a-configured-github-repository-dispatches-it.md). A GitHub forge has to keep what the other forges give a Run: push rights minted per writing Run and ended when the Run settles or its Lease lapses ([ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md)), a push fence and API allowlist that keep the harness inside its branch and repository ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)), delivery facts read from the forge ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)), checks read on the pushed commit ([ADR-0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md)), authenticated and deduplicated webhooks, and the human-merge boundary. Which GitHub credential carries those guarantees, and what does GitHub not let Ploeg do?

## Decision Drivers

* A Run's push credential reaches one repository, and holding the Lease and being able to push stay one fact.
* GitHub cannot scope a credential to a branch; Ploeg's push fence has to do that, as it does on Forgejo.
* GitHub lists no installation tokens, so the Forgejo sweep (list by name, revoke orphans) has no equivalent.
* One GitHub App identity already exists for the tracker (ADR-0085, `pkg/githubapp`); a second minter or a second App would be two things to secure.
* github.com serves git and the REST API from different hosts (`github.com`, `api.github.com`).

## Considered Options

* A GitHub App installation token per writing Run, minted by ploegd, revoked on settle, bounded by GitHub's one-hour expiry
* One fine-grained personal access token shared by every Run
* A GitHub App user-to-server token, or an OAuth token, per Run
* No per-Run credential on GitHub: support GitHub only for reading Roles

## Decision Outcome

Chosen option: "A GitHub App installation token per writing Run", because it is the only GitHub credential that ploegd can mint for one repository with named permissions and revoke on demand, with an expiry GitHub enforces even when ploegd cannot.

What is decided:

* **Forge provider.** [`pkg/provider/github`](../../pkg/provider/github/) implements the forge SPI: comments, pull request facts, mergeability (`mergeable`, and `mergeable_state: dirty` as a conflict), commit status from both commit statuses and check runs (GitHub Actions reports through check runs), ancestry through the compare API, changed paths, change and diff, repository inspection and clone URLs. Every list follows the `Link` header, and every request waits out a primary or secondary rate limit as long as GitHub asks, up to one minute ([`pkg/githubapi`](../../pkg/githubapi/)). A cached ploegd token GitHub refuses with 401 is minted again once.
* **Per-Run token.** With `PLOEG_GITHUB_APP_ID`, ploegd mints an installation token per writing Run through [`pkg/githubapp`](../../pkg/githubapp/) (`Minter.Mint`, uncached), limited to the Run's repository and to `contents:write`, `pull_requests:write`, `checks:read`, `statuses:read` and `metadata:read`. `workflows` is not asked for, so a Run cannot change GitHub Actions workflows. A grant wider than asked is revoked and refused. ploegd revokes the token (`DELETE /installation/token`) when the Run settles, its Lease lapses or the sweep finds its Run without a Lease. ploegd keeps minted tokens in memory under an id that names the Run (`github-app:<run>-<nonce>`), never the token; a token whose ploegd restarted before revoking it ends at GitHub's one-hour expiry. That expiry is the bound this option guarantees where Forgejo has none.
* **Long Runs.** The claim carries `forgeTokenExpiresAt`. Fifteen minutes before it, the worker asks `POST /api/v1/runs/{token}/forge-credential` for a new token for the Run's own repository; ploegd swaps it on the Lease and revokes the old one. The worker's forge proxy and its own git and API reads use the current token, so a Run behind the proxy keeps pushing past the hour. A harness that holds the token itself (forge token isolation off) loses push rights when it expires.
* **Push fence and allowlist.** The worker's forge proxy fences pushes to the Run's branch, as on Forgejo, and allows only the GitHub API calls a Role needs: read the repository and pull requests, open and update the Run's pull request, read and post conversation comments, read statuses and check runs. Merge, contents writes, refs, settings, dispatches and other repositories get 403. On github.com the proxy sends `/api/v3/...` to `api.github.com`. Git authenticates as `x-access-token`.
* **Webhooks.** `POST /webhooks/forge/github` verifies `X-Hub-Signature-256` over the raw body, refuses a delivery without `X-GitHub-Delivery`, and deduplicates on it. It normalizes `pull_request` (opened, reopened, synchronize, closed, a dirty merge state), `pull_request_review` (submitted), `check_run` (completed as failed, timed out, cancelled or needing action) and `status` (failure, error); everything else, `ping` included, is accepted and dropped. A merged or closed event is confirmed against the API before it settles a Work Item, so a replayed delivery cannot settle a pull request that is open again. The forge route is separate from the tracker's `POST /webhooks/tracker/github`: a repository or organization webhook with its own secret (`PLOEG_GITHUB_FORGE_SECRET`) and the forge events.
* **Fallback.** Without an App, `PLOEG_GITHUB_TOKEN` (a fine-grained personal access token) authenticates ploegd and the workers. It is clearly weaker: one shared token, no per-Run scope, no revocation by Ploeg. ploegd logs a warning at boot.
* **Not supported.** Ploeg never merges. Comment attachments (GitHub has no API for them), pull request activity and CI history readers (the Run-facts pipeline records less on GitHub until they exist), the Claude self-hosted environment push exchange of ADR-0084 (Forgejo only) and SSH remotes.

### Consequences

* Good, because a Run's GitHub token reaches one repository with five permissions, dies at revocation or after an hour, and cannot change workflows.
* Good, because the tracker and the forge share one App, one key and one minter.
* Bad, because ploegd cannot enumerate GitHub tokens: a ploegd restart loses the handle on tokens it minted, and they live until their expiry. ploegd runs as one replica, so no other replica needs the handle.
* Bad, because a token is repository-wide on GitHub: a harness that holds it directly can push any branch, open a pull request into any base or merge if branch protection allows. The proxy is the fence; repositories should protect their default branch with a ruleset that requires a pull request and a human approval.
* Bad, because worker pods on github.com need egress to both `github.com` and `api.github.com`.

### Confirmation

* `go test ./pkg/provider/github/ ./pkg/githubapp/ ./pkg/githubapi/ ./pkg/forgebroker/ ./pkg/worker/ ./pkg/httpapi/ ./cmd/ploegd/` in CI: HTTP fixtures for every provider call, signature, delivery and replay handling, per-Run mint, revoke and sweep, the router, the proxy allowlist and push credentials, the refresh endpoint and the claim contract.
* `sh scripts/helm-golden.sh check` renders the `executor-github` variant.
* The opt-in qualification `TestQualificationAGitHubRunDeliversAPullRequest` (`PLOEG_GITHUB_E2E_REPO`, `PLOEG_GITHUB_E2E_APP_ID`, `PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE`) mints a per-Run token against a dedicated repository, pushes a branch, is refused a workflow change, opens, reads, comments on and closes a pull request, and proves the revoked token is dead. Run it before a release that changes this path.

## Pros and Cons of the Options

### One fine-grained personal access token

* Good, because it needs no App registration.
* Bad, because it belongs to a person, reaches every repository it lists, lives until it expires and Ploeg cannot revoke it. Kept only as the marked-weaker fallback.

### A user-to-server or OAuth token per Run

* Bad, because it acts as a person, needs that person's consent flow, and its permissions are the person's, not the Run's.

### GitHub for reading Roles only

* Bad, because it does not deliver a pull request, which is the point of issue #44.

## Re-evaluation triggers

* GitHub lets an installation token be scoped to a branch or ref.
* GitHub adds an API to list or introspect installation tokens, which would let the sweep recover after a restart.
* ploegd runs more than one replica.
* A Run behind the proxy loses push rights at the hour mark in production.
* The ADR-0060 inbox is built, which changes where delivery dedup lives.

## More Information

* Issue [#44](https://github.com/ploeg-hq/ploeg/issues/44); the tracker half is ADR-0085.
* [Connect GitHub as a forge](../how-to/connect-github-as-a-forge.md) lists the App's permissions, the webhook and the chart values.
