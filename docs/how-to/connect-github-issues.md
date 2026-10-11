---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-11
verified_by: "Read pkg/provider/githubissues, pkg/githubapp, cmd/ploegd/github.go and ops/helm/ploeg; go test ./pkg/provider/githubissues/ ./pkg/githubapp/"
---

# Connect GitHub Issues as a work source

Use this when issues in one or more GitHub repositories should become Work Items. The decisions behind it are in [ADR-0085](../adrs/0085-a-team-label-on-an-issue-in-a-configured-github-repository-dispatches-it.md). GitHub as a forge (pull requests, branches, checks) is configured separately.

## How an issue becomes work

| On the issue | Ploeg does |
| --- | --- |
| A person adds the label `ploeg:<team>` | Queues a Work Item for `<team>`, routed by the repository's `trackers.github.projects` entry |
| A person assigns a login listed in `PLOEG_GITHUB_TEAM_MAP` | The same, for that login's Team |
| The team label or the listed assignee is removed | Withdraws the item if it belongs to that Team |
| The issue is closed, deleted or transferred | Withdraws unstarted work; settles `needs_human` or `awaiting_review` work without a comment; leaves a running Run to finish |
| The issue is edited or reopened, or another label changes | Records an update; nothing is queued. To run a reopened issue again, remove and re-add the team label |
| A team label is added to a closed issue | Nothing is queued |

A Work Item's id is `<owner>:<repo>:<number>` in lower case, for example `webgrip:app:42`, and its branch is `agent/github-webgrip-app-42`. Ploeg comments on the issue when a Shift finishes, a route is refused, an operator cancels or restarts it, and when its pull request is merged or closed. With `PLOEG_TRACKER_DONE_ON_MERGE=true` a merge also closes the issue as completed. No other state changes the issue.

HTML comments (`<!-- … -->`) in the issue body are removed before a Run reads it, because GitHub does not show them to the person who added the label. The rest of the body is untrusted data in the Run's prompt and ranks below the delivery contract and the repository's own policy ([ADR-0030](../adrs/0030-target-repository-instructions-rank-below-the-delivery-contract.md)).

## 1. Register the GitHub App

Create a GitHub App owned by the organisation (or reuse the one the GitHub forge provider uses) with these repository permissions and nothing else for the tracker:

| Permission | Access | Why |
| --- | --- | --- |
| Issues | Read and write | Read the issue, post comments, close it on merge |
| Metadata | Read | Required by GitHub for every App |

Install it on only the repositories that are work sources. Download a private key. Ploeg mints a token per repository with exactly these two permissions; each expires within an hour.

To revoke access, remove the repository from the installation or uninstall the App: every token for it stops working immediately. Deleting the private key in the App settings stops new tokens; existing tokens expire within the hour. To rotate the key, add a second key, update the Secret, restart ploegd, then delete the old key.

## 2. Send issue events to Ploeg

A GitHub App has a single webhook URL. Until the forge provider routes App deliveries, add a repository or organisation webhook:

* Payload URL: `https://<ploeg>/webhooks/tracker/github`
* Content type: `application/json`
* Secret: a random value, stored as `PLOEG_GITHUB_TRACKER_SECRET`
* Events: **Issues** only

GitHub sends a `ping` on creation; Ploeg answers 202 and ignores it.

## 3. Configure ploegd

Store the private key and the webhook secret in Kubernetes Secrets and set the chart values:

```yaml
tracker:
  github:
    appId: "123456"
    appPrivateKeySecret: {name: ploeg-github-app, key: private-key.pem}
    webhookSecret: {name: ploeg-github-webhook, key: PLOEG_GITHUB_TRACKER_SECRET}
    # apiUrl: https://git.example/api/v3   # GitHub Enterprise Server
    # teamLabelPrefix: "ploeg:"
    # teamMap: "acme-bot=bronze"
config:
  trackers:
    github:
      projects:
        - name: webgrip/app        # the repository; no lookup runs
          repo: webgrip/app
          branch: development
          # team: bronze           # pin: every issue runs as this Team
```

Only repositories listed under `trackers.github.projects` are work sources; issue events from any other repository are acknowledged and ignored. When the file lists `teams:`, a label naming another Team is ignored. Every variable is in the [configuration reference](../reference/configuration.md).

## Limits

* **Deliveries.** A delivery must be signed with `X-Hub-Signature-256` and be at most 1 MiB. Ploeg records `X-GitHub-Delivery` after every effect succeeded and keeps it for 48 hours; a redelivery within that time is answered 202 and does nothing. GitHub does not retry a failed delivery itself: after an outage, open the webhook's *Recent Deliveries* and redeliver the failed ones. Redelivery keeps the delivery id, so a delivery that already took effect is not applied twice.
* **Rate limits.** One API call waits out a rate limit only when GitHub asks for at most 5 seconds; otherwise ingest uses the issue snapshot in the webhook and a write-back fails and is logged.
* **Comments** are read 100 per page, at most 1,000 per issue.
* **Renames and transfers.** A renamed repository changes the identity of its issues: update its `trackers.github.projects` entry, and expect new events to create new Work Items. A transferred issue is closed in its old repository.
* **Not supported:** GitHub Projects columns, status labels for `needs_human` and `awaiting_review`, sub-issues as epics, and webhook registration by Ploeg.

## Qualify against a test repository

The normal test suite never calls GitHub. To run the end-to-end qualification against a dedicated test repository:

```sh
PLOEG_GITHUB_E2E_REPO=owner/ploeg-qualification \
PLOEG_GITHUB_E2E_APP_ID=123456 \
PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE=/path/to/key.pem \
mise exec -- go test ./pkg/provider/githubissues/ -run Qualification -v
```

`PLOEG_GITHUB_E2E_TOKEN` (a fine-grained token with issues read and write) can replace the App variables, and `PLOEG_GITHUB_E2E_API_URL` points it at GitHub Enterprise Server. The test opens an issue, reads it, comments, reads the comments back and closes it.

| Symptom | Cause | Fix |
| --- | --- | --- |
| The webhook shows 400 | Wrong or missing secret | Set the same secret in GitHub and `PLOEG_GITHUB_TRACKER_SECRET` |
| 202, but no Work Item | Repository not under `trackers.github.projects`, label prefix or Team name wrong, or the issue is closed | Check the ploegd log line `github issue event ignored` or `github team label ignored` |
| Work Item queued, but no comments on the issue | App not configured or not installed on the repository | Set `appId` and the key; install the App on the repository |
| Log says `rate limited` | GitHub's primary or secondary rate limit | Wait for the reset; ingest continues from webhook snapshots |
