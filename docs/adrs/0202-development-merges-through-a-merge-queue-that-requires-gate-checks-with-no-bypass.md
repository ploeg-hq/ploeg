---
status: accepted
date: 2026-11-19
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-19
---

# `development` merges through a merge queue that requires gate checks, with no bypass

## Context and Problem Statement

The `Protect development` ruleset requires `verify`, `dco`, `govulncheck`, `analyze`, `cve-gate-self-test` and `cve-budget` for each image, on the pull request's own head. Two pull requests that each pass against an older `development` can still break the trunk together; two that each add the next migration number collide only after both merged, and the release candidate the second merge cuts carries both. The ruleset also lets a repository administrator bypass it, so a merge can land with no check at all. CI and both drills run again on every push to `development`, after the fact, when the only remedy is a revert.

How does a pull request reach `development` so that the commit that lands is the commit that was checked, and the ruleset does not have to list every matrix leg?

## Decision Drivers

* The commit that lands on `development` is the one the checks passed, combined with the trunk it lands on.
* Nobody, administrators included, merges past the checks.
* A job added to a workflow or a new image in the catalogue never needs a ruleset change.
* Two migrations never share a number, and an added migration never sorts below one `development` already applied.
* `dco` checks the commits a pull request adds, which a merge group does not name.

## Considered Options

* A merge queue on `development` with one gate check per workflow, and no bypass
* Require branches to be up to date before merging, with no bypass
* Keep the current ruleset and checks

## Decision Outcome

Chosen option: "A merge queue on `development` with one gate check per workflow, and no bypass", because it checks the combined commit without making every author rebase and rerun after each merge, and the gate jobs keep the ruleset's list short and stable.

1. **The queue.** The `Protect development` ruleset requires a merge queue: merge method `MERGE`, grouping strategy `ALLGREEN`, at most 3 entries built at once. The workflows whose checks it requires run on `merge_group`: [ci.yml](../../.github/workflows/ci.yml), [image-security.yml](../../.github/workflows/image-security.yml), [codeql.yml](../../.github/workflows/codeql.yml), [upgrade-drill.yml](../../.github/workflows/upgrade-drill.yml) and [restore-drill.yml](../../.github/workflows/restore-drill.yml). A check the queue requires must run on `merge_group`, or the queue waits for it forever.
2. **Gate checks.** `ci-gate` in `ci.yml` waits for `verify`, `dco`, `contract-bundle-reproducible`, every `worker-admission` leg and `hostile-repository`, runs even when one failed, and passes only when each succeeded or was skipped by its own condition. `dco` runs on pull requests only, so it is skipped in the queue; its result on the pull request still blocks entry. `image-security-gate` in `image-security.yml` waits for `catalogue`, every `cve-budget` and `arm64-build` leg, `cve-gate-self-test` and `govulncheck`, and passes only when each succeeded.
3. **Required checks.** The ruleset requires `ci-gate`, `image-security-gate`, `analyze`, `Restore an older release's backup, upgrade it and interrupt the upgrade` and `Back up, destroy, restore and reconcile`, from GitHub Actions. The two drills lose their `paths:` filter on pull requests so they report on every pull request and queue entry. The attack drill and the quickstart stay advisory and are not required.
4. **No bypass.** The ruleset's bypass list is empty. An administrator merges through the queue like everyone else.
5. **No second run after the merge.** `ci.yml` and both drills no longer run on a push to `development`: the queue already ran them on the commit that lands. They still run on `main`, on schedule and by hand. Only a pull request run cancels an older one of the same ref; a queue run is never cancelled by another.
6. **Migration order.** `verify` runs [scripts/check-migration-order.sh](../../scripts/check-migration-order.sh) against `github.event.merge_group.base_sha` in the queue and `github.event.pull_request.base.sha` on a pull request. It refuses an added migration under `pkg/store/migrations` whose number is at or below the base branch's highest, and two migrations with one number above `0036`, the one number the history already repeats.

The repository settings are applied by the owner; this record and the workflows are what the repository can carry.

### Consequences

* Good, because `development` only receives commits whose combination with the trunk passed every required check, so a release candidate is never cut from a combination nobody tested.
* Good, because the ruleset names five checks that do not change when a job, a Kubernetes leg or an image is added.
* Good, because a migration-number collision fails in the queue instead of after the merge.
* Bad, because every merge runs CI, image security, CodeQL and both drills a second time, which adds one queue run to the time to merge and roughly doubles the Actions minutes per merged pull request.
* Bad, because the drills now run on every pull request, not only on store, API, Shift engine and worker changes.
* Bad, because nobody can merge past a broken or stuck required check; an outage of one blocks every merge until it is fixed or the ruleset is changed.
* Bad, because the embedded PostgreSQL cache, saved only on `development`, is now saved only by the release gates of a candidate, not by every CI push.

### Confirmation

* `bash scripts/test-check-migration-order.sh`, run by `mise run verify`, fails when an added migration at or below the base's highest number, a repeated number above `0036`, or a base that is not a commit passes the check.
* The `ci-gate` and `image-security-gate` jobs print the `needs` they read and fail on any result outside their allowed set; a failed `verify` leaves `ci-gate` red.
* The ruleset is confirmed by reading it: `gh api repos/ploeg-hq/ploeg/rulesets` shows `Protect development` with a `merge_queue` rule (`MERGE`, `ALLGREEN`, 3 to build), the five required checks above and no bypass actors.

## Pros and Cons of the Options

### Require branches to be up to date, with no bypass

* Good, because it needs no workflow change.
* Bad, because each merge makes every other open pull request stale, so authors, Renovate and agent Runs rebase and wait for a full rerun in turn.

### Keep the current ruleset and checks

* Good, because each pull request runs its checks once.
* Bad, because two green pull requests can still break `development` or collide on a migration number, every new image needs a ruleset edit, and an administrator can merge past the checks.

## Re-evaluation triggers

* The queue's median wait exceeds one hour over a month of merges.
* GitHub lets a required check be satisfied by a pull request run inside a merge queue, which would let `dco` be required directly.
* A required check is found that does not run on `merge_group` and holds the queue.
* A second repository consumes `ploeg-hq/ploeg` through a pin that must land faster than one queue run.

## More Information

* Amends the branch rule of [0062](0062-github-is-the-independent-project-home.md) for `development`; `main` keeps its ruleset.
* [Release runbook](../ops/release-runbook.md) lists the rulesets as the repository holds them.
* 2026-11-19 — Accepted by the owner: the merge queue settings and removing the administrator bypass. The workflow side lands with this record; the owner applies the repository settings afterwards.
