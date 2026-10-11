---
status: proposed
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Each Run records the model, harness, plan and ploegd version it was configured with

## Context and Problem Statement

Team configuration changes while Runs keep finishing. Bronze moved from `deepseek-chat` to `fireworks-deepseek-v4p1-flash` on 2026-09-30, and silver's builder changed three times in twelve days. A Run keeps its configured model only in `run_llm_accounts.models`, which exists only under managed inference, and the models it actually called only in `usage.models` after settlement ([ADR-0045](0045-keep-run-usage-and-merge-facts.md)). The harness, its agent profile, the plan and the ploegd version a Run ran with are stored nowhere. A comparison of cost or outcome across a configuration change therefore mixes Runs of different configurations, and cost per merged pull request per Role and model cannot be measured. What should a Run record about its configuration, from which source, and how does a consumer see that the model it called was not the one it was given?

## Decision Drivers

* A recorded value must be what the Run was given, never a value derived later from today's configuration. Unknown is stored as unknown.
* ploegd should record from its own configuration where it has it, so the record does not depend on a worker reporting it.
* A worker of an older release, and Runs recorded before this change, must keep working and read as unknown.
* Consumers read execution facts through the operator API ([ADR-0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md)); ploegd does not compute their comparisons.

## Considered Options

* ploegd records the configuration on the Run at claim or admission, taking the harness from the worker's claim
* The worker reports its whole configuration in its outcome report
* Keep the configuration history outside Ploeg, such as Helm release history, and join on time

## Decision Outcome

Chosen option: "**ploegd records the configuration on the Run at claim or admission, taking the harness from the worker's claim**", because ploegd holds the inference policies, the plans and its own version at the moment a Run starts, and the harness is the only fact it lacks.

What is decided:

* **Fields.** `agent_runs` gains five nullable columns (migration 0050): `configured_model`, `harness`, `harness_profile`, `plan_hash` and `ploegd_version`. Nothing is backfilled; a Run recorded earlier reads all five as null.
* **Model.** The single model of the Run's (team, Role) managed inference policy (`PLOEG_WORKER_LLM_POLICIES`), which the chart renders from `role.model | team.model` ([ADR-0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md)). A policy that names more than one model, or no policy, records null.
* **Harness and profile.** The worker names its harness (`PLOEG_HARNESS`) and, for `acp`, the resolved agent profile (`opencode` when `PLOEG_ACP_PROFILE` is empty) as `harness` and `harnessProfile` in its claim request ([run API](../contracts/run-api.v1.schema.json) `claimRequest`). The chart's per-team and per-Role harness settings reach only the worker's environment, not ploegd, so the worker is the only source. A worker that sends neither, an Ask and an operator execution (which ploeg-worker does not run) record null.
* **Plan hash.** `sha256:` and the hex SHA-256 of the JSON encoding of the Team's plan as ploegd parsed it: the pool, the Rounds, each Role's name, writes flag, cap and planner flag, and the fix-round bound. The encoding has a fixed field order, so the same plan from `PLOEG_TEAM_PLANS` or from the configuration file hashes the same. A plan-less team, an Ask and an operator execution record null. The model and the harness have their own fields, so the hash does not cover them.
* **Version.** The `main.version` the ploegd binary was built with.
* **When.** On a claim, after the Run is leased, for both the Shift-scoped and the pre-Shift claim; on an operator execution's admission once its inference is reserved; on an Ask's admission. Each field is written once: a value already recorded is kept, and an empty or malformed value stays null. Recording runs outside the claim's transaction; a failure is logged and leaves the fields null, and never refuses the claim.
* **Served model groups.** A LiteLLM spend-log row names the provider's model in `model`, such as `fireworks_ai/accounts/fireworks/models/deepseek-v4p1-flash`, and the model group that served the call in `model_group`: the alias the key asked for, such as `fireworks-deepseek-v4p1-flash`, or the fallback group that answered instead. Settlement keeps the provider names in `usage.models` and `usage.byModel` as before, and adds `usage.modelGroups`: the sorted, distinct `model_group` values of the Run's entries. The Run list returns them as `usage.modelGroups`, empty when unknown.
* **Model mismatch.** The operator API derives `modelMismatch` on every Run: true when `usage.modelGroups` holds a group other than `configured_model`, such as a fallback; false when every group is `configured_model`; null while either side is unknown, which includes every Run settled before settlement recorded groups and every Run without managed inference. Provider model names are never compared, because they never equal the alias. The `ploeg_runs_model_mismatch_last_day{team}` gauge that [ADR-0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md) proposes is not built; when it is, it counts Runs finished in the last day whose `modelMismatch` is true.
* **Reads.** `configuredModel`, `harness`, `harnessProfile`, `planHash`, `ploegdVersion` and `modelMismatch` are on the operator API's `run` (Run detail, Work Item detail runs, Work Item facts runs) and `runListItem` (Run list), each present and null when unknown ([operator API](../contracts/operator-api.v1.schema.json)).

### Consequences

* Good, because a cost or outcome comparison can group Runs by the configuration they ran with, so a configuration change no longer confounds it.
* Good, because a Run that called a model other than its Role's is countable from one boolean.
* Good, because ploegd records the model and plan from its own configuration, so a misreporting worker cannot change them.
* Bad, because the harness is self-reported by the worker. It is authenticated by the worker's bootstrap capability for its team and Role, and a wrong value changes only this record.
* Good, because a fallback is visible on the Run: the fallback group appears in `modelGroups` and sets `modelMismatch`, while the provider names stay in `models` for pricing.
* Bad, because `modelMismatch` depends on the gateway's `model_group` field. A gateway that leaves it empty leaves every Run's mismatch unknown, never false.
* Bad, because the plan hash says that a plan changed, not how. Ploeg keeps no plan history; the deployment's configuration history explains a change.

### Confirmation

* `go test ./pkg/store/` writes each field once, keeps a malformed profile unknown, and checks `modelMismatch` as null without served groups (provider names alone included), false when only the configured alias served and true when a fallback group did, on Run detail and the Run list.
* `go test ./pkg/httpapi/` claims through the managed worker path with a harness and profile, and reads all six fields from Run detail, the Run list, Work Item detail and Work Item facts against `docs/contracts/operator-api.v1.schema.json`; it reads a Run without a record as null on all four; it admits an Ask and a paid operator execution and checks that each records its own policy's model with no harness or plan.
* `go test ./pkg/worker/` checks that a claim names the harness, and the profile for `acp`.
* `go test ./pkg/httpapi/` also settles a Run from fake spend logs: a row whose provider `model` differs from the alias but whose `model_group` is the alias gives false, and adding a fallback row with another `model_group` gives true, with `usage.modelGroups` on the Run list.
* `go test ./pkg/litellm/` reads `model_group` from both spend-log endpoints.
* `go test ./pkg/plan/` checks that the same plan hashes the same and a changed cap changes the hash.

## Pros and Cons of the Options

### The worker reports its whole configuration in its outcome report

* Good, because the worker knows its harness, profile and `LLM_MODEL` directly.
* Bad, because a Run that dies before reporting records nothing, and failed Runs are among those a comparison needs.
* Bad, because the model and plan would be self-reported where ploegd holds the authoritative values.

### Keep the history outside Ploeg and join on time

* Good, because nothing changes in Ploeg.
* Bad, because a rollout is not instantaneous: pods started before and after a change finish interleaved, so the time of a change does not tell which configuration a Run had.
* Bad, because every deployment would rebuild the join itself.

## Re-evaluation triggers

* The ADR-0076 metrics are built, so the mismatch gauge is added.
* LiteLLM stops reporting `model_group` in its spend logs, or changes what it names on a fallback.
* ploegd learns each Role's harness from its own configuration, such as through a launcher reservation ([ADR-0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md)), so the worker's report is no longer needed.
* A Role is configured with more than one model.

## More Information

* Migration `pkg/store/migrations/0050_run_config.sql`.
* Ticket VIK-2027, part of the cost per merged pull request epic VIK-2026.
* 2026-10-11: proposed.
