---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run records its Harness version, and the Teams read shows each Role's Harness and model

## Context and Problem Statement

[ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md) records the Harness name and agent profile a Run claimed with, but not the Harness version, and an operator consumer cannot see which Harness, model and version a Role uses before it starts a session. The owner wants people to choose the Harness per session, which needs both. Where does a Run's Harness version come from, and what does ploegd tell a consumer about a Role's Harness?

## Decision Drivers

* A recorded value is what the Run ran with, written once; unknown stays unknown (ADR-0091).
* A Run that dies before reporting still keeps what was known at claim.
* ploegd never executes an agent and holds no worker environment it was not given.
* Consumers compute their own comparisons from execution facts ([ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)).

## Considered Options

* The worker reports the version at claim when it knows it and in the Outcome Report's `harness` object when it learns more; ploegd records both once and learns each Role's configured Harness from the chart
* The worker reports the version only in the Outcome Report
* ploegd records the agent image digest instead of a version

## Decision Outcome

Chosen option: "the worker reports at claim and in the Outcome Report; ploegd learns the configured Harness from the chart", because it keeps the version of a Run that died early and lets ploegd show configuration it holds itself.

* **Version.** At startup the worker runs the Harness binary with `--version`, bounded to 5 s, and keeps the first output line up to 128 bytes. It skips the probe for `exec` and for a binary it cannot resolve before the clone, which then record no version. The ACP adapter also keeps the agent's `agentInfo` name and version from `initialize`. The claim request carries `harnessVersion`; the Outcome Report carries an optional worker-only `harness` object with name, profile, version and agent. ploegd writes `agent_runs.harness_version` and `harness_agent` once, never overwrites, and stores null when absent or malformed.
* **Configuration.** The chart renders `PLOEG_TEAM_HARNESSES` (team → Role → name and profile) from the same values as each worker's environment.
* **Reads.** `run` and `runListItem` carry `harnessVersion` and `harnessAgent`. The Teams read carries per Role `harness`, `model` (the Role's default model: the single model of its inference policy, as ADR-0091 defines it, or the policy's named default once [ADR-0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md) lets a policy list more than one) and `observedHarness`, the newest recorded version for that Role with its time and the Harness name and profile that recorded it.

### Consequences

* Good, because every Run that reaches its claim has a version, and a consumer can label a session's Harness before starting it.
* Good, because configuration and observation are separate fields, so a rollout in progress is visible.
* Bad, because the version is text the image prints about itself, not a verified identity.
* Bad, because the chart now passes Harness settings to two places; a `helm template` test has to keep them equal.

### Confirmation

* `go test ./pkg/harness/` asserts the `harness` object in `docs/contracts/outcomereport.v1.schema.json` against the Go type (`contract_test.go`).
* `go test ./pkg/store/ ./pkg/httpapi/` records the version at claim, keeps it when the Outcome Report differs, reads null for older Runs, and reads the Teams response against `docs/contracts/operator-api.v1.schema.json`.
* `go test ./pkg/worker/` checks the probe's bound and that a drop-box `harness` value is discarded.
* `mise run verify` renders a Role override from an `ops/helm/ploeg/ci/` values file and compares it with the committed goldens (`scripts/helm-golden.sh check`), so `PLOEG_TEAM_HARNESSES` and the worker's `PLOEG_HARNESS` are reviewed together.

## Pros and Cons of the Options

### The worker reports the version only in the Outcome Report

* Good, because it is one field in one place, as the owner first phrased it.
* Bad, because a Run killed before reporting records no version, and failed Runs are the ones a comparison needs.

### Record the image digest

* Good, because a digest is verifiable.
* Bad, because the worker cannot read its own digest without a registry or downward-API call, and a digest does not tell a person which agent version ran.

## Re-evaluation triggers

* ACP makes `agentInfo` required, so the probe is no longer needed for ACP agents.
* A Harness used by a Team prints no usable version, or takes longer than 5 s to print it.
* ploegd starts selecting the Harness per Run itself (ADR-0098), so the configured Harness becomes an allowlist rather than one value.

## More Information

* OpenSpec change `record-harness-per-run`.
* Builds on ADR-0091 (migration `0050_run_config.sql`); does not supersede it. It answers 0091's trigger "ploegd learns each Role's harness from its own configuration" by keeping the worker's claim as the source of what ran.
* 2026-10-13: the owner decided that Ploeg records the Harness and its version per Run, in the Outcome Report (additive) and the store (append-only), and that the Teams read shows per Role the Harness, its default, the model and the version. Proposed.
