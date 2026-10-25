---
type: reference
audience: [operator, contributor, agent]
owner: ploeg
last_verified: 2026-10-25
verified_by: "Read pkg/store/{store,shift,withdraw,operator_execution,llm_accounts}.go, pkg/worker/{worker,lease,api}.go, pkg/httpapi/server.go, pkg/harness/{adapter,spawner}.go, pkg/harness/adapters/acp/acp.go, cmd/ploegd/{main,sweep}.go and ops/helm/ploeg/values.yaml at the commit that bounded the worker by its Lease deadline; ran go test -race ./pkg/worker/ with the fencing, expiry, deadline, stop-guard, three-strike and lost-lease verdict checks removed one at a time"
---

# Leases and cancellation

This page states what a Lease guarantees, how long each failure takes to resolve, and which test proves each guarantee. Line references are to the commit that bounded the worker by its Lease deadline. To recover a Lease or Run by hand, use [Recover a stuck Lease or Run](../how-to/recover-a-stuck-lease-or-run.md).

## Terms

- **Attempt**: one claim of a Work Item, or of a pending Run in a Shift. Every attempt gets a fresh run token, and every later call from the worker is made with that token.
- **Run deadline** (`agent_runs.expires_at`): the attempt's liveness. Readers and writers in a Shift both carry one. A shift-less claim carries none and relies on its Lease alone.
- **Lease** (`leases`, one row per Work Item): a writer's exclusive right to the Work Item's branch ([ADR-0010](../adrs/0010-shift-owns-the-item-lease-owns-the-branch.md)).

## Defaults

| Setting | Binary default | Chart default | Source |
| --- | --- | --- | --- |
| Lease TTL `L` (`PLOEG_LEASE_TTL`) | 60s | 5m | [main.go:52](../../cmd/ploegd/main.go#L52), [values.yaml:373](../../ops/helm/ploeg/values.yaml#L373) |
| Sweep interval `S` (`PLOEG_SWEEP_INTERVAL`) | 15s | 15s | [main.go:53](../../cmd/ploegd/main.go#L53), [values.yaml:374](../../ops/helm/ploeg/values.yaml#L374) |
| Renewal interval `I` | `max(L/4, 5s)` = 15s | 75s | [lease.go:15](../../pkg/worker/lease.go#L15) |
| Stop margin `M`: the worker stops this long before its Lease ends | `L/6` = 10s | 50s | [lease.go:19](../../pkg/worker/lease.go#L19) |
| Failed renewals that stop a Run | 3 consecutive | 3 consecutive | [lease.go:11](../../pkg/worker/lease.go#L11) |
| Worker request timeout `H` | 30s | 30s | [worker.go:163](../../pkg/worker/worker.go#L163) |
| Shift-less re-queue backoff after a lost Lease | 1, 5, 15, then 60 minutes; `stale` after 10 | same | [store.go:1074](../../pkg/store/store.go#L1074), [store.go:53](../../pkg/store/store.go#L53) |
| Writer retries in a Shift after a lost Lease | 10, the infrastructure budget | same | [failedwriter.go:35](../../pkg/shiftengine/failedwriter.go#L35), [ADR-0021](../adrs/0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md) |

## Lifecycle

1. **Claim.** A shift-less claim takes the highest-priority queued Work Item with `FOR UPDATE SKIP LOCKED` and inserts its Lease in the same transaction ([store.go:353](../../pkg/store/store.go#L353)). A Shift claim takes the oldest pending Run the same way, sets the Run deadline to now plus `L`, and inserts a Lease only for a writer ([shift.go:376](../../pkg/store/shift.go#L376), [shift.go:449](../../pkg/store/shift.go#L449)). `leases.work_item_id` is the primary key, so a second writer for one Work Item cannot exist.
2. **Renew.** The worker renews every `I` ([worker.go:265](../../pkg/worker/worker.go#L265)). ploegd sets both deadlines to now plus `L` and answers with the deadline and `expiresInMs`, the time left until it ([server.go:857](../../pkg/httpapi/server.go#L857)). It renews a Run deadline only while the Run is `running`, and answers 404 when neither a running Run nor a Lease matches the token, or when the token's Lease or Run deadline has passed, swept or not ([store.go:435](../../pkg/store/store.go#L435), [store.go:465](../../pkg/store/store.go#L465), [store.go:444](../../pkg/store/store.go#L444), [store.go:460](../../pkg/store/store.go#L460)).
3. **Report.** Checkpoints resolve the token through the Lease, or through a `running` Run for a reader ([store.go:498](../../pkg/store/store.go#L498)). The outcome is a compare-and-swap on the Run moving from `running` to `finished` ([store.go:646](../../pkg/store/store.go#L646)). Both are refused with 404 once the deadline has passed, exactly as after a sweep ([store.go:488](../../pkg/store/store.go#L488), [store.go:594](../../pkg/store/store.go#L594)). Exactly one caller wins it, and the same transaction releases the Lease.
4. **Expire.** Each sweep finishes overdue shift-less Leases and re-queues their Work Item with backoff ([store.go:1016](../../pkg/store/store.go#L1016)). It also finishes overdue Runs, drops a writer's Lease, blocks the Run's model key and revokes its per-Run push credential ([shift.go:612](../../pkg/store/shift.go#L612), [sweep.go:104](../../cmd/ploegd/sweep.go#L104)). Both record `failed` with `failure_reason = lease_lost`. The Shift engine then re-opens a failed writer's Round within the infrastructure budget ([sweep.go:140](../../cmd/ploegd/sweep.go#L140)).
5. **Cancel.** Withdrawing a Work Item finishes its running Runs and deletes its Lease in one transaction ([withdraw.go:195](../../pkg/store/withdraw.go#L195)). The withdraw handler blocks their model keys and revokes their push credentials. The worker learns at its next renewal.

## Fencing

A tracker Run is fenced by its run token, and every write from the worker passes through the token's `running` state. After the sweeper, a withdrawal or the attempt's own outcome finishes the Run, the old token gets 404 on renewal, checkpoint and outcome. A newer attempt holds a different token, so nothing the old attempt sends can reach the new one. The fence lives in Postgres, so it survives a ploegd restart. A 404, 401 or 403 on renewal means "no longer ours", and the worker cancels the harness at once ([api.go:226](../../pkg/worker/api.go#L226), [lease.go:96](../../pkg/worker/lease.go#L96)).

Operator executions also carry a revision and a generation. A command must name both ([operator_execution.go:225](../../pkg/store/operator_execution.go#L225)), and a model credential is minted or recorded only for the current generation of a running, unexpired execution ([llm_accounts.go:149](../../pkg/store/llm_accounts.go#L149)). Tracker Runs have no generation number; their fencing token is the run token.

## Stopping before the Lease ends

The worker stops its harness for one of three reasons, whichever comes first. Each cancels the Run with `errLeaseLost`.

- **The Lease is no longer ours.** A 404, 401 or 403 on renewal.
- **Three strikes.** A renewal that fails without an answer (a refused or timed-out connection, or a 5xx) is a strike. Three consecutive strikes cancel the Run; any successful renewal resets the count ([lease.go:103](../../pkg/worker/lease.go#L103)).
- **The stop guard.** The worker times its Lease on its own clock: from the moment it sent the claim, and then each successful renewal, it adds the `expiresInMs` ploegd returned ([worker.go:259](../../pkg/worker/worker.go#L259), [lease.go:110](../../pkg/worker/lease.go#L110)). It cancels the Run `M` before that time, however the renewals in between failed, and abandons a renewal still in flight ([lease.go:61](../../pkg/worker/lease.go#L61)). Measuring from the send time makes the local deadline no later than ploegd's, and measuring a duration rather than reading `deadline` keeps clock skew between the two out of it. An older ploegd sends no `expiresInMs`; the worker then uses the TTL it read from the claim.

The worker then tries to report its outcome. A Run that can still report records `failed` with `failure_reason = lease_lost` ([worker.go:1085](../../pkg/worker/worker.go#L1085)), the sweeper's verdict for the same condition. A shift-less Work Item is re-queued under the infrastructure budget with the sweeper's backoff ([store.go:684](../../pkg/store/store.go#L684), [store.go:1065](../../pkg/store/store.go#L1065)), and a Shift re-opens the Round under its infrastructure budget ([ADR-0021](../adrs/0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)). A Run that cannot report is left to the sweeper, which records the same verdict.

## Timing bounds

`r` is the time the worker sent its last successful renewal, or its claim when no renewal has succeeded yet. ploegd sets the deadline after `r`, so the Lease can be reclaimed no earlier than `r + L`.

| From | To | Bound | Chart defaults | Binary defaults |
| --- | --- | --- | --- | --- |
| A worker dies | its attempt is finished by the sweeper | `r + L + S` | 5m15s | 75s |
| A worker dies, planned Shift | the retry Run is pending and claimable | `r + L + S`, the same sweep | 5m15s | 75s |
| A worker dies, shift-less claim | the Work Item is claimable again | `r + L + S + backoff`; the first backoff is 1 minute | 6m15s | 2m15s |
| A partition starts | the worker cancels its harness | `r + L − M`, whatever the failures look like and however long a request hangs | 4m10s | 50s |
| A partition starts, connections refused at once | the worker cancels its harness | `r + 3I` (below `L − M` for any `L ≥ 20s`) | 3m45s | 45s |
| The worker cancels its harness | its Lease can be reclaimed | at least `M` later | 50s | 10s |
| A Work Item is withdrawn | the worker cancels its harness | `I` plus one request | 75s | 15s |
| The worker cancels its harness | the harness process is gone | spawned harness: its process group gets SIGKILL at once, and its output pipes close within 10s ([spawner.go:233](../../pkg/harness/spawner.go#L233), [adapter.go:309](../../pkg/harness/adapter.go#L309)); ACP harness: up to 2s + 30s + 10s, the cancel notice, the cancel grace and the terminate grace ([acp.go:42](../../pkg/harness/adapters/acp/acp.go#L42), [acp.go:320](../../pkg/harness/adapters/acp/acp.go#L320)) | | |

Before the stop guard, the partition bound was `r + I + 2·max(I, H) + H` with `I = L/3`: Go's ticker keeps one tick queued while a renewal hangs for `H`, so the second and third strikes could follow back to back and land up to `H` after the Lease ended (5m30s with chart defaults, 110s with binary defaults). The guard caps every case at `r + L − M`. Add the ploegd sweep's own duration and, for re-dispatch, the time the executor takes to start a pod. Ploeg does not bound either.

## Residual risks

- **An ACP harness can outlive a short Lease.** The worker cancels `M` before the Lease ends, but an ACP harness may take up to 42s to stop. With chart defaults `M` is 50s, so it is gone first. With binary defaults `M` is 10s, and the harness may run up to 32s past the point where the Lease can be reclaimed; with a shift-less claim, which uses the shared forge credential, it could still push in that window. A spawned harness is killed at once.
- **The stop guard trusts the worker's clock rate.** It measures a duration of at most `L` on the worker's monotonic clock, so only a clock running more than `M/L` (a sixth) slow could defeat it.
- **Side effects outside Ploeg are not rolled back.** A push, a pull request comment or model spend made before the fence took effect stays. A refused report's usage is not recorded. A Run with a managed Inference Account is settled from the gateway by the settlement sweep ([architecture](../architecture.md)); without one, the Shift's spent total misses that attempt's spend.

## Tests

The tests are in [lease_fencing_linux_test.go](../../pkg/worker/lease_fencing_linux_test.go). They run a real worker against a real ploegd over an embedded Postgres, with a controllable network, and with renewal ticks and a worker clock the test steps by hand.

| Guarantee | Test | Fails when |
| --- | --- | --- |
| A late renewal, checkpoint or outcome from attempt N is refused while attempt N+1 holds the Work Item, and changes nothing | `TestLateReportsFromASupersededAttemptAreRefusedAndChangeNothing` | the `running` check leaves the outcome or checkpoint statement, or renewal stops requiring `running` |
| A ploegd restart mid-Run keeps the live Run renewing and reporting, and dispatches nothing twice | `TestControllerRestartMidRunKeepsTheLiveRunAndDispatchesNothingTwice` | either sweep stops comparing the deadline with now |
| A partition longer than the Lease re-dispatches the Work Item; after healing, the old worker stops and its report is refused | `TestPartitionLongerThanTheLeaseRedispatchesAndFencesTheOldWorker` | renewal revives a finished Run, or the outcome statement loses its `running` check |
| Racing claims for one Work Item produce exactly one attempt | `TestRacingClaimsForOneItemDispatchItExactlyOnce` | |
| Three consecutive failed renewals stop the harness, and a success resets the count | `TestThreeConsecutiveFailedRenewalsStopTheHarnessThreeQuartersOfALeaseAfterTheLastSuccess` | the three-strike cancel is removed, strikes never reset, or the worker reports a lost Lease as `stuck` |
| A worker that loses its Lease and can still report leaves the same Run, Work Item, Shift and retry state as the sweeper would | `TestAWorkerThatLosesItsLeaseReportsTheSweepersVerdict` | the worker reports `stuck`, or a shift-less `lease_lost` outcome is charged as an agent attempt |
| A worker whose renewal hangs stops its harness `M` before its Lease ends, and its report lands | `TestACutOffWorkerStopsBeforeItsLeaseCanBeReclaimed` | the stop guard is not armed, or a renewal does not extend it |
| A renewal, checkpoint or outcome after the deadline, before any sweep, is refused and changes nothing | `TestAnExpiredLeaseIsRefusedBeforeTheSweeperRuns` | any of the three stops comparing the deadline with now |
| A withdrawal stops the harness at the first renewal after it | `TestWithdrawnRunStopsItsHarnessAtTheNextRenewal` | renewal revives a finished Run |
| The renewal interval is a quarter of the TTL, never under 5s | `TestRenewalIntervalIsAQuarterOfTheLeaseAndNeverUnderFiveSeconds` | |
| Three fast strikes land before the stop guard, which stops at least `L/6` before the Lease ends | `TestThreeFastStrikesComeBeforeTheStopMarginAndTheMarginBeforeTheLeaseEnds` | the stop margin is removed |

Store-level coverage for the same guarantees: `TestSweptRunCannotReport` and `TestWriterTakesTheLease` ([shift_test.go](../../pkg/store/shift_test.go)), and `TestOperatorExecutionCommandsFenceRevisionGenerationAndReplay` and `TestOperatorExecutionExpiryNeverRequeuesOrRenewsOldAuthority` ([operator_execution_test.go](../../pkg/store/operator_execution_test.go)).
