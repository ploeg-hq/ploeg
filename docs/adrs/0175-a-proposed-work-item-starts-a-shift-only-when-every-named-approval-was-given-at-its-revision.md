---
status: proposed
date: 2026-10-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A proposed Work Item starts a Shift only when every named approval was given at its revision

## Context and Problem Statement

A proposed Work Item waits until one `POST /api/v1/operator/work-items/{id}/approve` queues it ([ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md)). One call from any execute-capable Operator Consumer with the item's Team in scope is the whole gate. The call takes no command id, records one actor, and is not tied to what the item said when that person looked at it. An Operator Consumer whose own process needs several people to agree, for example someone who owns the scope and someone who owns the budget, has to keep that gate itself and then make the one call. Ploeg's audit then shows one approval, a replayed call cannot be told from a second one, and a description, Team or budget that changes after the people agreed still starts a Shift on their earlier approval.

How does an Operator Consumer make Ploeg refuse to start a Shift until each of several named approvals has been given, by a person, for the Work Item as it now stands?

## Decision Drivers

* Ploeg decides admission ([ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)). A gate a consumer keeps outside Ploeg is bypassed by any other caller that holds an execute permission.
* An approval is for the Work Item a person saw. If what would run changes, the approval no longer applies.
* Every change names a person ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)). The proof is recorded, and it is required under `PLOEG_OPERATOR_ATTRIBUTION=proven` ([ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)).
* A retried request changes nothing twice: a command id replays, and a reused one with another body conflicts. Ploeg's other operator commands already work this way.
* Ploeg names no consumer and none of a consumer's roles ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)). An approval name is an opaque label that Ploeg compares and never interprets.
* Nothing changes for a proposed Work Item that has no required approvals, or for a caller that sends today's `{reason}` body.

## Considered Options

* **The consumer names the required approvals on the Work Item, each approval is bound to a revision Ploeg computes, and every path that starts a Shift checks them**
* The consumer keeps the gate and calls today's `approve` once its own approvals are complete
* Required approvals per Team in Ploeg's configuration
* A count of approvals (any N persons) instead of names

## Decision Outcome

Chosen option: the first, because it is the only option where Ploeg itself refuses the Shift, its audit names who approved what and at which revision, and a later change cannot start work on an earlier approval.

Proposed, not built. Each clause below states the contract that the implementation must meet.

1. **Declaring.** An Operator Consumer with execute permission declares the set of required approvals in one of two ways. It can pass `requiredApprovals` when it admits a Work Item (`POST /api/v1/operator/work-items`, ADR-0102); the Work Item is then held as `proposed` instead of queued. It can also send `PUT /api/v1/operator/work-items/{id}/required-approvals` with `{names, commandId}` for a Work Item that is `proposed`, including one a Run created (ADR-0031). The PUT replaces the whole set. A name matches `^[a-z0-9][a-z0-9-]{0,62}$`, and a set holds one to eight distinct names. A PUT on a Work Item in any other state answers 409 `not_proposed`. Each declaration is audited as `work_item.approvals_required`, with `names`, `previous` and `revision`. Approvals already given under a name that stays in the set are kept. Approvals under a removed name are voided and listed in `voided`. Declaring changes nothing that the revision covers.
2. **The revision.** Every Work Item has an `approvalRevision`: `sha256:` followed by the lowercase hex SHA-256 of the canonical JSON ([RFC 8785](https://www.rfc-editor.org/rfc/rfc8785)) of the fields that decide what a Shift would do. These are the title, the description, the Team, the Work Target (repository and base branch), the allotted budget or `budgetUsd`, the admission's harness, model and `bakeOff` choices ([ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [ADR-0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md), [ADR-0135](0135-a-bake-off-runs-one-work-item-on-several-configurations-and-a-shadow-judge-recommends-while-a-person-picks.md)), and the tracker's own `revision` when the Work Item has one. Work Item detail carries it. Ploeg recomputes it in the same transaction as every change to one of those fields.
3. **Approving.** `POST /api/v1/operator/work-items/{id}/approve` takes `{approval, revision, commandId, reason?}`. The request needs execute permission and an attributed person (ADR-0108). The `revision` must equal the current `approvalRevision`, or the request answers 409 `revision_changed` with the current revision and records nothing: a person approves the Work Item they saw, not a newer one. The approval is stored with its name, revision, command id, reason and the request's `attribution`, including `proof` (`proven` or `asserted`). Under `PLOEG_OPERATOR_ATTRIBUTION=proven` the operator middleware has already refused an unproven request (ADR-0171). Each approval is audited as `work_item.approval_given`, with `approval`, `revision`, `commandId` and `attribution`.
4. **Idempotency.** A repeated `commandId` with the same body and person answers 200 with the first result and `replayed: true`. The same `commandId` with another Work Item, approval name, revision or person answers 409 `command_conflict`. A second approval under a name that is already given at the current revision answers 200 with `alreadyGiven: true` and the first approval's attribution. It is not stored, so a name holds exactly one approval per revision. A name outside the declared set answers 422 `unknown_approval` and lists the declared names. On a Work Item with no required approvals, `approve` keeps today's behaviour. The `{reason}` body queues the item, and `commandId` is optional there.
5. **Voiding.** When `approvalRevision` changes, every approval given at an earlier revision stops counting. Ploeg keeps the rows and marks them `voided` in the same transaction. It audits one `work_item.approvals_voided` row with actor `ploegd`, `revisionBefore`, `revisionAfter`, `voided` (the names) and `changed` (the names of the fields that differ). An approval is never carried to a new revision.
6. **Admission refuses.** No path starts a Shift for a Work Item that has a required approval missing at its current revision. On such an item, an `approve` without `approval` answers 409 `approval_required`. So does a requeue ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)), including one with `resume: true`, and so does a team's `autoDispatch` route (ADR-0031). The body carries `missingApprovals`, the names not yet given at the current revision in declaration order, and `revision`. Nothing changes. The check runs again inside the transaction that creates the Shift, so a revision change racing an approval cannot start one. When the last missing approval is given on a `proposed` Work Item, that same request queues it, as today's `approve` does. Ploeg audits `work_item.approved` with `approvals`, the names and the revision, and then starts its Shift. On a Work Item outside `proposed`, such as `needs_human` after a voiding change, an approval is recorded and starts nothing. The next requeue admits it.
7. **Reading.** Work Item detail carries `approvals`, which holds `revision`, the `required` names with the approval given for each at that revision (`by`, `proof`, `at`, `reason`) or `null`, `missing`, and the voided approvals of the last 200 rows. ploeg-mcp's read tools pass this through unchanged.
8. **What Ploeg does not decide.** Ploeg does not know which person may give which approval, and it does not stop one person from giving several. An Operator Consumer that needs either rule enforces it before it calls `approve`, and the audit shows each person for review. Removing a name with `PUT` is the only way to drop a requirement, and it is audited.

The contract, sketched (proposed, not built; it enters `operator-api.v1.schema.json` with the Go types that serve it):

```json
{
  "requiredApprovalsRequest": {
    "names": ["scope", "budget"],
    "commandId": "6c1e0b0e-3f5c-4f0e-9f1a-2a4b1d8f7e21"
  },
  "approveRequest": {
    "approval": "budget",
    "revision": "sha256:3b7f…",
    "commandId": "a7d2e9c4-0b1f-4c55-8e3a-91f0c2d4b6aa",
    "reason": "Fits this month's plan."
  },
  "approveResponse": {
    "schemaVersion": "1.0",
    "decision": {"workItemId": 412, "team": "bronze", "state": "proposed", "approved": false},
    "approval": {"name": "budget", "revision": "sha256:3b7f…", "by": "oidc-…", "proof": "proven", "at": "2026-10-29T10:12:00Z"},
    "missingApprovals": ["scope"],
    "replayed": false,
    "alreadyGiven": false
  },
  "approvalRequiredError": {
    "code": "approval_required",
    "message": "The Work Item needs the approvals scope before a Shift can start.",
    "missingApprovals": ["scope"],
    "revision": "sha256:3b7f…"
  }
}
```

### Consequences

* Good, because Ploeg itself refuses the Shift. A second consumer, a retried request or a leaked consumer token without a person token (under `proven`) cannot start a Work Item that people have not approved.
* Good, because each approval names its person, its proof and the exact revision, so a reviewer can tell what was approved and by whom without reading the consumer's records.
* Good, because a change to the description, Team, target, budget or execution choices voids earlier approvals in the same transaction, and the audit names the changed fields.
* Good, because callers of today's `approve` and Work Items without required approvals see no change. Every surface change is an addition, which [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) allows in a candidate.
* Bad, because a tracker edit that changes only wording still voids every approval. Accepted, because Ploeg cannot judge which wording matters. The `changed` field lets a consumer explain the re-approval.
* Bad, because the revision covers a fixed list of fields, and each new field that changes what a Shift does must be added to it. A trigger below covers that.
* Bad, because Ploeg does not enforce who may give which approval. Accepted, because that is the consumer's policy and Ploeg knows none of its roles. The audit makes a breach visible.

### Confirmation

Once built, `go test ./pkg/httpapi/ ./pkg/store/` runs in `.forgejo/workflows/on_pull_request.yml` (`mise exec -- go test ./...`). Against PostgreSQL, the admission test `pkg/httpapi/required_approvals_test.go` must show the following:

* `TestAWorkItemMissingAnApprovalStartsNoShift`: `approve` without `approval`, a requeue and an `autoDispatch` route each answer 409 `approval_required` naming the missing approvals in declaration order, and no Shift row exists afterwards;
* `TestTheLastApprovalQueuesTheWorkItemAndStartsItsShift`, with one `work_item.approved` row naming every approval and the revision;
* `TestAChangedDescriptionVoidsEveryApproval` (and the same for Team, target, budget and tracker revision): one `work_item.approvals_voided` row with `changed`, and a following admission refused;
* `TestAnApprovalAtAStaleRevisionIsRefused` (409 `revision_changed`, nothing recorded);
* `TestARepeatedCommandIdReplays` and `TestACommandIdReusedWithAnotherBodyConflicts`;
* `TestEachApprovalIsAuditedWithItsPersonAndProof` under `asserted` and under `proven` with an in-test issuer;
* `TestARevisionRacingTheLastApprovalStartsNoShift`, where two transactions are interleaved through the store;
* `TestApproveWithoutRequiredApprovalsKeepsTodaysBehaviour`.

Every response is validated against `operator-api.v1.schema.json`. `go test ./internal/compat/` against the previous candidate must find no break. `go test ./internal/ledger/` keeps this record in the index.

## Pros and Cons of the Options

### The consumer keeps the gate and calls today's `approve`

* Good, because Ploeg needs no change.
* Bad, because any other execute-capable caller skips the gate, Ploeg's audit shows one actor, and nothing ties the approval to what the Work Item said.

### Required approvals per Team in Ploeg's configuration

* Good, because an operator sets the policy once for every Work Item of a Team.
* Bad, because approval names become an operator's configuration of a consumer's process. A change to that process would need a Ploeg deployment, and one Team could not hold Work Items with different requirements.

### A count of approvals instead of names

* Good, because it is simpler to declare.
* Bad, because a refusal cannot say which approval is missing, and two approvals by the same person would satisfy a count of two.

## Re-evaluation triggers

* A field that changes what a Shift does is added to the admission or the Work Item without being added to `approvalRevision`.
* A consumer asks Ploeg to require distinct persons across approvals, or a proven proof for one approval while the deployment runs `asserted`.
* A consumer needs to withdraw a given approval before admission without changing the required set.
* More than five Work Items a month re-enter approval because a tracker edit changed only text a person had already approved.
* ploeg-mcp gains an approve tool, which then needs the approval name and revision in its confirmation text ([ADR-0152](0152-ploeg-mcp-signs-each-confirmation-and-acts-on-it-once.md)).

## More Information

* Builds on [ADR-0031](0031-runs-create-work-items-held-for-approval-within-limits.md). This record does not answer its open question, whether a person approves created work before dispatch. It lets a consumer require more than one approval once the answer is yes.
* Attribution: [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md), [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md), [ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md).
* Scope: [ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md). Every route here acts only on Work Items whose Team the consumer names.
* Sibling record: [ADR-0176](0176-an-operator-consumer-sets-a-keyed-commit-status-on-a-pull-requests-head-through-ploegs-forge-credentials.md), which reports state on a pull request's head commit.
