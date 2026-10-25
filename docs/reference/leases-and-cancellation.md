---
type: reference
audience: [operator, contributor, agent]
owner: ploeg
last_verified: 2026-10-25
verified_by: "Read pkg/store/{store,shift,withdraw,operator_execution,llm_accounts}.go, pkg/worker/{worker,api}.go, pkg/httpapi/server.go, pkg/harness/{adapter,spawner}.go, pkg/harness/adapters/acp/acp.go, cmd/ploegd/{main,sweep}.go and ops/helm/ploeg/values.yaml at the commit that added this page; ran go test -race ./pkg/worker/ with the fencing, expiry and three-strike checks removed one at a time"
---

# Leases and cancellation

This page states what a Lease guarantees, how long each failure takes to resolve, and which test proves each guarantee. Line references are to the commit that added this page. To recover a Lease or Run by hand, use [Recover a stuck Lease or Run](../how-to/recover-a-stuck-lease-or-run.md).

## Terms

- **Attempt**: one claim of a Work Item, or of a pending Run in a Shift. Every attempt gets a fresh run token, and every later call from the worker is made with that token.
- **Run deadline** (`agent_runs.expires_at`): the attempt's liveness. Readers and writers in a Shift both carry one. A shift-less claim carries none and relies on its Lease alone.
- **Lease** (`leases`, one row per Work Item): a writer's exclusive right to the Work Item's branch ([ADR-0010](../adrs/0010-shift-owns-the-item-lease-owns-the-branch.md)).

## Defaults

| Setting | Binary default | Chart default | Source |
| --- | --- | --- | --- |
| Lease TTL `L` (`PLOEG_LEASE_TTL`) | 60s | 5m | [main.go:52](../../cmd/ploegd/main.go#L52), [values.yaml:366](../../ops/helm/ploeg/values.yaml#L366) |
| Sweep interval `S` (`PLOEG_SWEEP_INTERVAL`) | 15s | 15s | [main.go:53](../../cmd/ploegd/main.go#L53), [values.yaml:367](../../ops/helm/ploeg/values.yaml#L367) |
| Renewal interval `I` | `max(L/3, 5s)` = 20s | 100s | [worker.go:1177](../../pkg/worker/worker.go#L1177) |
| Failed renewals that stop a Run | 3 consecutive | 3 consecutive | [worker.go:1173](../../pkg/worker/worker.go#L1173) |
| Worker request timeout `H` | 30s | 30s | [worker.go:162](../../pkg/worker/worker.go#L162) |
| Shift-less re-queue backoff after a lost Lease | 1, 5, 15, then 60 minutes; `stale` after 10 | same | [store.go:1023](../../pkg/store/store.go#L1023), [store.go:53](../../pkg/store/store.go#L53) |
| Writer retries in a Shift after a lost Lease | 10, the infrastructure budget | same | [failedwriter.go:35](../../pkg/shiftengine/failedwriter.go#L35), [ADR-0021](../adrs/0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md) |

## Lifecycle

1. **Claim.** A shift-less claim takes the highest-priority queued Work Item with `FOR UPDATE SKIP LOCKED` and inserts its Lease in the same transaction ([store.go:353](../../pkg/store/store.go#L353)). A Shift claim takes the oldest pending Run the same way, sets the Run deadline to now plus `L`, and inserts a Lease only for a writer ([shift.go:376](../../pkg/store/shift.go#L376), [shift.go:449](../../pkg/store/shift.go#L449)). `leases.work_item_id` is the primary key, so a second writer for one Work Item cannot exist.
2. **Renew.** The worker renews every `I` ([worker.go:258](../../pkg/worker/worker.go#L258)). ploegd sets both deadlines to now plus `L`. It renews a Run deadline only while the Run is `running`, and answers 404 when neither a running Run nor a Lease matches the token ([store.go:441](../../pkg/store/store.go#L441), [store.go:457](../../pkg/store/store.go#L457)).
3. **Report.** Checkpoints resolve the token through the Lease, or through a `running` Run for a reader ([store.go:478](../../pkg/store/store.go#L478)). The outcome is a compare-and-swap on the Run moving from `running` to `finished` ([store.go:623](../../pkg/store/store.go#L623)). Exactly one caller wins it, and the same transaction releases the Lease.
4. **Expire.** Each sweep finishes overdue shift-less Leases and re-queues their Work Item with backoff ([store.go:982](../../pkg/store/store.go#L982)). It also finishes overdue Runs, drops a writer's Lease, blocks the Run's model key and revokes its per-Run push credential ([shift.go:612](../../pkg/store/shift.go#L612), [sweep.go:104](../../cmd/ploegd/sweep.go#L104)). Both record `failed` with `failure_reason = lease_lost`. The Shift engine then re-opens a failed writer's Round within the infrastructure budget ([sweep.go:140](../../cmd/ploegd/sweep.go#L140)).
5. **Cancel.** Withdrawing a Work Item finishes its running Runs and deletes its Lease in one transaction ([withdraw.go:195](../../pkg/store/withdraw.go#L195)). The withdraw handler blocks their model keys and revokes their push credentials. The worker learns at its next renewal.

## Fencing

A tracker Run is fenced by its run token, and every write from the worker passes through the token's `running` state. After the sweeper, a withdrawal or the attempt's own outcome finishes the Run, the old token gets 404 on renewal, checkpoint and outcome. A newer attempt holds a different token, so nothing the old attempt sends can reach the new one. The fence lives in Postgres, so it survives a ploegd restart. A 404, 401 or 403 on renewal means "no longer ours", and the worker cancels the harness at once ([api.go:205](../../pkg/worker/api.go#L205), [worker.go:1198](../../pkg/worker/worker.go#L1198)).

Operator executions also carry a revision and a generation. A command must name both ([operator_execution.go:225](../../pkg/store/operator_execution.go#L225)), and a model credential is minted or recorded only for the current generation of a running, unexpired execution ([llm_accounts.go:149](../../pkg/store/llm_accounts.go#L149)). Tracker Runs have no generation number; their fencing token is the run token.

## The three-strike rule

A renewal that fails without an answer (a refused or timed-out connection, or a 5xx) is a strike. Three consecutive strikes cancel the Run with `errLeaseLost`; any successful renewal resets the count ([worker.go:1205](../../pkg/worker/worker.go#L1205)). The worker then tries to report its outcome. A Run that can still report records `stuck` with `failure_reason = lease_lost` ([worker.go:1080](../../pkg/worker/worker.go#L1080)). A Run that cannot report is left to the sweeper, which records `failed`.

## Timing bounds

`r` is the time of the last successful renewal, or of the claim when no renewal has succeeded yet.

| From | To | Bound | Chart defaults | Binary defaults |
| --- | --- | --- | --- | --- |
| A worker dies | its attempt is finished by the sweeper | `r + L + S` | 5m15s | 75s |
| A worker dies, planned Shift | the retry Run is pending and claimable | `r + L + S`, the same sweep | 5m15s | 75s |
| A worker dies, shift-less claim | the Work Item is claimable again | `r + L + S + backoff`; the first backoff is 1 minute | 6m15s | 2m15s |
| A partition starts | the worker cancels its harness | `r + I + 2·max(I, H) + H` | 5m30s (`H < I`) | 110s (`H > I`) |
| A partition starts, connections refused at once | the worker cancels its harness | `r + 3I` | 5m | 60s |
| A Work Item is withdrawn | the worker cancels its harness | `I` plus one request | 100s | 20s |
| The worker cancels its harness | the harness process is gone | spawned harness: its process group gets SIGKILL at once, and its output pipes close within 10s ([spawner.go:233](../../pkg/harness/spawner.go#L233), [adapter.go:309](../../pkg/harness/adapter.go#L309)); ACP harness: up to 2s + 30s + 10s, the cancel notice, the cancel grace and the terminate grace ([acp.go:42](../../pkg/harness/adapters/acp/acp.go#L42), [acp.go:320](../../pkg/harness/adapters/acp/acp.go#L320)) | | |

The partition bound comes from Go's ticker: a renewal blocked for `H` drops ticks but keeps one queued, so when `H > I` the second and third strikes follow back to back. Add the ploegd sweep's own duration and, for re-dispatch, the time the executor takes to start a pod. Ploeg does not bound either.

## Residual risks

- **A partitioned worker can outlive its Lease.** Three strikes end at about `r + L`, and with slow failures later; the sweeper can reclaim the Lease from `r + L` on. A retry can therefore be claimed before the old harness stops. The overlap lasts up to `H` with chart defaults (50s with binary defaults), plus the harness stop time. ploegd refuses every write the old attempt sends in that window. Shift writers lose their per-Run push credential when the sweeper reclaims them, in a deployment that mints one. Shift-less claims receive the shared forge credential, so until the old worker stops, it can still push to the Work Item's branch.
- **An expired but unswept Lease can be renewed.** Neither renewal nor the outcome compares the deadline with the current time. A worker whose renewal or report arrives after `expires_at` but before the next sweep revives its Lease or finishes its Run normally. Nothing else can hold the Work Item in that window, so this extends exclusivity rather than breaking it.
- **A worker that strikes out but can still report parks its Work Item.** It reports `stuck`, so the Shift closes for a person, although `lease_lost` counts as an infrastructure failure everywhere else. The sweeper's verdict for the same condition is `failed`, which retries. `TestResolveOutcome_Precedence` pins this mapping.
- **Side effects outside Ploeg are not rolled back.** A push, a pull request comment or model spend made before the fence took effect stays. A refused report's usage is not recorded. A Run with a managed Inference Account is settled from the gateway by the settlement sweep ([architecture](../architecture.md)); without one, the Shift's spent total misses that attempt's spend.

## Tests

The tests are in [lease_fencing_linux_test.go](../../pkg/worker/lease_fencing_linux_test.go). They run a real worker against a real ploegd over an embedded Postgres, with a controllable network and renewal ticks the test steps one at a time.

| Guarantee | Test | Fails when |
| --- | --- | --- |
| A late renewal, checkpoint or outcome from attempt N is refused while attempt N+1 holds the Work Item, and changes nothing | `TestLateReportsFromASupersededAttemptAreRefusedAndChangeNothing` | the `running` check leaves the outcome or checkpoint statement, or renewal stops requiring `running` |
| A ploegd restart mid-Run keeps the live Run renewing and reporting, and dispatches nothing twice | `TestControllerRestartMidRunKeepsTheLiveRunAndDispatchesNothingTwice` | either sweep stops comparing the deadline with now |
| A partition longer than the Lease re-dispatches the Work Item; after healing, the old worker stops and its report is refused | `TestPartitionLongerThanTheLeaseRedispatchesAndFencesTheOldWorker` | renewal revives a finished Run, or the outcome statement loses its `running` check |
| Racing claims for one Work Item produce exactly one attempt | `TestRacingClaimsForOneItemDispatchItExactlyOnce` | |
| Three consecutive failed renewals stop the harness, and a success resets the count | `TestThreeConsecutiveFailedRenewalsStopTheHarnessOneLeaseTTLAfterTheLastSuccess` | the three-strike cancel is removed, or strikes never reset |
| A withdrawal stops the harness at the first renewal after it | `TestWithdrawnRunStopsItsHarnessAtTheNextRenewal` | renewal revives a finished Run |
| The renewal interval is a third of the TTL, never under 5s | `TestRenewalIntervalIsAThirdOfTheLeaseAndNeverUnderFiveSeconds` | |

Store-level coverage for the same guarantees: `TestSweptRunCannotReport` and `TestWriterTakesTheLease` ([shift_test.go](../../pkg/store/shift_test.go)), and `TestOperatorExecutionCommandsFenceRevisionGenerationAndReplay` and `TestOperatorExecutionExpiryNeverRequeuesOrRenewsOldAuthority` ([operator_execution_test.go](../../pkg/store/operator_execution_test.go)).
