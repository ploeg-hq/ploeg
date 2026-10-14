## 1. Contract

- [x] 1.1 Add the optional worker-only `harness` object (name, profile, version, agent name and version) to `OutcomeReport` in `pkg/harness/contract.go` and `docs/contracts/outcomereport.v1.schema.json`, with a `contract_test.go` assertion
- [x] 1.2 Add optional `harnessVersion` to `claimRequest` in `docs/contracts/run-api.v1.schema.json` and the claim handler's request type (`pkg/httpapi/server.go:471-481`)
- [x] 1.3 Add `harnessVersion` and `harnessAgent` to `run` and `runListItem`, and `harness`, `model`, `observedHarness` to the Teams read's Role in `docs/contracts/operator-api.v1.schema.json` and `pkg/store/operator.go:45-48`

## 2. Store

- [x] 2.1 Add the next numbered migration (`0054`; `0053_run_shadow.sql` took 0053) with nullable `harness_version` and `harness_agent` on `agent_runs`; never edit `0050`
- [x] 2.2 Extend `RecordRunConfig` (`pkg/store/run_config.go`) with the write-once fields and a test that a later different value is kept out
- [x] 2.3 Read the newest recorded version per (team, Role) for `OperatorTeams`; test null for a Role without one

## 3. Worker

- [x] 3.1 Probe the Harness binary with `--version` once after `CheckHarnessBinary`, bounded to 5 s, first line up to 128 bytes; skip it for `exec`, the custom `acp` profile and for a binary that cannot be resolved before the clone; test that timeout, failure and a skipped probe leave it unknown
- [x] 3.2 Keep ACP `agentInfo` from `initialize` (`pkg/harness/adapters/acp/acp.go:209-227`) and return it to the worker
- [x] 3.3 Send `harnessVersion` on the claim; fill `OutcomeReport.harness` and discard any drop-box value; test both in `pkg/worker`

## 4. ploegd configuration and reads

- [x] 4.1 Render `PLOEG_TEAM_HARNESSES` in `ops/helm/ploeg/templates/deployment.yaml` from the same override chain as `_helpers.tpl:181-184,217`, through one shared helper so it cannot diverge from each worker's `PLOEG_HARNESS` and `PLOEG_ACP_PROFILE`; cover a Role override in an `ops/helm/ploeg/ci/` values file and regenerate the goldens with `sh scripts/helm-golden.sh update`
- [x] 4.2 Parse it in `cmd/ploegd/operator.go` and serve the Role fields in `handleOperatorTeams`; model from the Role's inference policy as `recordRunConfig` reads it (its single model; `select-harness-per-run` switches this to the policy's `defaultModel`)
- [x] 4.3 Record the version from the claim and the Outcome Report in `pkg/httpapi`; test a Run killed after claim keeps the claim's version, that a recording failure never changes the Outcome, and that the claim and outcome routes still accept unknown fields

## 5. Docs and gates

- [x] 5.1 Update `docs/architecture.md` §6 and `docs/contracts/README.md` for the new fields
- [x] G.1 Run `mise run verify` (gofmt, vet, build, `go test ./...`, `openspec validate --all --strict`, Helm lint, template and golden checks); paste the output in the pull request
- [x] G.2 Run `mise run docs-check` (ADR ledger, contracts, generated configuration, domain); paste the output in the pull request
