# Design — open-change-requests-on-gitlab

Change-local rationale only. The durable commitment is ADR-0023.

## What the code already did, and why that shrank this change

`docs/architecture.md` §9 warns that it goes stale in both directions, so the
starting point was reading the shipped binaries and the chart rather than the
docs. Three findings removed most of the anticipated work:

1. `pkg/worker/git.go` builds clone and push URLs with `url.UserPassword` and a
   path of `/owner/repo.git`. For a GitLab subgroup project — owner `acme`,
   name `internal/widgets` — that already yields the correct three-segment
   path. Git is git; no dialect is needed to clone or push, which is why this
   arrived late and only in two places.
2. `pkg/shiftengine`'s `prPathRe` is `/(?:pulls?|merge_requests)/(\d+)/?$`. It
   has matched GitLab merge-request URLs all along.
3. `pullRequest(reports)` takes the Shift's change request from the writing
   Run's OutcomeReport links. The forge poll is ground truth for the Run that
   opened one and died before reporting, not the primary path — so a working
   briefing gets the loop closing even before the poll is right.

## Where the dialect is decided

ADR-0023. The short version for a reader of this change: `Target.Forge` already
existed and ploegd already read it, so putting the worker on the same field was
the only option under which the two halves cannot disagree. Deployment config
supplies the default for a Target that names none, matching the "empty = the
default forge" promise `pkg/work.Target` and the forge registry already make.

## Why the noun is in scope

The obvious minimal change is the endpoint alone. It is not enough: the
briefing is a prompt, and an agent told to open a "pull request" on GitLab goes
looking for a pull-request API, finds none, and improvises. `changeRequestNoun`
and `openChangeRequestInstruction` therefore switch on the same dialect value
and sit next to each other, so a future forge cannot get one and not the other.

## The chart's nil-pointer, fixed in passing

`ploeg.workerPodTemplate` dereferenced `.Values.executor.forgejo.url`
unconditionally. `forgejo: null` — the documented way to empty a block whose
defaults name Secrets the cluster has no reason to hold — therefore became a
nil pointer the moment `executor.enabled` flipped to true. Selecting GitLab
would have hit it immediately.

`ploeg.forge` resolves the active block with `index` and a default, which
tolerates a null or absent sibling. The `executor-gitlab` fixture renders with
`forgejo: null` deliberately, so the trap cannot return without failing a
golden.

## Contract discipline

`repo` in `taskspec.v1.schema.json` is `additionalProperties: false`, so adding
`RepoRef.Forge` without the schema edit is a contract break. The fixture in
`pkg/harness/contract_test.go` is named `fullTaskSpec` and must carry every
field: it was updated first, observed to FAIL with
`additional properties 'forge' not allowed`, and the schema edited after. That
ordering is the point — the gate is only worth having if it was seen to catch
this.

## No new claim or queue predicate

`ops/helm/ploeg/templates/scaledjob.yaml`'s scaler query is untouched. This
change adds no state a Run is claimed on: the dialect is read from a Work Item
the scaler already counts, after the claim. Nothing here needs to stay in sync
with `store.ClaimRole` or `store.PendingRuns`.

## Open questions

- **Tier 2 on GitLab** is a non-goal here and a named re-evaluation trigger on
  ADR-0023, not an oversight. Until a GitLab project access token can be minted
  and revoked per Run with repository-scoped push rights, GitLab runs on the
  shared token and only scheduling separates reader from writer at tier 2.
  Tier 1 is configured and pinned by a golden.
- **The worker's forge env is renamed, not aliased.** `FORGE_URL` replaces
  `FORGEJO_URL`, and the dialect reuses ploegd's existing `PLOEG_TARGET_FORGE`
  rather than a second spelling. Both are breaking for anyone setting them by
  hand; neither is for a chart-driven deployment, because the release train
  keeps chart and image in lockstep. An accepted-alias shim was written first
  and removed: two names for one value is a worse thing to own than a rename
  under a version bump.
- **A GitLab webhook secret** (`executor.gitlab.webhookSecret`) is wired
  through to ploegd, but nothing in this change verifies end to end that a
  GitLab review comment opens a fix Round — that needs a live GitLab and is the
  downstream consumer's acceptance test, listed in tasks group 5.
