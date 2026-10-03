---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg learns where a merged change is deployed from a generic deploy endpoint

## Context and Problem Statement

A Run card ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)) is meant to show how long a change has been live. Vloer counts days live from the release and gives the card a finish as that number grows. Ploeg knows when a pull request merged. It does not know when the merge reached test, acceptance or production, because nothing tells it.

Deploys happen outside Ploeg: a CI job, a GitOps controller, or a person. They run on several forges and several clusters. The owner decided on 2026-10-01: a generic deploy endpoint that any pipeline can call with one line, "commit X is live in environment Y". GitOps notifications can come later on top of it. Until a project wires it up, the release counts from the merge, and the card says so.

Two questions follow. Which pull requests did a deploy carry? A deploy names one commit, and that commit may contain many merges. And who may report a deploy?

## Decision Drivers

* One line in any pipeline. No SDK, no forge-specific payload.
* A pipeline credential must not be able to read or execute anything else in Ploeg.
* No git clone in Ploeg. Ploeg already talks to the forge API.
* A repeated or retried report changes nothing.
* Facts only on the card, and the first time a change reached an environment never moves later ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)).
* A missing deploy signal is said, not hidden.

## Considered Options

* **A generic `POST /api/v1/deploys` with its own bearer token, and ancestry from the forge compare API**
* GitOps notifications only (Flux notification-controller, Argo CD notifications)
* The forge's own deployments API
* Mark a pull request only when its merge commit is the deployed commit
* Clone the repository in Ploeg and ask git for ancestry

## Decision Outcome

Chosen option: "**a generic deploy endpoint with ancestry from the compare API**", because every pipeline can send one `curl`, and the forge already answers "is this commit in that one" without a clone.

1. **The endpoint.** `POST /api/v1/deploys` takes `{environment, repo: {forge, owner, name}, sha, deployedAt?, url?, source?}` and answers `202 {"deployId", "pullRequests"}`. `pullRequests` counts the pull requests this report marked for the first time. The environment is free text, trimmed and lowercased, 1 to 63 characters. `sha` is the full commit hash. `deployedAt` defaults to now and may be at most five minutes ahead. `url` keeps scheme, host and path only. `source` is `ci`, `gitops` or `manual`. Unknown fields are refused, so a typo like `deployed_at` fails loudly. The contract is `docs/contracts/deploy-api.v1.schema.json`.
2. **Its own token.** `PLOEG_DEPLOY_TOKEN` (chart `deploys.tokenSecret`, a reference to an existing Secret) is compared as a SHA-256 hash in constant time. It is not an operator credential, and an operator token does not work here. Without it the endpoint answers 404, the same as a forge provider that is not configured: an instance that takes no deploys has no such endpoint. A token shorter than 32 bytes stops ploegd at boot. A bad or missing token answers 401.
3. **Idempotent per (repository, environment, sha).** Migration `0025_deployments.sql` adds `deployments` with that unique key, owner and name lowercased. A repeat keeps the first report's time, link and source and returns the same `deployId`. The first report writes one `deploy.recorded` audit row.
4. **Ancestry from the compare API.** For each merged Ploeg pull request of that repository with a merge commit, merged no later than ten minutes after the deploy, not yet compared with this deploy, and with no deploy of that environment at or before this one's time, Ploeg asks the forge whether the merge commit is an ancestor of the deployed commit. Forgejo: `GET /repos/{o}/{r}/compare/{deployed}...{merge}`. GitLab: `GET /projects/:id/repository/compare?from={deployed}&to={merge}`. Both list the commits of the merge that the deploy lacks, so an empty list means "deployed". Ploeg reads only until it sees the count or the first commit. This is the optional `provider.AncestryReader`; a forge without it marks nothing.
5. **Bounded, then finished by a sweep.** One report checks at most 50 pull requests, newest merge first, within 20 seconds. The deploy is recorded before any comparison, so a failing or slow forge never fails the report. A deploy stays pending until every candidate was compared, and ploegd resumes pending deploys every minute, one pass of the same size each. Migration `0037_deployment_checks.sql` keeps each conclusive comparison per (deploy, pull request); ancestry of two fixed commits never changes, so a pair is compared once and a repeated report compares nothing new. A failed comparison is logged and retried: a pass that compared something is resumed at once, a pass in which every comparison failed waits one minute, doubling up to an hour, and after 12 such passes in a row the deploy is given up and logged. Replicas claim a pending deploy for five minutes, so two do not check it at once.
6. **The first time is kept.** `pull_request_deployments` holds one row per (pull request, environment) with the deploy that first carried it. A later deploy never moves it. A deploy reported late with an earlier time is still compared with the pull request, and replaces it when it carried the change. Such a correction does not count in `pullRequests`.
7. **The card.** Each play lists its first deploy per environment, and the card lists the earliest per environment across plays. `release` is the first deploy of the latest merged play to the release environment, with `source: "deploy"`. When the repository has never reported a deploy of that environment, `release` is that play's merge time with `source: "merge"`. When it has, but not of this change yet, `release` is null. With nothing merged, `release` is null. The release environment is `production`, unless the Work Target sets `release.environment`.

### Consequences

* Good, because a pipeline on any forge or CI system wires this up with one `curl` and one secret.
* Good, because a deploy of a commit that carries ten merges marks all ten, with no clone and no extra state in Ploeg.
* Good, because the card can say "counted from merge, no deploy signal" until a project reports deploys, instead of inventing a date.
* Bad, because every deploy costs up to 50 forge reads, and a pull request that never lands in an environment (merged into a branch that is not deployed) is compared on every deploy of that environment.
* Bad, because when a project first wires the endpoint, only the 50 newest merged pull requests are checked by the report. The sweep marks the rest, 50 a minute. Until then their cards show no release, since the repository now has deploys.
* Bad, because a forge outage during a deploy leaves pull requests unmarked until the forge answers again, at most an hour after it recovers. An outage longer than about seven hours gives the deploy up, and the next deploy of that environment marks them with a later time.
* Neutral: `deployment_checks` grows by one row per comparison. A deploy compares only pull requests that no earlier deploy of its environment carried.
* Neutral: the deploy token is one shared credential per Ploeg. It can report a deploy for any repository Ploeg knows, but it cannot read or change anything else.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/provider`: reading a compare response stops at the count or the first commit; equal commits need no read; a forge without compare returns `ErrNoAncestry`.
* `pkg/provider/forgejo`, `pkg/provider/gitlab`: ancestor, not an ancestor and diverged commits from fake compare responses; a 404 is an error, not a "no".
* `pkg/store`: migration 0025; a repeated report keeps the first one and is audited once; only merged, unmarked pull requests merged before the deploy are candidates, newest first and bounded; the first time per environment is kept and an earlier late report replaces it; a pull request first marked by a later deploy is a candidate of an earlier one, and a compared pair is never offered again; pending deploys are claimed under a lease, deferred with a doubling pause and given up after the last attempt; the card's deployments, release from a deploy, the merge fallback, null while production has not received the change, and a configured release environment.
* `pkg/httpapi`: 404 without a token; 401 for a missing, wrong or operator token; 405 for GET; every validation failure; normalization of environment, sha and link; marking with a fake Forgejo for ancestor, diverged and failed comparisons; a repeat with the same `deployId`; a later deploy and a second environment; deploys reported newest first end with the earliest first time, and repeats compare nothing; 120 candidates are all marked by the sweep without another deploy; a forge outage is retried by the sweep after its pause; repeated failures give a deploy up; responses validate against `deploy-api.v1` and the card against `operator-api.v1`.
* `pkg/config`, `cmd/ploegd`: `release.environment` validation and conflicts, and the boot check on the token.
* `pkg/config` (`TestOperatorChartKeepsConsumerCredentialsInController`): the chart renders `PLOEG_DEPLOY_TOKEN` as a Secret reference on ploegd only.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### GitOps notifications only

* Good, because Flux and Argo CD already know when a revision is applied.
* Bad, because it covers only GitOps deploys, and each controller sends its own payload.
* Neutral: a small adapter can turn such a notification into a call to this endpoint later.

### The forge's own deployments API

* Good, because the forge would hold the record.
* Bad, because Forgejo has no deployments API, and GitLab and GitHub each model deployments their own way, so a pipeline would need a forge-specific call.

### Exact match on the merge commit

* Good, because it needs no forge read.
* Bad, because a deploy usually carries several merges, and only the last one would be marked.

### Clone and ask git

* Good, because `git merge-base --is-ancestor` is exact and fast once cloned.
* Bad, because ploegd would need disk, clone credentials and fetch logic for every repository, and it has none today.

## Re-evaluation triggers

* A GitOps controller should report deploys directly. Add an adapter in front of this endpoint, or a new record if it needs its own contract.
* The log line "more merged pull requests await a deploy check than one pass checks" appears on most deploys of a repository, or "deploy check given up" appears at all.
* A third forge provider is added. Check that it can implement `AncestryReader`.
* Several teams need separate deploy credentials, or a pipeline credential leaks.
* Vloer needs deploys that are not tied to a Ploeg pull request.

## More Information

* [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md): the card these facts appear on.
* [ADR-0045](0045-keep-run-usage-and-merge-facts.md): the merge facts, including the merge commit, that ancestry starts from.
* [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md): registered targets, where `release.environment` is set.
* [Send deploys from a pipeline to Ploeg](../how-to/send-deploys-from-a-pipeline.md): the pipeline line for Forgejo Actions, GitHub Actions and GitLab CI.
* Vloer computes days live and the finish from `release`; Ploeg keeps sending `finish: "matte"`.
