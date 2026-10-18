---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-18
---

# A release opens each consumer's update by running the consumer's own recipe

## Context and Problem Statement

A candidate is published with no hand step ([ADR 0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md)) and carries its contract bundle ([ADR 0117](0117-every-release-publishes-a-contract-bundle-for-consumers.md)). A consumer then waited for its own scheduled Renovate run, once to open the bump and once to merge it: on 2026-10-18 `v0.2.0-rc.27` published at 07:08 and sat until the hourly run. The owner rejected schedules as the way a release moves along the delivery chain and chose, on 2026-10-18, that the producer opens the bump pull request ("pattern 3"). How does a Ploeg release open a consumer's update without Ploeg knowing who its consumers are ([ADR 0069](0069-ploeg-names-none-of-its-consumers.md)) or how they pin it?

## Decision Drivers

* A release reaches each consumer in minutes, not at the next scheduled run.
* Ploeg names no consumer and knows nothing of how a consumer pins it; the consumer owns that.
* One writer per pin, so a consumer's own bot and the release do not race on the same file.
* Least privilege: a token per forge that may write one consumer repository, and a fork without it does nothing.
* The consumer's own required checks still decide the merge.

## Considered Options

* After each candidate, run each configured consumer's recipe in a clone of it, push a branch, open the pull request and ask the forge to merge it when checks succeed
* Trigger the consumer's Renovate run through the renovate-operator webhook
* Keep the consumer's scheduled Renovate run

## Decision Outcome

Chosen option: "Run each configured consumer's recipe", because the consumer keeps the knowledge of its pins in a file of its own and the release only needs to know where that file is.

* The `consumers` job of [the release candidate workflow](../../.github/workflows/release-candidate.yml) runs after `contract-bundle`, only when `cut` made a tag and the repository variable `RELEASE_CONSUMERS` is set. It holds a JSON list; each entry has `forge`, `url`, `repo`, `base` and `recipe`, and optionally `message` (one line with `{version}`, default `chore(deps): update ploeg to {version}`) and `merge_style` (default `merge`). One matrix job runs per entry.
* A recipe is an executable path in the consumer repository. [scripts/release-consumers.py](../../scripts/release-consumers.py) clones `base` over HTTPS, checks out `ploeg/<version>`, runs `<recipe> <version>` (without the `v`) from the checkout root, and commits whatever it changed, as the token's user. The recipe gets git, a POSIX shell, Python 3 and mise on `PATH`, and never sees the token: git reads it from a one-off `http.extraHeader` and the script removes it from the recipe's environment.
* The script force-pushes `ploeg/<version>`, opens its pull request into `base` or updates the open one, and asks Forgejo to merge it when checks succeed (`POST /repos/{owner}/{repo}/pulls/{index}/merge` with `merge_when_checks_succeed`), retrying while Forgejo still checks mergeability and accepting an existing schedule. It then closes every other open `ploeg/` pull request the same user opened, with a comment naming the new one. A rerun of the same release updates the same branch and pull request. A recipe that changes nothing opens nothing.
* Forgejo is the only supported forge. Its token is the repository secret `RELEASE_CONSUMER_TOKEN_FORGEJO`, named in the workflow, so the job does not read the whole secrets context; another forge adds its own secret and client.
* The consumer disables its own bot for the pins its recipe moves, and keeps a scheduled check that fails when it falls behind the newest release. The first consumer to do so is [Unfold ADR-0041](https://forgejo.webgrip.dev/webgrip/unfold/src/branch/development/docs/adr/adr-0041-a-ploeg-release-opens-unfolds-pin-pull-request-by-running-unfolds-recipe.md).

### Consequences

* Good, because a candidate opens each consumer's update minutes after its bundle is published, and the consumer's required checks merge it.
* Good, because a consumer changes how it pins Ploeg by editing its own recipe, with no Ploeg release.
* Bad, because the consumer's recipe runs on a GitHub runner and must install its own toolchain there, so its network reach is the runner's.
* Bad, because a consumer's code runs in Ploeg's release workflow with that consumer's token in the job; the token's reach is the limit, so it writes one repository only.
* Bad, because a stable release does not run the job yet: the `Release` run of a pushed stable tag starts only the contract bundle workflow.

### Confirmation

`bash scripts/test-release-consumers.sh`, run by `mise run verify`, drives the script against a local bare repository and a fake Forgejo. It fails when the branch lacks the recipe's change or the bot's identity, when the recipe can read the token, when a rerun opens a second pull request, when the merge is not retried after 405 or an existing schedule is refused, when a person's `ploeg/` pull request is closed or the bot's older one is not, when an unchanged consumer or a dry run is written to, and when an unsupported forge, a plain-HTTP URL, a URL with credentials, a recipe outside the repository, an unknown field or a non-release tag is accepted. `actionlint` and `zizmor` pass on the workflow. The first candidate after the variable and the secret are set shows a `consumers` job and a pull request on each consumer.

## Pros and Cons of the Options

### Trigger the consumer's Renovate run

* Good, because the consumer keeps one bot for every dependency.
* Bad, because a run already in progress drops the trigger, so each trigger needs a repeat, and Ploeg would need a public route into the deployment's Renovate operator.

### Keep the scheduled Renovate run

* Good, because nothing changes.
* Bad, because every hop waits for the schedule twice, which the owner rejected.

## Re-evaluation triggers

A second forge needs a consumer, Forgejo changes or removes `merge_when_checks_succeed`, a consumer's recipe needs a credential beyond its repository, a stable release needs to reach a consumer, or the renovate-operator learns to keep a trigger that arrives during a run.

## More Information

* Owner decisions of 2026-10-18 on the RFC "One delivery contract for every project", phase P1b (homelab-cluster `docs/techdocs/docs/rfc/rfc-unfold-release-train.md`): pattern 3, the producer opens the bump pull request; decision 1B, a dedicated Forgejo user with write access to the one consumer repository.
* Builds on [ADR 0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md) and [ADR 0117](0117-every-release-publishes-a-contract-bundle-for-consumers.md); keeps [ADR 0069](0069-ploeg-names-none-of-its-consumers.md): consumers are configuration.
