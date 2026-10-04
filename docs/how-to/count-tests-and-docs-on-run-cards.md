---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-02
verified_by: "Read apps/ploeg pkg/playkpi/{shape,complexity}.go, pkg/config/config.go, pkg/store/play_pipeline.go and pkg/httpapi/play_pipeline.go; go test ./pkg/playkpi ./pkg/config ./pkg/store ./pkg/httpapi; the Forgejo endpoints were probed against forgejo.webgrip.dev 15.0.2. Not checked against a live GitLab."
---

# Count tests and docs on Run cards

**Goal:** a Run card's change shape counts your repository's tests and documentation where they really live, and its CI timing and complexity show up.

**How it works:** when a pull request merges, Ploeg reads its diff once and measures its shape: indentation complexity, counted lines, test lines against the other lines, documentation files and languages ([ADR-0058](../adrs/0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md)). A file is a test or documentation when it matches a path pattern. Ploeg ships defaults; you can replace either list per Work Target, next to its `rarity:` rules. Files the Work Target leaves out of size (`rarity.sizeExclude`: lockfiles, generated and vendored code) count for nothing here either.

Read [Before you start](index.md#before-you-start) for names and the database session.

## 1. Check the defaults first

The default test paths are `**/test/**`, `**/tests/**`, `**/__tests__/**`, `**/spec/**`, `**/testdata/**`, `*_test.go`, `test_*.py`, `*_test.py`, `*.test.{js,jsx,ts,tsx,mjs,cjs}`, `*.spec.{js,jsx,ts,tsx,mjs,cjs}`, `*Test.java`, `*Tests.java`, `*Test.kt`, `*Test.php`, `*Test.cs`, `*Tests.cs`, `*_spec.rb` and `*_test.exs`.

The default documentation paths are `**/docs/**`, `**/doc/**`, `*.md`, `*.mdx`, `*.rst`, `*.adoc`, `README*` and `CHANGELOG*`.

The syntax is the rarity path syntax: a pattern without a slash matches a file name at any depth, any other pattern is anchored at the repository root, `**` spans directories, `*` and `?` stay within one directory, and `{a,b}` lists alternatives. If your layout fits, configure nothing.

## 2. Replace a list

Add `cardShape:` to a registered target, or to a tracker project that names its `repo`, under the chart's `config:` value:

```yaml
config:
  targets:
    app:
      repo: webgrip/app
      cardShape:
        testPaths:                       # replaces the default test paths
          - "**/*_test.go"
          - "web/test/**"
          - "scripts/qa/**"
        # docPaths: []                   # [] = no file counts as documentation
```

* `testPaths` replaces the default test paths. `testPaths: []` counts no file as a test, so `testLines` is 0 and `testRatio` is 0 or null.
* `docPaths` replaces the default documentation paths in the same way.
* Leave a key out to keep its defaults.

A repository named in two places must have the same rules in both. ploegd refuses to start on a pattern it cannot read, a pattern listed twice, more than 100 patterns in one list, an unknown key, or `cardShape:` on a project without `repo:`.

## 3. Roll it out and check

Apply the chart change. The rules apply to pull requests that merge from then on: a play's shape is measured once, at its merge, and kept.

Read a merged card's plays:

```sh
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://ploeg.example/api/v1/operator/work-items/<id>/card" | jq '.card.plays[] | {number, timeline, ciTiming, shape}'
```

To see which plays were measured and when their activity and CI were last read:

```sql
SELECT number, opened_at, activity_captured_at, ci_runs_captured_at, ci_runs_source,
       shape->'complexity'->>'added' AS complexity, shape->>'testRatio' AS test_ratio
FROM pull_requests ORDER BY updated_at DESC LIMIT 20;
```

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| `shape` is absent on a merged play | The play merged before ADR-0058 shipped, the merge was found by polling instead of a webhook, or the forge did not return its files | Nothing to back-fill; check `pull_requests.files_captured_at` and that the forge webhook reaches Ploeg |
| `shape.complexity` is null | The diff read failed | Look for "merged pull request diff not read" in ploegd's log |
| `testRatio` is null | A file's lines are unknown, the file list was truncated, or only tests changed | Expected; `testLines` still shows the test lines when they are known |
| A YAML or JSON-heavy change has high complexity | Indentation complexity counts nesting in any file | Add those paths to `rarity.sizeExclude` if they are generated; otherwise read complexity together with the hotspots |
| `ciTiming.queueSeconds` is null on Forgejo | The CI is not Forgejo Actions, so its statuses carry no "Waiting to run" step | Expected; runs, failures and minutes still count where a start is known |
| `ciTiming.source` is `statuses` | The Forgejo has no `actions/runs` endpoint, so each commit's checks count as one run | Upgrade Forgejo, or accept the coarser runs |
| `timeline.forcePushes` is null | GitLab reports no force pushes | Expected on GitLab |
| `timeline` only shows reviews, `comments` is null | The forge's activity was never read for this pull request | Wait for the next pull request event; reads repeat at most every 10 minutes and always at the merge |
