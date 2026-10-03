---
status: proposed
date: 2026-10-03
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run card's grade penalizes rework, not review, and says which inputs it missed

## Context and Problem Statement

The 2026-10-02 code-quality review (VIK-1751) found three faults in formula 2026.2 ([ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md), [ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md)):

* **Careful review lowers the grade.** Review loses 1 per human review that requested changes and 0.5 per review round beyond the first. A round is one (play, head commit) pair any human reviewed, so an approval on a second commit, or a comment-only pass, costs as much as rework. Two reviewers asking for changes on the same commit cost twice. A team that reviews more often gets worse cards, which teaches the opposite of what a card is for.
* **Unknown scores as perfect.** When the cost is not reported, nothing was authorized, or the board maps no gates, `budgetShare` or `defectBounces` is null and delivery loses nothing. A card whose budget and bounces Ploeg cannot see can earn a 10 and a label that a card with known, clean facts earns only by having them.
* **The docs promise frozen grades the code does not keep.** `docs/concepts/run-cards.md` says a formula change never silently re-grades old cards, but Ploeg computes every grade on read under the current formula. ADR-0050 already lists this as a consequence; the concept page contradicts it.

How should review count, how should a missing input score, and what should the card promise about old grades?

## Decision Drivers

* A card records a change, and caring about a change must never cost points (`docs/concepts/run-cards.md` at the repository root).
* Unknown stays unknown ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)), and it must never look better than a known good fact.
* The same facts always give the same grade, and the card names the formula that gave it ([ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)).
* `operator-api.v1` changes only additively.
* The simplest honest option wins: no new write path or migration unless a promise needs one.

## Considered Options

* **Formula 2026.3: review counts rework rounds only; a missing input caps its subgrade at 9 and withholds the label; grades stay computed on read and the docs say so**
* Keep 2026.2's review penalties and only document them
* Score a missing input as the worst known value
* Snapshot each grade with its formula version at release (migration 0036)

## Decision Outcome

Chosen option: "**Formula 2026.3, computed on read**", because it removes the incentive against careful review, stops unknown from beating known, and keeps one honest promise instead of a broken one.

1. **Version.** `formula` is `2026.3`. Everything ADR-0050 and ADR-0052 decided for 2026.2 holds except the changes below.
2. **Review.** Review starts at 10 and loses 1 per **rework round**: a distinct (play, head commit) pair on which at least one human (not a forge login Ploeg acts as) requested changes. Approvals and comment-only reviews lower nothing, however many rounds they take. Several reviewers asking for changes on the same commit are one rework round, because the author reworks once. `inputs.review.reworkRounds` is new. `changeRequests` and `reviewRounds` stay on the card as context and lower nothing.
3. **Missing inputs.** `inputs.missing` lists, as `subgrade.input`, each input Ploeg has a source for but no fact on this card. Today these are `delivery.budgetShare` (the cost was not reported, or nothing was authorized) and `delivery.defectBounces` (the board maps no gates). The grade's evidence is complete when `missing` is empty.
   * A subgrade with a missing input is at most 9. The known penalties still apply, so a known overrun of more than a quarter with unknown bounces stays at 8. Unknown therefore never scores above a known clean fact, and it scores the same as one small known defect.
   * A grade with a missing input carries no label. A black or gold label says every input was seen.
   * `notCollected` keeps its meaning: inputs with no source on any card (survival, CI on the first check, reviewer findings). They are absent from every card alike, so they move no grade and cap nothing.
4. **Old grades.** Ploeg keeps computing the grade when the card is read, always under the current formula. A card read after this change shows `2026.3` and the grade 2026.3 gives, even if an earlier read showed `2026.2`. The card names the version on its back, and the docs no longer promise that old grades never change. No grade is stored, so no migration is needed.
5. **Contract.** `cardGrade.formula` accepts `2026.3`; `cardGradeInputs` gains `missing` and `review.reworkRounds`, both always sent. The code is `pkg/store/card_grade.go`.

### Consequences

* Good, because asking for another look, approving a fixed commit or leaving comments never lowers a card. Only rework does.
* Good, because a card states which facts it lacks, and a perfect grade or a label needs every fact Ploeg can collect.
* Good, because the docs now match the code: one formula, named on the card, applied to every card read.
* Bad, because a card's grade can change when the formula changes, so a grade quoted last month may differ today. The version on the card shows that it changed, not what it was. If the owner needs a grade that never changes once given, that is a stored snapshot and a new decision.
* Bad, because Work Items whose harness reports no cost, and boards without gates, now top out at a delivery of 9 and no label until those facts exist. That is the point, but teams on such boards will see it.
* Bad, because a rework round does not tell a reviewer's taste from a real defect. Reviewer findings (not collected) would separate them.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store`: `TestComputeGradeFormula` (approved and comment-only rounds lower nothing, rework rounds cost 1 each, the 2026.3 version), `TestCardGradeCountsReworkRoundsNotReviews` (rounds and rework counted from a card's reviews, several reviewers on one commit are one rework round), `TestComputeGradeMissingInputsCapTheirSubgradeAndWithholdTheLabel` (each missing input listed, delivery capped at 9, known penalties below the cap kept, no label) and `TestComputeGradeInputsKeepUnknownsUnknown`.
* `pkg/httpapi`: a card with a 2026.3 grade validates against `operator-api.v1`.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Keep 2026.2's review penalties and only document them

* Good, because nothing changes for existing cards.
* Bad, because the card would keep rewarding fewer reviews, which no documentation fixes.

### Score a missing input as the worst known value

* Good, because unknown could never beat any known value.
* Bad, because a board without gates cannot report bounces at all. Its every card would lose delivery points for a configuration choice, which punishes the change for the process.
* Bad, because there is no worst known bounce count: each bounce costs 1.5 without a limit.

### Snapshot each grade with its formula version at release (migration 0036)

* Good, because a grade, once given, would never change.
* Bad, because durability grows with days live, and cracks, reverts, hotfixes and bounces arrive after the release, so the snapshot would need re-grading anyway or freeze a grade that is already wrong. ADR-0050 rejected storing grades for the same reason.
* Bad, because it needs a write path, a migration and a rule for which moment counts, for a promise nobody has asked to rely on yet.

## Re-evaluation triggers

* The owner asks for a grade that never changes once given.
* Reviewer findings or CI on the first check get a source: review gains real inputs, and rework rounds may weigh less.
* Over a quarter of graded cards in a month list a missing input: the cap needs a sharper source rather than a different score.
* Twenty graded cards exist under 2026.3 and the owner judges the penalties.

## More Information

* [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md): formula 2026.1 and computing on read. [ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md): formula 2026.2.
* [ADR-0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md): review rounds and comments as figures that grade nothing.
* Tracker: VIK-1751.
