---
status: proposed
date: 2026-09-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-12-31
---

# A conflicted pull request becomes a priority ticket that Ploeg resolves

## Context and Problem Statement

On 2026-09-30 pull request #48 stopped being mergeable, and the owner asked:
when a pull request gets merge conflicts, a ticket should be filed with
priority, and Unfold should pick it up by itself. That day 5 of 17 open pull
requests on webgrip/glide conflicted: three AGit pull requests from the owner's
sessions, one on a person's branch, and one agent branch that collided with
another agent branch ([evidence](../research/2026-09-30-merge-conflict-signals.md)).

Ploeg does not act on conflicts today. Forgejo sends no webhook when a base
branch moves under a pull request. The `merge_state_dirty` event that
`ParseWebhook` records comes from `pull_request_sync` payloads, which report
`mergeable: false` while the check is still running. `forgeFollowUps` acts only
on branches a Ploeg Shift worked. Ploeg's bot cannot push to an AGit pull
request. No tracker provider can create a task.

How does a conflict become work that Ploeg picks up, and who may push the fix?

## Decision Drivers

* The owner's words: a ticket, with priority, picked up automatically.
* Priorities live on the tracker (`apps/ploeg/AGENTS.md`), and the tracker is
  where the owner looks.
* The fix must reach the pull request the owner will review, including AGit
  pull requests the bot cannot update.
* A conflict fix must never destroy a person's commits or change what their
  pull request does.
* Every automatic source of work needs a stop: a cap per pull request, a cap on
  open tickets, and an off switch.
* What a Run works on comes from Ploeg's own records, never from text a person
  or an agent can edit (ADR-0030).

## Considered Options

* **Poll the forge, file a priority ticket on the routed board, assign it to a
  configured Team, and bind the ticket to the pull request from Ploeg's own
  record**
* A repair Follow-Up inside Ploeg, like `repairFailedChecks`, with no ticket
* A Forgejo Actions job on each push to the base branch that files the ticket
* Act on the `merge_state_dirty` webhook

## Decision Outcome

Chosen option: the first, because it gives the owner the ticket they asked for,
reuses the whole assignment path for dispatch and priority, works for every
repository Ploeg routes rather than one CI configuration, and keeps the
binding between ticket and pull request in Ploeg's hands.

### Owner decisions (2026-09-30)

1. A conflict on any open pull request in a routed repository gets a ticket,
   including the owner's own pull requests. #48 is the motivating case.
2. The ticket has priority and is picked up without a person assigning it.
3. For an AGit pull request, Ploeg opens a superseding pull request, and the
   owner closes the original.
4. Unfold builds this itself, from tickets on the Unfold board.
5. (2026-10-01, after agent pull request #45 conflicted and Vloer showed it as
   ready for review) A conflict must be clear inside Vloer. This holds on every
   route, whether or not it opts in to conflict tickets.

The values below (priority 5, two polls, the caps) are this record's defaults,
not owner decisions.

### Detect

A route opts in with a `conflicts` block. A route without one does nothing new.

```yaml
trackers:
  vikunja:
    projects:
      - name: Unfold
        id: "10"
        repo: webgrip/glide
        branch: development
        conflicts:
          team: bronze          # its first tracker assignee receives the ticket
          priority: 5           # Vikunja "DO NOW"
          labels: [repo/glide, theme/ploeg, ready, agent-ready]
          maxOpen: 3            # open conflict tickets on this route at once
          maxPerPullRequest: 3  # conflict tickets one pull request can ever get
          ignoreAuthors: [renovate]
```

Label names resolve at boot, like project names, and ploegd refuses to start if
one is missing. It never creates a label.

On every review reconcile (`PLOEG_REVIEW_RECONCILE_INTERVAL`, default
10 minutes), ploegd lists the open pull requests against the route's base
branch. A pull request is a conflict when it reads `mergeable: false` on two
polls in a row with the same head SHA, is not a draft, and its author is not in
`ignoreAuthors`. Renovate is ignored because it rebases its own pull requests.
A pull request whose branch belongs to a Work Item that is queued or has a live
Shift is skipped: that Shift is working the branch. The webhook's
`merge_state_dirty` stays recorded and triggers nothing.

### File the ticket

ploegd stores the conflict (repository, pull request, head SHA, head kind), then
creates a task on the route's board: the title
`<repo>: resolve merge conflicts in pull request #<n>`, the configured priority
and labels, and a body that links the pull request, names its head and base,
and says that unassigning the ticket stops the work. It then assigns the Team's
first tracker assignee. From here the existing intake takes over: the
assignment webhook creates the Work Item, its priority comes from the ticket,
and `Claim` takes it ahead of lower-priority work.

Creating a task is an optional provider capability, not a new method on
`TrackerProvider`. A route whose tracker lacks it fails at boot if it sets
`conflicts`. Vikunja gets it first.

At most one conflict ticket per pull request is open at a time. When a route
has `maxOpen` open conflict tickets, ploegd files no more, and the oldest
conflicted pull request is filed first once one closes. When a pull request
has had `maxPerPullRequest` tickets, ploegd comments on it once that it keeps
conflicting and files nothing more. If the pull request is merged, closed or
mergeable again before a Run starts, ploegd withdraws the Work Item and closes
the ticket with a comment saying why.

### Bind

At intake, ploegd looks up the task in its conflict records. A task Ploeg filed
gets its pull request, branch and mode from that record. A task a person wrote
that mentions a pull request is ordinary work on a new branch. Editing a
conflict ticket's text does not change what the Run works on.

| Head of the pull request | Mode | Branch the Run pushes |
| --- | --- | --- |
| A branch in the same repository | in place | the pull request's own branch, so the pull request updates |
| AGit (`refs/pull/<n>/head`) or a fork | superseding | `agent/vik-<ticket>`, started from the pull request's head |

An in-place Work Item does not make Ploeg the branch's owner.
`FindBranchOwner` ignores it, so a failed check or a review on a person's pull
request still sends nothing to an agent. A superseding pull request is an
ordinary Ploeg pull request from then on.

### Resolve

The Run's brief, built by ploegd like `work.RepairBrief`, tells the writer to:

1. Check first with `git merge-tree --write-tree`. If the branch merges
   cleanly, report `no_change_needed`.
2. If the base already contains what the pull request changes, report `stuck`
   and say that the pull request looks superseded. Push nothing.
3. Otherwise, merge the base into the branch. Never rebase, and never
   force-push.
4. Resolve each conflict so that both sides' intent survives. Regenerate
   generated files with the repository's own generator instead of merging them
   by hand. Renumber a record or migration whose number the base has since
   used.
5. Run the repository's gates, then push the merge commit. If the push is
   rejected because someone pushed meanwhile, fetch, merge again and retry
   once. If it is rejected again, report `stuck`.
6. When two sides change the same behaviour differently, stop and report
   `stuck` with both versions quoted. Choosing between them is a product
   decision (ADR-0036).

In superseding mode the writer then opens a pull request whose body starts
with `Supersedes #<n>`, and ploegd comments on the original with a link to it.
ploegd records the original as superseded and files no more tickets for it.

### Settle

* **In place:** once the pull request reads mergeable on two polls after the
  Run's push, the Work Item is `done` and ploegd completes the ticket with a
  comment. The pull request stays the person's to review and merge.
* **Superseding:** the Work Item follows the normal path, `awaiting_review`
  until the new pull request merges.
* **`no_change_needed`:** the ticket is completed with a comment saying
  that no conflict was found.
* **`stuck` or `failed`:** the Work Item goes to `needs_human` as it does today.
  The stuck reason is posted on the pull request and on the ticket.

### Show

The review reconcile already reads every `awaiting_review` pull request. It
also reads the pull request's `mergeable` flag, head SHA and base branch, from
the same single request, through an optional forge capability. It records the
Work Item's merge state with the two-poll rule from Detect: `conflicted` after
two `false` polls on one head SHA, `clean` on `true`, and `unknown` otherwise
or after a new push. A forge read failure leaves the recorded state as it was.
Entering or leaving `conflicted` writes an audit row with the Work Item's id.

The operator API adds `pullRequest` (number, merge state, base branch, head
SHA, checked at) to each Work Item. Vloer flags `conflicted` in attention tone
on the review lane chip, the Now row and the first line of the "Before you
merge" checklist, and notifies once per head SHA. `unknown` and a missing field
never read as clean.

Showing a conflict changes no Work Item state: the item stays
`awaiting_review`, because the pull request is still the person's to review.
It needs no `conflicts` block. Detect reuses the same two-poll rule for the
other open pull requests on routes that opt in.

### Relation to other records

* [ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md):
  this answers its first open question for one source of work, a conflict the
  forge reports, with "written to the tracker and dispatched without approval".
  Work that Runs create is unchanged and still waits for approval. Conflict
  resolution is one narrow, recurring kind of work with a fixed brief, capped
  per pull request and per route, and its result is still a pull request a
  person reviews. That is why it may skip the approval step.
* [ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md):
  Ploeg now creates tracker items. Until Unfold has its own Vikunja user (owner
  decision 5 there), the tickets are written by the account whose token ploegd
  holds, which is the owner's. Conflict tickets carry no decisions, so they are
  never grounds.
* [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) and
  [ADR-0013](0013-push-rights-are-minted-per-run.md): the Lease and the per-Run
  token still govern Ploeg's writers. A person pushing to their own branch
  meanwhile is handled by merge commits and a fast-forward-only push.

### Consequences

* Good, because the owner sees every conflict on the board, with priority, and
  can stop one by unassigning it.
* Good, because detection, ticket, dispatch and review reuse the reconcile loop,
  assignment intake, `Claim` ordering and the pull request as blackboard
  (ADR-0011).
* Good, because a Run's binding to a pull request comes only from Ploeg's own
  record.
* Bad, because a conflict is found up to two reconcile intervals late, about
  20 minutes by default. Accepted: nobody reviews a pull request in that window,
  and Forgejo offers no event.
* Bad, because the superseding mode leaves the original pull request and its
  review thread behind. Accepted until Forgejo v17 (re-evaluation triggers).
* Bad, because every conflict now costs model spend. `maxOpen` × the Team's
  Shift pool bounds it per route, and `maxPerPullRequest` per pull request.
* Bad, because ploegd's tracker token needs rights to create tasks, set labels
  and assign users. That is a homelab-cluster change.

### Confirmation

This record is proposed, and nothing has been built yet. When the tickets land,
`go test ./...` in `.forgejo/workflows/on_pull_request.yml` is to cover:

* `pkg/provider/forgejo`: listing open pull requests against a recorded
  Forgejo 15 response, including an AGit pull request and a draft.
* The conflict watch: one `false` poll files nothing; two with the same head
  SHA file one ticket; `maxOpen`, `maxPerPullRequest` and `ignoreAuthors` hold;
  a merged or closed pull request withdraws its queued Work Item.
* `pkg/provider/vikunja`: task creation, labels and assignment against
  `httptest`, using PUT where Vikunja creates.
* Intake: a task Ploeg filed binds to its record; a person's task that
  mentions a pull request does not; an in-place Work Item is invisible to
  `FindBranchOwner`.
* `pkg/work`: a golden file for the conflict brief.
* Show: one `false` poll leaves an `awaiting_review` Work Item `unknown`; two on
  one head SHA make it `conflicted` and write one audit row; a read failure
  changes nothing; the operator API returns `pullRequest`. Vloer's `npm test`
  covers each merge state on the chip, the Now row and the checklist.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### A repair Follow-Up inside Ploeg, with no ticket

* Good, because `CreateRepairFollowUp` already exists, and no tracker writes
  are needed.
* Bad, because the owner asked for a ticket, and the work would be invisible on
  the board where priorities live.
* Bad, because it covers only branches a Ploeg Shift worked. #48 would get
  nothing.

### A Forgejo Actions job on each push to the base branch

* Good, because it runs exactly when conflicts appear, and is quick to write.
* Bad, because mergeability is computed after the push, so the job would have
  to poll anyway.
* Bad, because it is configuration in one repository, not an Unfold capability
  for every repository Ploeg routes, and it puts a tracker token in CI.
* Bad, because Ploeg would still need the binding, so it saves nothing inside
  Ploeg.

### Act on the `merge_state_dirty` webhook

* Good, because the event is already parsed.
* Bad, because Forgejo never sends it when the base branch moves, and the
  `pull_request_sync` payloads it does come from report `false` while the check
  is running.

## Re-evaluation triggers

* The homelab Forgejo reaches v17, where a user with code write access can push
  to `refs/pull/<n>/head`. AGit pull requests can then be updated in place, and
  the superseding mode shrinks to forks.
* Forgejo exposes the raw pull request status or `conflicted_files` in its API,
  or adds a webhook for mergeability. The two-poll confirmation or the polling
  can then go.
* After 10 conflict Work Items, the owner has rejected more than 2 of their
  results, or a reviewer reports that a resolution changed a pull request's
  intent. Conflict tickets then stay unassigned until a person assigns them.
* More than 10 conflict tickets in a week on one route. Fix the hot spots
  (generated files merged by hand, sequence numbers in file names) or serialize
  writers (VIK-571), rather than paying to resolve them.
* Unfold gets its own Vikunja user, so tickets can be authored as Unfold.
* A second tracker provider needs task creation.

## More Information

* Evidence: [2026-09-30 merge conflict signals](../research/2026-09-30-merge-conflict-signals.md).
* Board: epic VIK-1584 on the Unfold board, slices VIK-1585 to VIK-1588, Show
  as VIK-1598 (Ploeg) and VIK-1599 (Vloer), and the rollout VIK-1589 on
  Homelab Roadmap.
* VIK-1280 polls pull request head checks in the same reconcile, for the same
  reason: Forgejo 15 does not deliver the event.
* VIK-571 asks whether to serialize writing Runs per repository and was waiting
  for an observed collision between two agent branches. #45 is one.
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) caps the
  fix loop that a conflict Work Item's plan may run, and
  [ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)
  ranks the pull request's text below the brief.
