---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A worker container meets the restricted Pod Security Standard by default

## Context and Problem Statement

The worker pod runs an agent harness on code Ploeg does not control. Its init container and the sandbox launcher already meet the Kubernetes `restricted` Pod Security Standard, but the harness container itself had no `securityContext`. It ran as whatever user the harness image names, which for common agent images is root, with privilege escalation allowed, the runtime's default capabilities and no seccomp profile at pod level.

A namespace that holds worker pods can therefore enforce no more than `baseline`. A server dry run of the rendered worker Jobs and SandboxTemplates against a `restricted` namespace reported `allowPrivilegeEscalation != false`, `unrestricted capabilities` and `runAsNonRoot != true` for the `worker` container. Should the chart give the harness container a restricted context by default?

## Decision Drivers

* The harness runs code an agent wrote or fetched; it is the container most worth confining.
* A namespace should be able to enforce `restricted`, not only warn about it.
* The worker already sets `HOME`, `TMPDIR` and the XDG directories under its shared work directory, which `fsGroup: 1000` makes writable, so the harness needs no writable image path.
* A harness image that truly needs root must still be runnable, visibly.

## Considered Options

* Give the harness container a restricted context by default, overridable per deployment
* Keep no context and document how to add one
* Make the restricted context opt-in

## Decision Outcome

Chosen option: "Give the harness container a restricted context by default, overridable per deployment", because it is the only option where a deployment that changes nothing can enforce `restricted`.

1. `executor.workerSecurityContext` sets the harness container's `securityContext`. Its default is `runAsNonRoot: true`, `runAsUser: 1000`, `runAsGroup: 1000`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]` and `seccompProfile: RuntimeDefault`. The uid matches the existing `fsGroup`.
2. The worker pod's own `securityContext` adds `seccompProfile: RuntimeDefault`.
3. The same template feeds ScaledJobs, CronJobs and SandboxTemplates, so all three change together. The DinD sidecar stays privileged; a Team with `dind: true` still needs its exception.
4. A deployment whose harness image must run as root sets `workerSecurityContext: {}` or its own context, and its namespace can then enforce no more than `baseline`.

### Consequences

* Good, because a namespace holding only workers without DinD can enforce `restricted`.
* Good, because a harness that escapes its process gains no capabilities and cannot raise its privileges.
* Bad, because a harness image that writes outside the work directory, or installs packages as root at run time, fails until its image or its context changes.
* Neutral, because the uid is fixed at 1000; an image built around another uid sets its own context.

### Confirmation

`scripts/helm-golden.sh check` holds the rendered context in every executor golden. Before this change, a server dry run of the rendered worker pods as Jobs in a namespace that warns on `restricted` printed the three violations above; with it, the same dry run printed none. The agent image used by the reference deployment ran `opencode`, `qwen`, `goose`, `openspec`, `uv` and a `git clone` as uid 1000 with `HOME` under the shared work directory.

## Pros and Cons of the Options

### Keep no context and document how to add one

* Good, because no existing harness image can break.
* Bad, because every deployment stays at `baseline` until someone reads the documentation.

### Make the restricted context opt-in

* Good, because a deployment chooses when to test its images.
* Bad, because the safe setting is the one nobody turns on.

## Re-evaluation triggers

* A supported harness image needs root at run time.
* Kubernetes changes what `restricted` requires.
* A Team needs a different uid per Role.
