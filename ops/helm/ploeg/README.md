# Ploeg Helm chart

This chart installs `ploegd`, Ploeg's controller: a Deployment and a Service on port 8080. With `executor.enabled: true` it also installs the worker workloads that run agent Runs (a KEDA ScaledJob, a CronJob or the experimental agent-sandbox launcher).

The chart's defaults point at nothing outside your cluster except the public images. It names no database, model gateway, forge or tracker, and it sets no node selector. You supply each of those. Missing required values make the render fail with a message that names the value.

The [configuration reference](../../../docs/reference/configuration.md) lists every value. This page covers what an install needs.

## Images

| Image | Default | Notes |
| --- | --- | --- |
| `ploegd` (also runs the worker's `ploeg-worker` binary) | `ghcr.io/ploeg-hq/ploegd:<appVersion>` | The release image; see [release versioning](../../../docs/ops/release-versioning.md). Set `image.repository` and `imagePullSecrets` to pull from a private copy of the same digest. |
| DinD sidecar | `docker.io/library/docker:29.8.2-dind@sha256:…` | Only for teams with `harness.dind: true`. Behind a pull-through proxy, set `executor.dindImage` to your copy of the same digest. |
| Agent image | none | Ploeg publishes no agent image. Set `executor.harness.image`, or `harness.image` on a team or Role, before you enable the executor. |

## Required values

| Value | Required when | What it is |
| --- | --- | --- |
| `database.existingSecret.name` | always | Secret holding ploegd's PostgreSQL connection URI under `database.existingSecret.key` (default `uri`). CloudNativePG creates `<cluster>-app` with a `uri` key. |
| `executor.litellm.adminUrl` | `executor.workerAuth.mode` is `managed` (the default) | LiteLLM management URL, such as `http://litellm.<namespace>.svc.cluster.local:4000`. ploegd refuses to start in managed mode without it. |
| `executor.litellm.baseUrl` | `executor.enabled` | Model gateway URL the workers call, such as `http://litellm.<namespace>.svc.cluster.local:4000/v1`. |
| `executor.forgejo.url`, `executor.gitlab.url` or `executor.github.url` | `executor.enabled`, for the forge `executor.forge` names | Forge URL workers clone from and push to. |
| `executor.harness.image` | `executor.enabled` | Agent image, unless every team or Role sets its own. |
| `executor.scaler.host` | `executor.enabled` with the `keda` or `sandbox` executor | PostgreSQL host KEDA polls. It must resolve from the KEDA operator's namespace, so use the full name. |

## Secrets to create first

The chart references these Secrets by name and creates none of them. The names are defaults you can change in values.

| Secret (key) | Needed for |
| --- | --- |
| the database Secret (`uri`) | always |
| `agent-litellm-master` (`LITELLM_MASTER_KEY`) | managed worker mode: ploegd's LiteLLM management key |
| `ploeg-worker-signing` (`key`) | managed worker mode: at least 32 bytes of signing material |
| `ploeg-worker-bootstrap` (`registry.json`, plus one `<team>--<role>` key per worker) | managed worker mode: the bootstrap registry, a JSON array; `[]` while no team runs |
| `agent-builder-token` (`FORGEJO_TOKEN`, `GITLAB_TOKEN` or `GITHUB_TOKEN`) | a forge URL is set |
| `ploeg-scaler` (`password`) | the `keda` or `sandbox` executor |

[Managed workers](../../../docs/ops/managed-workers.md) describes the bootstrap registry and the signing key.

## Optional integrations

Each of these is off until you set it, and ploegd starts without it:

- **Tracker.** `webhook.existingSecret` verifies Vikunja webhooks; `tracker.url` and `tracker.tokenSecret` turn on Vikunja write-backs; `tracker.clickup.*` configures ClickUp; `tracker.github.*` configures GitHub Issues (ADR-0085). Without a webhook secret, ploegd rejects every tracker webhook.
- **Forge for ploegd alone.** With the executor off, `executor.forgejo.url` (or `publicUrl`) still gives ploegd a forge provider for publishing findings. Without it, findings stay in the database.
- **Scheduling.** `nodeSelector` (ploegd) and `executor.nodeSelector` (workers) are empty. Set a label of your own to keep privileged DinD workers off control-plane nodes.
- **Monitoring.** `monitoring.serviceMonitor` and `monitoring.prometheusRule` need the Prometheus Operator CRDs.
- **Harnesses and models a person may choose.** A planned team lists `executor.teams[].harnesses`, each entry with the fields of `harness` and one `default: true`; a Role narrows it with `harnesses: [<key>]` and `defaultHarness`, where a key is `acp/<profile>` or the Harness name. Every allowed entry renders its own workload per Role, `ploeg-worker-<team>-<role>-<key>`, and an operator admission may name any of them ([ADR-0098](../../../docs/adrs/0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [ADR-0099](../../../docs/adrs/0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md)). An entry outside `ploeg.qualifiedHarnesses` (`openhands`, `acp/openhands`, `exec`) is admitted only as a Role's default unless the team sets `allowUnqualifiedHarnesses: true`; `claude-environment` cannot be listed. A Role's `models` lists the model aliases an admission may choose, and must contain its `model` ([ADR-0103](../../../docs/adrs/0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md)). Before you remove an entry, let its pending Runs finish: a Run that stores a removed Harness waits until `PloegHarnessOutsideAllowlist` sends a person to it. [`ci/executor-harness-selection-values.yaml`](ci/executor-harness-selection-values.yaml) renders an example.

## Minimal install

ploegd alone, with the executor off. Create the namespace and the four Secrets above, then:

```sh
helm install ploeg oci://ghcr.io/ploeg-hq/charts/ploeg --version <version> -n ploeg \
  --set database.existingSecret.name=<database-secret> \
  --set executor.litellm.adminUrl=http://litellm.<namespace>.svc.cluster.local:4000
```

The pod becomes ready once PostgreSQL answers; `/readyz` checks only the database. Its log says which integrations are off.

Before you enable the executor, set the values the table marks `executor.enabled`, create the forge and scaler Secrets, and install KEDA for the `keda` executor. Teams, plans and routing are in the [configuration reference](../../../docs/reference/configuration.md).
