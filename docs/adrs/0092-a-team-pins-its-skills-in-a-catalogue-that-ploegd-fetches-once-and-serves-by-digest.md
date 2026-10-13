---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Team pins its skills in a catalogue that ploegd fetches once and serves by digest

## Context and Problem Statement

[ADR-0083](0083-a-run-is-told-which-skills-bear-on-its-work-item.md) selects the skills
a Run is told to read from three sources: Ploeg's embedded skills, the repository's
`.agents/skills/`, and directories named in `PLOEG_SKILL_DIRS`. It does not say how an
organisation's shared skills get into those directories. Today that is done outside
Ploeg: a runner image copies a few skills from a skill repository's main branch at
build time, and an entrypoint could add more at start-up from a variable nothing sets.
That broke Runs once already, when a skill was renamed upstream, and it only works for
the one harness the image starts. How does a Team give its Runs a reviewed, pinned set
of shared skills that every harness gets, that a person can update in one pull request,
and that is the same set a developer installs on their own machine?

## Decision Drivers

* A skill change reaches Runs only through a reviewed change, pinned to a commit and a content hash.
* Every harness, including one that runs off the worker, gets the same skills.
* A Team admin edits one file; updates arrive as dependency pull requests.
* A developer can install exactly what the agents get with an existing tool.
* Follow an existing format; the Agent Skills specification stops at one skill (research 2026-10-13).
* A missing or changed skill never stops a Run silently, and never stops it at all.

## Considered Options

* A Team catalogue in the Agent Package Manager format, fetched and verified by ploegd and served to Runs by digest
* Bake skills into the runner image and select among them (today, extended)
* Each Run clones the skill repository at a pinned ref
* A Ploeg-specific catalogue file
* Skill bundles as OCI image volumes, like toolchains (ADR-0035)

## Decision Outcome

Chosen option: "A Team catalogue in the Agent Package Manager format, fetched and
verified by ploegd and served to Runs by digest", because it is the one existing
manifest with a commit-and-hash lockfile that Renovate already updates, it needs no
egress from a Run, and the same file installs the same skills for a person.

* **Catalogue.** A Team names an `apm.yml` and its `apm.lock.yaml`. Ploeg reads a
  strict subset: git sources with `repo_url`, `virtual_path`, `resolved_ref`,
  `resolved_commit` (40 characters) and `content_hash`. Any other shape is refused with
  a reason. A repository may carry its own `apm.yml` and lock; their entries join the
  Team's only from hosts on the Team's allowlist.
* **Always-on.** A Team lists skills every Run gets regardless of selection
  (`skills.always`), in place of skills baked into an image.
* **Fetch once.** ploegd fetches each locked entry from its forge once, checks
  `content_hash`, and keeps it content-addressed under fixed size limits. A Run asks
  ploegd for its skills with its Run token; it never reaches the skill's forge.
* **Install for every harness.** The worker installs every admitted skill under the
  Run's home for discovery (ADR-0035's directories plus each harness's own), and
  ADR-0083's selection names the few that bear on the Work Item. A harness that runs off
  the worker fetches the same set with the Run token.
* **Fail open on availability, closed on content.** A skill that cannot be fetched, or
  whose content does not match its hash, is left out and recorded with the reason; the
  Run continues. A skill is never installed from content that does not match the lock.
* **Record.** ploegd records per Run every admitted skill's name, version, source,
  digest, whether it was selected and why, and whether the transcript shows it opened.
* **Metadata.** Ploeg's routing keys live under `metadata` (`ploeg-tags`,
  `ploeg-paths`). Ploeg also honours a top-level `paths` that a skill already declares
  for Claude Code or OpenHands.

### Consequences

* Good, because a skill update is a Renovate pull request against the catalogue, reviewed like a dependency bump.
* Good, because runner images carry no skills, and a renamed or deleted skill shows up as a refused entry, not a failed build or Run.
* Good, because a developer runs `apm install` on the same file and gets the agents' skills in their own tool.
* Good, because the Run record answers "which skills did this Run have, and did it read them" without a paid probe.
* Bad, because ploegd gains a fetcher and a content store. Bounded by size limits and the host allowlist, and much smaller than a registry.
* Bad, because APM is a 0.x draft. Ploeg reads a subset and pins the lockfile versions it accepts; a format change is caught by a contract test, not in production.

### Confirmation

* `go test ./pkg/skillcatalogue/` proves the strict lock subset, the host allowlist, refusal on a hash mismatch, and content-addressed reuse.
* `go test ./pkg/worker/` proves a Run installs admitted skills in every harness directory, continues when an entry is refused, and records each skill.
* `go test ./internal/ledger/` keeps this record and its index row in step.
* CI runs all three with `mise exec -- go test ./...`.

## Pros and Cons of the Options

### Bake skills into the runner image and select among them

* Good, because a Run needs no fetch.
* Bad, because a skill change is an image release, the set is the same for every Team, and it reaches only the harness the image starts.

### Each Run clones the skill repository at a pinned ref

* Good, because it is simple.
* Bad, because it gives every Run forge egress and credentials, and an air-gapped Team gets nothing.

### A Ploeg-specific catalogue file

* Good, because Ploeg controls the format.
* Bad, because no dependency bot updates it and no developer tool installs from it.

### Skill bundles as OCI image volumes

* Good, because the kubelet already pulls and caches toolchains this way.
* Bad, because someone must build an image per skill set, there is no common skill media type, and an off-worker harness cannot mount a volume.

## Re-evaluation triggers

* The Agent Skills specification merges a version, lock or index format (issues #46 or #254).
* APM reaches 1.0 or changes `apm.lock.yaml` incompatibly.
* A harness Teams use reads an OCI skill artifact natively.
* ploegd's skill store passes 100 MiB or 500 distinct digests.

## More Information

* Evidence trail: [2026-10-13 sharing Agent Skills](../research/2026-10-13-sharing-agent-skills.md).
* OpenSpec change `pin-skill-catalogues` carries the requirements and tasks.
* Refines [ADR-0083](0083-a-run-is-told-which-skills-bear-on-its-work-item.md) (sources) and [ADR-0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) (installation); records per Run like [ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md); measures opening from [ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md) transcripts.
