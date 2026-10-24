---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A request for changes sends work back only from a named or writing reviewer, and only up to a cap

## Context and Problem Statement

With `forgeFollowUps.reworkOnChangesRequested` on, a "changes requested" review on a Team's pull request queues its Work Item again with its attempts reset and a new Shift with a fresh pool ([ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md)). Any review author other than Ploeg's own bot could do this, as often as they liked. On a public repository anyone can review, so anyone could spend a Team's money without limit and put text into a writer's prompt. The threat model records this as [F-36](../security/threat-model.md#findings). Who may send work back, and how often?

## Decision Drivers

* A stranger's review must not cost money or reach a Run's prompt.
* People who maintain the repository must keep sending work back without extra setup.
* Spend must stay bounded even when every reviewer is trusted.
* A refused or capped review must be visible to a person, never silently dropped.
* The forges differ in what they can report about a login.

## Considered Options

* A named list, plus everyone the forge reports with write access, and a per-Work-Item cap
* A named list only
* The webhook's author association (GitHub's `author_association`)
* A cap only

## Decision Outcome

Chosen option: "A named list, plus everyone the forge reports with write access, and a per-Work-Item cap", because it keeps maintainers working without configuration and refuses everyone else.

* A request for changes is acted on only when its reviewer is listed in the Team's `forgeFollowUps.reworkReviewers` (case ignored) or the forge reports that login with write access or more to the repository: `write`, `admin` or `owner` on Forgejo, `write` or `admin` (maintain included) on GitHub, Developer or more on GitLab.
* A reviewer who is not listed is refused when the forge says they cannot write, and also when the forge cannot say (no permission read, an error, a token not allowed to read permissions). Ploeg fails closed.
* A refused review is not stored, so its body never reaches a Run. ploegd audits `review.rework_refused` with the reviewer and the reason (`reviewer_not_allowed` or `permission_unknown`) and logs a warning. The audit row is quiet on a running Run's board.
* `forgeFollowUps.maxReworks` caps how many times requests for changes queue one Work Item again, default 3. `work_items.review_requeues` counts the re-queues, both from a review on a Work Item awaiting review and from a review left pending when a Shift closes. A Work Item at the cap keeps its state and its attempts, the review is stored for the next Shift a person starts, and ploegd audits the request with action `capped` (or `review.rework_capped` when a Shift closes over a pending review).

### Consequences

* Good, because an outsider's review on a public repository costs nothing and writes nothing a Run reads.
* Good, because the default needs no new configuration where maintainers review.
* Good, because each Work Item can spend at most `maxReworks` extra Shift pools on review.
* Bad, because a forge token that cannot read permissions refuses every unlisted reviewer. On Forgejo the permission endpoint answers only a repository administrator or the login itself, so a bot with write access only must list its reviewers in `reworkReviewers`. The audit reason `permission_unknown` names this case.
* Bad, because a deployment that relied on any reviewer sending work back now refuses outsiders. That is the fix, not a regression.
* Bad, because the counter never resets. A person who wants more rework after the cap starts the next Shift themselves.

### Confirmation

`go test ./pkg/httpapi/ -run ChangesRequested` states each case: a reviewer without write access is refused and audited, a reviewer the forge cannot judge is refused, a listed reviewer and a forge writer both re-queue, and a request past the cap leaves the state, attempts and Shift unchanged. `go test ./pkg/shiftengine/ -run HumanReview_AShiftClosingAtTheReworkCap` covers the pending-review path. `go test ./pkg/provider/...` checks each forge's permission read. All run in the pull request CI's `go test ./...`.

## Pros and Cons of the Options

### A named list only

* Good, because it needs no forge read and works the same on every forge.
* Bad, because every deployment that uses rework today would have to list its maintainers before it works again.

### The webhook's author association

* Good, because it costs no API call.
* Bad, because only GitHub sends it, and `MEMBER` and `COLLABORATOR` do not mean write access: an organisation member or a read-only collaborator would pass.

### A cap only

* Good, because it bounds spend.
* Bad, because a stranger could still spend up to the cap on every pull request and put text into a writer's prompt.

## Re-evaluation triggers

* Forgejo lets a token with write access read other logins' repository permissions.
* An audit shows `permission_unknown` refusals for a maintainer more than five times in a month.
* A Team asks for a different cap per reviewer, or for the counter to reset when a person requeues.
* A forge Ploeg supports gains reviews from apps or bots that Ploeg must tell apart from people.

## More Information

* Threat model: [F-36 and T-PD-12](../security/threat-model.md)
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md): the review loop and its caps
* Issue: [#227](https://github.com/ploeg-hq/ploeg/issues/227)
