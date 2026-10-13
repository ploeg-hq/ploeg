# Give each Team a pinned skill catalogue that every Run gets

## Why

ADR-0083 decides which skills a Run is told to read, from skills already on
disk. How an organisation's shared skills get onto that disk is outside Ploeg
today: a runner image copies a few from a skill repository's main branch at
build time. A rename upstream once failed every Run of that image, the set is
the same for every Team, only the harness the image starts sees it, and nobody
can tell afterwards which version a Run had. The owner asked on 2026-10-13 for
the pinned catalogue option, designed so that sharing a skill is as easy as
possible for the people who write, choose and review them.

## What Changes

**Seams touched:** ploegd configuration and run API, the worker's skill
installation, the operator API; one additive Run record.

- A Team names a skill catalogue: an `apm.yml` and its `apm.lock.yaml`
  (Agent Package Manager format, strict subset), plus `skills.always` and a
  source-host allowlist. A repository may add its own `apm.yml` and lock.
- ploegd fetches each locked skill once, verifies its `content_hash`, stores it
  content-addressed under fixed limits, and serves it to a Run by digest over
  the run API.
- The worker installs every admitted skill for discovery in every harness's
  directory, and ADR-0083's selection names the few that bear on the Work Item.
  A Work Item label `skill:<name>` puts an admitted skill first.
- A skill that cannot be fetched or does not match its hash is left out with a
  reason; the Run continues.
- ploegd records each Run's skills (name, version, source, digest, selected and
  why, opened) and serves them, plus a Team's catalogue with its use, on the
  operator API.
- `ploeg-okf skills explain` shows, for a title or a Work Item, which skills
  would be selected, why, and which nearly were.

## Capabilities

### New Capabilities

- `skill-catalogue`: how a Team pins shared skills, how they reach a Run, and
  how their use is recorded.

### Modified Capabilities

None. `skill-selection` is still a change (`select-skills-per-work-item`);
this change lands after it and states its additions (catalogue sources,
`paths`, the `skill:` label) as `skill-catalogue` requirements.

## Non-goals

- A skill registry or marketplace. Skills stay in the forges their authors use.
- Signing. No standard exists; the lock's commit and content hash are the pin.
- Editing the catalogue from an operator consumer. It is a file under review; a consumer shows it
  and links to it.
- Running a model to choose skills (ADR-0083).

## Impact

- Code: new `pkg/skillcatalogue` (lock reader, fetcher, store); `pkg/config`
  (Team `skills`); `pkg/httpapi` (run and operator routes); `pkg/worker`
  (fetch, install, record); `pkg/harness/skills` (sources, `paths`, label);
  `cmd/ploeg-okf` (`skills explain`, `skills check`); a migration for Run skills
  and the store.
- Contract: run API gains skill routes; `taskspec.v1` `skills` (ADR-0083) gains
  `version` and `selected`; additive.
- Chart: `config.teams[].skills`; the worker's network needs no change.
- Downstream, outside this repository: runner images drop baked skills; the
  skill repository adds routing examples to its CI; the operator consumer shows Run and Team
  skills.
