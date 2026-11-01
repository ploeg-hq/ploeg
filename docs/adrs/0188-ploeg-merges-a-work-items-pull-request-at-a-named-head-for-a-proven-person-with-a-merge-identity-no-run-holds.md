---
status: proposed
date: 2026-11-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-02-28
---

# Ploeg merges a Work Item's pull request at a named head for a proven person, with a merge identity no Run holds

## Context and Problem Statement

Ploeg opens pull requests and settles a Work Item when the forge says its pull request was merged, but it never merges one itself. [ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md) lists "Ploeg never merges" under *Not supported*, and [`docs/security/self-assessment.md`](../security/self-assessment.md) and [`docs/security/threat-model.md`](../security/threat-model.md) (row T-HN-3 and "Merge through the API — closed") rely on it. What exists today:

* **Settlement on merge.** `ReviewWatch.HandleForgeEvent` in [`pkg/shiftengine/review.go`](../../pkg/shiftengine/review.go) moves an `awaiting_review` Work Item to `done` on a `pr_merged` forge event (`provider.ForgePRMerged`), and `ReviewWatch.Reconcile` reads the forge for the same answer when an event is missed. It looks only at items `Store.AwaitingReview` returns. `provider.PullRequestFacts` already carries `MergeCommitSHA`, `MergedAt` and `MergedBy`.
* **Forge port.** `provider.ForgeProvider` ([`pkg/provider/provider.go`](../../pkg/provider/provider.go)) reads and comments. Writes to a pull request are optional capabilities, such as `PullRequestCloser` ([`pkg/provider/close.go`](../../pkg/provider/close.go), [ADR-0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md)) and the proposed `CommitStatusWriter` ([ADR-0176](0176-an-operator-consumer-sets-a-keyed-commit-status-on-a-pull-requests-head-through-ploegs-forge-credentials.md)). [ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md) makes each capability one interface found by type assertion; only the tracker side has a naming helper today (`provider.TrackerCapabilities` in [`pkg/provider/capabilities.go`](../../pkg/provider/capabilities.go)).
* **Forge credentials.** ploegd's Forgejo adapter authenticates with `PLOEG_FORGEJO_TOKEN` ([`cmd/ploegd/main.go`](../../cmd/ploegd/main.go)). Without `PLOEG_FORGEJO_BOT_PASSWORD`, the same value is what every worker pushes with (`forgebroker.Static{Token: env.sharedToken}` in [`cmd/ploegd/forgecreds.go`](../../cmd/ploegd/forgecreds.go)); with it, each writing Run gets a token minted for `PLOEG_FORGEJO_BOT` and restricted to its repository ([ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md), [ADR-0172](0172-the-forgejo-broker-runs-only-on-forgejo-15-and-refuses-a-token-the-forge-did-not-restrict-to-the-runs-repository.md)). On GitHub, a Run's installation token comes from the same App as ploegd's, and the fallback `PLOEG_GITHUB_TOKEN` authenticates ploegd and the workers alike (ADR-0086). Every credential ploegd holds for a forge today is therefore the agents' identity, or the agents' very token. The worker's forge proxy refuses merge calls (ADR-0086, T-HN-3); that is the only thing between a Run and a merge.
* **Operator API.** `OperatorPrincipal` ([`pkg/httpapi/operator.go`](../../pkg/httpapi/operator.go)) carries a Team scope (`AllowsTeam`) and the permissions `execute`, `verify` and `readContent`, parsed from `PLOEG_OPERATOR_CONSUMERS`. `operatorAuth` attributes every change and, under `PLOEG_OPERATOR_ATTRIBUTION=proven`, refuses one without a person token ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)). A proven person is `ProvenPerson.ID`, the pseudonymous id `operatorclient.PersonID` derives ([`pkg/operatorclient/person.go`](../../pkg/operatorclient/person.go)). `POST /api/v1/operator/work-items/{id}/approve` already exists and means admitting a proposed Work Item before spend ([ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md), [`pkg/httpapi/operator_proposed.go`](../../pkg/httpapi/operator_proposed.go)); the operator delivery contract's approval "never means merge approval" ([`docs/contracts/operator-delivery.md`](../contracts/operator-delivery.md)).

A person who has reviewed a Work Item's pull request in an operator console must today leave it for the forge to merge. An Operator Consumer that wants to offer "merge this" must hold its own forge credentials for every repository Ploeg works in, which is the duplication ADR-0176 avoided for commit statuses. The owner decided on 2026-11-01 that Ploeg should do it.

How does an authorised Operator Consumer have Ploeg merge a Work Item's pull request at the commit a proven person reviewed, so that no agent can ever merge its own work, branch protection still decides, and the forge's history says who asked?

## Decision Drivers

* **An agent never merges its own work.** The credential that merges is one no Run, worker, claim, Lease or Task Spec ever carries (rule R8), and it is not the identity agents push as.
* **The commit merged is the commit the person reviewed.** A push after the request makes the forge refuse; Ploeg never relies on its own check alone.
* **The forge's rules decide** ([ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) gates review on CI; branch protection gates merging). Ploeg never force-merges, never merges as an administrator, and never runs its own "wait until green, then merge" loop.
* **Delivery facts come from the forge** ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)). "Merged" is what the forge reports, and the Work Item settles through the path that exists.
* **Every merge names a proven person and the consumer** ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), ADR-0171), in Ploeg's audit and in the forge's history.
* **Least authority.** Merging is a separate permission from controlling executions, and the repository chooses how it merges.
* **Core stays vendor-free** (ADR-0183), and **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **A merge route on the Work Item: Ploeg merges with a separate per-forge merge identity, at the named head, through the forge's own "merge when checks succeed"**
* The Operator Consumer merges with forge credentials of its own
* Ploeg merges with the forge credential it already holds
* Ploeg waits for CI itself and merges when the head is green
* A forge merge queue or merge train

## Decision Outcome

Chosen option: "**A merge route on the Work Item: Ploeg merges with a separate per-forge merge identity, at the named head, through the forge's own 'merge when checks succeed'**", because it is the only option where the identity that can merge is out of every agent's reach, the forge enforces both the head and its own protection rules, and the consumer needs no forge credentials.

Proposed, not built. Each clause states the contract the implementation must meet.

### 1. The merge identity

A **merge identity** is a forge account or App, one per forge, that only ploegd holds and that Ploeg uses for nothing but merging and cancelling a scheduled merge.

| Forge | Credential (proposed) | Must differ from |
| --- | --- | --- |
| Forgejo | `PLOEG_FORGEJO_MERGE_TOKEN`, a token of a dedicated forge user with `write:repository` (chart `executor.forgejo.mergeTokenSecret`) | `PLOEG_FORGEJO_TOKEN` and the user `PLOEG_FORGEJO_BOT` |
| GitHub (later) | `PLOEG_GITHUB_MERGE_APP_ID` and `PLOEG_GITHUB_MERGE_APP_PRIVATE_KEY`, a second GitHub App with `contents: write` and `pull_requests: write`; ploegd mints its installation token per call, uncached | `PLOEG_GITHUB_APP_ID`; a personal access token is not accepted |
| GitLab (later) | `PLOEG_GITLAB_MERGE_TOKEN`, a token of a dedicated user or a project or group access token | `PLOEG_GITLAB_TOKEN` and the user `PLOEG_GITLAB_BOT` |

* **Start checks.** When a merge credential is set, ploegd reads who it is (Forgejo `GET /api/v1/user`) and refuses to start when the value equals any credential a Run can receive, or when its login equals the agents' identity or the login of ploegd's other forge credential. A deployment without a merge credential starts as today; the merge route then answers `422 capability_missing` (section 4).
* **Where it lives.** Only in ploegd's process, read by the composition root and handed to the forge adapter's merger. It is never in a claim, a Lease, `forgebroker`, the worker's environment or its proxy, a response, a log line, an event or a row. The worker's forge proxy keeps refusing every merge call. When [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md) is accepted, the merge credential is one more secret of the forge connection in that store.
* **Forge side.** Operators should allow merging into protected branches only to the merge identity (Forgejo branch protection `enable_merge_whitelist`, a GitHub ruleset that restricts updates with the merge App as its only bypass actor, GitLab "Allowed to merge"), so that even a token held by a harness cannot merge. `GET /api/v1/operator/connections` ([ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md)) reports per Work Target whether the agents' identity can merge into the base branch (`agentCanMerge`: `true`, `false` or `unknown`) where the forge lets Ploeg read the protection.

* **Unsafe protection refuses.** A merge identity is only worth its risk when it is the only non-person way to merge. When `agentCanMerge` is `true` for a Work Item's Work Target, the merge route answers `409 protection_unsafe` and reaches no forge: the operator first restricts merging on the base branch. `unknown` is allowed and logged at `WARN` on every merge, because some forges do not let Ploeg read their protection.
* **Least scope.** The merge identity has write access only to the repositories Ploeg's Work Targets name, and no administrator rights; the start check reads its permission on each registered Work Target (`GET /repos/{owner}/{repo}` `permissions`) and refuses to start when it is an administrator of any of them.

### Security controls on the route

Merging is the highest-impact write Ploeg makes, so the route carries controls no other operator route needs:

* **A fresh person.** `POST` needs a proven person whose authentication is at most 15 minutes old (`auth_time` of the ID token behind ADR-0171's proof; `PLOEG_MERGE_MAX_AUTH_AGE`, default `15m`). An older proof answers `401 reauthentication_required`, so a stolen long-lived session cannot merge.
* **A rate limit per consumer.** At most 20 merge requests per consumer per hour by default (`PLOEG_MERGE_RATE_LIMIT`); more answer `429` and write an audit row. Merging in bulk is not what this route is for.
* **A kill switch.** `PLOEG_MERGE_ENABLED=false` makes the route answer `503 merge_disabled` and drops nothing already merged; removing a consumer's `merge` permission takes effect on ploegd's next start.
* **Visible use.** Every transition increments `ploeg_pull_request_merges_total{state,reason,consumer}` and writes the audit row of section 3, so an operator can alert on a merge outside working hours or a burst of refusals.
* **No agent path.** A Run never reaches the route: Runs hold no operator token, ploeg-mcp's merge tools need the person's confirmation (ADR-0108), and the worker's forge proxy refuses every merge call to the forge.

### 2. API

New operator routes, versioned with the operator API (`docs/contracts/operator-api.v1.schema.json` and its Go types change together):

| Method and path | Effect |
| --- | --- |
| `POST /api/v1/operator/work-items/{id}/merge` | Ask Ploeg to merge the Work Item's pull request. Body `{ "headSha": "…", "whenChecksPass": true, "number": 87 }`. |
| `GET /api/v1/operator/work-items/{id}/merge` | The Work Item's merge requests, newest first, with their states. |
| `DELETE /api/v1/operator/work-items/{id}/merge` | Cancel the open merge request: Ploeg asks the forge to drop the scheduled merge. `204`, also when there was none, so a retry is safe. |

* `headSha` (required) is the full commit id the person reviewed. `whenChecksPass` (default `false`) lets the forge merge later, once its checks pass. `number` picks the pull request when the Work Item has more than one, as for keyed comments and statuses (ADR-0176). `reference` and `displayName` are optional and only shape attribution (section 6). Unknown fields are refused.
* **Who may call it.** The consumer needs the new permission **`merge`** in `PLOEG_OPERATOR_CONSUMERS` (default `false`, independent of `execute`; `OperatorPrincipal.CanMerge`), and its Team scope must include the Work Item's Team; otherwise the Work Item is `404 not_found`, as on every operator route. `POST` and `DELETE` need a **proven person** under every value of `PLOEG_OPERATOR_ATTRIBUTION`: an asserted `X-Ploeg-Actor` is refused with `403 person_token_required` even under `asserted`. Reading needs only the Team scope.
* **Which Work Items.** Only a Work Item in `awaiting_review` with an open pull request Ploeg recorded for it. Anything else is `409 not_awaiting_review` or `409 no_open_pull_request`. This keeps the existing settlement in force: `ReviewWatch` only settles `awaiting_review` items.
* **The head.** Ploeg reads the pull request's head from the forge (ADR-0059). If it is not `headSha`, the answer is `409 head_changed` with the current head, and nothing reaches the forge. Otherwise Ploeg sends `headSha` with the merge, so a push between Ploeg's read and the forge's merge makes the forge refuse.
* **Answers.** `202` with the merge request (section 3) when the forge took it, whether it merged at once or scheduled the merge. `200` with the existing request when a request for the same pull request and `headSha` is already `requested` or `scheduled` (`created: false`). `409 merge_pending` with the open request when one exists for another head: cancel it first. `422 capability_missing` when the Work Item's forge has no merger. A refusal by the forge answers `409 merge_refused` with the reason of section 3 and stores the request as `refused`. A forge that cannot be reached answers `503 forge_unavailable` and stores nothing. The route gets the 30-second timeout that `operatorAuth` already gives asks and execution credentials, not the default 5 seconds.
* **ploeg-mcp** gains `ploeg_merge_work` and `ploeg_cancel_merge`. Like every ploeg-mcp action they need the person's confirmation (ADR-0108); the confirmation text names the repository, the pull request number and title, the base branch, the short head commit, the repository's merge method, and whether the forge will wait for checks.

### 3. The merge request record

A new table, `pull_request_merges`, holds one row per request: the Work Item, forge, repository and pull request number, `head_sha`, `when_checks_pass`, the merge method the forge used, `state`, `reason`, the merge commit and `merged_by` the forge reported, the request's attribution (person, consumer, channel, proof) and timestamps. At most one row per pull request is `requested` or `scheduled` (a partial unique index).

| State | Meaning | Reached by |
| --- | --- | --- |
| `requested` | The forge took an immediate merge; Ploeg waits for the forge to report it | The merge call |
| `scheduled` | The forge will merge when its checks pass | The merge call with `whenChecksPass` while checks are not yet green |
| `merged` | The forge reports the pull request merged | A `pr_merged` forge event or `ReviewWatch.Reconcile`'s read, never the merge call's own answer |
| `refused` | The forge did not and will not merge this request | The merge call's answer, or a later forge fact (below) |
| `cancelled` | Nobody will merge this request | `DELETE`, or the pull request was closed without merging |

`reason` on `refused` is one of `head_changed`, `checks_failed`, `conflict`, `protection` (branch protection or a required review), `not_mergeable` (draft, already closed, or the forge's own answer), `forbidden` (the merge identity lacks permission) and, on GitHub only, `merge_method_ambiguous` (section 5).

After `scheduled`, Ploeg acts only on what the forge tells it and never merges by itself:

* A `pr_synchronized` event or a reconcile read showing a new head: Ploeg asks the forge to drop the scheduled merge and records `refused` with `head_changed`. A forge that already refuses a moved head (the head sent in section 2) makes this belt and braces.
* A `check_failed` event on the scheduled head that ADR-0119 treats as the change's failure: Ploeg drops the scheduled merge and records `refused` with `checks_failed`. A person asks again after the fix round.
* A `merge_state_dirty` event: `refused` with `conflict`, the schedule dropped.
* The pull request merged: `merged`, with `merged_by` and the merge commit from `PullRequestFacts`. When `merged_by` is not the merge identity (a person merged in the forge meanwhile), the row still records `merged`, with that login.

The Work Item finishes the way it already does: `ReviewWatch` settles it at `done` on the merge. Nothing about Work Item states changes.

Every transition writes one `audit_log` row on the Work Item, with the request's attribution, never a credential: `pull_request_merge.requested`, `.scheduled`, `.merged`, `.refused` (with `reason` and the forge's answer), `.cancelled`. Ploeg also posts one comment on the pull request when it takes a request ("Merge requested by `<displayName>`, or `<person>` without one, through `<consumer>` at `<short head>`; the forge merges when its checks pass"), so people in the forge see why the merge identity is about to merge.

### 4. The forge capability

A new optional capability of the Forge port, **`PullRequestMerger`**, named `pull_request_merger` by a new `provider.ForgeCapabilities` helper (the forge twin of `TrackerCapabilities`, ADR-0183):

```go
type PullRequestMerger interface {
    MergePullRequest(ctx context.Context, repo string, pr int, req MergeRequest) (MergeAnswer, error)
    CancelScheduledMerge(ctx context.Context, repo string, pr int) error
}
```

`MergeRequest` carries the head commit, whether to wait for checks and the commit message trailer. `MergeAnswer` says `merged`, `scheduled` or `refused` with a reason, and the method used. The adapter maps its forge onto it, Forgejo first:

* **Forgejo:** `POST /repos/{owner}/{repo}/pulls/{index}/merge` with `Do` set to the repository's `default_merge_style` (read from `GET /repos/{owner}/{repo}`), `head_commit_id` set to the head, and `merge_when_checks_succeed` set from `whenChecksPass`. `force_merge` is never sent. `DELETE /repos/{owner}/{repo}/pulls/{index}/merge` drops a scheduled merge.
* **GitHub (later):** with `whenChecksPass` and checks not yet green, GraphQL `enablePullRequestAutoMerge` with `expectedHeadOid`; otherwise `PUT /repos/{owner}/{repo}/pulls/{number}/merge` with `sha`. `disablePullRequestAutoMerge` cancels. The repository must allow auto-merge for the first.
* **GitLab (later):** `PUT /projects/:id/merge_requests/:iid/merge` with `sha` and `merge_when_pipeline_succeeds`; `POST …/cancel_merge_when_pipeline_succeeds` cancels.

An adapter without the capability makes the route answer `422 capability_missing` naming `pull_request_merger`. A future `forgetest` suite (ADR-0183 step 7) gains its cases: merged at once, scheduled, a moved head refused, a scheduled merge cancelled, and the method taken from the repository.

### 5. Merge method

Ploeg never chooses a merge style; it uses the repository's own. On Forgejo that is `default_merge_style`. On GitLab the method is a project setting and squash follows the project's `squash_option`. GitHub has no repository default, only a set of allowed methods: Ploeg uses the method when exactly one is allowed and otherwise refuses with `merge_method_ambiguous`, until a record decides otherwise.

### 6. Attribution in the forge

The forge shows the merge identity as the one who merged. The merge commit's message ends with the trailer:

```text
Merge-Requested-By: <person> (<consumer>)
Merge-Request: <merge request id>
Merge-Request-Ref: <reference>
```

Git history can never be rewritten on a shared branch, so it holds nothing that names a person:

* `<person>` is `ProvenPerson.ID`, the pseudonymous id ADR-0150 derives and the audit row records; `<consumer>` is the consumer's `name` from `PLOEG_OPERATOR_CONSUMERS`.
* `Merge-Request` is the `pull_request_merges` row's id, so anyone who may read Ploeg's audit resolves who asked, when and through which channel.
* `Merge-Request-Ref` is optional: an `https` URL the consumer passes as `reference` (at most 2048 characters, no query string, no user info), pointing at its own record of the decision, which resolves for people the consumer lets in and stops resolving when the consumer erases the record.

The readable name goes where it can be erased. The consumer may pass `displayName` (at most 100 characters); Ploeg uses it only in the pull request comment of section 3 and never stores it, logs it or writes it to git. A forge comment can be edited or deleted on an erasure request; a commit cannot. Ploeg sends the forge's default title and, as the message body, the pull request's title and description followed by the trailers. A method that creates no new commit message (Forgejo `rebase` and `fast-forward-only`, GitLab fast-forward) carries no trailer; the audit row and the pull request comment of section 3 then hold the attribution.

### 7. Out of scope

* **Automatic merge policies**, where Ploeg merges without a person's request (for example "merge every Renovate pull request whose checks pass"). Not decided; see *Re-evaluation triggers*.
* Choosing or overriding a merge style, deleting the head branch after the merge, and merge queues or merge trains.
* What a consumer shows a person before it calls the route, and how it decides who may ask.
* Merging a pull request that is not a Work Item's (for example one ADR-0109 reads).

### Consequences

* Good, because a person can have a reviewed pull request merged from any operator console, and the consumer holds no forge credentials.
* Good, because the identity that merges is one no Run can reach, and with the forge-side restriction of section 1 even a harness holding its own token cannot merge.
* Good, because the forge enforces the head and its own protection: a push after the request, a failed check or a missing review stops the merge, and Ploeg has no "merge when green" loop that could disagree with the forge.
* Good, because the Work Item settles through the existing `ReviewWatch` path, and "merged" stays a forge fact.
* Bad, because "Ploeg never merges" stops being true. ADR-0086's *Not supported* line, `docs/security/self-assessment.md`, the threat model's T-HN-3 row and "Merge through the API — closed", and [`docs/how-to/connect-github-as-a-forge.md`](../how-to/connect-github-as-a-forge.md) must change when this is built, to "Ploeg merges only when a proven person asks, with an identity no Run holds". The threat model gains a row for a stolen consumer credential plus a person token asking for a merge.
* Bad, because operators provision a second forge identity per forge, and on GitHub a second App.
* Neutral: the commit names the person only by a pseudonymous id and a reference, so a reader of the forge follows the reference or reads the pull request comment, which carries the readable name the consumer passed. Accepted: a name written into a merge commit can never be erased from history, while a comment can, and the id stops identifying anyone once its mapping is deleted.
* Bad, because the security controls (fresh authentication, rate limit, unsafe-protection refusal) add refusals a person has to understand; each answers with its own code and the consumer explains it.
* Bad, because a scheduled merge on GitHub depends on the repository allowing auto-merge, and GitHub repositories that allow several methods are refused until a method rule is decided.
* Neutral, because a consumer with `merge` can merge any `awaiting_review` pull request in its Teams that the forge's rules allow. It is a real grant; operators give it to the consumer that serves people who decide merges, and branch protection stays the final word.

### Confirmation

Proposed, none of these exist yet. Once built, `mise exec -- go test ./...` in `.forgejo/workflows/on_pull_request.yml` runs them:

* `pkg/httpapi` tests with an `httptest` Forgejo fake: a merge at the forge's head answers `202` and sends `head_commit_id`, `merge_when_checks_succeed` and the repository's `default_merge_style`, and never `force_merge`; a stale `headSha` answers `409 head_changed` and calls no forge; a repeat answers `200` with one row; a consumer without `merge` or without the Team is refused; an asserted actor is refused with `person_token_required` under `asserted` and `proven`; a Work Item not in `awaiting_review` answers `409`; a forge without `PullRequestMerger` answers `422 capability_missing`; `DELETE` twice answers `204` twice and calls the forge's cancel once.
* `pkg/shiftengine` tests that a `pr_merged` event moves a `scheduled` request to `merged` and settles the Work Item as today, and that `pr_synchronized`, `check_failed` and `merge_state_dirty` events drop a scheduled merge and record `refused` with `head_changed`, `checks_failed` and `conflict`.
* A `cmd/ploegd` start test that a merge credential equal to `PLOEG_FORGEJO_TOKEN`, or whose login is `PLOEG_FORGEJO_BOT`, stops ploegd.
* A redaction test that no claim, Lease, Task Spec, worker environment, response, log line, event or row contains the merge credential, and a worker proxy test that every merge path stays `403`.
* The commit message test: the trailers are `Merge-Requested-By: <person id> (<consumer name>)`, `Merge-Request: <id>` and, when given, `Merge-Request-Ref: <reference>`; a `displayName` never appears in the commit, a log line or a row.
* Security tests: `409 protection_unsafe` when `agentCanMerge` is `true`; `401 reauthentication_required` for a proof older than `PLOEG_MERGE_MAX_AUTH_AGE`; `429` past the rate limit; `503 merge_disabled` under the kill switch; ploegd refuses to start with a merge identity that is an administrator of a registered repository.
* `go test ./internal/ledger/` keeps this record well-formed, and `go test ./internal/boundary/` keeps it free of consumer names.

## Pros and Cons of the Options

### The Operator Consumer merges with forge credentials of its own

* Good, because Ploeg changes nothing and its "never merges" line holds.
* Bad, because every consumer needs an identity, permissions and rate limits on every forge, the duplication ADR-0079 and ADR-0176 avoided.
* Bad, because the consumer reads the head itself and may merge a commit other than the one Ploeg gated on.

### Ploeg merges with the forge credential it already holds

* Good, because no new identity is needed.
* Bad, because that credential is the agents' identity, and without per-Run minting it is the very token every worker pushes with. Any agent that reaches the forge with it, past the proxy, could merge its own work. That is the one thing the owner ruled out.

### Ploeg waits for CI itself and merges when the head is green

* Good, because it works on a forge without native auto-merge.
* Bad, because Ploeg's view of "green" (ADR-0119, which leaves out the statuses ADR-0176 writes) differs from branch protection's, so the two can disagree, and a polling merge loop is a second engine deciding when code lands.
* Bad, because a crash between "green" and "merge" needs its own recovery, which the forge's own scheduler already has.

### A forge merge queue or merge train

* Good, because the forge serialises merges and tests each against the latest base.
* Bad, because only GitHub and GitLab have one, each configured per repository, and Forgejo has none. It can be a later method of the same capability.

## Re-evaluation triggers

* The owner decides an automatic merge policy, where Ploeg merges without a person's request. That needs its own record and reopens section 1's "nothing but merging on request".
* A record answers ADR-0171's machine-attribution trigger, so a workload could ask for a merge.
* Forgejo changes `merge_when_checks_succeed`, `head_commit_id` or the cancel route, or ships a merge queue.
* The GitHub or GitLab adapter gains `PullRequestMerger`, or a GitHub repository that allows several methods is refused with `merge_method_ambiguous` in practice.
* ADR-0184 is accepted or rejected, which decides where the merge credential lives.
* A merge identity's credential is found in a Run, a log or a response, or an agent identity is found able to merge in `/connections`.
* A person needs the commit itself to carry a readable name, not a person id and a reference.

## More Information

* Amends, when accepted and built, the "Ploeg never merges" line of [ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md) and the security documents listed under *Consequences*.
* Siblings: [ADR-0176](0176-an-operator-consumer-sets-a-keyed-commit-status-on-a-pull-requests-head-through-ploegs-forge-credentials.md) (`headSha`, `head_changed` and `number` follow it) and [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) (keyed comments).
* Related: [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md), [ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md), [ADR-0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md), [ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md), [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md), [ADR-0172](0172-the-forgejo-broker-runs-only-on-forgejo-15-and-refuses-a-token-the-forge-did-not-restrict-to-the-runs-repository.md), [ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md), [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md).
* Forge APIs: [Forgejo API reference](https://forgejo.org/docs/latest/user/api-usage/) (`MergePullRequestOption`), [GitHub `enablePullRequestAutoMerge`](https://docs.github.com/en/graphql/reference/mutations#enablepullrequestautomerge), [GitHub merge a pull request](https://docs.github.com/en/rest/pulls/pulls#merge-a-pull-request), [GitLab merge a merge request](https://docs.gitlab.com/api/merge_requests/#merge-a-merge-request).
* Open questions: whether Ploeg should reproduce each forge's default merge message exactly instead of the pull request's title and description; whether `whenChecksPass` should also be allowed while ADR-0119 still holds the Shift on pending CI, which today keeps the Work Item out of `awaiting_review`.
