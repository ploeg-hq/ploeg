---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-10-24
verified_by: "Read ops/helm/ploeg/{values.yaml,values.schema.json,templates/_helpers.tpl}, pkg/worker/{toolchain,verify,basesync,worker}.go, pkg/shiftengine/{engine,reviewloop,forge_ci_gate,review}.go, pkg/config/{ci,fast_check}.go, pkg/provider/{forgejo,github,gitlab}, pkg/harness/skills and cmd/ploeg-worker/main.go"
---

# Give Runs a toolchain and checks

**Symptom:** agents open pull requests that fail formatting, linting or tests in CI, and the pull request says the checks were "left to CI" because the Run had no toolchain.

**Goal:** mount the target repository's toolchain into every Run, tell the agent which checks to run, and have the worker run them again on the pushed commit after a writing Run and post the result on the pull request. A Work Item reaches `awaiting_review` only when those checks passed and the forge's own CI passed on the pull request's head. [ADR-0035](../adrs/0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) records the design, [ADR-0070](../adrs/0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md) the rule for the worker's checks, and [ADR-0119](../adrs/0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md), which supersedes it, adds the forge's CI.

Read [Before you start](index.md#before-you-start) for names. Make the change in the GitOps repository's HelmRelease values, not with `kubectl`.

## 1. Pick a toolchain image

Choose an image that holds the toolchain at a fixed path, pulled through your registry proxy and pinned by digest, for example the official `golang` image. The kubelet pulls it, not the Run, so the worker pod's egress policy does not apply to the pull.

The toolchain's binaries must run in the agent image. Go's binaries are statically linked and run anywhere. A dynamically linked toolchain, such as Node.js, needs an image built for the same C library as the agent image (musl for the Alpine-based `agent-runner`).

## 2. Configure the harness block

Set `toolchains` and `verify` on `executor.harness`, a team's `harness` block or a Role's `harness` block. The most specific block wins, one field at a time:

```yaml
executor:
  teams:
    - name: silver
      harness:
        toolchains:
          - name: go
            image: harbor.webgrip.dev/dockerhub/library/golang:1.27-alpine@sha256:<digest>
            path: [/usr/local/go/bin]
            env: {GOTOOLCHAIN: local}
        verify:
          - test -z "$(gofmt -l .)"
          - go vet ./...
          - go test ./...
        verifyTimeout: 20m
```

- `path` lists directories inside the image. The chart mounts the image at `/opt/ploeg/toolchains/<name>`, and the worker puts `/opt/ploeg/toolchains/<name>/<path>` in front of the harness's `PATH`.
- `env` is added to the harness environment. `GOTOOLCHAIN=local` stops `go` from downloading the toolchain a `go.mod` asks for.
- `verify` lines run with `sh -c` from the repository root, in order, and stop at the first failure. A formatter that lists files and exits 0 needs a wrapper that fails on output, as above.

Image volumes need Kubernetes 1.35 or later.

## 3. Check dependency downloads

A check that downloads dependencies fails in the sandbox unless the worker pods can reach a source for them. Go modules come from `proxy.golang.org`, which default-deny egress blocks. Either allow egress to an in-cluster module proxy and set `GOPROXY` in the toolchain's `env`, or use an image that already carries the module cache and set `GOMODCACHE` and `GOFLAGS=-mod=mod` with `GOPROXY=off`. Without either, keep only dependency-free checks such as `gofmt` in `verify`.

## 4. Verify

After Flux applies the release:

1. A worker pod starts only when every toolchain directory exists. If one is missing, the pod log says `toolchain "<name>": ...` and the pod exits before it claims anything.
2. The pod log of a Run has `prepared the Run's sandbox` with the counts of skills, toolchains and verify commands.
3. After a writing Run opens or updates a pull request, the pod log has `verified the pushed commit`, the Run's summary ends with `[Ploeg verification passed]`, `[Ploeg verification failed: <command>]` or `[Ploeg verification incomplete: <reason>]`, and the pull request has a comment headed "Ploeg verification" when the Run belongs to a Shift.

The worker runs the checks in a fresh clone of the Run's branch, not in the agent's checkout, and only when that clone is at the pull request's head. If the branch moves while the checks run, the result is `incomplete`. The worker also sends the result as a structured `verification` record on the Run's outcome: the full commit, and each check with its exit status and times. Ploeg stores it with the Run, and the usage report on the pull request takes the result and commit from it. The summary and findings text is for people to read; an agent cannot change the reported result by writing a marker of its own.

## The forge's CI

After the worker's checks pass, the Shift does not settle until the forge reports the combined status at the pull request's head. A forge that reports no status for the commit counts as passed, so a repository without CI keeps working.

- **Pending.** The Shift stays open and the sweeper reads the CI again on every pass, up to the route's `ci.waitTimeout`, then the Work Item goes to `needs_human` with close reason `ci_not_finished`. Set it on a target or a project in ploegd's config file:

  ```yaml
  targets:
    ploeg:
      repo: webgrip/ploeg
      ci:
        waitTimeout: 90m
  ```

- **Failed because of the change.** A failing check the forge does not mark cancelled, and that does not fail on the pull request's base commit too, re-opens the writing Round like a failed verification, within the same fix-round cap and budget. The writer's briefing names each failing check and its link; Ploeg does not fetch the log. A reviewer's approval does not override it.
- **Cancelled, or failing on the base too.** Ploeg asks the forge to re-run CI on the head once, without a fix round. If it still does not pass, the Work Item goes to `needs_human` with close reason `ci_not_passed`. GitHub re-runs the failed and cancelled jobs of the commit's workflow runs. Ploeg's Forgejo and GitLab providers cannot re-run CI: Forgejo's API has no re-run call for Actions, and the GitLab provider does not call GitLab's pipeline retry yet. On them the Work Item goes to `needs_human` straight away.
- **After the hand-off.** When the head CI of an item already in `awaiting_review` stops being green, the review reconcile applies the same wait and re-run, then moves it to `needs_human`. The Shift that wrote the pull request has closed, so no fix round runs and no new Shift starts by itself.

Forgejo has no cancelled state: a cancelled job reports `failure` with the description `Has been cancelled`, and that text is how Ploeg recognizes it.

## Keep pull requests on top of their base

A Work Item reaches review only when its pull request contained its base branch at the hand-off ([ADR-0146](../adrs/0146-a-writing-run-rebases-onto-its-base-branch-and-passes-its-repositorys-fast-check-before-review.md)). Every writer is told to rebase onto the base before its last push and to resolve conflicts itself. After it exits, the worker reads the pull request's head and base on the forge:

- The head already contains the base: nothing changes.
- The base moved and the rebase is clean: the worker rebases, runs the repository's fast check on the rebased tree, and pushes with a lease on the head it read. The summary ends with `[rebased onto <base>]`.
- The rebase conflicts: the worker pushes nothing, the Run ends `stuck` naming the conflicting paths, and the Work Item goes to `needs_human`.
- The fast check fails on a clean rebase: the worker pushes nothing, and the Run ends `stuck` naming the failing command.

The result is the "Ploeg base sync" part of the Run's findings. Declare the fast check on a target or a project in ploegd's config file. Keep it to commands that finish in seconds to a few minutes and need no git history: the worker runs it on the rebased files without `.git`. Without one, a clean rebase is pushed and the check is recorded as skipped.

```yaml
targets:
  shop:
    repo: acme/shop
    fastCheck:
      - test -z "$(gofmt -l .)"
      - go vet ./...
teams:
  silver:
    forgeFollowUps:
      rebaseOnConflict: true
```

`rebaseOnConflict` covers conflicts that appear after the hand-off. When the review reconcile confirms a conflict on a pull request in review (two polls at one head), it queues a rebase Follow-Up on the same branch for the Team that owns it. The Follow-Up shares `maxRepairs` with failed-check repairs.

## What the agent gets

Every writing and reading Run gets Ploeg's own skills under its `HOME`: `ploeg-verify-before-handoff`, and for readers `ploeg-review-against-work-item`. The environment variable `PLOEG_SKILLS_DIR` names their directory, `PLOEG_VERIFY_SCRIPT` names a script that runs the `verify` lines, `PLOEG_FAST_CHECK_SCRIPT` names one that runs a writer's repository fast check, and the prompt lists the skills, the toolchains and the checks. Planners get none of them.

| Symptom | Cause | Fix |
| --- | --- | --- |
| The pod exits with `toolchain "<name>": ... no such file or directory` | The image volume did not mount, or `path` names a directory the image lacks | Check the image reference and the path inside the image, and that the nodes run Kubernetes 1.35 or later |
| `go test` fails with `dial tcp ... i/o timeout` | Module downloads are blocked | Follow [step 3](#3-check-dependency-downloads) |
| The pull request is open but the Work Item went back to its writer or to `needs_human` | The checks failed or were incomplete on the pushed commit; a configured plan re-opens the writing Round while fix rounds remain, otherwise the Shift closes as `checks_not_passed` | Read the "Ploeg verification" comment, fix the code or the check, and re-assign the Work Item |
| The Work Item went to `needs_human` with `ci_not_finished` | The forge's CI on the pull request's head was still running when the route's `ci.waitTimeout` ran out | Wait for CI and re-assign, or raise `ci.waitTimeout` for the repository |
| The Work Item went to `needs_human` with `ci_not_passed` | The forge's CI failed on checks the writer cannot fix: cancelled, or failing on the base branch too, and one re-run did not help | Read the message: it names each check and why it was not sent to the writer. Fix the base branch or the CI, then re-assign |
| The Work Item went to `needs_human` and the Run says the pull request conflicts with its base | The base branch changed the same lines and the agent could not resolve them without choosing between two behaviours | Read "Ploeg base sync" for the paths, resolve them on the branch or rebase it, and re-assign |
| The summary has no `Ploeg verification` part | The Run did not open or update a pull request, or it read rather than wrote | Only writing Runs with a pull request are verified by the worker |
| `go: downloading go1.x` then a timeout | `go.mod` asks for a newer toolchain | Set `GOTOOLCHAIN: local` and use an image at least as new as `go.mod` asks |
