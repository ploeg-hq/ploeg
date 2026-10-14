## Context

Observed in the code on 2026-10-13 (branch `feat/per-run-harness-and-interaction`, from `origin/development` at `703d1b7`):

- The worker's Harness is fixed per worker deployment: `PLOEG_HARNESS` and `PLOEG_ACP_PROFILE` are read once at startup (`cmd/ploeg-worker/main.go:146-161`) and turned into one adapter before the first claim (`cmd/ploeg-worker/main.go:188-192`, `pkg/worker/adapters.go:119`). The chart resolves the Harness role → team → global (`ops/helm/ploeg/templates/_helpers.tpl:181-184`, the ACP profile at `:217`) and the model as `role.model | team.model` into `LLM_MODEL` (`_helpers.tpl:415-416`).
- ADR-0091 (proposed, implemented) adds five nullable columns to `agent_runs` in migration `0050_run_config.sql` (`configured_model`, `harness`, `harness_profile`, `plan_hash`, `ploegd_version`) and records `harness` and `harness_profile` from the claim request on both claim paths (`pkg/httpapi/server.go:569,611`, `pkg/httpapi/run_config.go:26-39`), write-once through `COALESCE` (`pkg/store/run_config.go:26-37`). An Ask and an operator execution record no harness. It records no Harness version. The worker does not know one: nothing in `pkg/worker` probes the binary, and the ACP adapter discards `initResp.AgentInfo` (`pkg/harness/adapters/acp/acp.go:209-227`), which the SDK exposes as an optional name and version (`acp-go-sdk@v0.13.5`, `InitializeResponse.AgentInfo`).
- ploegd learns Roles only from plans (`cmd/ploegd/operator.go:46-58`) and returns them as `{id, queueDepth}` (`pkg/store/operator.go:45-48,405-477`; `pkg/httpapi/operator.go:203-228`). The chart passes ploegd per-team plans and caps (`ops/helm/ploeg/templates/deployment.yaml:86,96`, `PLOEG_TEAM_PLANS` and `PLOEG_TEAM_MAX_RUNNING`) but no Harness settings, which is why ADR-0091 takes the Harness from the worker.
- The Outcome Report already carries worker-only objects (`verification`, `delivery`, `pkg/harness/contract.go:265-272`).
- ploegd decodes the claim request and the Outcome Report without rejecting unknown fields (`pkg/httpapi/server.go:529,776`), although `claimRequest` declares `additionalProperties: false` in `docs/contracts/run-api.v1.schema.json:27`.

ADRs designed within: 0091 (record-once, null is unknown, ploegd records from its own configuration where it can), 0045 (keep every usage figure a harness reports), 0069 (Ploeg names none of its consumers), 0079 (consumers compute; it supersedes 0074, which ADR-0091 still cites), 0018 (outcome drop box), 0039 (a Run calls only its Role's model; narrowed by ADR-0103 in `select-harness-per-run` to the one model resolved for the Run). `docs/architecture.md` §9 says "native harness state is opaque"; that remains true and is unaffected.

## Goals / Non-Goals

**Goals:**
- Every Run says which Harness version it ran with, when the worker can know it.
- A consumer can show, per Role, the configured Harness, profile and model and the version last seen, before it starts a session.

**Non-Goals:**
- Selecting a Harness per Run (`select-harness-per-run`), interaction (`interactive-runs`).
- Re-deciding ADR-0091's fields or timing.
- A history of configuration. The chart's release history explains a change (ADR-0091).

## Decisions

1. **Two sources, one write-once record.** The worker sends `harnessVersion` with its claim when it knows it before starting the Harness, and the Outcome Report's `harness` object when it learned more during the Run (the ACP `agentInfo`). ploegd applies the same `COALESCE` write-once update as `RecordRunConfig`. Alternative rejected: Outcome Report only, as the owner's decision literally reads. A Run that dies before reporting would record nothing, the gap ADR-0091 already named. The Outcome Report still carries the object, which satisfies the decision for every Run that reports.
2. **How the worker learns a version.** At startup, after `CheckHarnessBinary` (`pkg/worker/adapters.go:90-105`) passes, the worker runs the Harness binary with `--version` once, bounded to 5 s, and keeps the first non-empty output line, cut to 128 bytes. A failure leaves the version unknown and never stops the worker. The probe is skipped, and the version stays unknown, for `exec` and the custom `acp` profile (their program is the Team's own and `--version` may not be a harmless flag; a custom profile's ACP `agentInfo` still names the agent) and for a binary `CheckHarnessBinary` cannot resolve before the clone exists (a relative path or a task placeholder, `adapters.go:98-100`). For `acp` the adapter also reports `agentInfo` from `initialize`. For `claude-environment` the binary is the local CLI that dispatches, so its version describes the dispatcher, not the remote session (ADR-0084); it is recorded as is and the read says what it is through `harness`. Alternative rejected: an image digest. The worker cannot read its own image digest without a downward-API or registry call, and a digest does not tell a person which agent version ran.
3. **ploegd learns each Role's configured Harness.** The chart renders `PLOEG_TEAM_HARNESSES`, a JSON map team → role → `{name, profile}` computed with the same override chain as the pod template, so both come from one value. A plan-less team uses the role key `""`. This fires one of ADR-0091's re-evaluation triggers ("ploegd learns each Role's harness from its own configuration"); ADR-0097 answers it by keeping the claim as the record's source. The worker's claim stays the source of what actually ran: a pod that started before a rollout may run an older Harness, and the record must say so.
4. **Teams read.** Each Role gains `harness` (`{name, profile}` or null), `model` (the Role's default model: the single model of its managed inference policy as in ADR-0091, or the policy's `defaultModel` once `select-harness-per-run` lets a policy list more than one (ADR-0103); null without a policy) and `observedHarness` (`{name, profile, version, agent, recordedAt}` from the newest Run of that team and Role with a recorded version, or null). The query reads `agent_runs` by `(team, role)` ordered by id descending with `LIMIT 1` per Role, so it adds one bounded lookup per Role. `observed` is in the name so a consumer cannot mistake it for configuration.
5. **Schema.** The next numbered migration, `0054_run_harness_version.sql` (`0053_run_shadow.sql` took 0053 first), adds `harness_version TEXT` and `harness_agent JSONB` to `agent_runs`, both nullable with length checks like `0050`, and `harness_recorded_at TIMESTAMPTZ`, set when the version is first written, which the Teams read returns as `observedHarness.recordedAt`. A partial index on `(team, role, id DESC) WHERE harness_version IS NOT NULL` bounds the per-Role lookup. No backfill.

## Risks / Trade-offs

- [A Harness prints its version in a format nobody parses] → the value is stored as text and compared only for equality by consumers; no parsing in ploegd.
- [`--version` starts a heavy runtime (OpenHands is Python)] → bounded at 5 s and done once per pod, before the claim; a timeout records unknown.
- [`PLOEG_TEAM_HARNESSES` drifts from the pod environment when values are edited by hand] → both are rendered from one chart value; a `helm template` test asserts they agree for every workload.
- [The observed version is stale after an image change until the next Run] → it carries `recordedAt`, and the configured Harness is shown separately.

## Migration Plan

Additive throughout. Deploy ploegd first (migration, new reads serving nulls), then workers. Either order is safe: an older ploegd decodes the claim and the Outcome Report without rejecting unknown fields (`pkg/httpapi/server.go:529,776`), so it ignores `harnessVersion` and the `harness` object, and a newer ploegd reads an older worker's Runs as unknown. Rollback: roll back workers or ploegd independently; the migration's columns stay and read as null. A `pkg/httpapi` test pins that the claim and outcome routes still accept fields they do not know, so a later strict decoder cannot silently break a mixed-version rollout.

## Open Questions

1. **Is a worker-probed `--version` acceptable as "the Harness version"?** Recommendation: yes, plus ACP `agentInfo` when present. Trade-off: it is self-reported text from the image, not a verified identity; the alternative (image digest) is verifiable but needs a registry read and says nothing a person can read.
2. **Should the Teams read show only the latest observed version per Role, or every version seen in a window (for example the last 24 hours)?** Recommendation: latest only. Trade-off: during a rollout a consumer sees one version while two run; a window shows that but makes every Teams read scan more Runs.
3. **ADR-0091 is still `proposed`.** This change builds on it. Recommendation: ratify 0091 together with 0097, since both are implemented or planned against the same columns. Trade-off: none beyond the review time; while 0091 stays proposed, this change designs against code that exists, not against a ratified decision.
