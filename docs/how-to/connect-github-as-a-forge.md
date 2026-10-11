---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-11
verified_by: "Read pkg/provider/github, pkg/githubapp, pkg/forgebroker/github.go, pkg/worker/forgeproxy.go, cmd/ploegd/github_forge.go and ops/helm/ploeg; go test ./pkg/provider/github/ ./pkg/forgebroker/ ./pkg/worker/"
---

# Connect GitHub as a forge

Use this when Runs should open pull requests on github.com or GitHub Enterprise Server. The decisions behind it are in [ADR-0086](../adrs/0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md). Issues as a work source are configured separately ([Connect GitHub Issues](connect-github-issues.md)); both use the same GitHub App.

## What Ploeg does on GitHub

| Ploeg | On GitHub |
| --- | --- |
| A writing Run starts | ploegd mints an installation token for the Run's repository alone, valid for at most one hour |
| The Run pushes and opens its pull request | Through the worker's proxy: pushes only to the Run's branch, the pull request API, comments, statuses and check runs; merging and everything else is refused |
| The Run runs longer than 45 minutes | The worker asks ploegd for a new token; ploegd revokes the old one |
| The Run settles, its Lease lapses or the sweep finds it orphaned | ploegd revokes the token (`DELETE /installation/token`) |
| A reading Role reviews | Uses the read-only token in `executor.github.readTokenSecret` |
| A person merges or closes the pull request | The webhook settles the Work Item: merged is done, closed without merging needs a human |
| A check run or commit status fails, or a review requests changes | Recorded; a Team's `forgeFollowUps` decides whether it becomes a repair or rework Follow-Up |

Ploeg never merges a pull request. Protect the default branch with a ruleset that requires a pull request and an approval, so the boundary also holds for a harness that holds the token itself.

## 1. Register the GitHub App

Use the App you registered for GitHub Issues, or create one owned by the organisation. Give it these repository permissions:

| Permission | Access | Why |
| --- | --- | --- |
| Contents | Read and write | Clone, and push the Run's branch |
| Pull requests | Read and write | Open and update the Run's pull request, post Ploeg's comments |
| Checks | Read | Read GitHub Actions results on the pushed commit |
| Commit statuses | Read | Read other CI results on the pushed commit |
| Metadata | Read | Required by GitHub for every App |
| Issues | Read and write | Only when the App is also the GitHub Issues tracker |

Do not grant Workflows, Administration or anything else: a token that comes back wider than Ploeg asked for is revoked and refused. Install the App on only the target repositories, and download a private key.

Revocation: ploegd revokes every Run token when the Run ends. A token whose ploegd restarted before it could revoke it ends at GitHub's one-hour expiry. To cut all access at once, remove the repository from the installation or uninstall the App.

## 2. Create the secrets

| Secret | Holds | Used by |
| --- | --- | --- |
| `ploeg-github-app` / `private-key.pem` | The App's private key | ploegd only |
| `agent-builder-token` / `GITHUB_TOKEN` | A fine-grained token; with the App it can be read-only | Writer pods at boot |
| `agent-reader-token` / `GITHUB_TOKEN` | A fine-grained read-only token (Contents, Pull requests, Checks, Commit statuses: read) | Reading Roles |
| `ploeg-github-forge-webhook` / `PLOEG_GITHUB_FORGE_SECRET` | A random webhook secret | ploegd only |

## 3. Configure the chart

```yaml
tracker:
  github:
    appId: "123456"              # shared with the GitHub Issues tracker
    appPrivateKeySecret: {name: ploeg-github-app, key: private-key.pem}
    apiUrl: ""                   # Enterprise Server: https://<host>/api/v3
executor:
  forge: github
  forgejo: null
  github:
    url: https://github.com      # or the Enterprise Server root
    tokenSecret: {name: agent-builder-token, key: GITHUB_TOKEN}
    readTokenSecret: {name: agent-reader-token, key: GITHUB_TOKEN}
    webhookSecret: {name: ploeg-github-forge-webhook, key: PLOEG_GITHUB_FORGE_SECRET}
    botLogin: ploeg[bot]         # the App's login, so Ploeg's own reviews never cause rework
```

Keep `executor.forgeTokenIsolation: proxy` for any harness that supports it: only behind the proxy does a Run keep pushing after its first token expires. Worker pods need egress to `github.com` and `api.github.com` (or the Enterprise Server host).

Without an App, `executor.github.tokenSecret` becomes `PLOEG_GITHUB_TOKEN` for ploegd and the shared push credential for every writer. That is weaker: Ploeg cannot scope it per Run or revoke it, and ploegd says so at boot.

## 4. Add the webhook

On the repository or the organisation, add a webhook:

* Payload URL: `https://<ploegd>/webhooks/forge/github`
* Content type: `application/json`
* Secret: the value of `PLOEG_GITHUB_FORGE_SECRET`
* Events: Pull requests, Pull request reviews, Check runs, Statuses

The GitHub Issues tracker has its own webhook at `/webhooks/tracker/github` with its own secret. Ploeg verifies `X-Hub-Signature-256`, refuses a delivery without `X-GitHub-Delivery`, ignores a redelivery it has seen in the last 48 hours, and confirms a merged or closed event against the API before it settles anything. GitHub's own `ping` is accepted.

## Verify

1. ploegd logs `github forge authenticates as a GitHub App` and `per-run GitHub App installation tokens enabled`.
2. The webhook's recent deliveries show `202` for the ping.
3. Dispatch a small Work Item. The worker logs `using a per-run forge credential minted for this run`, and the pull request appears authored by the App.
4. To qualify against a dedicated test repository, run `PLOEG_GITHUB_E2E_REPO=<owner/repo> PLOEG_GITHUB_E2E_APP_ID=<id> PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE=<pem> mise exec -- go test ./pkg/provider/github/ -run Qualification -v`. It opens and closes a pull request and deletes its branch.

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| Webhook deliveries answer `400` | Wrong secret, or the delivery has no `X-GitHub-Delivery` | Set the same secret on both sides |
| The claim is released with "could not mint a push credential" | The App is not installed on the repository, or lacks a permission | Install it on the repository; check step 1 |
| A push is refused with `refusing to allow a GitHub App to create or update workflow` | The Run changed `.github/workflows` | Intended; a person changes workflows |
| Pushes fail after about an hour | The harness holds the token directly (isolation off) | Turn on `forgeTokenIsolation: proxy`, or keep Runs under an hour |
| Reads answer `403` with a rate-limit message | GitHub's secondary rate limit; Ploeg waits up to one minute | Fewer concurrent Runs per installation |
