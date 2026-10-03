---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-09-23
verified_by: "Read apps/ploeg ops/helm/ploeg/{values.yaml,templates/scaledjob.yaml,templates/cronjob.yaml,templates/deployment.yaml}, cmd/ploegd/sweep.go, cmd/ploeg-worker/main.go (signal handling), pkg/store/{store,shift,llm_settlement}.go and pkg/store/operator.go (team paused field never set)"
---

# Drain workers before maintenance

**Symptom:** you are about to upgrade Ploeg, change the LiteLLM gateway, rotate a credential without overlap, or work on the database, and a Run in flight would fail or leave spend unsettled.

**Goal:** stop new claims, let every running Run finish and every inference account settle, then scale down, and afterwards bring everything back in the right order.

Read [Before you start](index.md#before-you-start) for names and the database session.

**Not implemented yet:** Ploeg has no dispatch switch. There is no chart value, API call or per-team pause; the operator view's team `paused` field is always empty ([operator.go](../../pkg/store/operator.go)). You stop claims by pausing the Kubernetes workloads that start workers. Ingest continues meanwhile: new assignments queue and new Rounds open as `pending` Runs, and they wait for the workers to return.

## 1. Stop Flux from undoing the drain

The pause steps change live objects that Flux manages. Suspend the HelmRelease for the maintenance window:

```sh
kubectl -n ploeg patch helmrelease ploeg --type=merge -p '{"spec":{"suspend":true}}'
```

Resume it in [step 6](#6-resume). While it is suspended, Flux applies no change to Ploeg from Git.

## 2. Stop new claims

List the worker workloads. The chart renders one per team and Role:

```sh
kubectl -n ploeg get scaledjobs,cronjobs
```

For the KEDA executor (`executor.type: keda`), pause every ScaledJob. KEDA then starts no new Jobs and leaves running Jobs alone. Pausing a ScaledJob requires KEDA 2.13 or later.

```sh
for sj in $(kubectl -n ploeg get scaledjobs -o name); do
  kubectl -n ploeg annotate "$sj" autoscaling.keda.sh/paused=true --overwrite
done
```

For the CronJob executor (`executor.type: cronjob`), suspend every CronJob:

```sh
for cj in $(kubectl -n ploeg get cronjobs -o name); do
  kubectl -n ploeg patch "$cj" -p '{"spec":{"suspend":true}}'
done
```

Do not remove the executor from the values (`executor.enabled: false`) while Runs are in flight. Deleting a ScaledJob also deletes the Jobs it started, which stops their Runs.

Vloer sessions run as operator executions, which the worker workloads do not start. Ask users to finish or cancel their sessions in Vloer. **Not implemented yet:** a Ploeg switch that refuses new operator admissions.

## 3. Let running Runs finish

Watch the Runs and the worker pods:

```sql
SELECT r.id AS run_id, r.work_item_id, r.team, r.role, r.writes,
       r.started_at, now() - r.started_at AS running_for,
       EXISTS (SELECT 1 FROM operator_executions e WHERE e.run_id = r.id) AS operator_run
FROM agent_runs r WHERE r.state = 'running' ORDER BY r.started_at;
```

```sh
kubectl -n ploeg get pods -l app.kubernetes.io/name=ploeg-worker
```

A Run is bounded by the harness timeout (100 minutes by default) and the Job deadline (`activeDeadlineSeconds`, 7200 seconds). To end one sooner, delete its Job. The worker receives SIGTERM and has 90 seconds to stop the harness, revoke its key and report an outcome ([main.go](../../cmd/ploeg-worker/main.go)):

```sh
kubectl -n ploeg delete job <job>
```

An interrupted writer counts as a failed Run. After the drain, the Shift engine re-opens its Round, within the retry cap.

## 4. Let spend settle

A finished Run's inference account still holds budget until the sweeper blocks its key and settles it from the gateway's spend logs. It settles after the account has been unchanged for `PLOEG_LLM_SETTLE_AFTER` (15 minutes by default). If your maintenance touches the gateway or the database, wait for this:

```sql
SELECT a.state, count(*) AS accounts, max(now() - a.updated_at) AS oldest_unchanged
FROM run_llm_accounts a JOIN agent_runs r USING (run_token)
WHERE r.state = 'finished' AND a.state <> 'reconciled'
GROUP BY a.state;
```

`blocked` rows settle on their own after the quiet period. Rows in `minting`, `issued` or `unknown` need a working gateway. [Investigate a Run's spend](investigate-a-runs-spend.md) explains each state. Maintenance can go ahead with unsettled accounts: their hold stays until settlement, so a Shift never looks cheaper than it was.

## 5. Scale down the controller if the work needs it

Stop `ploegd` only when the maintenance needs it, for example database work. While it is down, its sweeper, its API and its webhooks are down too. A tracker assignment made in that window may never arrive; re-assign it afterwards.

```sh
kubectl -n ploeg scale deployment/ploeg --replicas=0
```

## Verify the drain

All of these are empty or zero before you start maintenance:

```sql
SELECT count(*) AS running_runs FROM agent_runs WHERE state = 'running';
SELECT count(*) AS live_leases FROM leases;
```

```sh
kubectl -n ploeg get pods -l app.kubernetes.io/name=ploeg-worker
kubectl -n ploeg get jobs
```

Completed Jobs can remain listed; no worker pod may be `Running`.

## 6. Resume

Reverse the order: controller, then workers, then Flux.

1. If you scaled `ploegd` down, bring it back and wait until it is ready:

   ```sh
   kubectl -n ploeg scale deployment/ploeg --replicas=1
   kubectl -n ploeg rollout status deployment/ploeg --timeout=180s
   ```

2. Unpause the ScaledJobs or unsuspend the CronJobs:

   ```sh
   for sj in $(kubectl -n ploeg get scaledjobs -o name); do
     kubectl -n ploeg annotate "$sj" autoscaling.keda.sh/paused-
   done
   for cj in $(kubectl -n ploeg get cronjobs -o name); do
     kubectl -n ploeg patch "$cj" -p '{"spec":{"suspend":false}}'
   done
   ```

3. Resume the HelmRelease. Flux then reconciles any change from Git that waited:

   ```sh
   kubectl -n ploeg patch helmrelease ploeg --type=merge -p '{"spec":{"suspend":false}}'
   kubectl -n ploeg get helmrelease ploeg
   ```

Verify: within one KEDA polling interval (30 seconds by default), a queued item or pending Run gets a worker pod and its Run reaches `running`.

## Symptom, cause, fix

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| New worker pods appear after pausing | Flux or KEDA reverted the pause, or KEDA is older than 2.13 | Suspend the HelmRelease first. Check the KEDA version with `kubectl -n keda get deployment keda-operator -o jsonpath='{..image}'` |
| A Run is still `running` after its pod is gone | The pod died without reporting | The sweeper expires it after one Lease TTL; see [Recover a stuck Lease or Run](recover-a-stuck-lease-or-run.md) |
| Running Runs disappeared when the executor was disabled | The ScaledJob was deleted, which deleted its Jobs | Pause instead of deleting; let the retry re-open the Round |
| Accounts stay `unknown` during the drain | The gateway cannot report spend for that key | [Investigate a Run's spend](investigate-a-runs-spend.md) |
| Nothing starts after resuming | ScaledJobs still paused, or HelmRelease still suspended | Remove the annotation; resume the HelmRelease |
| Assignments made during maintenance never ran | `ploegd` was down when the webhook arrived | Re-assign the ticket in the tracker |
