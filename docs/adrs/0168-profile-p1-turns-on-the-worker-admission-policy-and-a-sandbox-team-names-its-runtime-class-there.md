---
status: proposed
date: 2026-10-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# Profile P1 turns on the worker admission policy, and a sandbox team names its runtime class there

## Context and Problem Statement

The chart ships a ValidatingAdmissionPolicy over every `ploeg-worker` pod in the release namespace (`templates/agent-admission.yaml`). It refuses host namespaces, hostPath, a mounted service-account token, privilege, root, unconfined seccomp or AppArmor and Kata override annotations, and with `security.workerAdmission.runtimeClassNames` set, any runtime class outside that list. It was off by default, and profile P1 ([ADR 0154](0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md)) did not require it, so P1's worker pod shape rested on the chart's templates alone. A worker pod changed outside the chart, such as a patched ScaledJob or an edited template in a fork, was admitted with weaker isolation and nothing recorded it. The 2026-10-27 security review recorded this as finding F-49 ([#359](https://github.com/ploeg-hq/ploeg/issues/359)). Should P1 require the policy, and what must it hold for the sandbox executor, whose VM runtime class the chart's templates set but cannot keep?

## Decision Drivers

* P1 rows the chart cannot see after rendering, such as the sandbox executor's VM runtime class, are what an admission policy holds at the API server.
* A deployment outside P1 must keep rendering what it rendered before. A DinD deployment, which P1 already excludes, would fail if the policy turned on for it.
* Turning a P1 control off must be written in the values, as ADR 0154 requires for every other departure.
* The policy needs Kubernetes 1.30. The support matrix lists 1.29 as experimental and no CI job starts it.

## Considered Options

* `security.workerAdmission.enabled` unset follows `supportProfile`: on under P1, off under `unsupported`; `false` leaves P1
* Default `enabled: true` for every profile
* Keep the policy opt-in and list it as an installer row in the P1 table

## Decision Outcome

Chosen option: "`enabled` unset follows `supportProfile`", because it turns the policy on for every P1 deployment without touching a deployment that already declared itself `unsupported`.

1. `values.yaml` sets `security.workerAdmission.enabled: null`. The chart reads it through `ploeg.workerAdmissionEnabled`: an explicit `true` or `false` wins, and unset is on under `supportProfile: P1` and off under `unsupported`. The install notes print the result.
2. `security.workerAdmission.enabled: false` under P1 is a departure, so the render fails with the profile message and names F-49. `supportProfile: unsupported` renders it.
3. Under P1, a team or Role on the sandbox executor needs `security.workerAdmission.runtimeClassNames` to be non-empty, and when the list is non-empty every worker workload's resolved `runtimeClassName` must be in it. Otherwise the chart would render pods the policy refuses, or a sandbox pod could drop its VM runtime class and still be admitted.
4. The DinD refusal of `security.workerAdmission` fires only for an explicit `enabled: true`. Under P1, DinD already fails with the profile's own `harness.dind is true` message, and under `unsupported` the policy stays off unless the values turn it on.
5. On Kubernetes older than 1.30 a P1 render now fails with the native-admission floor message, which names the way out: `supportProfile: unsupported` with `enabled: false` and a cluster policy engine instead.
6. `security-contract.json` moves to revision 2: `nativeAdmissionDefault` becomes `{"P1": true, "unsupported": false}`.
7. The default changes for every P1 install, so the change ships as a `BREAKING CHANGE` in v0.3.0 and is recorded against `v0.2.0-rc.83` in [breaking-changes.yaml](../contracts/breaking-changes.yaml).

### Consequences

* Good, because a worker pod edited outside the chart is refused at the API server in every P1 deployment, and the refusal names the rule it broke.
* Good, because the sandbox executor's runtime class becomes something the API server checks, not only something the template sets.
* Bad, because a P1 install now needs cluster-scoped RBAC for `admissionregistration.k8s.io` and Kubernetes 1.30. Both are already what the support matrix tests; a 1.29 cluster declares itself `unsupported`.
* Bad, because one release with a sandbox team and a keda or cronjob team must give every worker workload a runtime class in the list, since the policy has one allowlist for the namespace. Splitting such teams across releases or listing both classes is the installer's choice.

### Confirmation

* `scripts/helm-golden.sh check`, run by `mise exec -- bash scripts/verify.sh` and CI's `verify` job: `leaves_p1` cases for `enabled: false`, a sandbox team with an empty list (global and per team), a sandbox class outside the list and a keda workload with no class against a non-empty list; `renders_p1` cases for an explicit `true`, a listed class and the three sandbox spellings with the list set; and render checks that the P1 defaults and an `unsupported` render with `enabled: true` carry the policy binding while an `unsupported` render with `enabled` unset does not. The P1 goldens carry the policy.
* `scripts/check-security-profile.py` (offline CEL) and `scripts/check-worker-admission-live.py` (CI's `worker-admission` job on kind 1.30 and 1.37) run the `p1-default` and `p1-sandbox` variants, which set no `enabled`: the rendered pods are admitted and each hostile variant, `hostPID: true` among them, is refused by the rule that names it. The offline check also requires the 1.29 P1 render to fail with the way out and the contract to say revision 2.
* Mutation tests, before this record was written: a guard that never departs on `false`, a P1 that never enables, an `unsupported` that also enables, the sandbox empty-list check removed, the membership check removed, and both departures made unconditional each failed both scripts; removing P1 enablement also failed the live check on kind 1.37.

## Pros and Cons of the Options

### Default `enabled: true` for every profile

* Good, because the value would read the same in every values file.
* Bad, because every `unsupported` DinD deployment would fail on the DinD refusal until it wrote `enabled: false`, a break for deployments P1 does not cover.

### Keep the policy opt-in and list it as an installer row

* Good, because nothing changes for any install.
* Bad, because the chart can see this setting, and ADR 0154 refuses every P1 departure the chart can see.

## Re-evaluation triggers

* Kubernetes 1.29 returns to the support matrix as supported.
* The chart can read the namespace's Pod Security level, so the policy could cover it too.
* A worker pod needs a field the policy refuses.
* A deployment needs a sandbox team and another executor in one release with different runtime classes, which the single allowlist cannot express.

## More Information

* Finding F-49 in the [threat model](../security/threat-model.md#findings); issue [#359](https://github.com/ploeg-hq/ploeg/issues/359). Related: [#15](https://github.com/ploeg-hq/ploeg/issues/15), [#16](https://github.com/ploeg-hq/ploeg/issues/16), [#34](https://github.com/ploeg-hq/ploeg/issues/34).
* [Self-hosting security contract](../ops/self-hosting-security.md), [ADR 0154](0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md).
