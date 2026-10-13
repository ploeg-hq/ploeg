## Context

`skills.Install` writes Ploeg's role skills under the Run's home
(`.agents/skills` plus the harness directory from `SkillDirectories`).
`select-skills-per-work-item` (not yet built) adds selection over embedded,
repository and `PLOEG_SKILL_DIRS` skills. Team settings are split between the
ploegd file config (`pkg/config.Team`) and the chart's `executor.teams[]`, which
reaches the worker only as environment. The worker already calls ploegd with its
Run token (transcript ingest, outcomes). Transcripts (ADR-0090) carry tool-call
paths only for `acp` Runs. TaskSpec is handed to the harness and not stored.

Survey of formats: `docs/research/2026-10-13-sharing-agent-skills.md`.

## Goals / Non-Goals

**Goals:** one reviewed file per Team decides which shared skills its Runs
have; every harness gets them; updates arrive as dependency pull requests; a
person can see, per Run, what was given, why and whether it was read; writing a
skill that Ploeg picks is testable without a model.

**Non-Goals:** see the proposal.

## The four people this is for

### A skill author

1. Writes `skills/<name>/SKILL.md` in the organisation's skill repository: a
   name, and a description that says when to use it. Optional
   `metadata.ploeg-tags` for the words Work Items use, and `paths` for the
   files it is about (the same field Claude Code and OpenHands read).
2. Runs `ploeg-okf skills check skills/<name>`: the specification's rules plus
   Ploeg's hints (no "Use when" in the description, a tag that is a stopword, a
   glob that matches nothing in a sample repository).
3. Runs `ploeg-okf skills explain --title "Add a release workflow" skills/`:
   the skills selected, the terms that matched, and the near misses with what
   was missing ("adr-writer: score 3, needs 4; no term from the title").
4. Adds routing examples next to the skill's evals; the skill repository's CI
   runs `explain` on them, so a description change that stops a skill being
   picked fails CI. No model call.
5. Merges. The release tags `<name>-v<semver>`; Renovate opens a bump against
   every catalogue that pins the skill.

### A Team admin

1. Keeps `apm.yml` and `apm.lock.yaml` for the Team where the Team's
   deployment lives (the GitOps repository), and names them in the Team's
   config with `skills.always` and `skills.sources`:

   ```yaml
   teams:
     builders:
       skills:
         catalogue: /etc/ploeg/skills/builders   # apm.yml + apm.lock.yaml
         always: [guard-secrets]
         sources: [forgejo.example.org/acme/*]
   ```

2. Adds a skill with `apm install acme/skills/skills/adr-writer#adr-writer-v2.1.0`,
   which writes both files; reviews and merges.
3. Gets Renovate pull requests for new skill releases, with the skill's
   changelog.
4. Opens the Team's skills page in the operator consumer: each skill, its
   version, how many Runs it was selected for and how many opened it in the
   last 30 days, and any refused entry with the reason.

### A repository maintainer

1. Puts repository-only skills in `.agents/skills/` (unchanged).
2. Optionally adds an `apm.yml` and lock to the repository for shared skills
   the repository needs; entries from hosts the Team allows join that Team's
   catalogue for Runs against this repository.
3. Runs `apm install` locally and gets exactly the skills the agents get, in
   whichever tool they use.

### The person who files or reviews a Work Item

1. Files the Work Item as today. Optionally adds a `skill:<name>` label to put
   a skill first; the label can only pick among skills already admitted.
2. On the Run, sees "Skills given": each name, version, why it was selected,
   and whether the agent opened it.

## Decisions

### Read the Agent Package Manager format, not a Ploeg one

It is the only consumer manifest with a lock that records the commit and a
content hash, and Renovate updates it (manager `apm`). Ploeg parses a strict
subset (git entries with `repo_url`, `virtual_path`, `resolved_ref`,
`resolved_commit`, `content_hash`; `lockfile_version` 1 or 2) and refuses any
other entry with a reason, so Ploeg does not depend on the `apm` binary or on
the parts of a 0.x draft it does not need. Alternatives: `skills-lock.json`
pins a ref, not a commit, and no bot updates it; a Ploeg file has no tooling.

### ploegd fetches and serves; a Run never reaches a skill's forge

ploegd already talks to forges. It fetches a locked entry at
`resolved_commit`, hashes the skill directory the way the lock does, refuses a
mismatch, and stores the files under their digest in Postgres (bytea, at most
1 MiB per skill and 256 files). Run routes:

- `GET /api/v1/runs/{token}/skills` → the admitted set: name, version, source,
  digest, refused entries with reasons.
- `GET /api/v1/runs/{token}/skills/{digest}` → a tar of the skill's files.
- `POST /api/v1/runs/{token}/skills/repository` with the repository lock's
  entries → the subset ploegd admits under the Team's allowlist, fetched the
  same way.

A per-Run clone would give every Run forge egress and credentials; an OCI
volume per skill set needs an image build per change and cannot reach a
harness that runs off the worker.

### Install everything admitted, name only what is selected

All admitted skills are installed for discovery (bounded by
`PLOEG_SKILL_INSTALL_LIMIT`, default 30), in `.agents/skills` and each
harness's directory: `.openhands/skills/installed`, `.claude/skills`,
`.qwen/skills` for the Qwen Code ACP profile. ADR-0083's selection then names
at most `PLOEG_SKILL_LIMIT` of them. A `claude-environment` Run's runner calls
the same run routes with the Run token.

### Precedence when names collide

Role skills, then `skills.always`, then the Team catalogue, then the
repository's lock, then `.agents/skills` in the clone. The first wins, the
others are recorded as shadowed. A repository cannot replace a skill its Team
pinned.

### Availability fails open, content fails closed

A fetch error, a refused source or a hash mismatch leaves that skill out and
records why; the Run continues. Only content that matches its lock is ever
installed. An `always` skill that is missing is recorded the same way and also
raised on the Team's skills page; it does not stop the Run.

### Record per Run, and measure opening from what is already recorded

A `run_skills` table holds, per Run, each admitted skill's name, version,
source, digest, selected and reason, and `opened` (null when unknown).
`opened` comes from transcript tool calls whose paths include the skill's
`SKILL.md`: available for `acp` Runs now; OpenHands and Claude Code Runs need
their adapters to transcribe tool calls (a task here, useful beyond skills).
This replaces `select-skills-per-work-item` task 3.2's paid probe with
measurement on Runs that are already paid for.

### Honour `paths`; keep Ploeg keys in `metadata`

A skill that declares `paths` for Claude Code or OpenHands is matched on them
like `metadata.ploeg-paths`, so an author writes globs once. `ploeg-tags` and
`ploeg-paths` stay under `metadata`, as the specification asks for
client-specific keys.

### A label hints, it never adds

`skill:<name>` on the Work Item moves an admitted skill to the front of the
selection with the reason "asked for on the Work Item". A name that is not
admitted is ignored and recorded. Tracker content still cannot bring a skill
into a Run, which keeps ADR-0083's driver.

## Risks / Trade-offs

- [APM changes its lock] → Ploeg accepts listed `lockfile_version`s only and a
  contract test pins a sample lock; an unknown version refuses the catalogue
  with a reason, and Runs keep role and repository skills.
- [The store grows] → content-addressed, so a version shared by many Teams is
  stored once; digests no catalogue references for 30 days are deleted.
- [A malicious skill in an allowed source] → it is pinned by hash in a reviewed
  file, the same trust as any dependency. Scanning belongs in the skill
  repository's CI.
- [Thirty installed skills crowd a harness's skill list] → the limit is
  configurable, and the selected ones are named in the prompt.
- [Renovate cannot run `apm install` in its image] → verify before relying on
  it; the fallback is a small CI job that regenerates the lock.

## Migration Plan

1. Ship with no Team catalogue configured: behaviour is ADR-0083's.
2. A Team adds a catalogue whose `always` list equals the skills its runner
   image bakes; compare the Run records with the image's set.
3. Runner images stop baking skills and drop their start-up skill code.

Rollback: remove the Team's `skills` block; Runs get role and repository
skills.

## Open Questions

- Should a Tenant hold a catalogue that its Teams inherit, once tenancy is
  built? Left to that work.
- Should the operator consumer offer "add this skill" that opens the pull
  request against the catalogue for the admin?
