# How Ploeg can learn that a pull request conflicts

Checked on 2026-09-30 against Forgejo `v13.0/forgejo`, `v12.0/forgejo` and `forgejo` (main at d1d5f34), and against forgejo.webgrip.dev, which runs 15.0.2. Decision: [ADR-0040](../adrs/0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md).

## The question

The owner asked, after pull request #48 stopped being mergeable: when a pull request gets merge conflicts, a ticket should be filed with priority, and Glide should pick it up by itself. Where does that signal come from, and who can push the fix?

## What happened on 2026-09-30

At `development @ 57f9a49`, 5 of 17 open pull requests reported `mergeable: false`:

| PR | Author | Head | What conflicts |
| --- | --- | --- | --- |
| #10 | ryangr0 | branch `docs/registries-network-profile` | not inspected |
| #11 | ryangr0 | AGit `refs/pull/11/head` | adds `0036-a-repo-label-selects-among-registered-targets…`, whose decision landed as the accepted ADR-0038: superseded rather than conflicted |
| #45 | agent-builder | branch `agent/vik-1305` | `docs/how-to/review-an-agent-pr.md`: two agent branches edited one page |
| #48 | ryangr0 | AGit `refs/pull/48/head` | `apps/ploeg/docs/adrs/README.md` and the generated `docs/reference/decisions.md`; its new record is numbered 0039, which `development` has since used for another record |
| #49 | ryangr0 | AGit `refs/pull/49/head` | eleven Vloer source, test and doc files after the redesign (#51) merged |

`git merge-tree --write-tree origin/development <head>` lists the conflicting files without a checkout. Three observations shape the design:

* Most conflicts are in indexes and generated files that every docs pull request touches. Resolving them means regenerating, not hand-merging.
* A clean textual merge is not enough. #48 also needs its record renumbered, which only `go test ./internal/ledger/` catches.
* Some conflicted pull requests should be closed, not fixed (#11).

## What Forgejo signals

**No webhook announces a conflict.** When a branch receives a push, `TestPullRequest` finds the open pull requests that target it, updates their ahead and behind counts, and queues a mergeability check (`AddToTaskQueue`). The check writes `status` and `conflicted_files` to the database and calls no notifier. The notifier interface has no event for a change in mergeability. The only webhook is the ordinary `push` on the base branch. Sources: [`services/pull/pull.go`](https://codeberg.org/forgejo/forgejo/src/branch/v13.0/forgejo/services/pull/pull.go), [`services/pull/check.go`](https://codeberg.org/forgejo/forgejo/src/branch/v13.0/forgejo/services/pull/check.go), [`services/notify/notifier.go`](https://codeberg.org/forgejo/forgejo/src/branch/forgejo/services/notify/notifier.go).

**`mergeable: false` has four meanings.** `(*PullRequest).Mergeable` is false for status `CHECKING`, `CONFLICT` and `ERROR`, and for a title with a work-in-progress prefix ([`models/issues/pull.go`](https://codeberg.org/forgejo/forgejo/src/branch/v13.0/forgejo/models/issues/pull.go)). The API exposes neither the raw status nor `conflicted_files`, only `mergeable`, `draft` and `merge_base` ([`modules/structs/pull.go`](https://codeberg.org/forgejo/forgejo/src/branch/v13.0/forgejo/modules/structs/pull.go)).

**The existing webhook branch is mostly noise.** `pull_request_sync` webhooks are sent right after the check is queued, while the status is still `CHECKING`, so their payload usually says `mergeable: false`. `ParseWebhook` in `pkg/provider/forgejo/forgejo.go` turns that into `merge_state_dirty`, which Ploeg records and never acts on.

**Polling works.** Every entry of `GET /repos/{owner}/{repo}/pulls?state=open` carries `mergeable`, read from the stored status without triggering a recheck ([`routers/api/v1/repo/pull.go`](https://codeberg.org/forgejo/forgejo/src/branch/v13.0/forgejo/routers/api/v1/repo/pull.go)). Right after a base push, the stored value can still read the old `MERGEABLE` for a moment, and then `CHECKING`. A conflict shows once the check finishes, so a pull request that reads `false` on two polls in a row, with the same head, is not in `CHECKING`.

## Who can push the fix

**Same-repository branch:** the per-Run forge token is scoped to the repository, not the branch ([`pkg/forgebroker/broker.go`](../../pkg/forgebroker/broker.go)), and a Follow-Up already works on its source's branch (`work.Branch`). A merge commit pushed there updates the pull request in place.

**AGit pull request:** a push to `refs/for/<base>/<topic>` gets the pusher's lowercased user name as a prefix on the head branch, so a different user pushing the same topic opens a new pull request ([`services/agit/agit.go`](https://codeberg.org/forgejo/forgejo/src/branch/v13.0/forgejo/services/agit/agit.go)). Ploeg's bot cannot update #48. Forgejo main (v17 and later) lets a user with code write access push to `refs/pull/N/head` to update an AGit pull request ([`routers/private/hook_pre_receive.go`](https://codeberg.org/forgejo/forgejo/src/branch/forgejo/routers/private/hook_pre_receive.go), `preReceivePull`). Versions 12 to 16 lack that route.

**Fork:** a fork's branch is outside the repository the token is scoped to.

## What already exists in Ploeg

* Priority reaches the queue. The Vikunja priority is copied onto the Work Item ([`pkg/provider/vikunja/vikunja.go`](../../pkg/provider/vikunja/vikunja.go)), and `Claim` orders by `priority DESC, created_at` ([`pkg/store/store.go`](../../pkg/store/store.go)).
* Assignment dispatches. A tracker item assigned to one of a Team's `assignees` becomes a queued Work Item ([tracker execution](../contracts/tracker-execution.md)).
* Repair Follow-Ups (`forgeFollowUps.repairFailedChecks`) act only on branches a Ploeg Shift worked (`store.FindBranchOwner`), and on Forgejo they never fire, because Forgejo 15 has no commit-status webhook (VIK-1280).
* No tracker provider can create a task. Vikunja can comment and set done.
* Ploeg writes to Vikunja with the owner's own account until Glide gets its own user ([ADR-0036](../adrs/0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md), owner decision 5).
