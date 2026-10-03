## 1. Launcher

- [x] 1.1 `pkg/sandboxlaunch`: a REST client for `sandboxclaims` (create, get, delete) using the in-cluster token and CA, with the API base overridable for tests
- [x] 1.2 Build the claim: name from the pod, owner reference to the Job, `warmPoolRef`, `lifecycle{shutdownPolicy: Delete, ttlSecondsAfterFinished, shutdownTime}`
- [x] 1.3 Wait loop: exit on `Finished=True` (delete the claim first), on 404, or at the deadline; never create a second claim
- [x] 1.4 Tests against an `httptest` fake API server: one claim created with the expected body; delete on `Finished`; 404 ends cleanly; create failure returns an error and no retry; deadline ends the wait
- [x] 1.5 `ploeg-worker sandbox-launch` subcommand: environment-to-config wiring only

## 2. Chart

- [x] 2.1 `values.yaml` and `values.schema.json`: `executor.type: sandbox` and an `executor.sandbox` block (runtimeClassName, ttlSecondsAfterFinished, shutdownMarginSeconds, launcher resources)
- [x] 2.2 `templates/sandbox.yaml`: per (Team, Role) a `SandboxTemplate` from `ploeg.workerPodTemplate` plus `activeDeadlineSeconds`, `dnsPolicy`, optional `runtimeClassName`, `networkPolicyManagement: Unmanaged`, `envVarsInjectionPolicy: Disallowed`; a `SandboxWarmPool` with `replicas: 0`; launcher ServiceAccount, Role and RoleBinding
- [x] 2.3 `templates/scaledjob.yaml`: render for `sandbox` too, with the launcher pod template and without the dind label on the ScaledJob; trigger unchanged
- [x] 2.4 `ci/executor-sandbox-values.yaml`, its golden render, and the case in `scripts/helm-golden.sh`

## 3. Docs

- [x] 3.1 `docs/contracts/executor.md`: the agent-sandbox executor, its backstops and its prerequisites
- [x] 3.2 Regenerate the configuration reference if the docs gate asks for it

## 4. Gates

- [x] 4.1 `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`
- [x] 4.2 `helm lint ops/helm/ploeg` and `./scripts/helm-golden.sh check`
- [x] 4.3 `go test ./internal/ledger/` and `mise run docs-check` from the Glide root
