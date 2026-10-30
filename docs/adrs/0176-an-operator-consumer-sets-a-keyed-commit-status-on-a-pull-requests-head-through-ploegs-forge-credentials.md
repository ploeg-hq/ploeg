---
status: proposed
date: 2026-10-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An operator consumer sets a keyed commit status on a pull request's head through Ploeg's forge credentials

## Context and Problem Statement

An Operator Consumer can keep one comment per key on a Work Item's pull request with Ploeg's forge credentials ([ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)). A comment is something a person reads. Branch protection, merge queues and forge dashboards do not read comments; they read the commit statuses on the pull request's head commit. A consumer that records a decision about a pull request has no way to make the forge see it unless it holds forge credentials of its own for every repository Ploeg works in. That duplicates exactly what ADR-0079 avoided for comments. How does an Operator Consumer set a named status on a pull request's head commit, on every forge Ploeg supports, without its own forge credentials and without the status changing Ploeg's own review gate?

## Decision Drivers

* The consumer needs no forge identity, webhook or rate limit of its own (ADR-0079).
* The status belongs to one commit. The head SHA comes from the forge, never from an agent ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)), and a status set for one head never moves to another.
* Repeating a write changes nothing. The same key on the same head is one status, whatever the number of calls.
* Every change names a person ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)).
* A consumer's status must not reach Ploeg's own CI gate ([ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md)). If it did, a consumer's `pending` would hold a Shift and its `failure` would pay for a fix round the writer cannot win.
* Ploeg knows nothing of what a status means ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **A keyed status route on the Work Item: Ploeg writes the status with its forge credentials on the forge's head SHA, keeps one status per key and head, and leaves its own statuses out of its CI gate**
* The consumer holds forge credentials and writes statuses itself
* Check runs (GitHub Checks API) instead of commit statuses
* Ploeg carries a key's last status forward to each new head

## Decision Outcome

Chosen option: the first, because commit statuses are the one surface that GitHub, Forgejo and GitLab all offer and that their branch protection reads, and because the route reuses the credentials, scope and audit of the keyed comment.

Proposed, not built. Each clause below states the contract that the implementation must meet.

1. **The route.** `PUT /api/v1/operator/work-items/{id}/pull-request-statuses/{key}` takes a `pullRequestStatusRequest` (`state`, `description`, `targetUrl?`, `number?`, `headSha?`). It needs execute permission and an attributed person, like the keyed comment. `key` matches `^[a-z0-9][a-z0-9-]{0,62}$`. `state` is `pending`, `success` or `failure`. `description` is 1 to 140 characters, the shortest limit among the supported forges. `targetUrl` is an `https` URL of at most 2048 characters with no credentials. `number` picks the pull request when the Work Item has more than one, as for comments. The route acts only on Work Items whose Team the consumer names ([ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)).
2. **The commit.** Ploeg reads the pull request's head SHA from the forge at the time of the write. It never uses one it stored earlier or one an agent reported. When the request carries `headSha` and the forge's head differs, the request answers 409 `head_changed` with the current head and writes nothing. A consumer that decided about one commit cannot then mark a newer one.
3. **The context.** The forge context is `ploeg/<key>`. On GitLab the context is the status `name`. One key is one context. Several consumers that share a deployment share the key space, as they do for keyed comments.
4. **Writing on every forge.** Ploeg writes through a `CommitStatusWriter` that each forge provider implements:
   * **GitHub:** `POST /repos/{owner}/{repo}/statuses/{sha}`. ploegd's installation token gains `statuses: write` ([ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md)); a Run's token keeps `statuses: read`.
   * **Forgejo:** `POST /repos/{owner}/{repo}/statuses/{sha}` with ploegd's own token, which needs `write:repository`.
   * **GitLab:** `POST /projects/:id/statuses/:sha`, with `failure` sent as `failed`.

   A forge provider without the writer answers 503 `forge_unavailable`, and a forge that refuses the write answers 502 `forge_unavailable` with the forge's status code. A refusal for missing permission answers 502 `forge_permission` and names the permission. Writes for the same pull request and key are serialized under the same kind of lease as keyed comments. A concurrent write answers 409 `status_busy`.
5. **Idempotency.** Ploeg stores the last status it wrote for each pull request, key and head SHA. A request whose state, description and target URL equal the stored ones answers 200 with `unchanged: true` and calls no forge. This keeps retries under GitHub's limit of 1000 statuses per SHA and context, and away from GitLab's refusal to repeat a state transition. A changed request overwrites the context's state on that SHA, as every forge does. A status on an earlier head is never rewritten, deleted or carried to a new head. The facts mark it `stale` instead.
6. **Attribution and audit.** Each write the forge accepts is audited as `pull_request_status.set`, with `key`, `context`, `number`, `headSha`, `state`, `description`, `targetUrl` and the request's `attribution`, including `proof`. An `unchanged` request writes no row. Under `PLOEG_OPERATOR_ATTRIBUTION=proven`, a write without a person token is refused before the route (ADR-0171).
7. **Ploeg's own gate.** ADR-0119 judges the forge's CI at the head without the contexts Ploeg wrote there. Ploeg leaves out exactly the (SHA, context) pairs it recorded writing. It does not leave out every context that starts with `ploeg/`, so a repository's own CI job with that name still counts. When the forge's combined state includes a status Ploeg wrote, Ploeg recomputes the state from the remaining checks.
8. **Reading.** The Work Item's facts (ADR-0079) carry `commitStatuses`: per pull request and key, `context`, `headSha`, `state`, `description`, `targetUrl`, `setAt`, `by` and `stale` (true when `headSha` is no longer the pull request's head).

The contract, sketched (proposed, not built; it enters `operator-api.v1.schema.json` with the Go types that serve it):

```json
{
  "pullRequestStatusRequest": {
    "state": "success",
    "description": "Approved for merge by the release owner.",
    "targetUrl": "https://example.org/decisions/412",
    "headSha": "9f2c4e1d0b7a6c5e4f3a2b1c0d9e8f7a6b5c4d3e"
  },
  "pullRequestStatusResponse": {
    "schemaVersion": "1.0",
    "status": {
      "workItemId": 412,
      "number": 87,
      "key": "release-approval",
      "context": "ploeg/release-approval",
      "headSha": "9f2c4e1d0b7a6c5e4f3a2b1c0d9e8f7a6b5c4d3e",
      "state": "success",
      "forge": "github",
      "setAt": "2026-10-29T10:20:00Z"
    },
    "unchanged": false
  },
  "headChangedError": {
    "code": "head_changed",
    "message": "The pull request's head is now 1a2b3c…; nothing was written.",
    "headSha": "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
  }
}
```

### Open question: how a machine caller is attributed

Statuses are often set by a program, such as a pipeline that finished a check or a bot that read a decision, with no person in the request. ADR-0171 names this exact case as a re-evaluation trigger ("a consumer that changes work has no person behind it … decide how a machine caller is attributed") and leaves it open. This record does not decide it. Until it is decided, this route follows ADR-0171 unchanged: under `proven` a machine caller cannot set a status, and under `asserted` it names an actor that the audit records as `asserted`. The options for the record that answers the trigger:

* **A workload token.** The caller presents a token that the identity provider issued to the machine (the OAuth 2.0 machine grant of [RFC 6749 §4.4](https://www.rfc-editor.org/rfc/rfc6749#section-4.4), exchanged for Ploeg's audience like a person token, ADR-0150). Attribution records `proof: workload` and the machine's subject. Only routes and consumers that declare it accept it.
* **Always a person.** The machine acts for the person who started it and carries that person's exchanged token, so attribution stays `proven`.
* **A named machine actor under `asserted`.** Ploeg accepts `X-Ploeg-Actor: machine:<name>` on this route only, recorded as `asserted`, also under `proven`. This reopens F-07 for this route.

The answer belongs in a record that answers ADR-0171's trigger for every change route, not only this one.

### Consequences

* Good, because branch protection, merge queues and forge dashboards can require a consumer's decision, and the consumer holds no forge credentials.
* Good, because one key on one head is one status however often the request is retried, and a decision about one commit never marks another.
* Good, because a consumer's status cannot hold a Shift or open a fix round in Ploeg's own gate.
* Good, because every surface change is an addition ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)).
* Bad, because ploegd's GitHub App now asks for `statuses: write`, and an existing installation must accept the new permission before the route works on GitHub. Until then the route answers `forge_permission`.
* Bad, because a status that a consumer requires through branch protection blocks merging after every push until the consumer sets it again. Accepted, because a decision is about a commit. The `stale` fact tells the consumer when to decide again.
* Bad, because consumers sharing a deployment share keys. Two consumers using one key overwrite each other's status, as they would for a keyed comment.

### Confirmation

Once built, `go test ./pkg/httpapi/ ./pkg/provider/... ./pkg/shiftengine/` runs in `.forgejo/workflows/on_pull_request.yml` (`mise exec -- go test ./...`). Tests use forge fakes built on `net/http/httptest` for GitHub, Forgejo and GitLab, and must show the following:

* `TestAKeyedStatusIsWrittenOnTheForgesHead` on each forge, with the forge's request body and the GitLab `failed` mapping;
* `TestAStaleHeadShaWritesNothing` (409 `head_changed`);
* `TestARepeatedStatusCallsNoForge` (`unchanged: true`, no audit row) and `TestAChangedStatusOverwritesTheContext`;
* `TestEachStatusIsAuditedWithItsPersonAndProof` under `asserted` and `proven`;
* `TestAStatusPloegWroteIsLeftOutOfTheCIGate`, where a `failure` under `ploeg/<key>` opens no fix round and a `pending` holds no Shift, while a repository job named `ploeg/build` still counts;
* `TestAForgeWithoutAStatusWriterAnswersForgeUnavailable` and `TestAMissingPermissionAnswersForgePermission`.

Every response is validated against `operator-api.v1.schema.json`. `go test ./pkg/githubapp/` checks the new ploegd permission and that a Run's permissions are unchanged. `go test ./internal/ledger/` keeps this record in the index.

## Pros and Cons of the Options

### The consumer holds forge credentials and writes statuses itself

* Good, because Ploeg changes nothing.
* Bad, because every consumer needs an identity, permissions and rate limits on every forge, and the head SHA it writes to may differ from the one Ploeg gates on.

### Check runs instead of commit statuses

* Good, because a check run carries richer output on GitHub.
* Bad, because only GitHub has check runs, and only a GitHub App may create them. Forgejo and GitLab would need a second surface.

### Ploeg carries a key's last status forward to each new head

* Good, because a required status would not block merging after a push.
* Bad, because the status would then claim something about a commit that nobody decided on.

## Re-evaluation triggers

* A record answers ADR-0171's machine-attribution trigger. This route then adopts that answer.
* A supported forge adds or changes a commit status state, its length limits, or how it treats a repeated state.
* A consumer needs a status that is richer than state, description and URL, or a status that is not tied to a pull request.
* Two consumers in one deployment collide on a key, or a deployment asks for a context prefix other than `ploeg/`.
* Ploeg supports a forge without commit statuses.

## More Information

* Sibling of [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)'s keyed pull request comment, whose lease, scope and audit this route reuses.
* Gate interaction: [ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) rules 2 to 6.
* Attribution: [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md) and [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md), including its first re-evaluation trigger, and [threat model](../security/threat-model.md) F-07.
* Sibling record: [ADR-0175](0175-a-proposed-work-item-starts-a-shift-only-when-every-named-approval-was-given-at-its-revision.md), which gates admission on named approvals.
* Forge APIs: [GitHub commit statuses](https://docs.github.com/en/rest/commits/statuses), [Forgejo `CreateCommitStatus`](https://forgejo.org/docs/latest/user/api-usage/), [GitLab commit status API](https://docs.gitlab.com/api/commits/#set-the-pipeline-status-of-a-commit).
