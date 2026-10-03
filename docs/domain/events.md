---
type: reference
audience: [owner, integrator, contributor, agent]
owner: ploeg
generated_by: "mise run domain"
---

# Domain Events — Ploeg

*Generated from `model.yaml` — do not edit by hand.*

## OperatorExecutionAdmitted

One authenticated session was bound to a Work Item, Shift and Run.

**Concerns:** Operator Execution  
**Triggers:** Makes explicit start available without launching an agent.  

## OperatorExecutionCommandAccepted

A command passed identity, revision and generation checks and advanced the serialized revision.

**Concerns:** Operator Execution  
**Triggers:** The executor or workbench observes the new lifecycle or supervision state.  

## OperatorExecutionExpired

Executor authority expired while work or a stop request remained unresolved.

**Concerns:** Operator Execution  
**Triggers:** Repeatedly attempt capability blocking; never enqueue a replacement execution.  

## WorkItemIngested

A tracker webhook was mirrored into a new Work Item, recording the Scope it arrived in.

**Concerns:** Work Item  
**Triggers:** Eligible assignments are routed at ingestion; an intermediate ingested database state is not required.  

## WorkItemAssigned

An Assignment matched a Routing Rule; the Work Item is queued with its Team and its pinned Work Target.

**Concerns:** Work Item  
**Triggers:** The Team Queue grows; the Executor's scaler may spawn a Run.  

## ShiftOpened

A Team took up a queued Work Item; the branch, budget pool and Round counter come into existence.

**Concerns:** Shift  
**Triggers:** A Round is planned and its Runs are spawned with Task Specs.  

## RoundStarted

A set of Runs was spawned together — either a fan-out of readers or a single writer.

**Concerns:** Shift  
**Triggers:** Each Run receives the same injected state; none observes the others.  

## LeaseAcquired

A writing Run acquired the recorded lease for its Shift's branch. External credential issuance is a separate operation.

**Concerns:** Lease  
**Triggers:** The configured credential broker supplies access. The database lease alone does not prove external push fencing; legacy static credentials have weaker guarantees.  

## PushRightsRevoked

The configured broker confirmed revocation of a Run's scoped forge token.

**Concerns:** Lease  
**Triggers:** Access through that token is removed. Lease expiry alone cannot establish this event, and a shared static token cannot provide per-Run revocation.  

## BudgetAuthorized

A Run reserved min(roleCap, poolRemaining) against its Shift before spawning.

**Concerns:** Shift  
**Triggers:** Managed credential issuance may proceed after the durable reservation. Reservation, external key creation and confirmation have separate states; uncertainty retains the hold.  

## BudgetSettled

Accounting finalized a Run's spend and released the corresponding reservation. A managed Run's outcome report alone does not settle its inference account.

**Concerns:** Shift  
**Triggers:** Unused allowance becomes available only after settlement; missing or uncertain accounting retains a conservative hold.  

## LeaseExpired

A Lease TTL lapsed without renewal.

**Concerns:** Lease  
**Triggers:** Apply the lane's recovery policy and record the reason. Legacy tracker work may requeue; operator-owned work is excluded and reconciled without an automatic replacement.  

## CheckpointWritten

A Run reported durable progress via the report API.

**Concerns:** Checkpoint  
**Triggers:** Progress is available for audit and recovery. The Task Spec supports a Checkpoint, but the current unattended worker does not populate it automatically.  

## OutcomeReported

A Run ended with an Outcome Report.

**Concerns:** Run  
**Triggers:** Work Item transitions per the Outcome; write-back to tracker/forge through providers.  

## FollowUpCreated

Intended event — an actionable Forge Event becomes a Follow-Up. Current forge webhook ingestion records and deduplicates events without creating this work.

**Concerns:** Work Item  
**Triggers:** The owning Team's Queue grows; vague or security-sensitive feedback goes to needs_human instead.  
