---
status: proposed
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-11
---

# A Run writes Work Item Notes without review, and a wider scope is a Promotion a person accepts

## Context and Problem Statement

[0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md) holds every learning back until a person accepts it. The owner asked on 2026-10-11 for agents to write knowledge per Work Item, client, repository and organization, and chose to let a Run write notes about its own work without review. A Run that found how a test suite must be started, or which approach already failed, helps the next Run of the same work most, and that next Run often starts within minutes. Review still guards knowledge that reaches other work. Where is the line, and how does a note move across it?

## Decision Drivers

* A later Run of the same work gets what an earlier Run learned, without waiting for a person.
* Nothing a model wrote reaches a Run of other work before a person accepts it.
* The agent may suggest a wider scope; only a person grants it.
* A note survives a ploegd restart between the outcome and the graph write, and is written once.
* The volume a Run can write stays bounded.

## Considered Options

* Work Item Notes written directly, readable within the root Work Item; a wider scope is a Promotion a person accepts
* Repository-level notes written directly too, reviewed afterwards
* Every learning reviewed before any Run sees it (0066 as proposed)

## Decision Outcome

Chosen option: "Work Item Notes written directly, readable within the root Work Item; a wider scope is a Promotion a person accepts", because it removes review where the blast radius is one piece of work that ends in a reviewed pull request anyway, and keeps it where a note would reach other work.

* **Learnings stay the channel.** `OutcomeReport.learnings` keeps its shape and cap from 0066 and gains an optional `scope`: `work-item` (the default), `repository`, `client` or `tenant`. The scope is the agent's suggestion.
* **Every kept learning becomes a Work Item Note** in the Tenant's notes graph, carrying the Work Item, its root, its repository, its Client when it has one, the Run's trace and the time. Later Runs of every Work Item under the same root read it ([0085](0085-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md)); no other Run does.
* **Framing.** The pack lists Work Item Notes separately as notes earlier Runs of this work wrote, unreviewed, below reviewed knowledge and below the delivery contract.
* **A wider scope is a Promotion.** A learning whose `scope` is wider than `work-item` also creates a pending Promotion in ploegd's store that names the note and the suggested scope. A person may also request a Promotion for any note ([0087](0087-operator-consumers-read-knowledge-and-write-notes-for-a-named-person-within-scopes-ploegd-checks.md)). A Tenant admin accepts it, with edits or a narrower scope, or rejects it; a rejected Promotion is kept so the same suggestion is not raised again.
* **Accepting writes the concept.** ploegd, as the Tenant's review actor, forks a branch from the knowledge graph's `main`, writes the concept, merges and deletes the branch in one operation. No proposal branch stays open, so schema changes are never blocked by waiting reviews.
* **Writing once.** The outcome and a note outbox row are stored in one transaction; ploegd writes outbox rows to the notes graph until the commit is confirmed, with a merge load keyed on the Run's trace and the learning's index, so a replay changes nothing. An acceptance records its branch name before it starts; after a restart ploegd finds the concept on `main` and finishes, or deletes the branch and retries.
* **Retention.** A Work Item Note is kept while its root Work Item is open and for 90 days after it closes, unless a Promotion references it.

### Consequences

* Good, because a Work Item's second and third Runs start from what the first learned, with no review delay.
* Good, because every promoted concept traces back to the note, Run and Work Item it came from, and to the person who accepted it.
* Bad, because one wrong or injected note can steer later Runs of the same root until it expires; the reviewer of the pull request is the guard, helped by the TaskSpec listing every note the Run was given.
* Bad, because Promotions add a review queue; an ignored queue means knowledge stays local to its work.
* Neutral, because the cap of 10 learnings per Run also caps notes.

### Confirmation

Proposed checks, in the existing `go test ./...` CI step once built:

* `TestWorkItemNote_ReachesLaterRunsOfTheSameRootOnly`: a note written by a Run of Work Item X is in the next Run of X and of a Work Item created under X, and absent from a Run of an unrelated Work Item in the same repository.
* `TestPromotion_NothingReachesOtherWorkBeforeAcceptance`: a learning with `scope: repository` is absent from every other root's Runs until a Tenant admin accepts it, and present after.
* `TestNoteOutbox_WritesOnceAcrossARestart`: ploegd stops after storing the outcome and before the graph confirms; on restart the note exists exactly once.
* `TestPromotion_AcceptanceSurvivesARestart`: ploegd stops between the merge and the delete; on restart the concept exists once and no branch remains.
* The OutcomeReport schema case that refuses an unknown `scope`.

## Pros and Cons of the Options

### Repository-level notes written directly too, reviewed afterwards

* Good, because repository conventions spread with no review delay.
* Bad, because one wrong or injected note reaches every Run on that repository until a person notices, which is the failure 0066 exists to prevent.

### Every learning reviewed before any Run sees it

* Good, because no model text reaches another prompt unreviewed.
* Bad, because the Run most helped by a learning, the next Run of the same work, waits for a person or never gets it.

## Re-evaluation triggers

* A pull request is rejected because of a wrong Work Item Note, twice in a month.
* The first 20 Promotions show an acceptance rate below 25 %, or above 90 % with no edits.
* A Tenant asks for unreviewed notes at repository scope.
* Notes grow past 1,000 per Tenant per month.

## More Information

* Replaces 0066 once accepted; on acceptance set `supersedes: 0066` and mark 0066 superseded in the Records index. Until then both stay proposed, and 0066's cap and fields remain the contract.
* Unfold system ADR-0034 (proposed) records the product decision.
* 2026-10-11 — Proposed after the owner chose direct Work Item Notes with review only for a wider scope.
