## 1. Operator visibility

- [x] 1.1 Implement versioned operator DTOs, schema, bounded store reads and scoped bearer authorization.
- [x] 1.2 Implement De Vloer server client, user/team policy and useful operator overview/details.
- [x] 1.3 Verify scope denial, secret omission, failure behavior and snapshot pagination.

## 2. Worker authority

- [x] 2.1 Move broker management to the control process and provide authenticated Run-scoped worker capabilities.
- [x] 2.2 Construct allowed Harness environments and keep management credentials out of worker manifests.
- [x] 2.3 Add append-only accounting migration and preserve unresolved holds after expiry; keep KEDA predicates aligned where affected.
- [x] 2.4 Prove regression tests fail for the original environment/accounting behavior.

## 3. Shared execution baseline

- [x] 3.1 Persist operator execution identity and idempotent revision-checked commands in Ploeg.
- [x] 3.2 Link De Vloer sessions and delegated Executor operations to that authority.
- [x] 3.3 Exercise start, detach/reconnect, intervention, pause/resume, cancel/restart and paid-submission ambiguity.
- [x] 3.4 Demonstrate retained evidence and attributable cost without inventing live calls or settlement.

## 4. Delivery validation

- [x] 4.1 Run gofmt, go build, go vet, go test and internal/ledger gates through mise.
- [x] 4.2 Run Helm lint and all three existing CI chart renderings.
- [x] 4.3 Run De Vloer tests, check, typecheck, browser and cross-service integration checks.
- [x] 4.4 Record reproducible evidence, qualification boundaries and operational configuration; validate OpenSpec.

Qualification: real PostgreSQL and both HTTP services passed the opt-in workbench integration, with real Git checks and zero model calls. Browser evidence is retained in De Vloer under `docs/research/evidence/unified-2026-09-10/`. Live gateway/cluster/throughput qualification remains outside these local implementation checks. The original chart failed the management-isolation assertion for all 13 workers; the changed chart passes for all 13.
