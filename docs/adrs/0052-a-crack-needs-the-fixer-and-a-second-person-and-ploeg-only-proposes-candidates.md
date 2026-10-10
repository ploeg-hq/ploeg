---
status: rejected
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# A crack needs the fixer and a second person, and Ploeg only proposes candidates

## Context and Problem Statement

A Run card gets a crack when a bug is traced back to the play that caused it, and a mend when the bug's fix merges. The owner decided that a crack needs a human to confirm it. [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md) left reliability at 10 and listed crack weight, reverts and hotfixes as not collected until this flow existed. The Run cards game-theory research (`docs/research/2026-10-01-run-cards-game-theory.md` at the repository root) shows two risks. At pull request level, an SZZ-style automatic link (a bug traced back through the lines its fix changed) is wrong about as often as it is right. And a steward who controls the links to their own card will under-link. The question is who may put a crack on a card, how much it weighs, and how it is mended.

## Decision Drivers

* Cracks only with human confirmation (owner decision).
* The steward never decides whether a bug links to their own card.
* Reporting your own bug costs less than having it found, and hiding it costs more.
* A mended card ends below a never-cracked one, so shipping a bug and fixing it never pays.
* Every step is attributable to a person and audited. The card's history is append-only.
* The grade stays a versioned formula over stored facts ([ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)).

## Considered Options

* **Ploeg proposes candidates from stored file lists, the fixer proposes a crack, a second person confirms it, the steward may dispute and a referee decides**
* Ploeg applies a crack automatically from SZZ or a revert
* The steward accepts or rejects every crack on their own card

## Decision Outcome

Chosen option: "**Ploeg proposes, the fixer names the cause, a second person confirms, a referee settles disputes**", because it is the only option where neither the automation nor the steward alone can put a crack on a card or keep one off it.

1. **Facts kept at merge.** When a Ploeg play merges, Ploeg reads its changed files (old and new path of a rename), labels and commits from the forge (Forgejo `GET /pulls/{n}/files` and `/commits`, GitLab `/merge_requests/{iid}/diffs` and `/commits`) and keeps up to 300 paths per pull request (migration 0027, `pull_request_files`). A forge without this read records no files.
2. **Reverts.** Any merged pull request in a repository with merged plays is read the same way. It reverts a play when its title begins with "Revert" and names the play's number (`#n`, `!n` or `owner/repo#n`), or when one of its commits says `This reverts commit <sha>` with the play's merge or head commit (at least 7 hex characters). The play is marked in `pull_request_reverts` and the card's grade gets the `RV` qualifier.
3. **Candidates.** For a bug Work Item, `GET /api/v1/operator/work-items/{id}/crack-candidates` lists merged plays of other Work Items in the same team and repository that merged within 365 days before the bug Work Item was created and touched a path its fix plays touched. They are ranked by shared paths, then newest first. A candidate says whether its play was reverted. Ploeg never turns a candidate into a crack.
4. **Proposal.** The fixer proposes with `POST /api/v1/operator/work-items/{bug}/cracks`: the card, optionally the play, the severity S1–S4 set by triage, primary or contributing, and a note. The play must have merged before the bug Work Item was created. A bug cracks at most three cards. Discovery is `self` when the proposer is the card's steward, `concealed` only when the steward merged the bug's fix without linking it, and `discovered` otherwise. A reverted card's crack is at least S2.
5. **Confirmation.** A second person who is neither the card's steward nor the proposer confirms with `POST /api/v1/operator/cracks/{id}/confirm`, and may correct the severity and share. Only now does the crack reach the card's `condition` and its grade.
6. **Dispute.** Within five working days (Monday to Friday, UTC) of confirmation, the steward alone may dispute with a reason (`/dispute`). A disputed crack keeps counting, so a dispute is not a way to delay.
7. **Referee.** A disputed crack is resolved (`/resolve`) as upheld (confirmed again) or unlinked (off the card, kept as history). The referee is anyone not involved, meaning not the steward, proposer, confirmer or disputer. When the team's `cards.referees` names people, only they may resolve. The decision is final.
8. **Evolved.** Anyone but the steward may mark a bug as a changed requirement for a card (`POST /api/v1/operator/work-items/{bug}/evolved`). The card gets `evolved` and no crack. A proposed attribution can become evolved; a confirmed one cannot.
9. **Mend.** When a play of the bug Work Item merges, every attribution of that bug records the mend: its pull request, when, who merged it and whether that was the steward. A mend found before the crack was proposed is recorded at proposal. Someone other than the steward who mends gets the roster role `cosigner`. A sweep confirms a mend once it stood 30 days with the bug Work Item done, and reopens it when another crack on the same card was confirmed within those 30 days.
10. **Weight.** A crack weighs severity (S1 4, S2 2, S3 1, S4 0.25) × share (primary 1, split evenly among the bug's primary causes; contributing 0.25) × discovery (self 0.5, discovered 1, concealed 1.5) × warranty (full 1 when the bug Work Item was created within 180 days of the card's release, half 0.5 within 365, history 0 after) × mend (0.5 when a confirmed mend was the steward's, 0.75 when it was someone else's, 1 otherwise).
11. **Formula 2026.2.** This extends [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)'s 2026.1 and changes only reliability and durability. Reliability is 10 minus the summed crack weight. A revert counts as at least weight 2 and is not added to a heavier crack. A card with an in-warranty crack or a revert has reliability at most 9.5, so a mended card stays below a never-cracked one. Durability loses 2 per revert and 1 per hotfix. A hotfix is a merged fix play of a confirmed crack's bug that carries a hotfix label (the team's `cards.hotfixLabels`, default `hotfix`). Qualifiers are `RV`, `HF`, `OB` and `RT`, in that order. `reliability.crackWeight`, `reliability.reverted`, `durability.reverts` and `durability.hotfixes` are collected.
12. **Actor and audit.** Every mutation needs an operator consumer with `execute` and the `X-Ploeg-Actor` header (or `X-Ploeg-Acting-User`). That person is compared, ignoring case, with the steward, a forge login. Each step writes an audit row: `card.crack_proposed`, `card.crack_confirmed`, `card.crack_disputed`, `card.crack_resolved`, `card.crack_evolved`, `card.crack_mended`, `card.mend_confirmed`, `card.mend_reopened` and `card.reverted`.

### Consequences

* Good, because no crack exists without two people, and the steward can neither confirm nor dismiss one on their own card.
* Good, because reporting your own bug weighs half, and a steward who fixes their own bug without linking it weighs 1.5 once someone else links it.
* Good, because reverts and hotfixes are facts the forge already reports, so the grade's reliability and durability inputs have a source.
* Bad, because the steward is compared with an operator actor name. Until tracker users and operator actors are linked to forge logins, a steward who signs in under another name passes the steward checks. Vloer must send the forge login as the actor.
* Bad, because candidates are only as good as the stored file lists. A play merged before migration 0027, a forge without the change read, or a fix outside a Ploeg Work Item gives no candidates, and its card can still be proposed by hand.
* Bad, because the weights are the research's first guess. Each tuning is a new formula version.
* Bad, because the five working days count Monday to Friday in UTC and ignore public holidays.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store`: the state machine (`TestCrackAttributionFlowNeedsTwoPeopleAndARefereeForDisputes`, `TestCrackDisputeUpheldConfirmsAgain`, `TestCrackProposalRefusals`, `TestMarkEvolvedGivesTheCardEvolvedAndNoCrack`), including every forbidden actor, the three-card limit, a play merged after the bug, unproven concealment, the five working days and a final referee decision; revert marking by number and by commit, and the S2 floor (`TestRevertsMarkCardsAndFloorTheirCrackSeverity`); mends recorded, confirmed after 30 days with the bug done, reopened by a new crack, cosigned by someone else (`TestMendsAreRecordedConfirmedAndReopened`, `TestAMendByAnotherPersonCoSignsAndAReCrackReopensIt`); candidate ranking, window and team scope; the weight, warranty and formula tables (`TestCrackWeightAppliesSeverityShareDiscoveryWarrantyAndMend`, `TestWarrantyByCardAgeWhenTheBugWasRaised`, `TestComputeGradeCracksRevertsAndHotfixes`).
* `pkg/forgefacts`: what counts as a revert claim (`TestReverts`).
* `pkg/provider/forgejo` and `pkg/provider/gitlab`: the change read against `httptest` fakes, including the 300-file bound.
* `pkg/httpapi`: files, reverts and mends from forge webhooks, and the operator endpoints with their refusals, each response validated against `operator-api.v1`.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Ploeg applies a crack automatically

* Good, because no person spends time on attribution.
* Bad, because PR-level SZZ precision is about 0.19 (Mozilla, arXiv 2209.03311), so most automatic cracks would be wrong.
* Bad, because it contradicts the owner's decision that cracks need human confirmation.

### The steward accepts or rejects each crack

* Good, because the person who knows the change best decides.
* Bad, because the measured person controls the measurement, and under-linking is the most damaging form of gaming the research names.

## More Information

* The owner's card contract addendum (P3), section "Condition: cracks and mends", gives the card shape and the attribution flow.
* The Run cards game-theory research, `docs/research/2026-10-01-run-cards-game-theory.md` at the repository root, sections 2.2 to 2.7 and 3.4, gives the weights, the warranty and the honesty calculation.
* [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md): the formula this extends. [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md): the card.
* 2026-10-03: [ADR-0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md) proposes formula 2026.3, which keeps this record's reliability and durability and changes review and missing inputs.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. This decision stays in force while the card endpoints are deprecated and is removed with them in the next minor release.
* 2026-10-10 — Rejected by [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md): the Run card left Ploeg, and the code, routes and tables this record describes were removed. The operator consumer owns the card.

## Re-evaluation triggers

* Tracker users or operator actors are linked to forge logins: the steward checks compare identities instead of names.
* Ten disputes are resolved, or a team's dispute rate passes one in three confirmed cracks: the flow or the weights need the owner.
* Twenty confirmed cracks exist: the weights are tuned under a new formula version.
* A fix outside a Ploeg Work Item needs to mend a crack: mends need a source other than the bug Work Item's plays.
