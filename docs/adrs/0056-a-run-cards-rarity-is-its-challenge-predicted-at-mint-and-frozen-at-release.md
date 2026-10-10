---
status: proposed
date: 2026-10-02
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run card's rarity is its challenge, predicted at mint and frozen at release

## Context and Problem Statement

[ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) left a Run card's `rarity` null until the owner decided what it measures. On 2026-10-02 the owner decided: rarity is how exceptional the work was, its challenge. It stays apart from the grade (how well it was done, [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md)), the finish (how long it has held up), pulls (luck) and sets ([ADR-0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md)). The questions are which stored facts measure challenge without the steward being able to pad them afterwards, when a card learns its rarity, what a tier is relative to, and whether a tier can change.

## Decision Drivers

* Challenge comes from facts the steward does not control after the fact. Cost, time, tokens, bounces and the grade are never inputs, because they reward slow or expensive work.
* Ploeg sends facts and deterministic derived values ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md)), and an unknown fact never moves a score ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)).
* Rarity is scarce by design: a tier must mean the same share of cards in a busy project and a quiet one.
* A rarity once shown on a revealed card never changes, so a collection does not shift under its owner.
* Card state never gates anything Ploeg authorizes, budgets or merges.
* `operator-api.v1` changes only additively.

## Considered Options

* **A versioned challenge score, predicted before the merge and revealed at release, tiered by percentile in a project-and-quarter cohort and frozen at reveal**
* The same score with fixed thresholds only
* A score computed on every read, never frozen
* Rarity as a mix of challenge and quality

## Decision Outcome

Chosen option: "**A versioned challenge score, tiered by percentile in its cohort and frozen at reveal**", because percentiles keep tiers scarce in every project, and freezing keeps a revealed card's tier stable while its cohort grows.

1. **Score, formula `2026.1`.** `score = 100 × (0.30 reach + 0.25 sensitive + 0.20 novelty + 0.25 size)`, rounded to one decimal. Each component runs from 0 to 1:
   * **Reach** `= min(1, ln(max(1, modules) + 3 × (repos − 1)) / ln 12)`. A module is the top-level directory of a touched file, or the first two directories under `apps`, `packages`, `services`, `libs`, `modules`, `crates`, `components`, `plugins` or `projects`. A file at the root is the module `.`. Each repository beyond the first counts as three modules.
   * **Sensitive** `= min(1, ln(1 + sensitive files) / ln 9)`. A sensitive file matches the Work Target's sensitive paths. The defaults are `**/migrations/**`, `**/*.sql`, `**/schema*.json`, `**/*.proto`, `**/openapi*.{yml,yaml,json}`, `Dockerfile`, `**/helm/**`, `**/.github/workflows/**` and `**/.forgejo/workflows/**`. A Work Target's `rarity.attentionPaths` adds to them, and `rarity.sensitivePaths` replaces them.
   * **Novelty** `= novel files / files`. A file is novel when no merged play of the same repository, on another card, touched it in the 180 days before this card first touched it. Ploeg only knows the files of its own plays, so the first cards in a repository read as novel.
   * **Size** `= min(1, ln(1 + counted lines) / ln 2001)`. Counted lines add the lines added and removed, without the files in the Work Target's `rarity.sizeExclude`. The default list holds lockfiles, generated files (`*.pb.go`, `*.min.js`, snapshots and similar) and vendored code (`vendor`, `node_modules`, `third_party`, `dist`).
   * A file in the size exclude list counts for nothing: not for reach, sensitive ground or novelty either.
   * A component whose facts are unknown is 0, and its input is null on the card. Complexity and estimate against actual are not collected, and the card lists them in `notCollected`.
2. **Predicted.** From the first Run (the card's mint), the card carries a predicted score from what is known before the merge: the repositories of its plays (or its Work Target), the files and counted lines of its earlier merged plays, the diff size the forge reports for its current play (uncounted, since its file list is read at merge), and whether it is in an epic's set, which adds 10. It is recomputed on each read until the reveal.
3. **Revealed.** A card is revealed when its state is `merged`, it has a `release` (a recorded deploy, or the merge when the repository never reported one, [ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)), and the files of every merged play are recorded. The revealed score uses the files and lines of all its merged plays. A merged play whose files the forge never returned keeps the card unrevealed.
4. **Tier.** The cohort is the revealed cards of the same Work Target (the card's target repository, lowercased `owner/name`) whose reveal falls in the same calendar quarter in UTC, under the same formula. The percentile is `100 × (1 + cohort scores strictly below) / cohort size`, the card included, so a tie never lifts a card. Above 99 is legendary, above 95 epic, above 85 rare, above 60 uncommon, otherwise common: the top 1 %, the next 4 %, the next 10 % and the next 25 %. While the cohort holds fewer than 30 cards, fixed thresholds decide instead: 85 legendary, 70 epic, 55 rare, 35 uncommon, and `percentile` is null. A predicted score is ranked the same way in the cohort of the quarter it is read in.
5. **Freezing.** Revealing stores the revealed tier, score, percentile, cohort, inputs and the predicted tier in `card_rarity` (migration 0031), and they never change after, even when the cohort grows or the formula changes. A card read with a release freezes then and there. The 15-minute sweep (`SweepCardRarity`) reveals up to 25 released cards nobody read per tick, oldest check first, rechecking a card at most once an hour. Readers of one cohort take turns under an advisory lock, so two cards revealed at once are ranked against each other.
6. **Sets.** An epic's own card is legendary while its set is complete ([ADR-0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md)), whatever its own score; otherwise its tier is computed like any other card's. This follows the set as read: it is not frozen, so a set that reopens on an unmended crack drops its epic back. A child card's own tier is never lifted by its set, though set membership adds to its prediction.
7. **Card.** `rarity` is a `cardRarity` object or null: `formula`, `predicted`, `revealed`, `tier` (revealed, else predicted, except the set rule), `score`, `percentile`, `cohort` (`target`, `quarter`, `size`), `inputs` and `revealedAt`. `score`, `percentile`, `cohort` and `inputs` describe the revealed moment once revealed and the predicted one before. `rarity` is null before the first Run unless the card was revealed or is an epic with a complete set. The schema is `cardRarity` and `cardRarityInputs` in `operator-api.v1.schema.json`; the code is `pkg/rarity` and `pkg/store/card_rarity.go`.
8. **Line counts.** To count lines without lockfiles, Ploeg now keeps the lines each changed file added and removed beside its path (`pull_request_files.additions` and `.deletions`), as Forgejo's files endpoint reports them and as GitLab's diffs show them. A file the forge did not count stays null. A play recorded before this change counts its total diff size when none of its files is excluded, and is unknown otherwise.

This decision replaces point 8 of [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) for `rarity` only.

### Consequences

* Good, because every number on the card's back can be recomputed by hand from the printed inputs and the formula above.
* Good, because a padded change cannot buy a tier: size is log-damped and capped, lockfiles and generated files count for nothing, and cost, time and bounces are not inputs.
* Good, because percentiles keep legendary near 1 % in every project and quarter once a cohort reaches 30 cards.
* Bad, because the first card of a quarter to pass 30 that tops its cohort is legendary at once, so the start of a busy quarter is richer in legendaries than its end.
* Bad, because a card revealed early in a quarter is ranked against fewer cards than one revealed late, and freezing keeps that difference.
* Bad, because the predicted tier is coarse until a play's files are known: most cards are predicted common.
* Bad, because a forge read that fails at merge leaves a card unrevealed for good, since Ploeg does not re-read a merged play's files.
* Bad, because the weights, saturation points and thresholds are a first guess. Each tuning is a new formula version and a new record.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/rarity`: the score's table of cases (nothing known, one module, a migration, two repositories, saturation, repositories alone, the set bonus on a prediction only, the cap); fixed thresholds below 30 cards and percentile cut-offs from 30, including ties; the quarter in UTC; path pattern matching, the default sensitive and excluded paths, replacing defaults and adding attention paths, refused patterns; modules.
* `pkg/store`: counted lines with excluded, uncounted and truncated files; per-file lines kept, unknown ones null; no rarity without a Run or without options; a prediction from an open play's diff and from earlier plays' files; a reveal at the merge with novelty inside and outside the 180-day window; the tier frozen after the cohort grows; percentiles once a cohort holds 30 cards; no reveal without recorded files; sweep candidates and checks; an epic legendary while its set is complete.
* `pkg/httpapi`: a revealed card validates against `operator-api.v1`, an attention path counts as sensitive, and the sweep reveals a card nobody read.
* `pkg/config`: rarity rules load per Work Target and refuse bad patterns, duplicate patterns, rules without a repository and conflicting rules.
* `pkg/provider/forgejo` and `pkg/provider/gitlab`: per-file lines are read.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Fixed thresholds only

* Good, because a tier would not depend on the other cards.
* Bad, because a project with large changes would be all epic and a project of small fixes all common, so a tier would say more about the project than about the card.

### Compute on every read, never frozen

* Good, because no write path and no stored state.
* Bad, because a card's tier would drop as better cards join its cohort, and a formula change would re-tier every card silently.

### A mix of challenge and quality

* Bad, because the grade already measures quality. Mixing them would make two of the five axes say the same thing, which collectible games avoid (see the run cards trading-card research).

## More Information

* The owner's decision of 2026-10-02 sets the meaning (challenge), the two moments, the cohort and the tier shares.
* [Run cards](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/concepts/run-cards.md) explains the five axes; [mark sensitive paths for card rarity](../how-to/mark-sensitive-paths-for-card-rarity.md) shows the configuration.
* [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md): the card; [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md): the grade, versioned the same way; [ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md): the files kept at merge.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. This decision stays in force while the card endpoints are deprecated and is removed with them in the next minor release.

## Re-evaluation triggers

* A Work Target reaches 30 revealed cards in a quarter, and the owner compares the tiers against how hard the changes felt.
* Ploeg gains a source for complexity or estimates: they become inputs under a new formula version.
* Merged plays with unrecorded files exceed 5 % of a quarter's merged plays: Ploeg should re-read them.
* The owner asks for cohorts per Team, or for a reveal at acceptance instead of release.
