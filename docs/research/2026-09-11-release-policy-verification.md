# Experimental release policy verification

The corrected policy calculates **v0.3.0-rc.5** from the actual repository history after withdrawal of the mistaken semantic-version tag. This is a local calculation, not evidence that the replacement was published.

The [test suite](https://forgejo.webgrip.dev/webgrip/glide/src/commit/700a3d97a614a66371ebbc78a1796ac87e0ae395/apps/ploeg/scripts/release-policy.test.cjs) ran in the exact CI image `harbor.webgrip.dev/webgrip/semantic-release:0.3.3`, digest `sha256:6a0b7c4369320b8ca5d676bb1d6f6fc675d2d56cf738824b5ca9a855334b930f`, with the repository mounted read-only, networking disabled and a temporary filesystem for shell-test outputs. Installed versions were semantic-release 25.0.9, commit-analyzer 14.0.0-beta.3, and shared configuration 1.2.3.

All 14 tests passed. They reproduced the old major result using the real historical [breaking commit](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9028cf0718c65acbfc4f8bfd1be71f32417221ce), proved the corrected minor result, generated release notes retaining the managed-authentication warning, loaded the local guards through the installed plugin pipeline, and executed the actual artifact-publisher shell against accepted, rejected and injection-shaped tags. Reusable artifact jobs also passed explicit failed-prerequisite checks.

The opt-in current-history test used semantic-release's installed tag discovery, commit reader, last-release selector and next-version calculator. After fetching origin's release-channel notes, its result was:

```json
{"lastRelease":"v0.3.0-rc.4","analyzedCommits":7,"type":"minor","nextRelease":"v0.3.0-rc.5","publication":false}
```

The local checkout originally lacked release-channel notes and selected the older stable baseline. CI fetches these notes before selecting a release; the local proof fetched the same notes without restoring the withdrawn tag. No release API, remote write, tag creation or publication was performed by the test suite. The opt-in historical assertion is specific to this correction and is not enabled in future CI runs; the fixed historical regression always runs.

The installed engine checks conditions before existing-release promotion, then checks the computed version before preparation, tagging and publication. [ADR 0028](../adrs/0028-automatic-releases-stay-zero-major-candidates.md) records why both guards are required. [The artifact workflow](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/.forgejo/workflows/on_release_published.yml) independently rejects non-experimental tags, including manual dispatch. An independent review identified Forgejo's reusable-workflow caller-gate limitation; each reusable call now receives a guarded `enabled` value as well.

Repository validation passed: `mise exec -- go test ./...`, `go vet ./...`, `go build ./...`, formatting, Helm lint, all four committed Helm golden renders, brand checks, the ADR ledger and all 10 OpenSpec items. Unchanged Go packages used the normal Go test cache. These checks do not assert a deployed runtime migration or a GA release.

## Publication recovery

[Source CI run 251](https://forgejo.webgrip.dev/webgrip/ploeg/actions/runs/251) passed repository checks and the release-policy tests. Its actual release process selected `0.3.0-rc.5`, passed both guards, updated the chart and changelog, and pushed [aec8a87](https://forgejo.webgrip.dev/webgrip/ploeg/commit/aec8a8717660d008bf1d68c6652f3c5e32866e2b) with the correct tag. Forgejo then returned HTTP 500 when the plugin tried to create the release page. The cause of that server error was not established.

Authenticated reads confirmed that the tag existed at the expected commit and that no published or draft release existed for it. Creating the release from that same tag, with the unchanged generated notes and explicit target commit, returned HTTP 201 as release 14503. [The corrected release](https://forgejo.webgrip.dev/webgrip/ploeg/releases/tag/v0.3.0-rc.5) is marked prerelease. Recovery did not change the version, move the tag or rewrite commits. It triggered [artifact publication run 252](https://forgejo.webgrip.dev/webgrip/ploeg/actions/runs/252).

## Reusable publisher correction

[Run 252](https://forgejo.webgrip.dev/webgrip/ploeg/actions/runs/252) passed tag parsing, built the Harbor image and completed signing, but skipped all six reusable publisher leaf jobs. Its overall success and successful wrapper jobs did not establish chart or mirror publication. The earlier local expression tests supplied job-result values that Forgejo does not supply at this boundary, so their positive-path result was insufficient.

Forgejo 15.0.2 [reparses deferred workflow-call inputs](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.2/services/actions/job_emitter.go#L300) with job outputs and workflow needs but without job-result values. The `needs.<job>.result == 'success'` conditions therefore denied valid releases at this stage. The corrected callers use only `needs.parse-release-tag.outputs.version != ''`, the documented reusable-input pattern. That output exists only after the strict zero-major candidate check passes. The existing Harbor dependency remains on both mirror callers.

The regression now executes the actual parse shell, supplies its resulting version to the reusable-input expression, and tests both absent and empty result fields. The [pinned runner's context construction](https://code.forgejo.org/forgejo/runner/src/tag/v12.8.0/act/jobparser/jobparser.go#L77) uses an empty string when no result is supplied. The regression failed against the previous workflow for the valid `v0.3.0-rc.5` case before the correction. It also rejects major, stable and empty tags because those cases emit no version. Live validation must inspect the chart and mirror leaf jobs, not just the overall workflow status.

The corrected suite passed all 13 regular tests in the same exact CI image; its one-time correction-history test was intentionally skipped because `v0.3.0-rc.5` now exists. All 10 OpenSpec items, the ADR ledger and whitespace checks passed. No application or chart source changed in this follow-up.

[Source CI run 254](https://forgejo.webgrip.dev/webgrip/ploeg/actions/runs/254) then passed and automatically published [v0.3.0-rc.6](https://forgejo.webgrip.dev/webgrip/ploeg/releases/tag/v0.3.0-rc.6) at [97722a2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/97722a233fd486c7ec4cf18e6a54ea79f28945a8), without manual recovery. [Artifact run 255](https://forgejo.webgrip.dev/webgrip/ploeg/actions/runs/255) is the publication evidence for this version; inspect the actual publishing jobs, because successful wrapper jobs alone did not prove publication in run 252.

A direct `mise exec -- helm show chart oci://harbor.webgrip.dev/webgrip/charts/ploeg --version 0.3.0-rc.6` read succeeded. The published chart digest was `sha256:348a07babca3f542277d66fe9096fb18990c59108d32a4c7980f40328e955fe2`; both `version` and `appVersion` were `0.3.0-rc.6`.
