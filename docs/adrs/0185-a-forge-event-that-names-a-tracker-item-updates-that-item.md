---
status: proposed
date: 2026-10-31
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# A forge event that names a tracker item updates that item

## Context and Problem Statement

People follow work on the Tracker Item, but the work happens on a forge. Some trackers link the two themselves: ClickUp, for example, has native integrations for GitHub, GitLab and Bitbucket that attach a pull request to a task when its branch, title or commits carry the task's id. None of them covers Forgejo or Gitea, and no third-party integration for Forgejo was found. On a Forgejo installation, and for any tracker without such an integration, a pull request (including one a person opens by hand) never appears on the item it implements, and the item's status does not move when the pull request opens or merges. Ploeg already watches the forge: it receives and verifies forge webhooks for its Work Targets and normalizes them into Forge Events, `pr_opened`, `pr_synchronized`, `pr_merged` and `pr_closed` among them. Should Ploeg link a pull request to the Tracker Item it names, and move that item, for every forge it supports?

## Decision Drivers

* Linking must work for every forge Ploeg supports (Forgejo, GitHub, GitLab), not only the ones a tracker vendor integrates with.
* It must work for pull requests people open by hand, not only for those a Run delivers.
* Text in a branch name, title or commit message is written by whoever can push; it must never select authority ([ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md), [ADR-0170](0170-tracker-and-forge-text-enters-the-prompt-in-an-untrusted-frame-it-cannot-close.md)).
* Core never knows a vendor's id format; the tracker adapter does ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)).
* Forges redeliver and send several events per pull request; the effect must be the same however often it arrives ([ADR-0173](0173-a-webhook-delivery-is-claimed-under-a-lease-and-marked-seen-only-after-its-effects-commit.md)).
* Vendor rate limits apply to every write-back.

## Considered Options

* **Ploeg links and moves the referenced Tracker Item from the Forge Events it already ingests, opt-in per route**
* Rely on the tracker vendors' own forge integrations
* A separate bridge service between forge webhooks and tracker APIs

## Decision Outcome

Chosen option: "**Ploeg links and moves the referenced Tracker Item from the Forge Events it already ingests**", because Ploeg already holds the verified forge event, the tracker credential, the routes and the status mapping, so the link costs one rule in the engine and works for every forge it supports.

1. **Trigger.** For a repository Ploeg watches as a Work Target, a Forge Event of kind pull request opened, updated, merged or closed, or a push, is scanned for Tracker Item references. Push is not a Forge Event kind today; adding it is part of this decision.
2. **Reference format belongs to the adapter.** Each tracker adapter supplies how its references look and how to read one (the proposed `ReferenceNamer` capability of ADR-0183, in [ploeg-hq/ploeg#393](https://github.com/ploeg-hq/ploeg/pull/393); ClickUp's is `CU-<id>`, which the ClickUp work in progress on branch `feat/clickup-parity` already uses for branch names). No prefix appears as a literal outside the adapter. Ploeg scans the branch name, the pull request title and, for a push or a pull request's commits, the commit messages. A tracker without the capability gets no linking.
3. **Content never selects authority.** A reference is acted on only when the Tracker Item it names lies in a Scope that a Routing Rule for that tracker already covers, and that rule's Work Target is the repository the event came from, within the same Tenant. A reference to anything else is ignored and logged without its text. A reference never creates a Work Item, never starts a Shift and never reaches another route or Tenant.
4. **Link.** Ploeg writes one comment on the Tracker Item naming the pull request with its URL, edited in place by marker on later events, and sets the tracker's link or URL field where the adapter offers one. The comment is Markdown and the adapter renders it, per ADR-0183's rule that comment bodies are Markdown.
5. **Status.** Ploeg moves the item through the status mapping the operator configured for that board ([ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md)): to the review status when a pull request opens, and to done when it merges, but only if the item belongs to a Work Item Ploeg owns or the route opts in to moving items it does not own. A closed, unmerged pull request moves nothing. A status without a mapping leaves the item where it is.
6. **Idempotent and limited.** The effect is keyed on (Tracker Item, pull request): a redelivered or repeated event edits the same comment and sets the same status, and does nothing when nothing changed. Writes go through the adapter's rate-limit policy and the publication outbox ([ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)).
7. **Opt-in per route.** A route enables linking, and separately the moving of items Ploeg does not own. Both are off by default.

### Consequences

* Good, because a Forgejo installation gets the pull-request link and status moves that the vendors offer only for the large forges.
* Good, because pull requests people open by hand show on their items, so the tracker reflects all the work on a board, not only Ploeg's.
* Good, because the rule lives where the verified event and the route already are; nothing new runs or holds credentials.
* Bad, because a person who can push to a watched repository can comment on, and on an opted-in route move, any item in the Scopes routed to it. Accepted: they can already do so in the tracker in most installations, the effect is a comment and a mapped status, and the route opt-in bounds it.
* Bad, because an installation that also enables the vendor's own integration gets two links on the same item. The operator should enable one of them per repository.
* Neutral, because reading commit messages for a pull request costs one forge read per event, inside the forge's rate limit.

### Confirmation

Proposed, none of these exist yet:

* A table test feeds Forge Events from `httptest` fakes of Forgejo, GitHub and GitLab whose branch, title or commits name a Tracker Item, and checks one comment and the mapped status on a fake tracker.
* A test that a reference to an item outside the route's Scopes, to another route's repository, or to another Tenant writes nothing and creates no Work Item.
* A test that the same event delivered twice, and opened then synchronized, leaves one comment and one status write.
* A test that with the route opt-in off nothing is written, and that a merge does not move an item Ploeg does not own unless the route opts in.
* The tracker conformance suite of ADR-0183 checks each adapter's reference parsing round-trips its own format and rejects others.

## Pros and Cons of the Options

### Rely on the tracker vendors' own forge integrations

* Good, because nothing is built.
* Bad, because no tracker vendor Ploeg supports integrates with Forgejo or Gitea, which is Ploeg's reference forge.
* Bad, because each vendor integration needs its own forge-wide grant to the vendor, outside the routes Ploeg enforces.

### A separate bridge service between forge webhooks and tracker APIs

* Good, because Ploeg's engine stays smaller.
* Bad, because the bridge needs its own copy of the tracker credential, the webhook secrets, the route table and the status mapping, which is a second place where authority is decided.
* Bad, because a second service must be deployed, upgraded and kept consistent with Ploeg's routes.

## Re-evaluation triggers

* A tracker Ploeg supports ships a native Forgejo or Gitea integration.
* A forge Ploeg supports offers tracker linking itself through a standard every supported tracker accepts.
* Someone reports a reference that reached an item outside its route, or a status moved without an opt-in.
* A tracker's rate limit is hit by linking on a busy repository.

## More Information

* Open questions: whether to scan pull request bodies too; whether merge should move an item that several open pull requests reference.
* Related: [ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) maps statuses per board; [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md) resolves forges through a registry; [read pull requests others open](../how-to/read-pull-requests-others-open.md) already brings pull requests people open into Ploeg.
