# Operator delivery API 1.0

This additive contract governs a Delivery Candidate, Verification Receipt and Publication Operation for a completed operator execution. The [JSON schema](operator-delivery.schema.json) and [Go types](../../pkg/store/operator_delivery.go) describe the wire records. The [accepted ADR 0027](../adrs/0027-candidate-delivery-uses-trusted-evidence-and-a-publication-barrier.md) records the decision; [the OpenSpec change](../../openspec/specs/operator-delivery/spec.md) states its requirements.

Every endpoint is below `/api/v1/operator/executions/{execution}/delivery`, authenticates a registered operator consumer, and requires `X-Ploeg-Actor` identifying the execution owner. `X-Ploeg-Acting-User` records the authenticated human when an authorized administrator acts for that owner. The caller must derive these identities from its authenticated session. Team scope remains mandatory; unknown or unauthorized execution objects return 404. All mutation requests require one bounded JSON object with no unrecognized fields. Responses include `schemaVersion: "1.0"` and never include execution credentials.

| Method and suffix | Authority | Input | Result |
| --- | --- | --- | --- |
| `GET` | Owning execution consumer or scoped verifier | None | `delivery` containing nullable candidate, receipt, approval and operation; `lifecycle: "single-completed-execution"`; `publicationEnabled`, the registered policy's setting for the execution's repository (false without a policy) |
| `POST /candidates` | Verifier capability | Generation, repository ID/URL, base SHA, canonical SHA, tree SHA, artifact SHA-256, policy SHA-256 | Immutable `candidate`, `created` |
| `POST /verification` | Verifier capability | Candidate ID, policy SHA-256, canonical SHA, tree SHA, artifact SHA-256, passed, testCount, verifierId, evidence SHA-256 | Immutable `receipt`, `created` |
| `POST /approval` | Owning execution consumer | Candidate ID, receipt ID, policy SHA-256 | Immutable `approval`, `created` |
| `POST /publication` | Owning execution consumer | Operation ID, candidate ID, receipt ID, approval ID, policy SHA-256, branch | `operation`, `effectAuthorized` |
| `POST /publication/{operation}/status` | Verifier capability | State, canonical SHA, branch; remote ID/URL when published | `operation` |

Creation returns 201; unchanged replay returns 200. A malformed binding returns 400, missing capability 403, an incompatible state or policy 409. Infrastructure failure returns 503. These are control-service APIs; workspace run credentials cannot access them.

## Registered policy and trusted verifier

`PLOEG_OPERATOR_CONSUMERS` accepts a separate `verify` flag, which defaults false and grants no command, credential, approval or publication-reservation authority. A separate consumer can record delivery evidence for an execution owned by another consumer only within its configured Team scope and with the exact owner identity. Deploy its credential solely in the trusted verification control service. The flag is not cryptographic proof of independent execution: its operator is responsible for isolating the service and qualifying the checks.

`PLOEG_OPERATOR_DELIVERY_POLICIES` is a JSON array with `repositoryId`, `policySha256`, `verifierId`, positive `minTests`, optional `publicationEnabled` (default false) and optional `publisherLogin`, the forge login that must have opened a published pull request. All values describe policy; credentials remain environment references in consumer configuration. Empty configuration admits no delivery evidence. The policy digest must bind the approved base, pinned verifier image, protected inputs, exact checks and execution limits. The verifier ID identifies the registered implementation, such as `consumer-docker-v1`. The server does not accept a request-selected policy, verifier or minimum test count.

The trusted service canonicalizes exported artifacts, checks the original approved base, and constructs a commit whose parent is that base before submitting a candidate. Ploeg verifies execution, generation and repository bindings. It does not parse Git bundles or run tests in this slice. A receipt binds that canonical candidate and records independent evidence. Failed receipts are retained immutably so a restart can replay the result; they cannot be approved. Passing receipts require at least the configured positive check count. Changing a failed receipt into a passing one is forbidden in this lifecycle.

Approval binds the exact candidate, receipt and current policy. It records the acting authenticated human. It never means merge approval. Policy drift prevents a fresh publication reservation.

## Publication barrier and recovery

A first reservation commits `reserved` before returning `effectAuthorized: true`. Only that response authorizes the trusted caller to perform its bounded external effect. All identical replays return the same operation with `effectAuthorized: false`. If the transaction commits but the response is lost, the operation stays frozen even if the caller did not start the external request. There is no timeout or lease that regenerates effect authority.

The publisher must use the operation identity as deterministic remote metadata, push only the exact canonical SHA to the reserved branch, and positively validate the remote proposal's repository, branch and head before reporting `published`. Ploeg accepts `unknown` or `published`; a published report must identify the reserved canonical SHA and branch and a remote ID and a pull request URL `<registered repository>/pulls/<number>`. Only the trusted verifier-capable service can report it. `unknown` can become `published` on a positive exact match. Published state is terminal, except for identical replay. No negative lookup, empty result, timeout or expired lease clears this barrier or grants another POST.

Ploeg does not take the report on its word. Before it records `published`, it reads that pull request from the forge with its own read credential and requires:

* head commit equal to the candidate's canonical SHA;
* head branch equal to the reserved branch;
* base branch equal to the execution's base branch;
* head and base repository equal to the registered repository;
* state open or merged;
* author equal to the policy's `publisherLogin`, when the policy names one.

Any difference answers 409 `publication_mismatch` naming the fields that differ, and the operation keeps its state. A forge Ploeg cannot read with (no forge provider that reads pull requests for the registered repository) answers 409 `publication_unverifiable`; a failed forge read answers 503 `forge_unavailable`; a URL that is not a pull request of the registered repository answers 400 `invalid_publication_evidence`. An identical replay of an already published operation answers 200 without another forge read. The transition to `published` posts one comment on the Work Item's tracker task naming the pull request; a replay posts none.

Ploeg stores authority and evidence here. The default deployment performs no live forge publication: the trusted publisher pushes and opens the pull request, and Ploeg confirms the result.

## Bounded lifecycle

Only `completed` and stop-confirmed executions at the recorded generation admit candidates. Repository registration is stored at admission; historical executions with empty registration cannot enter delivery. Candidate creation freezes generation advancement. The current unique execution per Work Item excludes a successor delivery attempt. Failed verification or unresolved publication therefore requires explicit human follow-up; this contract does not claim a general Work Order or automatic retry implementation. Demo executions can exercise candidate evidence and approval but cannot reserve publication effects.
