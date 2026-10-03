---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-09-27
verified_by: "Read apps/ploeg ops/helm/ploeg/{values.yaml,values.schema.json,templates/_helpers.tpl}, pkg/worker/{toolchain,verify,worker}.go, pkg/harness/skills and cmd/ploeg-worker/main.go"
---

# Give Runs a toolchain and checks

**Symptom:** agents open pull requests that fail formatting, linting or tests in CI, and the pull request says the checks were "left to CI" because the Run had no toolchain.

**Goal:** mount the target repository's toolchain into every Run, tell the agent which checks to run, and have the worker run them again after a writing Run and post the result on the pull request. [ADR-0035](../adrs/0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) records the design.

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
3. After a writing Run opens or updates a pull request, the pod log has `verified the writing Run's checkout`, the Run's summary ends with `[Ploeg verification passed]` or `[Ploeg verification failed: <command>]`, and the pull request has a comment headed "Ploeg verification" when the Run belongs to a Shift.

The worker also sends the result as a structured `verification` record on the Run's outcome: the full commit, whether the working tree was dirty, and each check with its exit status and times. Ploeg stores it with the Run, and the usage report on the pull request takes the result and commit from it. The summary and findings text is for people to read; an agent cannot change the reported result by writing a marker of its own.

## What the agent gets

Every writing and reading Run gets Ploeg's own skills under its `HOME`: `ploeg-verify-before-handoff`, and for readers `ploeg-review-against-work-item`. The environment variable `PLOEG_SKILLS_DIR` names their directory, `PLOEG_VERIFY_SCRIPT` names a script that runs the `verify` lines, and the prompt lists the skills, the toolchains and the checks. Planners get none of them.

| Symptom | Cause | Fix |
| --- | --- | --- |
| The pod exits with `toolchain "<name>": ... no such file or directory` | The image volume did not mount, or `path` names a directory the image lacks | Check the image reference and the path inside the image, and that the nodes run Kubernetes 1.35 or later |
| `go test` fails with `dial tcp ... i/o timeout` | Module downloads are blocked | Follow [step 3](#3-check-dependency-downloads) |
| The summary has no `Ploeg verification` part | The Run did not open or update a pull request, or it read rather than wrote | Only writing Runs with a pull request are verified by the worker |
| `go: downloading go1.x` then a timeout | `go.mod` asks for a newer toolchain | Set `GOTOOLCHAIN: local` and use an image at least as new as `go.mod` asks |
