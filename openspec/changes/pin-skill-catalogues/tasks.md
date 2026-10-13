Lands after `select-skills-per-work-item`, which provides selection.

## 1. Catalogue and store

- [ ] 1.1 `pkg/skillcatalogue`: parse the `apm.yml` / `apm.lock.yaml` subset, refuse anything else with a reason; contract test on a sample lock per accepted `lockfile_version`
- [ ] 1.2 Team `skills` in `pkg/config` (`catalogue`, `always`, `sources`) and `config.teams[].skills` in the chart, with `values.schema.json`
- [ ] 1.3 Fetch at `resolved_commit` through the forge provider, hash as the lock does, refuse a mismatch
- [ ] 1.4 Content-addressed store in Postgres with size limits and 30-day collection of unreferenced digests; migration
- [ ] 1.5 Tests: allowlist, mismatch, fetch-once across Teams, unsupported lock

## 2. Run routes and worker

- [ ] 2.1 `GET /runs/{token}/skills`, `GET /runs/{token}/skills/{digest}`, `POST /runs/{token}/skills/repository`; add them to the run API contract
- [ ] 2.2 Worker fetches the admitted set, reads the repository lock at the base commit, installs into `.agents/skills` and the harness directory, up to `PLOEG_SKILL_INSTALL_LIMIT`
- [ ] 2.3 `SkillDirectories`: `.qwen/skills` for the Qwen Code ACP profile; `claude-environment` runner fetches with the Run token
- [ ] 2.4 Precedence and shadowing; `skills.always` outside `PLOEG_SKILL_LIMIT`
- [ ] 2.5 Selection inputs: top-level `paths`, `skill:<name>` labels
- [ ] 2.6 Tests: install per harness, refused entry keeps the Run going, repository cannot replace a Team skill

## 3. Record and read

- [ ] 3.1 `run_skills` migration and writes at claim; `TaskSpec.skills` gains `version` and `selected`
- [ ] 3.2 `opened` from transcript tool-call paths for `acp`; transcribe tool calls for `openhands` and `claude-code` adapters
- [ ] 3.3 Operator routes: a Run's skills; a Team's catalogue with 30-day selected and opened counts and refused entries
- [ ] 3.4 Replace `select-skills-per-work-item` task 3.2's paid probe with the opened rate over real Runs

## 4. Author tools and docs

- [ ] 4.1 `ploeg-okf skills explain` with near misses; `ploeg-okf skills check` (specification rules plus Ploeg hints)
- [ ] 4.2 How-to: write a skill Ploeg selects; how-to: give a Team shared skills; reference rows in `docs/reference/configuration.md`
- [ ] 4.3 Run the gate set from `.forgejo/workflows/on_pull_request.yml` and `openspec validate pin-skill-catalogues --strict`

## 5. Outside this repository (tracked as their own tickets)

- [ ] 5.1 Skill repository: routing examples per skill checked by `ploeg-okf skills explain` in CI
- [ ] 5.2 Operator consumer: "Skills given" on a Run, a Team skills page
- [ ] 5.3 Runner images: stop baking skills, remove start-up skill code, once a Team's catalogue covers them
- [ ] 5.4 Renovate: enable the `apm` manager where catalogues live and confirm it can regenerate the lock
