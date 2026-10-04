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
| `executor.forgejo.url` or `executor.gitlab.url` | `executor.enabled`, for the forge `executor.forge` names | Forge URL workers clone from and push to. |
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
| `agent-builder-token` (`FORGEJO_TOKEN` or `GITLAB_TOKEN`) | a forge URL is set |
| `ploeg-scaler` (`password`) | the `keda` or `sandbox` executor |

[Managed workers](../../../docs/ops/managed-workers.md) describes the bootstrap registry and the signing key.

## Optional integrations

Each of these is off until you set it, and ploegd starts without it:

- **Tracker.** `webhook.existingSecret` verifies Vikunja webhooks; `tracker.url` and `tracker.tokenSecret` turn on Vikunja write-backs; `tracker.clickup.*` configures ClickUp. Without a webhook secret, ploegd rejects every tracker webhook.
- **Forge for ploegd alone.** With the executor off, `executor.forgejo.url` (or `publicUrl`) still gives ploegd a forge provider for publishing findings. Without it, findings stay in the database.
- **Scheduling.** `nodeSelector` (ploegd) and `executor.nodeSelector` (workers) are empty. Set a label of your own to keep privileged DinD workers off control-plane nodes.
- **Monitoring.** `monitoring.serviceMonitor` and `monitoring.prometheusRule` need the Prometheus Operator CRDs.

## Minimal install

ploegd alone, with the executor off. Create the namespace and the four Secrets above, then:

```sh
helm install ploeg oci://ghcr.io/ploeg-hq/charts/ploeg --version <version> -n ploeg \
  --set database.existingSecret.name=<database-secret> \
  --set executor.litellm.adminUrl=http://litellm.<namespace>.svc.cluster.local:4000
```

The pod becomes ready once PostgreSQL answers; `/readyz` checks only the database. Its log says which integrations are off.

Before you enable the executor, set the values the table marks `executor.enabled`, create the forge and scaler Secrets, and install KEDA for the `keda` executor. Teams, plans and routing are in the [configuration reference](../../../docs/reference/configuration.md).
