---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An epic is a set of the Work Items declared its children before their first Shift

## Context and Problem Statement

The owner's card contract for Unfold makes an epic its own set card: its children are the cards in the set, and the set completes when every child has merged, settled and carries no unmended crack. Ploeg knows Work Items but not epics. Trackers do: Vikunja has task relations (`parenttask` and `subtask`), and ClickUp has a task's `parent`. An epic may be a Work Item itself, or only a tracker task nobody assigned to Ploeg. The Run cards game-theory research names the gaming risk: declare an epic after the fact, around children that already survived. The question is where membership comes from, when it counts, and when a set is complete.

## Decision Drivers

* Ploeg sends facts and deterministic derived values only ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)).
* A set is declared before the work, so it cannot be assembled from survivors afterwards.
* An epic that is not a Work Item still gives its children set information.
* Sets are team-level; a card shows its own team's set only.
* A crack shows on a set until mended and never voids it.

## Considered Options

* **Ploeg reads each Work Item's parents from the tracker and counts a relation it saw no later than the child's first Shift**
* An operator declares sets in Ploeg
* Vloer reads tracker relations itself

## Decision Outcome

Chosen option: "**Ploeg reads parents from the tracker and counts a relation seen before the first Shift**", because the tracker is where people already structure epics, and the time Ploeg first saw a relation is a fact Ploeg can store and nobody can backdate.

1. **Source.** A tracker provider that implements `provider.RelationReader` reports a Work Item's parents: Vikunja from the `parenttask` relations of `GET /tasks/{id}`, ClickUp from `parent` on `GET /task/{id}`, with the parent's name from a second read. Ploeg reads them when a tracker item is assigned (after the Work Item is stored and before its Shift opens), and on every update or close event of an item that has a Work Item. A Vikunja `task.relation.created` or `task.relation.deleted` event counts as an update of both tasks.
2. **Storage.** Migration 0028 keeps one row per (Work Item, parent) in `work_item_epics`, with the parent's tracker id and title, `first_seen_at`, `last_seen_at` and `removed_at`. A parent the tracker stops reporting is removed. A parent reported again afterwards starts over with a new `first_seen_at`. Each change writes a `work_item.epics_seen` audit row.
3. **Declared before.** A relation counts when `first_seen_at` is no later than the opening of the child's first Shift. A Work Item without a Shift counts provisionally. A child with several parents belongs to the earliest-seen parent that counts.
4. **Members.** A set's members are the Work Items of the card's team whose counted parent is the epic, ordered by when their first Shift opened, at most 100.
5. **Card.** The card's optional `set` (`cardSet` in `operator-api.v1`) has `role` (`epic` on the epic's own card when the epic is a Work Item of the team, `child` otherwise), `epic` (`workItemId` or null, `ref`, `title`), `position` (from 1, null on the epic card), `size`, `children` (epic card only: `workItemId`, `title`, `state`, `settled`, `cracked`) and `complete`. A Work Item whose relation does not count has no `set`.
6. **Complete.** A set is complete when every member's card state is `merged`, its latest merged play has been live 30 days or more, and it has no confirmed crack without a confirmed mend ([ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md)). Live time is counted from the play's first deploy to its release environment, or from the merge when the repository never reported that environment, as the card's release is ([ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)). A later crack makes the set incomplete until mended; it never removes the set.

### Consequences

* Good, because an epic declared after its children started counts none of them, so a set cannot be assembled from survivors.
* Good, because a team that structures epics on its board gets sets without configuring anything.
* Bad, because only children that are Work Items count. A tracker subtask done by hand is not a member, so a set can be complete while the epic still has open work outside Ploeg.
* Bad, because Ploeg learns a relation only when it receives an event for the child (or, on Vikunja, a relation event). A relation created silently on a board without webhooks is first seen late and may not count.
* Bad, because the research's minimum of three children and six Size points is not applied. Size points do not exist yet, and the contract does not ask for a minimum.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store`: added, removed and redeclared parents (`TestRecordEpicsTracksAddedRemovedAndRedeclaredParents`); a child declared after its first Shift is not a member, position and size, completion after 30 days live, an epic that is a Work Item lists its children, and an unmended crack leaves the set incomplete (`TestCardSetCountsChildrenDeclaredBeforeTheirFirstShift`); another team's children never count.
* `pkg/provider/vikunja` and `pkg/provider/clickup`: parents read against `httptest` fakes, and a Vikunja relation event updates both tasks.
* `pkg/httpapi`: relations are read before the first Shift, a relation seen after it does not count, and the card with its `set` validates against `operator-api.v1`.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### An operator declares sets in Ploeg

* Good, because the declaration time is exact.
* Bad, because it duplicates structure the board already has, and the two drift.

### Vloer reads tracker relations itself

* Bad, because the declared-before rule needs the first-seen time stored before the first Shift, which only the component that opens Shifts can do.
* Bad, because every consumer of cards would need tracker credentials.

## More Information

* The owner's card contract addendum (P4), section "Sets", gives the card shape and the completion rule.
* The Run cards game-theory research (`docs/research/2026-10-01-run-cards-game-theory.md` at the repository root), sections 3.7 and table row 20, gives the declared-before rule.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. The facts this record keeps stay and are exposed raw through the delivery facts endpoints; the card figures built on them are removed in the next minor release.

## Re-evaluation triggers

* Size points exist: the research's minimum of three children and six Size points can apply.
* A team wants tracker-only subtasks to count: membership needs the epic's own child list.
* Ploeg polls tracker relations instead of reading them on events.
