---
status: proposed
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-04
---

# A Run is briefed with an OKF knowledge pack written outside the clone

## Context and Problem Statement

A Run gets the Work Item, earlier Rounds' findings ([0011](0011-the-pull-request-is-the-blackboard.md)), an OpenSpec brief when one is named, the repository's instruction files ([0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)) and Ploeg's skills ([0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md)). It gets nothing of what earlier Runs or people learned about the repository: conventions, decisions, pitfalls. Unfold system ADR-0020 (proposed) puts that knowledge in OKF bundles. How does the worker give a Run the part that matters, without a new harness tool and without crowding out the task?

## Decision Drivers

* No harness gains a tool or a credential ([0011](0011-the-pull-request-is-the-blackboard.md), [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)).
* The Work Item and the delivery contract stay the dominant text in the prompt.
* A writing Run cannot commit the knowledge into the pull request.
* The Run records exactly which knowledge it got.
* Work the knowledge does not cover gets no knowledge section at all.

## Considered Options

* Select concepts per Work Item, write them as an OKF directory in the Run's scratch directory, put only the index in the prompt
* Put the selected concepts' full text in the prompt
* Mount the whole knowledge base and let the agent search it
* A knowledge tool the agent calls

## Decision Outcome

Chosen option: "Select concepts per Work Item, write them as an OKF directory in the Run's scratch directory, put only the index in the prompt", because it costs the prompt only an index, survives on every harness and records what the Run was given.

* Sources: the repository's own bundle (`PLOEG_REPO_KNOWLEDGE_DIR`, default `knowledge`, `-` for none), every bundle in `PLOEG_KNOWLEDGE_DIRS`, and OKF concepts inside the Work Item's context ([0067](0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md)). Later: the Tenant's Omnigraph graph by stored query.
* Selection: concepts matching the Work Item's title, description and labels, plus the concepts they link to, best first within `PLOEG_KNOWLEDGE_BUDGET_BYTES` (default 24,000). A weak match is not included.
* Delivery: an OKF directory in `scratch/knowledge` with an `index.md` naming why each concept is there, and `PLOEG_KNOWLEDGE_DIR`. The prompt section "Knowledge pack" carries the index, framed as evidence below the delivery contract.
* Provenance: `TaskSpec.knowledge` carries the index, concept paths and a content digest per source.

### Consequences

* Good, because every harness can read files, so no adapter changes.
* Good, because a reviewer can see what a Run was told and replay it on the same digests.
* Bad, because keyword selection misses synonyms until selection moves to a hybrid query.
* Bad, because a pack can be wrong; the framing makes the code win, but an agent may still follow a stale concept.

### Confirmation

`go test ./pkg/okf/ ./pkg/knowledge/ ./pkg/worker/` in the existing `go test ./...` CI step: `TestKnowledgePack_BriefsFromTheRepositoryAndConfiguredBundles` (pack outside the clone, sources and digests), `TestKnowledgePack_NoneWithoutAMatchOrASource`, `TestComposePrompt_CarriesTheKnowledgeIndexAsEvidence` (section before the delivery contract, evidence framing) and `TestTaskSpec_MatchesSchema` with a `knowledge` field.

## Pros and Cons of the Options

### Put the selected concepts' full text in the prompt

* Good, because the agent cannot skip it.
* Bad, because 24 KB of concepts would rival the Work Item and the contract, on every turn.

### Mount the whole knowledge base and let the agent search it

* Good, because nothing is missed by selection.
* Bad, because the agent spends turns searching, and the Run's provenance becomes "everything".

### A knowledge tool the agent calls

* Good, because lookups follow the agent's actual need.
* Bad, because every harness needs the tool and a credential near the agent; kept for later, beside the pack.

## Re-evaluation triggers

* The measurement spike shows packs do not reduce cost, rounds or stuck rate on two repositories.
* Selection moves to an Omnigraph hybrid query, which changes the provenance to a graph commit.
* A harness gains a native knowledge or retrieval channel Ploeg could feed instead of files.
* OKF publishes a version that changes the reserved fields.

## More Information

* 2026-10-04 — kept proposed by the owner until the measurement spike (VIK-1859) reports. Owner answers recorded meanwhile: a hosted Tenant's knowledge base is an Omnigraph graph per Tenant; an accepted learning lands in the Tenant graph, with an optional pull request to the repository's `knowledge/`; Tenant admins accept or reject learnings, and clients can read learnings about their repositories.
* Evidence: [OKF knowledge pack proof of concept](../research/2026-10-04-okf-knowledge-pack-poc.md).
* Unfold system ADR-0020 and the RFC "agents learn from a knowledge base exchanged as OKF" (Unfold `docs/research/2026-10-04-rfc-agent-knowledge.md`).
* OpenSpec change `add-knowledge-and-context-bundles`.
