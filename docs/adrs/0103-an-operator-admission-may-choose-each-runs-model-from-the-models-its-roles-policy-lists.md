---
status: accepted
date: 2026-10-14
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An operator admission may choose each Run's model from the models its Role's policy lists

## Context and Problem Statement

[ADR-0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md) (proposed) says a Run calls only its Role's model, and the chart renders each (Team, Role) managed inference policy with exactly one model (`ops/helm/ploeg/templates/_worker_control.tpl:21`). [ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md) (proposed) records that single model as the Run's `configured_model` and null for a policy with more than one. The owner decided on 2026-10-13 that a person may choose the model per session, alongside the Harness ([ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md)), from the models the Team lists. Which models may be chosen, how is the choice bounded and metered, and what does a Run record?

## Decision Drivers

* A Team, not the person in the dialog, decides which models its Runs may call and what they may spend ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)).
* Spend is measured at the gateway, and a Run's key bounds every model it can call ([ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md), ADR-0039).
* A choice that cannot be honoured is refused at admission, never queued.
* A retry, a re-opened Round and a later Round of one session run the same choice.

## Considered Options

* The admission names a model; ploegd accepts it only when a Role's managed inference policy lists it, resolves it per Role when the Shift creates each Run, and scopes the Run's key to that one model
* Keep the model per Role (ADR-0039 as written); a person chooses a Role or a Team instead
* Scope every Run's key to all of its Role's listed models and let the Harness pick

## Decision Outcome

Chosen option: "the admission names a model from the Role's policy, resolved per Run, one model per key", because it gives the person the choice the owner asked for while every Run still calls exactly one model the Team allowed and the gateway meters.

* **Allowed models.** A Role's managed inference policy lists the model aliases it allows and names one of them as its default. A Role configured with one `model`, as every Role is today, allows only that model.
* **Admission.** A submission through `POST /api/v1/operator/work-items` ([ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)) may name one `model`, next to its optional `harness`. ploegd refuses it with a client error naming the allowed models, and records nothing, when no Role of the Team's plan lists it, or when the Team's Runs have no managed inference policy. The model is part of the admission's idempotency fingerprint (R13). An Operator Execution admission takes no model.
* **Resolution per Role.** When the Shift creates a pending Run, ploegd stores the model it must call: the requested one when its Role's policy lists it, otherwise the Role's default. Without a requested model, every Run uses its Role's default. The admission response lists the resolved model per Role. Retries, re-opened Rounds and Runs created after a requeue ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)) resolve from the same stored request, so they keep the model.
* **Key and budget.** The Run's inference account and the key ploegd mints for it are scoped to the Run's resolved model only, never to the Role's whole list. The Run's cap is still its Role's cap from the Shift pool; the model does not change the cap.
* **Worker.** The claim response carries the Run's resolved model, and the worker passes it to the Harness as `LLM_MODEL` in place of the pod's configured value. The claude-code adapter pins every alias to it and keeps the advisor off, as ADR-0039 points 1 and 3 require.
* **Record.** `configured_model` (ADR-0091) records the Run's resolved model, also when its Role's policy lists more than one. `modelMismatch` compares served model groups with that value.

This narrows ADR-0039 point 1 from "the Role's model" to "the one model resolved for the Run from its Role's policy", and ADR-0091's model rule from "the policy's single model, else null" to "the Run's resolved model". Neither record is replaced: a Run still calls exactly one model, the advisor stays off, and the other fields ADR-0091 records keep their rules, so `supersedes` is `none`, as for [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)'s narrowings.

### Consequences

* Good, because a person can pair a Harness with a model it suits, still bounded by the Team's list and the Role's cap.
* Good, because the key, settlement and `configured_model` each still see one model per Run, so ADR-0039's metering argument holds.
* Bad, because `LLM_MODEL` stops being a pod constant: the claim has to carry it, and a worker that ignores it would call the pod's model, which the key then refuses.
* Bad, because cost per Role is no longer comparable without grouping by `configured_model`.
* Bad, because a Role whose list lacks the chosen model runs its default, so one session may call two models; the admission response lists them.

### Confirmation

* `go test ./pkg/httpapi/ ./pkg/store/` admits a listed and an unlisted model, a model with no managed inference, and a replay with another model (conflict); checks the stored model on builder, reviewer, retry, re-opened Round and requeued Runs; and checks that the reserved account and the minted key carry only the resolved model.
* `go test ./pkg/httpapi/` reads `configuredModel` as the resolved model for a Role whose policy lists two models.
* `go test ./pkg/worker/` checks that the claim's model reaches the Harness as `LLM_MODEL`.
* `mise run verify` renders a Role with two allowed models from an `ops/helm/ploeg/ci/` values file and compares it with the committed goldens (`scripts/helm-golden.sh check`).

## Pros and Cons of the Options

### Keep the model per Role

* Good, because nothing in policies, keys, claims or records changes.
* Bad, because a person who wants another model for one session has to use another Role or Team, which the owner rejected on 2026-10-13.

### Scope the key to the Role's whole list

* Good, because the worker needs no model from the claim.
* Bad, because the Harness, not ploegd, would choose which model the Run calls, `configured_model` would have no single value, and a subagent could switch models mid-Run, which ADR-0039 forbids.

## Re-evaluation triggers

* A Role's policy lists more than four models, or a consumer needs a model no Role of its Team lists.
* LiteLLM prices advisor iterations per model (ADR-0039's first trigger), which could let one Run call two models.
* Cost per Role and model shows a chosen model regularly exhausting its Role's cap before the Run reports.

## More Information

* OpenSpec change `select-harness-per-run`, design Decision 7 and the resolved Open Question 6.
* Narrows [ADR-0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md) point 1 and [ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md)'s model rule; a dated note is added to each when this record is accepted.
* 2026-10-13 — The owner decided that a session may choose its model from the Team's list: ploegd accepts a model only when the Role's managed inference policy lists it and otherwise refuses with a clear error, a Role that does not list it uses its default, the Run records the resolved model, its key is scoped to it, and retries keep it. Proposed.
* 2026-10-14 — Accepted. Merging `select-harness-per-run` ratifies this record. Implemented: `models` and `defaultModel` in each policy (`role.models` in the chart), `model` on `POST /api/v1/operator/work-items` (422 `model_not_allowed` and `model_requires_managed_inference`), `agent_runs.model_selected` (migration 0056), the reservation scoped to it, `model` in the claim response and `configured_model` from it. The dated notes on ADR-0039 and ADR-0091 are left to the owner.
