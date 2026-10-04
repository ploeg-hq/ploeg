---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-02
verified_by: "Read pkg/rarity/{paths,rarity}.go, pkg/config/config.go, pkg/store/card_rarity.go and pkg/httpapi/card_rarity.go; go test ./pkg/rarity ./pkg/config ./pkg/store ./pkg/httpapi. Not checked against a live forge."
---

# Mark sensitive paths for card rarity

**Goal:** a Run card's rarity counts the parts of your repository that are risky to change, and leaves out files that only make a change look bigger.

**How it works:** rarity is a challenge score from four facts of the merged change: how many modules and repositories it reached, how many sensitive files it touched, how many files were new ground, and how many lines it changed ([ADR-0056](../adrs/0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md)). Ploeg ships defaults for what is sensitive and what does not count as size. You can add your own sensitive paths per Work Target, or replace either list.

Read [Before you start](index.md#before-you-start) for names and the database session.

## 1. Add attention paths

Most repositories only need `attentionPaths`: the code where a change deserves extra care, on top of the defaults. Add `rarity:` to a registered target, or to a tracker project that names its `repo`, under the chart's `config:` value:

```yaml
config:
  targets:
    app:
      repo: webgrip/app
      rarity:
        attentionPaths:
          - "pkg/store/**"                # budgets, leases and the ledger
          - "**/budget*.go"
          - "ops/helm/"                    # a trailing slash means everything below
```

A pattern without a slash matches a file name at any depth (`Dockerfile`). Any other pattern is anchored at the repository root. `**` spans directories, `*` and `?` stay within one directory, and `{yml,yaml}` lists alternatives.

## 2. Replace a default list, if you must

* `sensitivePaths` replaces the default sensitive paths: migrations, SQL, JSON schemas, protobuf, OpenAPI files, `Dockerfile`, Helm charts and CI workflows. `sensitivePaths: []` keeps none of them.
* `sizeExclude` replaces the default list of files whose lines do not count: lockfiles, generated files and vendored code. A file on this list counts for nothing in rarity, not for reach or novelty either.

```yaml
      rarity:
        sizeExclude: ["**/*.lock", "**/generated/**", "docs/api/**"]
```

A repository named in two places must have the same rules in both. ploegd refuses to start on a pattern it cannot read, a pattern listed twice, more than 100 patterns in one list, or `rarity:` on a project without `repo:`.

## 3. Roll it out and check

Apply the chart change. The rules apply to cards revealed from then on: a card's rarity is frozen when it is revealed, so cards revealed earlier keep theirs.

Read a released card and look at its rarity inputs:

```sh
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://ploeg.example/api/v1/operator/work-items/<id>/card" | jq '.card.rarity'
```

`inputs.sensitive.paths` lists the sensitive files the change touched. To see which cards are revealed and in which cohort:

```sql
SELECT work_item_id, revealed_tier, predicted_tier, score, percentile, cohort_target, cohort_quarter, cohort_size
FROM card_rarity WHERE revealed_tier IS NOT NULL ORDER BY revealed_at DESC LIMIT 20;
```

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| `rarity.revealed` stays null on a merged card | The card has no release yet, a play is still open, or the forge did not return a merged play's files | Check `release` on the card; check `pull_requests.files_captured_at` for its plays |
| `inputs.size.countedLines` is null | A play was recorded before per-file lines were kept and touched an excluded file | Nothing to fix; size adds nothing for that card |
| A tier did not change after you edited the rules | Revealed tiers are frozen | Expected; only cards revealed after the change use the new rules |
| ploegd refuses to start naming `rarity` | A pattern is invalid, duplicated, or set differently for one repository | Fix the pattern named in the error |
