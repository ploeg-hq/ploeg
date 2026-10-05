---
status: proposed
date: 2026-10-05
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Run health is observable by Team and Role through one correlation key

## Context and Problem Statement

On 2026-09-29, Work Item 138 sat for 3 h 07 min without a pull request:
* twelve Runs were killed at the same 45-minute idle timeout;
* two sandbox start timeouts failed the wrong Run;
* the item waited 56 minutes behind one builder slot.

No alert fired ([incident](../research/2026-09-29-incident-work-item-138.md)). The [observability review](../research/2026-10-05-observability-and-unfinished-decisions.md) found four reasons:
* `PloegShiftStuck` counts a finished Run as progress, so kills reset it.
* Failure counters carry a reason but no Team or Role.
* No metric shows a builder that ended without a pull request, or the time a Run spent in each phase.
* Log lines do not share one key. The worker writes `trace=` while the log shipper reads `trace-id`, and most lines carry no Run, Shift or Role.

What must Ploeg emit so that a failing Team, a silent Shift or a slow phase is visible without reconstructing it by hand?

## Decision Drivers

* An operator learns about repeated failure from an alert, not from a person noticing.
* Every signal is attributable to a Team and a Role, because placement, models and caps are set per Team and Role.
* One key joins Ploeg's logs, its metrics, the gateway's spend and the gateway's spans.
* Ploeg emits signals and does not own dashboards or alert routing ([ADR-0074](0074-ploeg-publishes-execution-facts-and-consumers-own-presentation-and-delivery-analytics.md)). The chart ships default rules; a deployment decides where alerts go.
* The worker sandbox reaches only its model gateway and forge, so no worker signal may depend on new egress.

## Considered Options

* Labelled outcome and phase metrics, a fixed set of log keys, and default rules in the chart; OpenTelemetry in ploegd as a second step
* OpenTelemetry tracing in ploegd and the worker first
* Leave metrics to the deployment's SQL exporter

## Decision Outcome

Chosen option: "**Labelled outcome and phase metrics, a fixed set of log keys, and default rules in the chart; OpenTelemetry in ploegd as a second step**". It closes every gap WI-138 exposed without widening sandbox egress.

### Metrics

ploegd's `/metrics` adds:
* `ploeg_runs_finished_total{team,role,outcome,reason}`, which replaces `ploeg_runs_failed_total` after one release in which both are emitted;
* `ploeg_run_phase_seconds{team,role,phase}` with phases `queue_wait`, `launch`, `run`, and `review_wait`, as a histogram;
* `ploeg_shift_runs_without_pr_max{team}`: the most finished Runs in any open Shift that has no pull request;
* `ploeg_llm_accounts_unsettled{state}` and `ploeg_publication_operations{state}`;
* `ploeg_runs_model_mismatch_last_day{team}`, from `agent_runs.usage.models` against the Role's requested model;
* after [ADR-0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md): `ploeg_launches_waiting{team}` and `ploeg_launch_wait_seconds`.

`PloegShiftStuck` stops treating a failed Run as progress.

### Log keys

After a claim or bind, ploegd and the worker log every line with the same keys:
* `trace`: the `ploeg-<12hex>` alias;
* `run`, `shift`, `round`, `role`, `team` and `work_item`.

Both binaries use `log.With(...)` on a logger scoped to the Run. ploegd's `id` key for a Work Item becomes `work_item`.

### Default rules in the chart

The chart's PrometheusRule adds these, each documented in [alerts](../ops/alerts.md):
* `PloegRunsIdleKilled`: two idle or timeout kills per Team in 2 h;
* `PloegSandboxStartFailing`;
* `PloegShiftNoDelivery`;
* `PloegWriterNoPR`;
* `PloegServedModelMismatch`;
* `PloegSettlementBacklog`.

### OpenTelemetry, second step

ploegd emits one trace per Run, with the alias as an attribute and spans for claim or bind, launch, outcome and publication. The worker's spans come later, relayed through ploegd, so sandbox egress is not widened. The gateway is configured, outside this repository, to tag its spans with the same alias.

### Consequences

* Good, because each failure in the WI-138 timeline would have alerted within minutes, attributed to its Team and Role.
* Good, because one key joins logs, metrics, spend and spans, so an incident timeline becomes a query.
* Good, because no new deployable is needed, and no sandbox egress is widened.
* Bad, because label cardinality grows: Team × Role × outcome × reason. It is bounded by configuration, with tens of series per Team.
* Bad, because a renamed metric and log key break existing dashboards and the log shipper's mapping. The one release in which both are emitted gives deployments time to move.

### Confirmation

* **Metrics.** A `pkg/httpapi` metrics test asserts every new family with its labels, and fails on today's output.
* **Rule replay.** A `pkg/store` test replays the WI-138 sequence: two idle kills, one Team, one Shift, no pull request. It asserts that `ploeg_runs_finished_total` and `ploeg_shift_runs_without_pr_max` move as the rules expect.
* **Log keys.** A `pkg/worker` test captures the log output of a full fake Run and fails on any line after claim that lacks `trace`, `run` or `role`.
* **Chart.**
  * The golden render contains the new rules.
  * `promtool test rules`, or the chart's existing rule test, fires `PloegRunsIdleKilled` on a two-kill series and does not fire on one kill.
* **Deployed.** After rollout, a LogsQL query for one alias returns the ploegd and worker lines of that Run, and the alerts appear in vmalert's rule list.

## Pros and Cons of the Options

### OpenTelemetry tracing in ploegd and the worker first

* Good, because traces show phase timings and causality directly.
* Bad, because the worker sandbox has no route to a collector, so its spans need widened egress or a relay first.
* Bad, because traces alone do not alert. The counters are still needed.

### Leave metrics to the deployment's SQL exporter

* Good, because nothing changes in Ploeg.
* Bad, because every deployment re-derives Ploeg's semantics in SQL against tables that change with every migration. The homelab exporter's comment is already stale.

## Re-evaluation triggers

* The worker sandbox gains a route to an OTLP collector. Worker spans can then be direct.
* A Team's series count passes 200.
* An incident is diagnosed from logs because no metric or alert covered it.

## More Information

* Evidence: [observability and decisions without code](../research/2026-10-05-observability-and-unfinished-decisions.md), §1.
* Related: [ADR-0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md), [ADR-0074](0074-ploeg-publishes-execution-facts-and-consumers-own-presentation-and-delivery-analytics.md), [ADR-0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md), and the [alerts guide](../ops/alerts.md).
* Homelab follow-up, outside this repository: map `trace_id` from `trace` in the log shipper, and add LiteLLM, agent-sandbox and KEDA rules.
* 2026-10-05: proposed.
