---
status: accepted
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# The chart default is the strict isolation profile, and privileged DinD is an acknowledged opt-in

## Context and Problem Statement

The chart defaulted `executor.harness.dind` to `true`. A deployment that set nothing rendered a privileged `docker:dind` sidecar, mounted its TLS client certificates into the harness container (`DOCKER_HOST`, `DOCKER_CERT_PATH`, `/certs`), and, because a harness inside a Docker container cannot reach the worker's loopback, turned both credential proxies of [ADR 0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) off for that workload. The agent therefore held root on the node and the real per-Run model key and forge token, while `values.yaml` said the default worker met the `restricted` Pod Security Standard with both proxies on. The threat model records this as its first Critical finding ([F-01](../security/threat-model.md#findings)), and the supported profile P1 requires `dind: false` on every team and Role, which no default guaranteed. What should a deployment that sets nothing get, and how does one that needs Docker ask for it?

## Decision Drivers

* The outcome of touching nothing must be the strongest posture the chart can render, the "strict by default" driver of [ADR 0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md).
* A privilege that is root on the node must be impossible to get by accident, and its cost must be stated where it is taken.
* Every harness the chart offers that works with both proxies on works without DinD; the [qualification record](../research/2026-10-04-isolation-qualification.md) qualified `openhands`, `acp/openhands` and `exec` on Runs without DinD, and the reference deployment has run every worker without it since 2026-09-28.
* An existing deployment that builds inside a container must keep working with a values change, not a redesign, and the change is a recorded break under [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md).

## Considered Options

* Default `dind: false`; `dind: true` renders only with an explicit `acknowledgePrivilegedDind: true` in the same Role, team or executor chain
* Default `dind: false`; `dind: true` renders as before
* Keep `dind: true` as the default and document the exposure
* Remove DinD from the chart

## Decision Outcome

Chosen option: "**Default `dind: false`; `dind: true` renders only with an explicit `acknowledgePrivilegedDind: true`**", because it is the only option where a deployment that sets nothing gets the P1 worker settings and nobody gets node root without having written down that they accept it.

1. `executor.harness.dind` defaults to `false`. The default worker renders no sidecar, no `DOCKER_*` wiring, no `ploeg.webgrip.dev/privileged-dind` label, and `PLOEG_LLM_KEY_ISOLATION=proxy` and `PLOEG_FORGE_TOKEN_ISOLATION=proxy` for its qualified harness. The default harness stays `openhands`, the qualified harness with the most evidence without DinD; [ADR 0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md) decides its retirement separately and does not change this default's shape.
2. `harness.acknowledgePrivilegedDind` is a boolean on the same block as `dind`, on `executor.harness`, a team, a Role and an allowlist entry, and resolves through the same Role, team, executor chain. A workload whose resolved `dind` is `true` without a resolved `acknowledgePrivilegedDind` of `true` fails the render. The message names the workload and exactly what the harness would receive: the client certificates of a privileged Docker daemon, which is root on the node, and, with both proxies off, the real per-Run model key in `LLM_API_KEY` and, for a writer, the real forge token in `AGENT_BUILDER_TOKEN`.
3. An acknowledged `dind: true` renders byte for byte what the old default rendered, so an existing DinD deployment changes two values and nothing else. `harness.credentialIsolation: proxy` still forces the proxies on for such a workload, as before.
4. The break is recorded against `v0.2.0-rc.47` in [breaking-changes.yaml](../contracts/breaking-changes.yaml) and ships as a `BREAKING CHANGE`, so the next release raises the minor.

### Consequences

* Good, because F-01 closes: the advertised default render is the P1 worker posture, and the threat model's "Critical outside P1" rows now describe an opt-in someone wrote down.
* Good, because the chart's own text, the schema descriptions and the generated configuration reference describe the real render again.
* Bad, because a deployment that relied on the old default to get Docker fails its next render until it sets `dind: true` and `acknowledgePrivilegedDind: true`. Accepted: the failure is loud, names both values, and the alternative is a silent privilege.
* Bad, because the acknowledgement is one more value on a block that already has eleven. Accepted: it is the one value that records a decision rather than a setting.
* Neutral: [ADR 0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md), still proposed, names `executor.allowLegacy` for a wider gate that covers a proxy turned off as well as DinD. This record decides the DinD half now; when ADR 0106 is accepted, its gate subsumes `acknowledgePrivilegedDind` under the deprecation window of ADR 0134.

### Confirmation

* `scripts/helm-golden.sh check`, run by `mise run verify` and CI's `verify` job, holds three cases: the golden `executor-default-worker.yaml` (a team on the chart's harness defaults) must contain `PLOEG_LLM_KEY_ISOLATION`, `PLOEG_FORGE_TOKEN_ISOLATION` and `runAsNonRoot: true` and must not contain `name: dind`, `privileged: true`, the `privileged-dind` label, `PLOEG_CREDENTIAL_ISOLATION_OFF_REASON` or `DOCKER_HOST`; `reject-dind-without-acknowledgement-values.yaml` must fail with `harness.dind is true without harness.acknowledgePrivilegedDind: true`; the golden `executor-acknowledged-dind.yaml` must contain the sidecar, `privileged: true`, the label, `PLOEG_CREDENTIAL_ISOLATION_OFF_REASON=dind` and `DOCKER_CERT_PATH`, and neither proxy variable. Each assertion was broken once and seen to fail before this record was written.
* `go test ./internal/compat/` accepts the schema change only through the entry in `docs/contracts/breaking-changes.yaml` that names this record.
* The threat model's P1 table and finding F-01 cite the default and the pull request that changed it.

## Pros and Cons of the Options

### Default `dind: false`; `dind: true` renders as before

* Good, because the default is safe and nothing else changes.
* Bad, because a single `dind: true` on a team, copied from an example, still hands the agent node root and both real credentials with nothing in the values saying so.

### Keep `dind: true` as the default and document the exposure

* Good, because no existing render changes.
* Bad, because it keeps F-01 open: the advertised default contradicts the supported profile, and a reader of `values.yaml` was told the opposite of what rendered.

### Remove DinD from the chart

* Good, because the hazard disappears.
* Bad, because repositories whose gates build images need a daemon somewhere, and the sandbox executor's Kata path already isolates a DinD workload better than the chart can refuse it. Removal is a later decision, if ADR 0106's classes make a DinD class unnecessary.

## Re-evaluation triggers

* ADR 0106 is accepted and `executor.allowLegacy` ships, which subsumes `acknowledgePrivilegedDind`.
* ADR 0118 retires `openhands`, which changes the default harness and needs a qualified replacement that works without DinD.
* A harness is qualified for both proxies that needs DinD, which the current gate cannot express.
* The attack drill of [ADR 0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md) shows a Run escaping the default worker without DinD.
* Two adopters report that they had to acknowledge DinD on every team, which means the default is wrong for the repositories Ploeg serves.

## More Information

* Finding: [threat model F-01](../security/threat-model.md#findings); the fix is [pull request #231](https://github.com/ploeg-hq/ploeg/pull/231).
* Evidence: [credential isolation qualification](../research/2026-10-04-isolation-qualification.md); goldens `ops/helm/ploeg/ci/golden/executor-default-worker.yaml` and `executor-acknowledged-dind.yaml`.
* Builds on [ADR 0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR 0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md) and [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md); decides the DinD half of [ADR 0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) ahead of it.
* 2026-10-23: proposed with the change.
* 2026-10-24: accepted by the owner.
