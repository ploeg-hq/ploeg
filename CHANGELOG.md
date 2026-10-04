# Changelog

## 0.2.0-rc.2

### Changes

- The review poll reads whether an `awaiting_review` pull request conflicts with its base. The operator item's `pullRequest` gains `number`, `mergeState`, `baseBranch` and `checkedAt` (#55, ADR 0040).
- A card's play lists the paths its pull request changes, with `changedPaths` and `changedPathsTruncated` (#56, ADR 0046).
- The floor sweep waits up to 24 hours while unsettled budget holds could refill a Shift's pool, then parks it with `budget held by unsettled runs` instead of `budget exhausted` (#59, ADR 0048).
- Every Vikunja Work Item carries its task link (#59).

### Upgrade notes

- Two migrations run when `ploegd` starts: `0036_pull_request_changed_paths.sql` adds a table, and `0037_pull_request_merge_state.sql` adds columns to `pull_requests`.
- The new operator fields are optional, so a consumer that does not know them keeps working.

## 0.2.0-rc.1 — first candidate after the separation

A release candidate tagged on `development` ([ADR 0064](docs/adrs/0064-development-is-trunk-and-release-candidates-come-from-it.md)). Experimental.

### Changes

- A reviewer that fails is retried, and a Work Item closes `review_failed` when no review came (#47).
- A stopped ACP Run says which watchdog stopped it (#48).
- Only the worker reports whether a Run delivered a pull request; `outcomereport.v1` gains `delivery` (#49).
- `GET /api/v1/operator/unsettled-accounts` lists finished Runs whose spend Ploeg cannot settle (#50).
- `GET /api/v1/operator/route-refusals` lists recent routing refusals (#51).
- A current Run's verification comes only from the worker's structured record (#53).
- The chart installs on a cluster outside the homelab (#52).
- `development` is the trunk, and release candidates are tagged on it (#57).

### Upgrade notes

- Two migrations run when `ploegd` starts: `0036_route_refusals.sql` adds an index on `audit_log`, and `0036_run_evidence_version.sql` adds a nullable column to `agent_runs`.
- The chart no longer carries the homelab's defaults. An install names `database.existingSecret.name`. With the executor enabled, it also names the forge URL, `executor.scaler.host` for the `keda` and `sandbox` executors, and an agent image (`executor.harness.image`, or `harness.image` on a team or Role); rendering fails without them. [`ci/required-values.yaml`](ops/helm/ploeg/ci/required-values.yaml) shows a complete set.
- `nodeSelector` and the workers' node selector default to empty, `executor.runnerImage` is empty, `executor.dindImage` is the Docker Hub digest, and the Vikunja webhook secret is unset, so Vikunja webhooks are refused until you name it.

## 0.1.0 — independent baseline

New repository and module: `github.com/ploeg-hq/ploeg`. Source extracted from Unfold as documented in [PROVENANCE.md](PROVENANCE.md); standalone GitHub CI and independent releases. This version is experimental.

Earlier release history is preserved in [the historical changelog](docs/history/legacy-changelog.md). Those versions belong to the previous repositories and module.
