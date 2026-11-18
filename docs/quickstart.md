---
type: tutorial
audience: [operator, integrator, contributor]
owner: ploeg
last_verified: 2026-10-26
verified_by: "Every command below run in order on a fresh kind cluster with Ploeg 0.2.0-rc.77 by scripts/quickstart.sh; the Quickstart workflow repeats the run on a schedule, against the pinned and the newest candidate"
---

# Quickstart: your first Run on a local cluster

This guide takes you from nothing to one completed Run. You install the released, signed Ploeg chart on a local [kind](https://kind.sigs.k8s.io/) cluster, hand Ploeg one piece of work and read back what happened. It needs no account anywhere and spends no money. A clean run took about four minutes on a laptop that already had the kind node image. Most of that time goes to pulling images and waiting for the next worker.

## What you build

Ploeg does not do the work itself. It decides who may run work, starts a worker for it, gives the worker short-lived credentials and records the result. So a Run needs a few services around Ploeg. This guide stands up small local versions of each one:

| Piece | What it is | Here |
| --- | --- | --- |
| `ploegd` | Ploeg's controller. It admits work, hands it to workers and keeps the record. | The released chart and image |
| `ploeg-worker` | Claims one piece of work, runs a harness on it and reports the outcome. | Started by a Kubernetes CronJob every minute |
| PostgreSQL | Ploeg keeps all its state here. | A single `postgres` pod |
| Forge | The Git server the worker clones from. | A single Forgejo pod with one practice repository |
| Model gateway | Ploeg mints a capped key here for every Run. | A LiteLLM pod with a fake model that always gives the same answer |
| Harness | The program that does the work. A real one is a coding agent. | A short shell script that calls the model once and changes nothing |
| Work source | Where work comes from: a tracker such as GitHub Issues, or the operator API. | One `curl` request to the operator API |

A few Ploeg words you will see:

- A **Work Item** is one piece of work, such as one ticket.
- A **Shift** is Ploeg's attempt to finish a Work Item. It holds the budget.
- A **Run** is one worker execution inside a Shift.
- A **Team** is a named queue with its own workers, model and budget.

## Before you start

Install these tools. The versions are the ones this guide was tested with; newer ones should work.

| Tool | Tested version |
| --- | --- |
| Docker | 28.4 |
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | 0.33.0 |
| kubectl | 1.36.1 |
| [Helm](https://helm.sh/docs/intro/install/) | 4.3.0 |
| [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) | 2.4.3 |
| git, curl, openssl, [jq](https://jqlang.org/) | any recent |

Run every command in one terminal, in order. Later steps use shell variables that earlier steps set. The commands only touch the kind cluster this guide creates: the first step points `KUBECONFIG` at a file of its own.

Clone the repository for the manifests in [`ops/quickstart`](../ops/quickstart/). The Ploeg images and chart come from the release, not from the clone.

```sh
git clone --depth 1 https://github.com/ploeg-hq/ploeg.git
cd ploeg
export KUBECONFIG="${TMPDIR:-/tmp}/ploeg-quickstart.kubeconfig"
```

## 1. Pick a release and check its signatures

Releases are listed on the [releases page](https://github.com/ploeg-hq/ploeg/releases). This guide was tested with `0.2.0-rc.77`, a release candidate. Do not use `0.1.0`, the only stable release so far: it predates release signing, and its chart sets a default node selector that no kind node matches, so `ploegd` never starts.

```sh
export PLOEG_VERSION=0.2.0-rc.77
```

Every release signs its image and its chart with [cosign](https://docs.sigstore.dev/) keyless signing. There is no signing key to trust. Instead, cosign checks that the signature was made by Ploeg's release workflow on GitHub and that it is recorded in the public transparency log. The identity rule comes from [CI and infrastructure](ops/ci-and-infra.md#credentials-and-signing).

```sh
IDENTITY='^https://github\.com/ploeg-hq/ploeg/\.github/workflows/release\.yml@refs/(heads/development|heads/main|tags/v0\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?)$'
for artifact in ghcr.io/ploeg-hq/ploegd ghcr.io/ploeg-hq/ploeg-worker ghcr.io/ploeg-hq/charts/ploeg; do
  cosign verify "$artifact:$PLOEG_VERSION" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp "$IDENTITY" > /dev/null \
    && echo "verified $artifact:$PLOEG_VERSION"
done
```

cosign prints a short `Verification for …` report for each artifact, and the loop adds one `verified` line per artifact. You should see three `verified` lines: the controller image, the worker image and the chart. Stop here if you do not. The chart pins both images by digest, so the chart you verified also fixes the images the cluster runs.

## 2. Create the cluster

```sh
kind create cluster --name ploeg-quickstart
kubectl create namespace ploeg
kubectl config set-context --current --namespace ploeg
```

## 3. Start PostgreSQL, the forge and the model gateway

Each password is random and generated on your machine. None of them is written in this guide.

```sh
POSTGRES_PASSWORD=$(openssl rand -hex 24)
kubectl create secret generic postgres --from-literal=password="$POSTGRES_PASSWORD"
kubectl create secret generic ploeg-database \
  --from-literal=uri="postgres://postgres:$POSTGRES_PASSWORD@postgres:5432/ploeg?sslmode=disable"
kubectl create secret generic litellm-database \
  --from-literal=uri="postgresql://postgres:$POSTGRES_PASSWORD@postgres:5432/litellm"
kubectl create secret generic litellm-master \
  --from-literal=LITELLM_MASTER_KEY="sk-$(openssl rand -hex 24)"
kubectl apply -f ops/quickstart/postgres.yaml
kubectl rollout status statefulset/postgres --timeout=10m
kubectl apply -f ops/quickstart/forgejo.yaml -f ops/quickstart/litellm.yaml
kubectl rollout status deployment/forgejo --timeout=10m
kubectl rollout status deployment/litellm --timeout=10m
```

The gateway in [`litellm.yaml`](../ops/quickstart/litellm.yaml) serves one model, `quickstart-model`. It never calls a provider: LiteLLM's `mock_response` setting returns the same sentence to every request. Ploeg still treats it as a real gateway. It mints a key with a budget for each Run, watches its spend and revokes it when the Run ends.

## 4. Create the practice repository

The worker needs a repository to clone. Ploeg refuses a repository that has no `AGENTS.md` on its branch, because that file is where a repository tells agents how to work in it.

```sh
kubectl exec deploy/forgejo -- forgejo admin user create --admin --username quickstart \
  --email quickstart@example.invalid --random-password --must-change-password=false > /dev/null
FORGEJO_TOKEN=$(kubectl exec deploy/forgejo -- forgejo admin user generate-access-token \
  --username quickstart --token-name ploeg --scopes all --raw)
kubectl create secret generic agent-builder-token --from-literal=FORGEJO_TOKEN="$FORGEJO_TOKEN"
kubectl exec deploy/forgejo -- curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST http://localhost:3000/api/v1/user/repos \
  -H "Authorization: token $FORGEJO_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"hello","auto_init":true,"readme":"Default","default_branch":"main"}'
AGENTS_MD=$(printf '# hello\n\nA practice repository for the Ploeg quickstart. Change nothing.\n' | base64 | tr -d '\n')
kubectl exec deploy/forgejo -- curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST http://localhost:3000/api/v1/repos/quickstart/hello/contents/AGENTS.md \
  -H "Authorization: token $FORGEJO_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"message\":\"Add AGENTS.md\",\"content\":\"$AGENTS_MD\"}"
```

Both requests print `201`.

## 5. Create Ploeg's own secrets

Ploeg's workers do not get the gateway's master key or a long-lived model key. Instead:

1. Each worker starts with a **bootstrap token**. It proves which Team and Role the worker serves, and nothing else.
2. `ploegd` checks the token against its **bootstrap registry**, a JSON list of tokens with their Team and Role. That is worker enrollment.
3. `ploegd` then gives the worker a short-lived capability, signed with the **signing key**, and mints a capped model key for that one Run.

The Team in this guide is called `quickstart`. It has no planned Roles, so its one Role is the empty string and its worker's key is named `quickstart--default`. The **operator token** lets you call the operator API in the next steps.

```sh
kubectl create secret generic ploeg-worker-signing --from-literal=key="$(openssl rand -hex 32)"
BOOTSTRAP_TOKEN=$(openssl rand -hex 32)
kubectl create secret generic ploeg-worker-bootstrap \
  --from-literal=registry.json="[{\"token\":\"$BOOTSTRAP_TOKEN\",\"team\":\"quickstart\",\"role\":\"\"}]" \
  --from-literal=quickstart--default="$BOOTSTRAP_TOKEN"
OPERATOR_TOKEN=$(openssl rand -hex 32)
kubectl create secret generic ploeg-operator --from-literal=token="$OPERATOR_TOKEN"
```

[Managed workers](ops/managed-workers.md) explains the full model.

## 6. Install Ploeg

```sh
helm install ploeg oci://ghcr.io/ploeg-hq/charts/ploeg --version "$PLOEG_VERSION" \
  -n ploeg -f ops/quickstart/values.yaml --wait --timeout 5m
kubectl logs deploy/ploeg | grep -E 'ploegd ready|registered target'
```

The log shows `registered target ready` for `hello` and then `ploegd ready`. On start, `ploegd` creates its tables in PostgreSQL. You run no migration step yourself.

[`values.yaml`](../ops/quickstart/values.yaml) is short. It names the Secrets you created and registers the repository as the Work Target `hello`. It defines the `quickstart` Team with a budget of $0.10 per Run, and an operator consumer that may admit work for that Team. It uses the `cronjob` executor, so you do not need KEDA. The `exec` harness runs any command; here it is the shell script in that file.

The chart also installs a NetworkPolicy for `ploegd` and one for the worker pods (`kubectl get networkpolicy`). They are the same objects a production install gets, and on this cluster they do nothing: kind's default network plugin, kindnet, does not enforce NetworkPolicy. The guide keeps them anyway, so what you render here is what you would render on a cluster that does. [Network policies](ops/network.md) explains what they allow.

The values also set `supportProfile: P1`. P1 is the one deployment profile the [threat model](security/threat-model.md#the-supported-deployment-profile) covers, and under it the chart refuses to render any value that leaves the profile, such as a credential proxy turned off or a worker that runs as root ([ADR 0154](adrs/0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md)). So the Ploeg objects you install here are the P1 objects. The install as a whole is not a P1 deployment, for reasons the chart cannot check and the profile leaves to the installer. kindnet does not enforce NetworkPolicy. The stand-in PostgreSQL, Forgejo and LiteLLM pods share the namespace, so it cannot enforce the restricted Pod Security level. The worker pushes with the one shared Forgejo token, because the guide sets no `botPasswordSecret` for per-Run tokens (`ploegd` logs `per-run forge credentials disabled`). That is fine for a practice repository. The P1 table in the threat model lists what a real deployment adds, and the [support matrix](reference/support-matrix.md) shows which parts of this install CI tests and which a supported deployment replaces.

## 7. Hand Ploeg a piece of work

The operator API admits work without a tracker. Forward the `ploegd` port to your machine and send one Work Item. The forward runs in the background until you stop it in the last step, and the `curl` after it waits until `ploegd` answers through it.

```sh
kubectl port-forward svc/ploeg 8080:8080 > /dev/null &
curl -sf --retry 10 --retry-connrefused --retry-delay 1 -o /dev/null http://localhost:8080/readyz \
  && echo "ploegd answers" || echo "ploegd does not answer on localhost:8080; see step 10"
ADMISSION=$(curl -sS -X POST http://localhost:8080/api/v1/operator/work-items \
  -H "Authorization: Bearer $OPERATOR_TOKEN" -H "X-Ploeg-Actor: quickstart" \
  -H 'Content-Type: application/json' \
  -d '{"requestId":"first-run","team":"quickstart","target":"hello","title":"Say hello","description":"A practice task. Ask the model to say hello and change nothing."}')
WORK_ITEM=$(printf '%s' "$ADMISSION" | jq -r '.workItem.id // empty')
echo "Work Item ${WORK_ITEM:-not admitted: $ADMISSION}"
```

It prints `ploegd answers` and then `Work Item 1`. If it prints `not admitted`, the rest of the line is Ploeg's answer, and the table in step 10 lists the common reasons.

`requestId` makes the request safe to repeat. Sending the same body again returns the same Work Item instead of a second one.

`X-Ploeg-Actor: quickstart` names the person the change is made for, and Ploeg takes the consumer's word for it: the audit row records the person as `asserted`. A real deployment proves the person with a token from its identity provider instead and refuses a change without one ([ADR 0171](adrs/0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)); from chart 0.3.0 that is the default. This guide has no identity provider, so its values keep header attribution with `PLOEG_OPERATOR_ATTRIBUTION: asserted`, and the install notes warn about it.

## 8. Watch the Run

Within a minute the CronJob starts a worker. Wait until the Work Item leaves the queue:

```sh
for attempt in $(seq 1 30); do
  STATE=$(curl -sS http://localhost:8080/api/v1/operator/work-items/$WORK_ITEM \
    -H "Authorization: Bearer $OPERATOR_TOKEN" | jq -r .item.state)
  echo "$STATE"
  case "$STATE" in done|needs_human|withdrawn|null) break ;; esac
  sleep 10
done
```

It prints `queued` a few times, then `active` while the worker runs, and then `done`. A single `null` means step 7 admitted no Work Item: go back to its output. Now read the worker's side of the story:

```sh
kubectl logs -l app.kubernetes.io/name=ploeg-worker -c worker --tail=-1 \
  | grep -E 'claimed work item|minted per-run key|revoked per-run key|outcome reported' \
  | cut -c1-200
```

You see, in order: the worker claimed the Work Item, minted a model key for this Run alone, revoked that key when the harness ended, and reported the outcome.

## 9. Read the outcome

```sh
curl -sS http://localhost:8080/api/v1/operator/work-items/$WORK_ITEM \
  -H "Authorization: Bearer $OPERATOR_TOKEN" \
  | jq '{state: .item.state, shift: .item.latestShift.closeReason, runs: [.runs[] | {outcome, summary, harness, configuredModel}]}'
```

The output looks like this:

```json
{
  "state": "done",
  "shift": "plan_exhausted",
  "runs": [
    {
      "outcome": "no_change_needed",
      "summary": "The model gateway answered: Hello from the quickstart model. No provider was called.",
      "harness": "exec",
      "configuredModel": "quickstart-model"
    }
  ]
}
```

`plan_exhausted` means the Shift ran every step its plan had. Here that is one Run.

Ploeg also settles each Run's cost from the gateway's spend logs once its key has been quiet for a while. The quickstart sets that wait to one minute (`PLOEG_LLM_SETTLE_AFTER`); the default is 15 minutes. Until then the Shift still holds the Run's $0.10. A minute or two after the Run, check the cost:

```sh
sleep 90
curl -sS "http://localhost:8080/api/v1/operator/runs?team=quickstart" \
  -H "Authorization: Bearer $OPERATOR_TOKEN" \
  | jq '.runs[] | {id, outcome, settledUsd, costFinal}'
```

`settledUsd` is `0` because the fake model is free. `costFinal` stays `false` for 24 hours: in that window Ploeg reads the spend logs again in case the gateway records a late charge. [Investigate a Run's spend](how-to/investigate-a-runs-spend.md) explains holds and settlement.

## 10. When something goes wrong

Start with these two commands. Most problems show up in one of them.

```sh
kubectl logs deploy/ploeg --tail=50
kubectl get cronjobs,jobs,pods
```

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `cosign verify` fails | Wrong version, or you are not verifying a Ploeg release | Check `PLOEG_VERSION` against the releases page |
| Step 7 prints `not admitted` with `target_not_ready … has no AGENTS.md`, and `ploegd` logs `registered target not ready` | Step 4 did not add `AGENTS.md` | Repeat the last request of step 4. Ploeg checks again on the next admission, without a restart |
| `helm install` times out and `ploegd` logs `database unreachable` | PostgreSQL is not up yet, or the `ploeg-database` URI is wrong | `kubectl rollout status statefulset/postgres`, then check the Secret's password matches `postgres` |
| A pod stays in `CreateContainerConfigError` | A Secret it references does not exist | `kubectl describe pod <name>` names the Secret. Create it with the command from step 3, 4 or 5 |
| Step 7 prints `ploegd does not answer on localhost:8080`, then `jq: parse error` or `not admitted` with an HTML page | Another program on your machine listens on port 8080, so the port forward could not take it | Stop that program, or forward another local port, such as `18080:8080`, and use that port in every later `curl` |
| Admission returns `team_forbidden` or `invalid_admission` | The body names another Team, or a target that is not registered | Use `"team":"quickstart"` and `"target":"hello"` |
| The state stays `queued` for more than two minutes | No worker started, or it could not enroll | `kubectl get jobs`; then `kubectl logs job/<name> -c worker`. A bootstrap error means the `quickstart--default` key does not match `registry.json` |
| The state becomes `needs_human` | The Run reported `stuck` or failed | Read the Run's `stuckReason` with the command in step 9. `got no answer from the model gateway` means LiteLLM is down: `kubectl logs deploy/litellm` |

The [operator runbooks](how-to/index.md) cover stuck Leases, credential rotation and recovery in a real deployment.

## 11. Clean up

Stop the port forward and delete the cluster. This removes everything the guide created, including every Secret.

```sh
kill %1
kind delete cluster --name ploeg-quickstart
rm -f "$KUBECONFIG"
```

## Next steps

**Use a real model.** Only the gateway changes. In `ops/quickstart/litellm.yaml`, replace the `mock_response` model with a provider [LiteLLM supports](https://docs.litellm.ai/docs/providers), and give the provider key to the LiteLLM pod from a Secret. Keep the caps: the Team's `budget` is the most one Run's key may spend, and the operator consumer's `maxBudgetUsd` is the most one Work Item may spend. With the quickstart harness a Run makes one short request, so it costs a fraction of a cent. This path is not tested by this guide.

**Use a real agent.** Ploeg publishes no agent image. A real harness, such as OpenHands, Claude Code or an [ACP](contracts/acp-profiles.md) agent, runs in an image you build with that agent and `git` in it. Set `executor.harness.name` and `executor.harness.image` to it. [Give Runs a toolchain and checks](how-to/give-runs-a-toolchain-and-checks.md) adds the tools your repository's checks need.

**Connect a work source and a forge.** [Connect GitHub Issues](how-to/connect-github-issues.md) and [connect GitHub as a forge](how-to/connect-github-as-a-forge.md) replace the `curl` request and the local Forgejo. [Tracker configuration](ops/board.md) covers Vikunja and ClickUp.

**Understand the design.** Read the [architecture](architecture.md) and the [configuration reference](reference/configuration.md).
