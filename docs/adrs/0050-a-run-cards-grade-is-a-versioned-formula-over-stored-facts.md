---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run card's grade is a versioned formula over stored facts

## Context and Problem Statement

[ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) left a Run card's `grade` null until the owner decided how to grade. The owner's card contract for Unfold (P2b) now sets the shape: an overall grade from 1 to 10 in half steps, four subgrades (reliability, durability, delivery, review), a black or gold label, qualifiers such as OB (over budget) and RT (retried Run), and the inputs that produced it, so the back of the card can print the formula. Grades get compared across teams and over time. A grade that changes because the code changed, without anyone noticing, is worse than no grade. Some inputs the contract names have no source yet: cracks and mends (P3), reverts, hotfixes, survival snapshots, CI on the first check and reviewer findings.

## Decision Drivers

* Ploeg sends facts and deterministic derived values only ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)). The same facts always give the same grade.
* Unknown stays unknown ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)). An input without a source never moves a grade, and the card says it was not collected.
* A changed formula never re-grades old cards silently.
* `operator-api.v1` changes only additively.

## Considered Options

* **Ploeg computes the grade when the card is read, under a named formula version, and returns the inputs it used**
* Ploeg stores a grade per card when the play merges
* Vloer computes the grade from the card's facts

## Decision Outcome

Chosen option: "**Ploeg computes the grade when the card is read, under a named formula version**". Every input is already stored for the card, so computing on read needs no new write path. The version string and the printed inputs make a change visible.

1. **When.** `grade` is null until a human (not a forge login Ploeg acts as) approved or requested changes on a play, or a play merged. Then it is a `cardGrade` object.
2. **Version.** `formula` is `2026.1`. Any change to an input, weight, penalty, cap or rounding rule gets a new version string and a new ADR. A later version is only ever computed for cards read after it ships, and it never claims an old version's name.
3. **Formula 2026.1.** Each subgrade is rounded to the nearest half (a tie rounds up) and clamped to 1–10. `overall = 0.40 reliability + 0.25 durability + 0.20 delivery + 0.15 review` over the rounded subgrades, rounded and clamped the same way.
   * **Reliability** is 10. Crack weight (P3) and revert detection have no source yet, so `reliability.crackWeight` and `reliability.reverted` are null and listed in `notCollected`.
   * **Durability** is `6 + 4·sqrt(min(1, daysLive/180))`. `daysLive` counts whole days since the card's `release.at`, and is 0 while the card has no release. Reverts, hotfixes and survival snapshots are not collected and lower nothing.
   * **Delivery** starts at 10 and loses 1 when the recorded cost is over the authorized budget by up to a quarter, 2 when over by more, 1.5 per defect or unknown bounce ([ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md)), 1 per play beyond the first, and 0.5 per failed or stuck Run. `budgetShare` is null when the cost is not reported or nothing was authorized. `defectBounces` is null when the board maps no gates.
   * **Review** starts at 10 and loses 1 per human review that requested changes and 0.5 per review round beyond the first. A round is one (play, head commit) pair that humans reviewed. CI on the first check and reviewer findings are not collected.
4. **Provisional.** `provisional` is true until `daysLive` reaches 180. A provisional overall is capped at 9, and a provisional card carries no label.
5. **Label.** After that, `label` is `black` when all four subgrades are 10, `gold` when overall is 10, and null otherwise.
6. **Qualifiers.** `OB` when the recorded cost is more than the authorized budget, and `RT` when any Run of the Work Item failed or got stuck. `RV`, `HF` and `MN` are in the schema's vocabulary but Ploeg emits none of them until reverts, hotfixes and manual takeovers have a source.
7. **Inputs.** `inputs` holds every number each subgrade used, per subgrade, and `notCollected` lists the inputs without a source as `subgrade.input`. The schema is the `cardGrade` and `cardGradeInputs` definitions in `operator-api.v1.schema.json`; the code is `pkg/store/card_grade.go`.

### Consequences

* Good, because a grade can be recomputed by hand from the card's back, and a formula change shows up as a new version on the card.
* Good, because an input without a source is visibly absent instead of silently scoring 10 or 0.
* Bad, because until P3 lands every card's reliability is 10, so a grade overstates cards that later turn out cracked. `notCollected` says so on every card.
* Bad, because computing on read means a card graded before a formula change shows the new version when read afterwards. Old grades are not kept. If the owner needs a frozen grade per card, that is a stored snapshot and a new decision.
* Bad, because the penalties are a first guess. They need real cards to tune, and each tuning is a new version.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/store`: half-step rounding including ties; the formula's table of cases (not live, 45 days, 179 and 180 days, gold, black, over budget by up to and beyond a quarter, failed Runs, extra plays, defect bounces, change requests, extra rounds, the floor of 1); unknown inputs stay null and `notCollected` lists exactly the inputs without a source; a card with only a bot approval or a comment has no grade; a merged play grades from its release.
* `pkg/httpapi`: a card with a grade validates against `operator-api.v1`.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Store a grade per card when the play merges

* Good, because a grade would never change once given.
* Bad, because durability grows with days live and bounces arrive after the merge, so a stored grade would need re-grading anyway.

### Vloer computes the grade

* Bad, because the grade would depend on the Vloer version a viewer runs, and two viewers could see two grades for one card.
* Bad, because a client portal reading Ploeg directly would need its own copy of the formula.

## More Information

* The owner's card contract addendum (P2b–P4) names the formula's weights, the 180-day provisional cap and the labels.
* [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md): the card the grade is added to.
* [ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md): the bounces delivery counts.
* 2026-10-03: [ADR-0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md) proposes formula 2026.3. Review loses points for rework rounds only, a missing input caps its subgrade at 9 and withholds the label, and grades stay computed on read under the current formula, as the consequence above says.

## Re-evaluation triggers

* P3 lands crack confirmation, or Ploeg starts detecting reverts or hotfixes: reliability and durability get real inputs and the formula a new version.
* The owner asks for a grade that never changes once given.
* Twenty graded cards exist and the owner judges the penalties.
