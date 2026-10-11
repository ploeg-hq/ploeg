---
status: proposed
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A team label on an issue in a configured GitHub repository dispatches it, read and written back with a repository-scoped GitHub App token

## Context and Problem Statement

Ploeg takes work from Vikunja and ClickUp. Issue [#44](https://github.com/ploeg-hq/ploeg/issues/44) asks for GitHub Issues as a third work source, beside GitHub as a forge, which is a separate change. A GitHub issue differs from a Vikunja task in ways that decide the design:

* A GitHub App, the credential GitHub recommends for automation, cannot be assigned to an issue. Assignment is how Vikunja and ClickUp dispatch.
* An App is often installed on a whole organisation, and anyone can open an issue on a public repository.
* GitHub sends a delivery id (`X-GitHub-Delivery`) and keeps it when a person redelivers, but it does not retry a failed delivery by itself, and it gives up on a delivery after 10 seconds.
* The REST API has primary and secondary rate limits and paginates with a `Link` header.
* An issue body can hold HTML comments that GitHub does not render, so a person reading the issue does not see them.

How does a GitHub issue become a queued Work Item for the right Team, and how does Ploeg read it and write back to it, without hard-coding one organisation and without weakening leases, budget admission or the human approval boundaries?

## Decision Drivers

* A person with triage rights on the repository decides that an issue is worked, and on which Team, as on the other trackers.
* An App installed on an organisation must not turn every repository into a work source.
* The credential is the smallest GitHub offers: one repository, issues and metadata only, expiring within the hour.
* Identity and Scope must pass the operator API's name pattern `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`, so a GitHub item can also be bound by the [tracker execution contract](../contracts/tracker-execution.md).
* A redelivered webhook must not apply twice, and a delivery whose effects failed must be replayable.
* Issue text is untrusted. It reaches a Run as the Work Item description, below the delivery contract and repository policy ([ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)).

## Considered Options

* **A team label (`ploeg:<team>`), or an assignee listed for the Team, on an issue in a configured repository**
* Assignment only, to a machine user per Team
* A status field on a GitHub Projects board
* Polling repositories for labelled issues instead of webhooks

## Decision Outcome

Chosen option: "**A team label, or a listed assignee, on an issue in a configured repository**", because a label works with an App credential, needs no seat per Team, and is applied only by people with triage rights, the same boundary assignment gives on the other trackers.

### Identity and routing

* A Work Item's `externalId` is `<owner>:<repo>:<number>` and its Scope is `<owner>:<repo>`, both in lower case. The colon keeps both inside the operator name pattern, which forbids `/` and `#`. The work branch is `agent/github-<owner>-<repo>-<number>`.
* The repositories are listed in the configuration file under `trackers.github.projects`. A project's `name` is the repository `owner/name` and is its own id, so no lookup runs at boot. Routing, `team:` pins, `default:`/`allow:` targets and gates work as for the other trackers. A delivery from any other repository is acknowledged and ignored. No organisation is hard-coded.
* Adding the label `ploeg:<team>` (prefix `PLOEG_GITHUB_TEAM_LABEL_PREFIX`) dispatches the issue to that Team; removing it unassigns. When the file lists Teams, a label naming another Team is ignored. Assigning a user listed in `PLOEG_GITHUB_TEAM_MAP` does the same as the label. This map is separate from `PLOEG_TEAM_MAP` and `teams.<name>.assignees`, because a GitHub login can equal an unrelated Vikunja username.
* `closed`, `deleted` and `transferred` close the Work Item: an unstarted one is withdrawn, as for the other trackers. A transferred issue gets a new identity in its new repository. `edited` and `reopened` are updates. Reopening does not dispatch again; removing and re-adding the label does, so a person approves the spend again. A team label added to a closed issue does not dispatch.

### Credentials

* Reads and write-backs use a GitHub App installation token minted by `pkg/githubapp` for exactly one repository, with `issues: write` and `metadata: read`. The App needs no other permission for the tracker. Tokens are cached until five minutes before they expire, and a token GitHub rejects with 401 is forgotten and minted once more.
* `pkg/githubapp` is shared: a GitHub forge provider can mint its own tokens from the same App with the permissions it needs, per repository.
* Revocation: remove the repository from the App's installation (or uninstall the App), and every token for it stops working at once. Rotating or deleting the App's private key stops new tokens; existing ones expire within an hour.
* Without App credentials the tracker still ingests from the webhook snapshot and its write-backs log and do nothing, like the other trackers without a token.

### Webhooks

* Every delivery must carry a valid `X-Hub-Signature-256` HMAC of the raw body with the configured secret; the body is capped at 1 MiB. Only `issues` events are read; `ping`, other events and pull requests yield nothing.
* The tracker route skips a delivery whose `X-GitHub-Delivery` was already recorded, and records it only after every effect of the delivery succeeded. A redelivery is answered 202 and changes nothing; a delivery that failed (500) is replayed by GitHub's "Redeliver", which keeps the id. Ids are kept 48 hours in the existing `forge_deliveries` table under `tracker:github`. The effects are idempotent on their own (ingest is an upsert, withdrawal a compare-and-swap), so two concurrent copies are safe. This is the dedup step that [ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)'s inbox will replace.
* One API call waits out a rate limit only when GitHub asks for at most 5 seconds (`Retry-After`, or `X-RateLimit-Reset` with no requests remaining); otherwise it fails with a `RateLimitError`. Ingest then falls back to the webhook's own issue snapshot, so a limit never costs an assignment. Comments are read 100 per page by following `Link: rel="next"`, at most ten pages, and a `next` link outside the configured API root is refused so the token never leaves it.

### Untrusted text

* HTML comments are removed from the issue body before it becomes the Work Item description, so a Run reads only what a person reviewing the issue saw.
* The description stays data inside Ploeg's prompt, below the delivery contract and the target repository's policy ([ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)). Dispatch, budget admission, leases and publication approval are unchanged: the label only queues the Work Item, the Team's budget admits its Runs, and a person merges the pull request.

### Consequences

* Good, because any organisation's repositories can be work sources by listing them, with one App and no seats.
* Good, because a credential leak exposes one repository's issues for at most an hour.
* Good, because a redelivery cannot queue withdrawn work again, and a failed delivery can be replayed.
* Bad, because a renamed repository changes the Scope and identity of its issues: new events create new Work Items, and the configuration must be updated. Work Items made before the rename no longer receive events and are withdrawn by an operator. Accepted: renames are rare, and an identity readable as the repository name is worth more in branches, trailers and logs than a numeric repository id.
* Bad, because a delivery lost before Ploeg answered (an outage longer than GitHub's single attempt) is only recovered by a person redelivering it. The inbox of ADR-0060 or a reconciler that lists failed App deliveries closes this.
* Bad, because an App has one webhook URL. Until the forge provider shares it, issue events reach `/webhooks/tracker/github` through a repository or organisation webhook with its own secret.

### Confirmation

* `go test ./pkg/provider/githubissues/` checks signature verification, the event mapping, ignored deliveries, hidden-text removal, pagination, rate-limit waits and refusals, token refresh and the write-backs against an `httptest` GitHub.
* `go test ./pkg/githubapp/` checks that a token is minted for one repository with exactly the requested permissions, from a JWT that verifies with the App key.
* `go test ./pkg/httpapi/ -run GitHub` checks that a team label queues a routed Work Item, that a redelivery is skipped and a new delivery applied, and that closing withdraws unstarted work.
* `go test ./pkg/config/ -run GitHub` checks the configuration and that its Scope matches the tracker's.
* `TestQualificationGitHubIssuesReadAndWriteBack` runs against a dedicated test repository when `PLOEG_GITHUB_E2E_REPO` and App or token credentials are set, and is skipped otherwise.

## Pros and Cons of the Options

### Assignment only, to a machine user per Team

* Good, because it matches Vikunja and ClickUp exactly.
* Bad, because each Team needs a GitHub user with a seat and its own credential, and an App cannot be assigned. It remains available through `PLOEG_GITHUB_TEAM_MAP`.

### A status field on a GitHub Projects board

* Good, because it gives a board with columns.
* Bad, because Projects are GraphQL-only, belong to an owner rather than a repository, and need the organisation-level `projects` permission, which is wider than one repository's issues.

### Polling repositories for labelled issues

* Good, because nothing is lost when ploegd is down.
* Bad, because it spends rate limit on every repository whether or not anything changed, and adds a second ingest path. A reconciler for missed deliveries is the narrower fix.

## Re-evaluation triggers

* The inbox of ADR-0060 is built, so tracker dedup moves there.
* The GitHub forge provider lands, so the App's single webhook could serve both routes.
* A deployment needs GitHub Projects status columns, or status labels for `needs_human` and `awaiting_review`.
* A repository rename or transfer duplicates a Work Item in practice.
* GitHub changes how redeliveries keep `X-GitHub-Delivery`, or starts retrying failed deliveries itself.

## More Information

* Issue [#44](https://github.com/ploeg-hq/ploeg/issues/44); configuration and limits in [Connect GitHub Issues](../how-to/connect-github-issues.md).
* GitHub documentation: [validating webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries), [handling failed deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries), [installation access tokens](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app), [rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
* [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md) (repository-scoped credentials), [ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md) (work sources).
