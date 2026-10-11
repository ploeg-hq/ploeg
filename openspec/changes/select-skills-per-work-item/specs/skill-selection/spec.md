# Skill selection

## ADDED Requirements

### Requirement: A Run is told which skills bear on its Work Item

The worker SHALL select skills from Ploeg's embedded skills, the repository's
`.agents/skills/` at the Run's base commit and every configured skill directory,
and SHALL name each selected skill in the prompt with its path and the reason it
was selected, before the delivery contract (ADR-0083).

#### Scenario: A repository skill matches the Work Item

- **GIVEN** a repository skill whose description mentions database migrations
- **WHEN** a writer Run is claimed for a Work Item titled "Add a migration for run usage"
- **THEN** the prompt names that skill, its path in the clone and the terms that matched

#### Scenario: A path rule selects a skill for a reader

- **GIVEN** a repository skill with `metadata.ploeg-paths: ".forgejo/**"`
- **WHEN** a reader Run reviews a branch that changed `.forgejo/workflows/release.yml`
- **THEN** the prompt names that skill with the reason "changed .forgejo/workflows/release.yml"

#### Scenario: Nothing matches

- **WHEN** no skill other than the role skills matches the Work Item
- **THEN** the prompt lists only the role skills, as before this change

### Requirement: Role skills are always given

A writer SHALL always receive `ploeg-verify-before-handoff`, and a reader SHALL
always receive `ploeg-review-against-work-item` and
`ploeg-verify-before-handoff`, whatever else is selected.

#### Scenario: A full selection does not crowd out a role skill

- **GIVEN** more matching skills than `PLOEG_SKILL_LIMIT`
- **WHEN** a reader Run is briefed
- **THEN** both role skills are listed and at most `PLOEG_SKILL_LIMIT` other skills are listed

### Requirement: Selection is bounded and deterministic

The worker SHALL select at most `PLOEG_SKILL_LIMIT` skills besides the role
skills, best score first, ties broken by name, and SHALL make no model call to
select them.

#### Scenario: The same inputs select the same skills

- **WHEN** two Runs are briefed for the same Work Item, base commit and configuration
- **THEN** they are told the same skills in the same order

### Requirement: Skills never come from untrusted content

The worker SHALL NOT select a skill from tracker content, steering text or a
context bundle.

#### Scenario: A context bundle contains a SKILL.md

- **GIVEN** a context bundle that contains `skills/x/SKILL.md`
- **WHEN** the Run is briefed
- **THEN** that file is offered as context only and is not named as a skill

### Requirement: A Run records the skills it was given

`TaskSpec.skills` SHALL list each skill named in the prompt with its name,
source (`embedded`, `repository` or `configured`), path, reason and content
digest.

#### Scenario: An edited skill changes its digest

- **WHEN** a repository skill's `SKILL.md` changes between two Runs
- **THEN** the two TaskSpecs record different digests for it
