---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Every note carries its provenance, untrusted text is quoted, and a wider level needs evidence or a person

## Context and Problem Statement

[0088](0088-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md) lets a Run write Work Item Notes that later Runs of the same work read without review, and [0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md) keeps them in a Tenant's notes graph that only ploegd writes. On 2026-10-18 the owner asked how agents should communicate and delegated the design: agents never talk to each other, they share a blackboard behind Ploeg, and Omnigraph owns its knowledge at the Work Item, repository, Client and Tenant levels. A blackboard that every Run reads is only as good as what is on it. A note did not say whether a person or an agent wrote it, what it was drawn from, or when it stopped being true; its text reached the next agent as if it were part of the brief; two notes that disagreed sat side by side; a write could overwrite a decision made on a stale read; nothing but a person could ever widen a note, and the queue for that did not exist; a rejected pull request taught the next Run nothing; and there was no way to remove one Client's knowledge. How is a note kept honest, safe and removable?

## Decision Drivers

* Text an agent, a tracker or a forge wrote is data. It must never reach another agent as an instruction.
* A Client's knowledge is read only by that Client's Runs, whatever its level.
* No note is edited in place: history stays readable and a newer note says what it replaces.
* A write is decided on what the writer read; a write that raced another is never applied blindly.
* Knowledge reaches more work only with proof the work was accepted, or a person's OK. Client and Tenant knowledge always needs a person.
* Two current notes that disagree are a question for a person, not a silent coin toss for the next agent.
* How a pull request ended is the strongest signal a repository gives, and it is free.
* A Client must be removable completely, and the record says honestly how far Omnigraph lets that go.
* The graph schema changes only additively, so the Ploeg release running today keeps working until the new one lands.

## Considered Options

* Provenance on every note, quoting by trust, conditional append-only writes, evidence-or-person promotion, flagged contradictions, review outcomes as notes and a designed purge
* Keep notes as they are and rely on the pull request reviewer (0088's guard)
* Send every note through a person before any Run reads it (0066)
* One notes graph per Client

## Decision Outcome

Chosen option: "Provenance on every note, quoting by trust, conditional append-only writes, evidence-or-person promotion, flagged contradictions, review outcomes as notes and a designed purge", because it keeps 0088's speed for the next Run of the same work, removes the ways a note could mislead or leak, and costs no model call.

Decided by the owner's delegation on 2026-10-18; the choices a person should re-check are listed at the end.

* **Provenance.** Every note records its author kind (`run`, `person`, or `ploeg` for what ploegd observed itself), the author (the Run's trace alias, the person's id, or `ploegd`), its source references (`run:`, `work-item:`, `pull-request:`, `note:`, `person:`), its trust (`trusted` or `untrusted`), its level (`work-item` or `repository` in the notes graph), when the fact was observed, and optionally when it expires. A Run's text is always untrusted; ploegd refuses to store it as trusted. A person's note through an operator consumer, and a sentence ploegd composes from its own records, are trusted. Notes written before this record read as untrusted Work Item notes a Run wrote. A learning may name its `subject`, the note it `supersedes` and an `expiresAt` at most a year ahead; live state such as a red pipeline is only ever noted with an expiry.
* **Untrusted text is quoted data.** The briefing gives every note a provenance line. An untrusted note's title is reduced to one escaped line, and its body is fenced with a fence longer than any backtick run inside it, under a sentence that says it is data and not instructions. Trusted text reads as written. The rules live in package `notes` (`Body`, `Title`, `Inline`, `Quote`) so every renderer uses the same ones.
* **Client isolation on reads.** `notes.ForRun` is the only filter between the graph and a briefing. A Run reads the notes of its own root Work Item and the repository-level notes of its own repository, and only those whose Client equals its own; a note with a Client is never read by a Run without one. Superseded, retracted and expired notes are left out and counted. The scope still comes only from Ploeg's records (0087). Until Ploeg records Clients (Unfold system ADR-0017) every note and Run has none.
* **Conditional, append-only writes.** ploegd reads the notes on a new note's subject, decides what the new note does to them, and writes it with `add_note` or `add_note_superseding` on Omnigraph's `if-graph-commit` route, conditioned on the commit of that read. A 412 means nothing was written: ploegd reads again and decides again, up to three times per sweep, and otherwise leaves the note in the outbox for the next sweep. It never falls back to an unconditional write; a notes bundle without the conditional route makes notes wait, and the existing outbox-age alert reports it. A note is never edited: a newer note names the one it replaces in `supersedes`, and `add_note_superseding` records the `Supersedes` edge in the same commit.
* **What replaces what.** A current note on the same subject, at the same level and for the same root or repository and Client, is replaced when the new note says the same, when its author updates their own note, when a trusted note corrects an untrusted one, when ploegd records a newer observation of its own, when the new note's Run was briefed with it (a Run that started after the note was written), or when the writer names it and may replace it. A Run's note never replaces a person's.
* **Contradictions are flagged.** Any other current note on the subject that says something else is a contradiction. The new note is still written, listing what it contradicts, and ploegd opens a review. Until a person decides, every Run sees both notes marked contested. The person keeps the newer note, the older ones, or both; a note that is not kept gets a retraction, a trusted note by that person that supersedes it, so it leaves every briefing and stays in history.
* **Promotion needs evidence or a person.** A learning that suggests a wider scope opens a promotion review. A merged pull request of the note's Work Item is evidence: it applies the open repository promotions of that Work Item at once, unless a contradiction holds the note, by writing a repository-level copy keyed `promoted/repository/<key>` that names the note it came from and the evidence. A passing check is also accepted evidence for a repository promotion; no code path promotes on it alone yet. A person may accept or reject any promotion; a rejection needs a reason and is kept, so the suggestion is not raised again. Client and Tenant promotions need a person, always: evidence never widens a note beyond its repository. An accepted Client or Tenant promotion is written to the reviewed knowledge graph as a concept by the Tenant's review actor (`PLOEG_OMNIGRAPH_REVIEW_TOKEN`), in one keyed `put_concept` on `main`; without that actor it waits, accepted. A promoted note keeps its author and trust: a person's OK lets a Run's claim reach more work, it does not turn the Run's text into instructions.
* **Review outcomes are repository notes.** When a pull request ploegd delivered leaves review, ploegd writes a trusted repository-level note, `review/<repository>#<n>/<outcome>`: merged (by whom), rejected (closed after a reviewer's latest verdict asked for changes, naming the reviewer), or closed. The text is composed from Ploeg's records, never from the pull request's text, and a rejection's note says it is the strongest signal and to read the review before trying the same approach.
* **People write through Ploeg's contract.** `POST /api/v1/operator/work-items/{id}/knowledge-notes` keeps a person's trusted note at the Work Item or repository level; at repository level the person's own words are their approval. `GET /api/v1/operator/knowledge/reviews` and `POST .../reviews/{id}/decision` are where a person decides promotions and contradictions. The steering route `.../notes` keeps its meaning: it briefs one Run and is not knowledge.
* **Purge.** `delete_client_notes` removes every note of one Client from the notes graph's `main`, edges included, and `client_note_count` proves it reached zero; `delete_client_concepts` and `delete_repository_concepts` do the same for the reviewed knowledge graph as the review actor, and `PurgeClientNotes` removes the outbox rows, their text included, and the reviews from Ploeg's store, audited as `note.client_purged`. What Omnigraph's history allows: a delete is a new commit, and every older commit still holds the rows, readable by snapshot and by the change feed, until `omnigraph cleanup` reclaims the old versions. Cleanup is an offline, destructive maintenance command run with writers stopped; it works per dataset version, not per Client, so it drops every older version of the graph, everyone's history included, and a live branch or a checkpoint keeps the versions it needs. Commit metadata (ids, actors, times) survives cleanup; row content does not (tested against Omnigraph 0.11 on 2026-10-18: after `cleanup --keep 1` a snapshot read of the old commit answered "historical published dataset version was reclaimed"). Backups and volume snapshots outside Omnigraph keep their own copy and must be expired too. A full purge is therefore: run the three deletes, check both counts are zero, stop the Omnigraph writers, run `cleanup --keep 1 --older-than 0s --confirm` on both graphs, and expire backups older than the purge. No route runs the purge yet; it waits for Ploeg to record Clients.
* **Additive schema first.** The tenant-notes bundle gains nullable properties, the `Supersedes` edge and new stored queries, and `add_note` gains optional parameters; nothing the running release calls changes. The bundle is applied before the Ploeg release that uses it.

### Consequences

* Good, because an injected instruction in a note reaches the next agent fenced as data under a warning, and a person's decision reads as one.
* Good, because a Client's notes cannot reach another Client's Run even at repository level, and the filter is one tested function.
* Good, because a write decided on a stale read is refused by Omnigraph, not detected afterwards.
* Good, because knowledge from merged work reaches the rest of the repository without waiting for a person, and every promoted note names its evidence.
* Good, because a rejected or closed pull request now briefs the next Run on that repository, at no model cost.
* Good, because disagreement becomes a question a person answers once, instead of whichever note the agent happens to read first.
* Bad, because the notes graph's whole-branch precondition makes concurrent writers retry; at today's volume that is a re-read, at a much higher one it would need a per-subject condition Omnigraph does not offer.
* Bad, because a merged pull request is evidence that the work was accepted, not that each note about it is true; a wrong note in merged work reaches the repository, marked untrusted and naming its evidence.
* Bad, because a full purge erases every Client's history in that graph, needs the writers stopped, and Omnigraph cannot purge one Client's history alone.
* Bad, because the review queue grows with every wider suggestion and every disagreement; an ignored queue leaves contradictions marked contested.
* Neutral, because the briefing's composition, budget and catch-up belong to the blackboard umbrella record of the same day; this record decides only how notes are stored, validated, promoted and filtered, exposed to it as `Server.BriefingNotes` and package `notes`.

### Confirmation

In the existing `go test ./...` CI step:

* `pkg/notes`: `TestValidate_ProvenanceRules`, `TestForRun_ServesOnlyTheRunsClientScopeAndCurrentNotes`, `TestReconcile_SupersedesOrFlagsNeverKeepsSilently`, `TestPromote_NeedsEvidenceOrAPersonAndAPersonForWiderLevels`, `TestReviewOutcome_IsATrustedRepositoryNote`, `TestBody_QuotesUntrustedTextSoItCannotEscape`, `TestFromWire_ReadsAnOlderPloegdsNoteAsAnUntrustedRunNote`.
* `pkg/omnigraph`: `TestMutateIf_UsesTheConditionalRouteAndReportsAMovedBranch`.
* `pkg/httpapi`: `TestNoteWrite_RereadsTheSubjectWhenTheGraphMovedUnderIt`, `TestNoteWrite_AnOlderBundleGetsNoUnconditionalWrite`, `TestNoteWrite_SupersedesWhenInformedAndFlagsADisagreement`, `TestKnowledgeRead_NeverServesAnotherClientsOrAnExpiredNote`, `TestReviewOutcome_IsARepositoryNoteAndAMergePromotes`, `TestOperatorKnowledge_APersonWritesAndDecides`, `TestPurgeClient_RemovesTheClientsNotesEverywherePloegHoldsThem`, against a fake that answers the conditional route with 412 as Omnigraph 0.11 does.
* `pkg/worker`: `TestNoteConcepts_QuoteUntrustedTextAndNameItsAuthor`.
* `pkg/knowledgegraph`: `TestNotes_AgainstARealServer` runs the write, the refused stale write and the read against a real Omnigraph server when `PLOEG_TEST_OMNIGRAPH_URL` names one.
* The deployment's bundle rehearsal invokes every new stored query against a real server before the bundle is applied.

## Pros and Cons of the Options

### Keep notes as they are and rely on the pull request reviewer

* Good, because nothing changes.
* Bad, because the reviewer sees the diff, not the briefing; an injected note steers the next Run before any reviewer looks, and a disagreement is never seen at all.

### Send every note through a person before any Run reads it

* Good, because no model text reaches another prompt unreviewed.
* Bad, because the Run most helped by a note, the next Run of the same work, waits for a person or never gets it; 0088 rejected this for that reason.

### One notes graph per Client

* Good, because Omnigraph policy would enforce Client isolation, and a purge would delete a whole graph with its history.
* Bad, because a Tenant gains a graph, a policy and tokens per Client before Ploeg records Clients at all, and repository and Tenant notes still need a shared graph. Kept as the re-evaluation path for hosted Tenants.

## Re-evaluation triggers

* Ploeg records Clients (Unfold system ADR-0017, VIK-1876): wire the Client into every note and Run, and add the purge route.
* A hosted Tenant asks for a Client's knowledge to be erased, or a contract requires erasure within a deadline Omnigraph's offline cleanup cannot meet: revisit one graph per Client.
* Omnigraph gains per-key or per-type write preconditions, row-level policy, or per-row history purge.
* More than five notes a day wait on a 412 after three re-reads, or the review queue holds more than 20 open items for a week.
* A pull request is rejected because of a promoted repository note, twice in a month.
* A note's quoted text is followed by an agent as an instruction in any recorded Run.

## Choices to re-check

Decided by the owner's delegation; a person should confirm or change each:

1. A merged pull request alone promotes a Work Item's repository-scoped notes to the repository, without a person (0088 had every wider scope wait for a Tenant admin).
2. A passing check counts as evidence for a repository promotion, though nothing promotes on it alone yet.
3. A Run that was briefed with a note may replace it by writing on the same subject; only a Run that never saw it and disagrees is flagged.
4. A promoted or person-approved note keeps its author's trust: an approved Run note stays quoted data.
5. An accepted Client or Tenant promotion is written straight to the knowledge graph's `main` in one keyed write, instead of 0088's short-lived branch, merge and delete.
6. A rejected pull request is "closed after the latest non-comment review asked for changes"; any other close without merge is "closed".
7. A person's note at repository level needs no second person.
8. Repository-level notes are briefed to every Run on the repository, and review outcome notes are among them; the briefing budget decides how many reach the agent.
9. The purge runs Omnigraph's offline cleanup over the whole graph, erasing every Client's history in it.
10. Expiry is at most a year ahead and set by the writer; Ploeg does not guess expiries for live state.

## More Information

* Schema and stored queries: the tenant-notes and tenant-knowledge bundle of the deployment (homelab-cluster `kubernetes/apps/ai/omnigraph/app/bundle/`), applied before the Ploeg release that uses it.
* Store: migration `0062_note_provenance.sql` adds the provenance columns to the note outbox and the `note_reviews` table.
* Amends 0087 (Client isolation on every level, the review actor's concept writes and purge) and 0088 (provenance, quoting, repository promotion on evidence, review outcomes, the operator routes, retention by purge).
* 2026-10-18 — Accepted by the owner's delegation, as stream B of the blackboard design.
