# Ploeg Helm chart

This chart installs `ploegd`, Ploeg's controller: a Deployment and a Service on port 8080. With `executor.enabled: true` it also installs the worker workloads that run agent Runs (a KEDA ScaledJob, a CronJob or the experimental agent-sandbox launcher). `mcp.enabled` adds `ploeg-mcp`, the MCP server for people's AI clients, and `claudeRunner.enabled` adds the self-hosted Claude environment runner, so one release of this chart deploys everything a Ploeg release runs.

The chart's defaults point at nothing outside your cluster except the public images. It names no database, model gateway, forge or tracker, and it sets no node selector. You supply each of those. Missing required values make the render fail with a message that names the value.

The [configuration reference](../../../docs/reference/configuration.md) lists every value. This page covers what an install needs.

## Images

| Image | Default | Notes |
| --- | --- | --- |
| `ploegd` (also runs `ploeg-mcp`, the sandbox launcher and, with `harnessContainer: shared`, `ploeg-worker`) | `ghcr.io/ploeg-hq/ploegd:<appVersion>@<digest>` | The release image; see [release versioning](../../../docs/ops/release-versioning.md#the-published-chart-pins-its-images). Set `image.repository` and `imagePullSecrets` to pull from a private copy of the same digest. |
| `ploeg-worker` | `ghcr.io/ploeg-hq/ploeg-worker:<appVersion>@<digest>` | The worker container of a Run pod whose harness runs in its own container (`executor.harnessContainer: separate`, the default, ADR-0138), and the launcher binary the harness container runs. Distroless with git. `worker.image.repository` points it at a private copy; `executor.workerImage` overrides it whole and must name a ploeg-worker image. |
| `claude-runner` | `ghcr.io/ploeg-hq/claude-runner:<appVersion>@<digest>` | The `claudeRunner` Deployment, and the harness image of a `claude-environment` team or Role that names none. It holds no Claude Code; the pod installs Anthropic's signed release at start (ADR-0141). `claudeRunner.image.repository` points it at a private copy. |
| DinD sidecar | `docker.io/library/docker:29.8.2-dind@sha256:…` | Only for a team or Role that sets `harness.dind: true` together with `harness.acknowledgePrivilegedDind: true`; the default renders no sidecar (ADR-0136). Behind a pull-through proxy, set `executor.dindImage` to your copy of the same digest. |
| Agent image | none | Ploeg publishes no agent image. Set `executor.harness.image`, or `harness.image` on a team or Role, before you enable the executor. |

The published chart carries the digests of the images its release pushed, so a cluster sets no tag or digest to run that release. Setting `image.tag` (or `claudeRunner.image.tag`) to another version drops the chart's digest for that image; a tag of the form `<version>@sha256:<digest>` is used as written.

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

## ploeg-mcp and claude-runner

Both are off by default and create no Secret or route host of their own: the cluster names those.

- **`mcp.enabled`** runs `ploeg-mcp http` from the ploegd image, with a Service on `mcp.port` (8090). `mcp.env` carries the `PLOEG_MCP_*` settings of [Serve Ploeg's MCP tools over HTTPS](../../../docs/how-to/serve-ploeg-mcp-over-https.md), `mcp.extraEnv` and `mcp.envFrom` the consumer tokens from your Secrets. A group marked `execute` also needs `mcp.confirmationKeySecret`, the Secret key that signs each person's confirmation ([ADR-0152](../../../docs/adrs/0152-ploeg-mcp-signs-each-confirmation-and-acts-on-it-once.md)); without it ploeg-mcp refuses to start. `mcp.httpRoute` adds a Gateway API route. Its NetworkPolicy comes from `networkPolicy` below; `mcp.networkPolicy.ingress` and `.egress` are the older per-component form and still render.
- **`claudeRunner.enabled`** runs the runner of [ADR-0084](../../../docs/adrs/0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md) ([runner settings](../claude-runner/README.md)). It requires `claudeRunner.environmentSecret.name`, a Secret with `SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET`. `claudeRunner.extraArgs` adds flags such as `--git-host-rewrite`. Its NetworkPolicy allows DNS, ploegd, the forges and the internet on 443, because Anthropic is a hostname a NetworkPolicy cannot name; a CNI with FQDN policy can tighten it. `claudeRunner.loginVolumes` creates the PersistentVolumeClaims that `harness.claudeEnvironment.loginVolume` names; Helm keeps them on uninstall. The image carries no Claude Code: with `claudeRunner.claudeCode.install` (default `true`) an init container installs Anthropic's signed release into a volume the runner mounts read-only, so the pod needs `downloads.claude.ai:443` ([Run Claude Code in the claude-runner](../../../docs/how-to/run-claude-code-in-the-claude-runner.md)).

## Network policies

`networkPolicy.enabled` (default `true`) renders a default-deny NetworkPolicy for every pod the chart creates, with the egress each needs derived from the URLs in these values: ploegd reaches the database, the gateway, the forges and the trackers; a worker reaches DNS, ploegd, the gateway and the forge and nothing else. `networkPolicy.<endpoint>.peers` and `.port` name an endpoint exactly, `networkPolicy.<component>.extraEgress` adds what your cluster needs on top, and `networkPolicy.ploegd.ingressFrom` names who may call ploegd. Only a CNI that enforces NetworkPolicy applies them; kind's kindnet does not. [Network policies](../../../docs/ops/network.md) lists every policy and its rows.

`mcp.name` and `claudeRunner.name` set the Deployment name and the pods' `app.kubernetes.io/name`, which is also each Deployment's selector. They default to `<fullname>-mcp` and `<fullname>-claude-runner`. To take over objects that exist under other names, set these to the existing names, and set `fullnameOverride` and `instanceOverride` to the old release name when Ploeg moves into another Helm release, because a Deployment's selector cannot change.

## Optional integrations

Each of these is off until you set it, and ploegd starts without it:

- **Tracker.** `webhook.existingSecret` verifies Vikunja webhooks; `tracker.url` and `tracker.tokenSecret` turn on Vikunja write-backs; `tracker.clickup.*` configures ClickUp; `tracker.github.*` configures GitHub Issues (ADR-0085). Without a webhook secret, ploegd rejects every tracker webhook.
- **Forge for ploegd alone.** With the executor off, `executor.forgejo.url` (or `publicUrl`) still gives ploegd a forge provider for publishing findings. Without it, findings stay in the database.
- **Harness container.** `executor.harnessContainer: separate` (the default) renders every workload with both credential proxies on and no DinD as two containers: `harness`, a native sidecar from the agent image that runs `ploeg-worker harness-serve` with no Secret and no environment of its own, and `worker`, the Job's main container from the ploeg-worker image, which holds every credential ([ADR-0138](../../../docs/adrs/0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md)). `shared` renders the old single container and is deprecated; `harness.harnessContainer` sets it for one team or Role. DinD, a proxy off and `claude-environment` keep the single container, and asking for `separate` on them fails the render.
- **Scheduling.** `nodeSelector` (ploegd) and `executor.nodeSelector` (workers) are empty. Set a label of your own to keep acknowledged privileged DinD workers off control-plane nodes.
- **Monitoring.** `monitoring.serviceMonitor` and `monitoring.prometheusRule` need the Prometheus Operator CRDs.
- **Harnesses and models a person may choose.** A planned team lists `executor.teams[].harnesses`, each entry with the fields of `harness` and one `default: true`; a Role narrows it with `harnesses: [<key>]` and `defaultHarness`, where a key is `acp/<profile>` or the Harness name. Every allowed entry renders its own workload per Role, `ploeg-worker-<team>-<role>-<key>`, and an operator admission may name any of them ([ADR-0098](../../../docs/adrs/0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [ADR-0099](../../../docs/adrs/0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md)). An entry outside `ploeg.qualifiedHarnesses` (`openhands`, `acp/openhands`, `exec`) is admitted only as a Role's default unless the team sets `allowUnqualifiedHarnesses: true`; `claude-environment` cannot be listed. A Role's `models` lists the model aliases an admission may choose, and must contain its `model` ([ADR-0103](../../../docs/adrs/0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md)). Before you remove an entry, let its pending Runs finish: a Run that stores a removed Harness waits until `PloegHarnessOutsideAllowlist` sends a person to it. [`ci/executor-harness-selection-values.yaml`](ci/executor-harness-selection-values.yaml) renders an example.

## Database pool and memory

- **`database.pool`** sizes ploegd's PostgreSQL pool and sets two server-side limits on each connection, rendered as the `PLOEG_DB_*` variables of the [configuration reference](../../../docs/reference/configuration.md). The defaults are 8 connections, 2 kept idle, a 30-minute lifetime with 5 minutes of jitter, `application_name=ploegd`, `statement_timeout=30s` and `idle_in_transaction_session_timeout=60s`. A setting the connection URI names itself (`pool_max_conns`, `application_name`, `statement_timeout`, ...) wins over its value here, so a hand-written URI keeps working. Acquiring a connection waits until the request or loop that asked gives up; the `ploeg_db_pool_*` metrics in [metrics and alerts](../../../docs/ops/alerts.md) show when that happens.
- **`goMemLimit`**, **`mcp.goMemLimit`** and **`executor.sandbox.launcherGoMemLimit`** set `GOMEMLIMIT` for the Go-only containers: ploegd, ploeg-mcp and the sandbox launcher. Empty means 90% of the container's memory limit, rounded down to whole MiB (230MiB for ploegd's 256Mi), so the garbage collector works harder before the kernel kills the container. A Go size such as `200MiB` sets it, `off` renders none, and `GOMEMLIMIT` in the container's `env` wins. The worker container gets one from `executor.workerContainerResources` when the harness runs in its own container, and none with `harnessContainer: shared`, where its memory limit is shared with the harness process.

## Minimal install

ploegd alone, with the executor off. Create the namespace and the four Secrets above, then:

```sh
helm install ploeg oci://ghcr.io/ploeg-hq/charts/ploeg --version <version> -n ploeg \
  --set database.existingSecret.name=<database-secret> \
  --set executor.litellm.adminUrl=http://litellm.<namespace>.svc.cluster.local:4000
```

The pod becomes ready once PostgreSQL answers; `/readyz` checks only the database. Its log says which integrations are off.

Before you enable the executor, set the values the table marks `executor.enabled`, create the forge and scaler Secrets, and install KEDA for the `keda` executor. Teams, plans and routing are in the [configuration reference](../../../docs/reference/configuration.md).
