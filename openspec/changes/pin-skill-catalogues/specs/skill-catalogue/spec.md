# Skill catalogue

## ADDED Requirements

### Requirement: A Team pins its shared skills in a catalogue

ploegd SHALL read a Team's skill catalogue from an `apm.yml` and `apm.lock.yaml`
named in the Team's configuration, and SHALL admit only git entries whose
`repo_url` matches the Team's `skills.sources` and whose lock records
`resolved_commit` and `content_hash` (ADR-0092).

#### Scenario: A pinned skill from an allowed source

- **GIVEN** a Team allowing `forgejo.example.org/acme/*` and a lock entry for `adr-writer` at a 40-character commit with a content hash
- **WHEN** a Run of that Team asks for its skills
- **THEN** `adr-writer` is admitted with its version, source and digest

#### Scenario: A source outside the allowlist

- **GIVEN** a lock entry from a host the Team does not allow
- **WHEN** a Run asks for its skills
- **THEN** the entry is refused with the reason "source not allowed" and the other entries are admitted

#### Scenario: An unsupported lock

- **GIVEN** a lock whose `lockfile_version` Ploeg does not accept
- **WHEN** ploegd loads the Team's configuration
- **THEN** the catalogue is refused with that reason and Runs get role and repository skills only

### Requirement: Skill content is verified and fetched once

ploegd SHALL fetch a locked skill at its `resolved_commit`, SHALL refuse it when
its content does not match `content_hash`, and SHALL serve a verified skill to
any Run by digest without fetching it again.

#### Scenario: Content does not match the lock

- **GIVEN** a lock entry whose fetched files hash differently from `content_hash`
- **WHEN** a Run asks for its skills
- **THEN** the skill is refused with the reason "content does not match the lock" and is not installed

#### Scenario: Two Teams pin the same version

- **GIVEN** two Teams whose locks name the same digest
- **WHEN** Runs of both Teams start
- **THEN** ploegd fetched that skill once

### Requirement: A Run never reaches a skill's forge

A Run SHALL receive its skills from ploegd over the run API with its Run token,
and SHALL NOT need network access or credentials for the forge a skill comes
from.

#### Scenario: An air-gapped Team

- **GIVEN** a Team on the `airgapped` network profile with a catalogue
- **WHEN** its Run starts
- **THEN** the Run's admitted skills are installed

### Requirement: Every harness gets the admitted skills

The worker SHALL install every admitted skill, up to `PLOEG_SKILL_INSTALL_LIMIT`,
in `.agents/skills` under the Run's home and in the configured harness's own
skill directory, and a harness that runs off the worker SHALL fetch the same set
with the Run token.

#### Scenario: A Qwen Code Run

- **GIVEN** a Team running the `acp` harness with the Qwen Code profile
- **WHEN** its Run starts
- **THEN** each admitted skill is under `.agents/skills` and `.qwen/skills` in the Run's home

### Requirement: Always-on skills are given to every Run

Each skill named in a Team's `skills.always` SHALL be installed and named in
the prompt for every Run of that Team, besides the role skills and outside
`PLOEG_SKILL_LIMIT`.

#### Scenario: A narrow Work Item

- **GIVEN** `skills.always: [guard-secrets]`
- **WHEN** a Run is briefed for a Work Item no skill matches
- **THEN** the prompt names the role skills and `guard-secrets`

### Requirement: A missing skill never stops a Run

When a skill cannot be fetched or is refused, the worker SHALL continue the Run
without it and SHALL record the reason.

#### Scenario: The skill's forge is down

- **WHEN** ploegd cannot fetch a locked skill it has not stored before
- **THEN** the Run starts without it and its Run record lists it as refused with the fetch error

### Requirement: A repository adds shared skills only within its Team's allowlist

The worker SHALL read the repository's `apm.lock.yaml` at the Run's base commit
and ploegd SHALL admit its entries under the same rules as the Team's, and a
repository entry SHALL NOT replace a skill of the same name from the Team's
catalogue.

#### Scenario: A repository pins a skill its Team also pins

- **GIVEN** the Team pins `adr-writer` v2 and the repository's lock pins `adr-writer` v1
- **WHEN** a Run of that repository starts
- **THEN** v2 is installed and v1 is recorded as shadowed

### Requirement: A label can put an admitted skill first

A Work Item label `skill:<name>` SHALL place that admitted skill first in the
selection with the reason "asked for on the Work Item", and SHALL NOT admit a
skill that is not already admitted.

#### Scenario: The label names an unknown skill

- **GIVEN** a Work Item labelled `skill:deploy-prod` and no admitted skill of that name
- **WHEN** its Run is briefed
- **THEN** nothing is added and the Run record notes the unknown name

### Requirement: Declared paths select a skill

A skill's top-level `paths` SHALL be matched against changed paths the same way
as `metadata.ploeg-paths`.

#### Scenario: A skill written for Claude Code

- **GIVEN** a skill with `paths: [".forgejo/**"]` and no Ploeg metadata
- **WHEN** a reader Run reviews a branch that changed `.forgejo/workflows/release.yml`
- **THEN** the skill is selected with that path as the reason

### Requirement: Each Run's skills are recorded and readable

ploegd SHALL record, per Run, each admitted, refused and shadowed skill with
its name, version, source, digest, whether it was selected and why, and whether
the Run's transcript shows its `SKILL.md` opened, and SHALL serve the record and
each Team's catalogue with its 30-day use on the operator API.

#### Scenario: An ACP Run reads a selected skill

- **GIVEN** an `acp` Run whose transcript has a tool call reading the selected skill's `SKILL.md`
- **WHEN** an operator reads the Run's skills
- **THEN** that skill shows selected, with its reason, and opened

#### Scenario: A harness without tool-call transcripts

- **WHEN** an operator reads the skills of a Run whose harness does not transcribe tool calls
- **THEN** `opened` is unknown, not false

### Requirement: Selection can be explained before a Run

`ploeg-okf skills explain` SHALL print, for a title, description, labels and
changed paths against a set of skills, the selected skills with their reasons
and the unselected skills that matched at least one term with what they lacked.

#### Scenario: A near miss

- **GIVEN** a skill matching one term of a title where selection needs a score of 4
- **WHEN** `ploeg-okf skills explain --title "<title>" <dir>` runs
- **THEN** the skill is listed as a near miss with its score and the threshold
