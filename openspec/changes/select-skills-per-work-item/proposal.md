# Select the skills a Run is told to load from its Work Item

## Why

A Run gets two fixed skills by kind (`ploeg-verify-before-handoff` for a
writer, plus `ploeg-review-against-work-item` for a reader). Everything else a
repository or an organisation wrote as an Agent Skill reaches the agent only if
its harness discovers it and the model decides to load it from the description
alone. Cheap models, which most Teams run, rarely do. On Unfold on 2026-10-11 a
rule that a small model followed 10 times out of 10 when it was in front of it
was followed 4 times out of 10 when the model had to find it. The owner asked
on 2026-10-11 for Ploeg's agents to load skills on demand, chosen from the Work
Item and the context people added.

## What Changes

**Seam touched:** the harness (worker → coding agent), plus one additive
optional TaskSpec field.

- The worker selects skills for each Run from three sources: Ploeg's embedded
  skills, the repository's `.agents/skills/` at the Run's base commit, and
  configured skill directories (`PLOEG_SKILL_DIRS`).
- Selection reuses the knowledge pack's matching (ADR-0065): the Work Item's
  title, description and labels, the names of attached context files, and, for
  a reader or a later Round, the paths the branch changed, against each skill's
  name, description and optional `metadata` keys `ploeg-tags` and
  `ploeg-paths`.
- The role skills stay unconditional. At most `PLOEG_SKILL_LIMIT` (default 5)
  other skills are selected, best match first.
- The prompt names each selected skill with its path and why it was chosen, and
  tells the agent to read it before starting. Skills that were not selected stay
  where the harness can discover them; nothing is hidden.
- `TaskSpec.skills` records each selected skill's name, source, path, reason
  and content digest.

## Capabilities

### New Capabilities

- `skill-selection`: which skills a Run is told to load, how they are chosen,
  and how the choice is recorded.

### Modified Capabilities

None.

## Non-goals

- Running or validating a skill's scripts. A selected skill is text the agent
  reads; what it then runs is governed by the harness as today.
- Skills from tracker content or context bundles. Both are untrusted data
  (R8's credential boundary and the context bundle rules stay as they are).
- A model call to choose skills. Selection is deterministic and free.
- Installing a repository skill somewhere else. It is already in the clone.

## Impact

- Code: `pkg/harness/skills` (sources, metadata, selection), `pkg/worker`
  (gather inputs, install configured skills, prompt section, TaskSpec field),
  `pkg/knowledge` (shared term matching, exported).
- Contract: `taskspec.v1` gains optional `skills`; additive.
- Configuration: `PLOEG_SKILL_DIRS`, `PLOEG_SKILL_LIMIT`.
- With no repository skills and no configured directories, a Run is unchanged
  apart from the TaskSpec listing its two role skills.
