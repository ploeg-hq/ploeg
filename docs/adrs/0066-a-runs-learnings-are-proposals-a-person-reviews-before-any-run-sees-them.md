---
status: proposed
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-04
---

# A Run's learnings are proposals a person reviews before any Run sees them

## Context and Problem Statement

A knowledge base only improves if Runs can add to it ([0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md)). A Run that hit a pitfall or discovered a convention is the best placed to write it down. But a model's note fed straight into later Runs' prompts lets one bad Run, or one prompt-injected file, steer every Run after it. How does a Run contribute knowledge without that risk?

## Decision Drivers

* Nothing a model wrote reaches another Run's prompt before a person accepts it.
* The Run needs no tool to contribute: it already ends with an OutcomeReport ([0011](0011-the-pull-request-is-the-blackboard.md)).
* Each proposal names the Run and Work Item it came from.
* The volume a Run can propose is bounded, so review stays possible.

## Considered Options

* Learnings in the OutcomeReport, stored as proposed OKF concepts, accepted by a person
* Learnings written directly into the knowledge base
* No learnings; people write all knowledge

## Decision Outcome

Chosen option: "Learnings in the OutcomeReport, stored as proposed OKF concepts, accepted by a person", because it reuses the report every adapter already writes and puts a person between a model's note and every later prompt.

* `OutcomeReport.learnings`: at most 10 entries of `{type, title, description?, resource?, tags?, body}`. The worker drops entries without a type, title or body and keeps the first 10.
* The prompt invites learnings only when learnings are enabled (`PLOEG_KNOWLEDGE_OUTBOX` in the proof of concept), asking for a Pitfall, Convention or Fact and nothing that restates the Work Item.
* Each learning becomes an OKF concept under `learned/`, marked `status: proposed`, `proposed-by: <trace>` and `work-item: <ref>`. In the proof of concept the worker writes them to an outbox directory per Run. In production ploegd stores them and opens a learning branch in the Tenant graph.
* A proposed concept is never selected into a pack. A person accepts, edits or rejects it in Vloer; a rejected one is kept to suppress repeats.

### Consequences

* Good, because knowledge grows from real Runs without a model writing the prompts of later Runs.
* Good, because each accepted concept traces back to the Run and Work Item that taught it.
* Bad, because accepted knowledge waits for a person; an ignored review queue means no learning.
* Bad, because ploegd must store and serve proposals, which is not built yet.

### Confirmation

`go test ./pkg/worker/ ./pkg/harness/` in the existing CI step: `TestLearningConcepts_KeepsValidProposalsUnderLearned` (validation, cap, provenance), `TestKeepLearnings_WritesAReviewableBundlePerRun`, `TestResolveOutcome_KeepsTheAgentsLearnings` and the OutcomeReport schema case that refuses a learning without a body. Proposed: a ploegd test that a proposed learning is absent from every pack until accepted.

## Pros and Cons of the Options

### Learnings written directly into the knowledge base

* Good, because knowledge improves with no human work.
* Bad, because one wrong or injected note reaches every later Run, which is the failure this decision exists to prevent.

### No learnings; people write all knowledge

* Good, because every concept is human-written.
* Bad, because the cheapest source of pitfalls, the Run that hit them, is thrown away.

## Re-evaluation triggers

* The first 20 reviewed learnings show an acceptance rate below 25 %, or above 90 % with no edits (review adds nothing).
* Derived pitfalls from Run outcomes (stuck reasons, failed checks) cover what agents propose.
* A Tenant asks for unreviewed learnings within its own repositories.

## More Information

* 2026-10-04 — kept proposed by the owner until the measurement spike (VIK-1859) reports. Owner answers recorded meanwhile: a hosted Tenant's knowledge base is an Omnigraph graph per Tenant; an accepted learning lands in the Tenant graph, with an optional pull request to the repository's `knowledge/`; Tenant admins accept or reject learnings, and clients can read learnings about their repositories.
* Evidence: [OKF knowledge pack proof of concept](../research/2026-10-04-okf-knowledge-pack-poc.md).
* Unfold system ADR-0020; OpenSpec change `add-knowledge-and-context-bundles`.
