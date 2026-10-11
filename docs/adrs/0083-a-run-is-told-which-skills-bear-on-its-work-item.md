---
status: accepted
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run is told which skills bear on its Work Item

## Context and Problem Statement

[ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) gives every Run Ploeg's own skills by kind: a writer verifies, a reader reviews. Every other Agent Skill, from the repository or the organisation, reaches the agent only if its harness discovers it and the model chooses to load it from its description. Cheap models rarely do: on 2026-10-11 a rule a small model followed 10 times out of 10 when it was put in front of it was followed 4 times out of 10 when it had to be found. How does a Run get the skills that bear on its Work Item, without a model call and without trusting tracker content?

## Decision Drivers

* The weakest model a Team runs must still load the right skill.
* Selection is deterministic, bounded and recorded, so a Run can be explained afterwards.
* No skill comes from tracker content, steering or a context bundle.
* One notion of relevance for knowledge and skills.

## Considered Options

* The worker selects skills with the knowledge pack's matcher and names them in the prompt
* Install every available skill and rely on the harness to discover them
* Select skills only from explicit labels on the Work Item
* Ask a model which skills to load

## Decision Outcome

Chosen option: "The worker selects skills with the knowledge pack's matcher and names them in the prompt", because it puts the right skill in front of every model, costs nothing per Run, and reuses the relevance the knowledge pack (ADR-0065) already has.

* **Sources.** Ploeg's embedded skills, the repository's `.agents/skills/` at the Run's base commit, and every directory in `PLOEG_SKILL_DIRS`.
* **Selection.** `pkg/knowledge`'s term matching over each skill's name, description and `metadata.ploeg-tags`, against the Work Item's title, description, labels and attached context file names. A skill whose `metadata.ploeg-paths` glob matches a path the branch changed is selected first. At most `PLOEG_SKILL_LIMIT` (default 5) skills besides the role skills, best first, ties by name.
* **Role skills** from ADR-0035 are always given.
* **Briefing.** The prompt names each selected skill, its path and why it was chosen, and tells the agent to read it before starting. Unselected skills stay discoverable.
* **Record.** `TaskSpec.skills` lists name, source, path, reason and digest; `taskspec.v1` gains it as an optional field.

### Consequences

* Good, because a cheap model is told which skill to read instead of having to find it.
* Good, because the TaskSpec shows what a Run was told, so a wrong selection can be traced.
* Bad, because selection is only as good as a skill's description and tags; a vague skill is never selected.
* Bad, because common words can select loosely related skills until the shared stopword list grows.

### Confirmation

* `go test ./pkg/harness/skills/` proves selection is deterministic, bounded by `PLOEG_SKILL_LIMIT`, always keeps the role skills, prefers a `ploeg-paths` match, and never reads a skill from a context bundle.
* `go test ./pkg/worker/` proves the prompt names each selected skill with its path and reason, and that `TaskSpec.skills` validates against `taskspec.v1.schema.json`.
* CI runs both with `mise exec -- go test ./...`.

## Pros and Cons of the Options

### Install every available skill and rely on the harness to discover them

* Good, because it needs no code.
* Bad, because it is today's behaviour, and small models do not load what they are not told about.

### Select skills only from explicit labels on the Work Item

* Good, because it is exact.
* Bad, because every tracker item would need a person to label it, and a label names a skill the person must know exists.

### Ask a model which skills to load

* Good, because it understands intent beyond shared words.
* Bad, because it costs a model call per Run, is not deterministic, and adds a Run-shaped step outside the Shift's Budget.

## Re-evaluation triggers

* A harness Teams use starts loading skills by relevance on its own, measured with the same probe.
* The Agent Skills specification adds a standard field for paths or triggers.
* TaskSpecs show a selected skill that the Run's transcript never opened in more than half of Runs over a month.

## More Information

* 2026-10-11 — The owner asked for Ploeg's agents to load skills on demand from the Work Item and its context; the OpenSpec change `select-skills-per-work-item` carries the requirements.
* 2026-10-11 — Accepted by the owner. Implementation follows the change's tasks.
* The 4-of-10 and 10-of-10 figures come from Unfold pull request #308's probes (Haiku 5.5, a Forgejo workflow rule).
* Refines [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md); related: [ADR-0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md), [ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md).
