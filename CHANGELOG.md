# Changelog

## 0.2.0-rc.9

### Changes

- An operator can close the Work Item of an Operator Execution that has ended (#89). The new `close` execution command withdraws the Work Item of a `failed` execution, or of an `interrupted` one whose lease has expired, which is then recorded as `cancelled` with its Shift closed as `withdrawn_session_ended`. The withdrawal is audited as `work_item.withdrawn`. Running, waiting and unexpired interrupted executions answer 409, and a second close is refused. Before this, such a Work Item stayed in `needs_human` with no way out.

### Migration notes

- None. The command uses the existing execution command body; no migration and no configuration.

## 0.2.0-rc.8

### Changes

- An executor team can opt in to read-only MCP tools served by the LiteLLM gateway (#79, ADR 0078, proposed). `executor.teams[].litellmTeamId` and `executor.teams[].mcpAccessGroups`, and the same optional `litellmTeamId` and `mcpAccessGroups` on an additional LLM policy, are fixed on the Run's LLM account at reservation; ploegd mints the Run's key with `team_id` and `object_permission.mcp_access_groups`. A `claude-code` Run then loads the gateway's `/mcp` as its only MCP server through the worker's key proxy and keeps `--strict-mcp-config`; an `acp` Run gets it only when its agent advertises MCP over HTTP. Groups without a team id fail the chart render, boot, the store and the broker. Both unset mints exactly what rc.7 minted.
- The worker's loopback key proxy also swaps the placeholder in an `x-litellm-api-key` header for the Run's real key.
- `TestRun_StoppedProcessStaysStoppedWithTheWritersAccount` stops its fake `claude` only after the fake has written its account, so a slow exec no longer fails it (#80). No runtime behaviour changes.

### Migration notes

- Migration `0042` adds `run_llm_accounts.gateway_team_id` and `run_llm_accounts.mcp_access_groups` with defaults at startup. Existing accounts keep today's behaviour: no team, no MCP tools.
- No value is required. A team that sets `mcpAccessGroups` must also set `litellmTeamId`, and the LiteLLM team must allow every group it names, or the gateway refuses the mint.

## 0.2.0-rc.7

### Changes

- `TestCardComment_MergeWebhookPostsTheCard` sets its clock to its fixture's merge date. It read the wall clock, and from 2026-10-08 the fixture's card is seven days live, so its heading became "Foil finish" and the test failed on every run, in this repository and in every consumer that runs the Ploeg suite. No runtime behaviour changes.

### Migration notes

- None.

## 0.2.0-rc.6

### Changes

- Operators restart stopped work from a chosen Round: `POST /api/v1/operator/work-items/{id}/requeue` with optional `fromRound`, `poolUsd`, `note`, `commandId` and `expectedState` (ADR-0044). Allowed from `needs_human`, `stale` and `awaiting_review`. A restart past a writing Round reuses the earlier pull request, and the new Runs are briefed with the previous close reason, the failed Runs and the note. Replays by `commandId` return the first result.
- `/metrics` adds `ploeg_runs_finished_total{team,role,outcome,reason}`, `ploeg_shift_runs_without_pr_max{team}`, `ploeg_llm_accounts_unsettled{state}` and `ploeg_work_item_oldest_queued_seconds{team}`. `ploeg_runs_failed_total{reason}` is still emitted.
- `ploeg_shift_idle_seconds_max` no longer counts a failed Run as progress, so repeated idle kills keep the Shift idle and `PloegShiftStuck` can fire.
- The chart's PrometheusRule adds `PloegRunsIdleKilled`, `PloegRunsFailingRepeatedly`, `PloegSandboxStartFailing`, `PloegShiftNoDelivery`, `PloegQueueStalled` and `PloegSettlementBacklog`. Each is on by default when the rule is enabled and can be turned off in values.
- Every worker log line after a claim carries `trace`, `work_item`, `external_id`, `team`, `role`, `shift` and `round`.

### Migration notes

- Migrations `0040` (`work_item_requeues`) and `0041` (`agent_runs.trace_alias`, a stored generated column) run at startup. `0041` rewrites `agent_runs` once and holds its lock while it does; on a large table, deploy outside busy hours.
- Dashboards or alerts built on `ploeg_shift_idle_seconds_max` see higher values while failed Runs repeat; that is the fix.
- A log shipper that maps a trace field should read the `trace` key.

## 0.2.0-rc.5

### Changes

- The consumer-name boundary check skips a `.git` file as well as a `.git` directory, so Ploeg's tests pass when it is built as another repository's submodule or from a git worktree.

## 0.2.0-rc.4

### Changes

- A Run can be briefed from OKF knowledge packs: the repository's `knowledge/` directory, configured bundles and OKF concepts inside attached context, written outside the clone and indexed in the prompt; `TaskSpec.knowledge` records what was used. A Run may propose up to 10 learnings, which the worker keeps for a person to review and never briefs a Run with (#60, ADR 0065 and 0066, both proposed).
- The worker drops `CAP_SYS_PTRACE` before it runs a harness with credential isolation on, and refuses to start if the capability survives, so a Run never claims an isolation it cannot give (#63, ADR 0034).
- Both loopback proxies forward only requests that carry the Run's placeholder; anything else on the pod's loopback gets 403 (#64, ADR 0034).
- The forge proxy lets a writer push only `refs/heads/<run branch>`: other branches, tags, deleting the Run's branch, push certificates and unparseable pushes get 403 (#65, ADR 0034).
- A Run whose forge requests, pushed commits or proposed learnings carry its model key, its forge token or a per-Run canary credential fails with the new reason `credential_leak`; its forge writes stop and its commits stay on the branch for inspection, and it keeps no learnings (#66, #68).
- The branch probe after pushed-commit verification asks the forge by explicit URL outside the verify clone, so a check cannot redirect the token-carrying request with `url.*.insteadOf` (#72).
- `ploeg_runs_failed_total{reason}` counts failed Runs per reason, and the chart's PrometheusRule gains `PloegCredentialLeak` (critical) on its `credential_leak` series.
- Every git command the worker runs itself after the harness (the unpublished-work check, the leak scan, the OpenSpec gate, verification reads) runs in a worker-owned git directory, so `.git/config`, hooks or `include.path` the harness writes cannot redirect, observe or run inside it (#72).
- An Inference Account whose gateway key LiteLLM no longer holds (absent under its alias and `/key/info` 404) moves to `blocked` and settles from the gateway's spend logs, so `PloegModelKeyPastTTL` fires only for a key the gateway may still accept (#70).
- **The chart defaults `executor.litellm.keyIsolation` and `executor.forgeTokenIsolation` to `proxy`** for qualified harnesses (`openhands`, `acp/openhands`, `exec`; see `docs/research/2026-10-04-isolation-qualification.md`). A Role with DinD or an unqualified harness renders with both proxies off, and the worker logs one WARN naming the harness and the reason (#71).
- A Work Item reaches `awaiting_review` only when its configured checks passed on the pushed commit: the worker runs them in a fresh clone at the pull request's head, and a different head or a push during the checks makes the result `incomplete`. Failed or incomplete checks re-open the writing Round while fix rounds remain, otherwise park the item at `needs_human` with close reason `checks_not_passed` (#74, ADR 0070).
- ploegd binds a writing Run's delivery record to the Work Item's forge, repository, base branch, Shift branch and pull request before it believes it. A record that does not match is stored as `observed: unknown` and parks the item for a person; a checkpoint naming another branch or repository gets 400; publication, the review watch and readiness read the stored record (#75, ADR 0059).
- `ploeg_runs_without_observed_delivery_last_day{source=legacy|mismatch}` counts Runs that finished without a matching delivery record (#75).
- A Run Card's grade carries `evidenceComplete`, and the card image and pull request comment say whether the grade's evidence is complete or which inputs are missing (#73, ADR 0061).

### Upgrade notes

- Knowledge packs are off until a repository has `knowledge/` or the worker sets `PLOEG_KNOWLEDGE_DIRS`, `PLOEG_KNOWLEDGE_OUTBOX`, `PLOEG_KNOWLEDGE_BUDGET_BYTES` or `PLOEG_REPO_KNOWLEDGE_DIR`; without them a Run is unchanged.
- A harness that brings its own credentials instead of the placeholders Ploeg hands it now gets 403 from the proxies while `keyIsolation` or `forgeTokenIsolation` is `proxy`.
- A writer whose pod grants `CAP_SYS_PTRACE` and cannot drop it no longer starts with isolation on.
- The outcome contract's `failureReason` enum gains `credential_leak`; consumers that switch on it should treat it as a failure.
- `monitoring.prometheusRule.alerts.credentialLeak` is on whenever the PrometheusRule is.
- Breaking: installs that left the isolation values unset now run qualified harnesses behind both proxies. Opt out with `""` globally, or per team or Role with `harness.credentialIsolation: ""`; opt an unqualified harness in with `harness.credentialIsolation: proxy`.
- Verification now runs in a fresh clone at the pull request's head instead of the writer's checkout, and the OpenSpec gate reads the writer's checkout through the worker-owned git directory; a clone the harness corrupted fails closed (`stuck`).
- More Work Items may park at `needs_human`: failed or incomplete checks, a forge read that failed, and a delivery record that names another forge, repository, branch or pull request. A writing Round that pushed nothing reports `no_change_needed` instead of `pr_updated`.
- `0039_run_delivery.sql` adds `agent_runs.delivery` and `delivery_source` when `ploegd` starts; rows from before it stay empty.
- `cardGrade.evidenceComplete` is a new required boolean in `operator-api.v1`, and Shifts can close with the new reason `checks_not_passed`.

## 0.2.0-rc.3

### Changes

- A person can attach a zip, a tar.gz or a single file to a Work Item, and every Run claimed afterwards receives it verified, unpacked outside the clone and listed under "Context from people" in its prompt. A file attached after work has started reaches the next Run only (#61, ADR 0067 and 0068).
- `POST|GET /api/v1/operator/work-items/{id}/context`, `POST /api/v1/operator/executions/{id}/context` and `GET /api/v1/runs/{token}/context/{id}` serve them; `taskspec.v1` gains `context` and `run-api.v1` `claimResponse.context`.
- ADR 0034 (the harness gets placeholders, the worker keeps the credentials) is accepted.
- Ploeg names none of its consumers (#62, ADR 0069). Usage report links are four URL templates a deployment sets; a Work Target without a `cardStyle` gets the skin `default`; the pull-request card image is Ploeg's own; Operator Execution Shifts use branch `operator/<session>`.

### Upgrade notes

- **Breaking (#62):** `PLOEG_REPORT_GRAFANA_URL` and `PLOEG_REPORT_VLOER_URL` are removed. Set `PLOEG_REPORT_WORK_ITEM_URL` (`{id}`), `PLOEG_REPORT_TEAM_DASHBOARD_URL` (`{team}`), `PLOEG_REPORT_RUN_DASHBOARD_URL` (`{run}`) and `PLOEG_REPORT_SPEND_DASHBOARD_URL` instead. The default card skin is `default`. New Operator Execution branches are `operator/<session>`.
- `0038_work_item_context.sql` adds the `work_item_context` table when `ploegd` starts.
- `PLOEG_CONTEXT_MAX_BYTES` (default 20 MiB per upload) and `PLOEG_CONTEXT_MAX_TOTAL_BYTES` (default 50 MiB per Work Item) bound uploads. The chart does not set them yet.
- ploegd's 30-second read timeout bounds an upload, so large files need a fast link until the context routes get their own timeouts.
- The new fields are optional; a consumer or worker that does not know them keeps working.

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
