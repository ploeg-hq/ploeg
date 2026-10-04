# Ploeg

Ploeg admits, budgets and executes agent work from trackers, from Vloer and from other work. This is the standalone repository; `development` is trunk and `main` holds stable releases (ADR-0064). Use conventional commits and stage only the paths you changed. Never use `git add -A`, `git add .` or `git commit -a`. Run `mise run setup` once and every tool through `mise exec --`. Start at [docs/index.md](docs/index.md).

## Commits

Releases are explicit reviewed `v0.x.y` tags; commits do not automatically publish. Work from the board carries a `VIK-<taskID>` trailer. On pushed `main`, correct a mistake with a follow-up commit, never an amend. Before assuming your edits are uncommitted, run `git log --oneline -3 -- <paths>`: another session may already have committed them.

## Facts the tree won't tell you

- `cmd/*` is only environment-to-config wiring. Run logic lives in `pkg/worker`.
- `pkg/store/migrations/` is append-only. Add the next migration; never edit an existing one.
- Schemas in `docs/contracts/` and their Go types change together.
- Tests fake external services with `net/http/httptest`. A bug fix lands with a regression test that fails on the old code.

## Before opening a pull request

Run `mise exec -- go test ./...`, `go vet ./...` and the Helm checks that `mise run verify` runs, and put their output in the pull request.

In the Ploeg worker sandbox, registry pulls time out by design: a worker pod reaches only its model gateway and its forge. If a gate needs a toolchain the image lacks, skip it, do not retry the pull, and list it in the pull request under "Checks left to CI". CI runs every gate.

## Decisions and evidence

- Decisions go in [docs/adrs/](docs/adrs/README.md) (MADR 4.0). Supersession is append-only: write a new record with `supersedes: NNNN`, and never flip an accepted record's status. A decision that can change carries `review-by:` and re-evaluation triggers, and every record has a `### Confirmation` section. `go test ./internal/ledger/` enforces this, so do not run the `adr-writer` skill's validator here.
- Evidence goes in `docs/research/YYYY-MM-DD-<topic>.md`. Priorities live on the tracker, not in the repository.
- Non-trivial changes can go through OpenSpec ([openspec/config.yaml](openspec/config.yaml)): proposal, specs, design, ADR, tasks.

## Where to look

| Working on | Read |
| --- | --- |
| Anything non-trivial | [docs/architecture.md](docs/architecture.md); check its divergence section against the code before repeating it |
| Keys, budgets, spend | [docs/architecture.md](docs/architecture.md) and [managed workers](docs/ops/managed-workers.md) |
| Claims, Leases, the sweep | [docs/architecture.md](docs/architecture.md) and `pkg/store` |
| Scaling, KEDA | `ops/helm/ploeg` |
| Harness and worker seams | [docs/contracts/](docs/contracts/README.md) |
| CI, signing, Forgejo, OpenBao | [docs/ops/ci-and-infra.md](docs/ops/ci-and-infra.md) |
| Tracker and dispatch | [docs/ops/board.md](docs/ops/board.md) |
| Metrics and alerts | [docs/ops/alerts.md](docs/ops/alerts.md) and `pkg/store/metrics.go` |
