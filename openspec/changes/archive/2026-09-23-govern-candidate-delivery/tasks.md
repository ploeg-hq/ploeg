## 1. Contract and durable authority

- [x] 1.1 Write proposal, specification, design and proposed ADR before implementation.
- [x] 1.2 Add immutable candidate, trusted verification, approval and Publication Operation Store tables in migration 0015.
- [x] 1.3 Add scoped operator routes and server-selected policy validation; freeze publication by default.
- [x] 1.4 Publish a matching separate operator delivery schema and contract.

## 2. Verification and integration

- [x] 2.1 Exercise binding, actor scope, verifier capability, policy drift and approval mismatch with regression tests.
- [x] 2.2 Prove concurrent reservation gives one permission and ambiguous response replay never grants another.
- [x] 2.3 Integrate De Vloer canonicalization and independent verifier with the operator contract.
- [x] 2.4 Run the repository Go build/vet/test, ADR ledger and Helm gates through mise; report unrun external qualification explicitly.

## Validation evidence

The full Go suite passed, including the delivery Store and HTTP regressions. Go vet/build, Helm lint and all render goldens passed in the shared gate run. The opt-in `TestOperatorDeliveryWorkbenchQualification` passed against real PostgreSQL, Ploeg and De Vloer HTTP services, Git objects and fresh Docker checks: two configured checks, one verifier start across a workbench restart, the same immutable receipt and an exact candidate-bound approval. The fixture made zero model calls and performed no external publication. ADR 0027 remains proposed.
