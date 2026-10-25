---
status: proposed
date: 2026-10-25
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# The chart refuses values that leave profile P1 unless they declare the deployment unsupported

## Context and Problem Statement

Profile P1 is the one deployment the [threat model](../security/threat-model.md#the-supported-deployment-profile) covers, and the maintainer confirmed it on 2026-10-25 ([#15](https://github.com/ploeg-hq/ploeg/issues/15)). The chart renders P1 by default since [ADR 0136](0136-the-chart-default-is-the-strict-isolation-profile-and-privileged-dind-is-an-acknowledged-opt-in.md), but a single value still leaves it without a word: `networkPolicy.enabled: false`, `executor.workerAuth.mode: legacy`, a credential proxy set to `""`, a `credentialIsolation` override, an unqualified harness, `harnessContainer: shared`, a worker securityContext below `restricted`, the sandbox executor or the claude-runner. Finding [F-11](../security/threat-model.md#findings) records that gap, and [#16](https://github.com/ploeg-hq/ploeg/issues/16) requires such settings to fail clearly or be marked experimental. How does a values set say that it leaves P1, and what does the chart do when it does not say so?

## Decision Drivers

* A deployment outside the threat model must not look like one inside it, to its operator or to a reviewer reading the values.
* The way out must be written down in the values, the way ADR 0136 made privileged DinD a written acknowledgement.
* An upgrade must fail loudly with every departure named, never change a running deployment's shape silently.
* The chart can only check what it renders; the namespace's Pod Security level, the CNI, TLS and the gateway's version stay the installer's.

## Considered Options

* One top-level `supportProfile` value, `P1` by default: under `P1` the render fails and lists every departure; `unsupported` renders them and lists them in the install notes
* An acknowledgement value per departure, like `acknowledgePrivilegedDind`
* A warning in the install notes only
* Remove every setting that leaves P1

## Decision Outcome

Chosen option: "**One top-level `supportProfile` value**", because one value states one fact, that this deployment is outside the threat model, and the failure lists every reason at once so an operator fixes or acknowledges them in a single pass.

1. `supportProfile` is `P1` (the default) or `unsupported`; the schema refuses anything else.
2. Under `P1` the render fails when any of these holds, naming each one with the P1 value it needs: `networkPolicy.enabled: false`; `executor.workerAuth.mode: legacy`; `claudeRunner.enabled: true`; and, with the executor enabled, `executor.type: sandbox` or a team's `executorType: sandbox`; `executor.litellm.keyIsolation` or `executor.forgeTokenIsolation` other than `proxy`; `executor.workerSecurityContext` without `runAsNonRoot: true`, with `runAsUser: 0`, without `allowPrivilegeEscalation: false`, with `privileged: true`, without `ALL` dropped, with a capability added, or with a seccomp profile other than `RuntimeDefault` or `Localhost`; the derived worker container context failing the same checks or without a read-only root filesystem; and, for each worker workload, a resolved `harness.dind: true`, a harness outside `ploeg.isolationQualifiedHarnesses`, any `harness.credentialIsolation` override, or `harnessContainer` resolving to `shared`.
3. Under `unsupported` the same values render unchanged, and `NOTES.txt` lists each departure. An `unsupported` values set that meets P1 gets a note asking to set `P1` back.
4. The guards that already refuse a value keep their own message and run first: `acknowledgePrivilegedDind` (ADR 0136), a separate harness container with DinD (ADR 0138), and `security.workerAdmission` with DinD. A DinD workload therefore needs both its acknowledgement and `supportProfile: unsupported`: the first records the node-root hazard where it is taken, the second that the deployment is outside the model.
5. The P1 rows the chart cannot see stay with the installer and are not checked: Pod Security Admission on the namespace, a CNI that enforces NetworkPolicy, transport encryption, the gateway's version, the forge and its per-Run minting, Shift plans and Work Targets, webhook secrets, `ploeg-mcp` execute groups and registry egress.
6. Values that rendered before and leave P1 now fail until they set `supportProfile: unsupported`. The break is recorded against `v0.2.0-rc.72` in [breaking-changes.yaml](../contracts/breaking-changes.yaml) and ships as a `BREAKING CHANGE`.

### Consequences

* Good, because F-11's settings no longer render under the supported label: a values set either meets P1 or says in one place that it does not.
* Good, because the failure names every departure, so an upgrade is one edit, not a loop of renders.
* Bad, because existing deployments outside P1 fail their next render until they add one value. Accepted: the message names the value, and the alternative is a deployment that silently claims coverage it does not have.
* Bad, because `unsupported` is coarse: once set, a further departure renders without another acknowledgement. The install notes list the current departures on every upgrade, which is where a reviewer sees them.
* Neutral: [ADR 0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md), still proposed, names `executor.allowLegacy` for a per-Run isolation class. That gate is about one Run's class; `supportProfile` is about the deployment, and the two can coexist.

### Confirmation

* `scripts/helm-golden.sh check`, run by `mise run verify` and CI's `verify` job, renders `executor-default-worker-values.yaml` with one departure at a time (fifteen cases covering every condition in item 2) and requires each to fail with its own message under `P1` and to render under `unsupported`. It also requires the default values and `executor-default-worker-values.yaml` to render, `supportProfile: P2` to fail on the schema, the install notes of an `unsupported` render to list its departures, and the P1 notes to carry none. Removing the guard made all fifteen cases fail the script, and a guard that always fails broke the default render, before this record was written.
* `scripts/test-chart-network-policies.sh` renders `networkPolicy.enabled: false` with `supportProfile: unsupported`, so its "no policy" assertion cannot pass on a failed render.
* `go test ./internal/compat/` sees an added optional property; the behavioural break is recorded in `docs/contracts/breaking-changes.yaml` under this record.

## Pros and Cons of the Options

### An acknowledgement value per departure

* Good, because each hazard is acknowledged where it is set, as DinD is.
* Bad, because ten more booleans across four levels of the override chain is a value surface nobody can review, and the threat model's question is whether the deployment is covered, which is one answer.

### A warning in the install notes only

* Good, because nothing breaks.
* Bad, because GitOps controllers do not show notes, so the warning reaches nobody, and #16 asks for a clear failure or an explicit designation.

### Remove every setting that leaves P1

* Good, because the chart could then render only the supported profile.
* Bad, because DinD gates, the claude-runner and the sandbox executor serve real deployments, and their removal is each its own decision under ADR 0134's deprecation window.

## Re-evaluation triggers

* A second supported profile is defined in the threat model, which turns `supportProfile` into a choice among profiles.
* ADR 0106 is accepted and its isolation classes cover what `supportProfile` checks for workers.
* A P1 row the chart cannot check today becomes visible to it, such as the namespace's Pod Security label through a `lookup` the chart adopts.
* Two adopters report that `unsupported` hid a departure they did not mean to make.

## More Information

* Finding: [threat model F-11](../security/threat-model.md#findings); issue [#16](https://github.com/ploeg-hq/ploeg/issues/16).
* Code: `ops/helm/ploeg/templates/_support_profile.tpl`, `ops/helm/ploeg/templates/NOTES.txt`, `supportProfile` in `values.yaml` and `values.schema.json`; tests in `scripts/helm-golden.sh`.
* 2026-10-25: proposed.
