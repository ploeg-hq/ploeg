## 1. Selection

- [ ] 1.1 Export the term matching in `pkg/knowledge` (`Terms`, field scoring) so skills can use it
- [ ] 1.2 Read skills from the clone's `.agents/skills/` at the base commit and from `PLOEG_SKILL_DIRS`, parsing `metadata.ploeg-tags` and `metadata.ploeg-paths`
- [ ] 1.3 `skills.Select(sources, query, changedPaths, limit)`: role skills always, path matches first, then scored skills, ties by name, at most `PLOEG_SKILL_LIMIT`
- [ ] 1.4 Tests: determinism, limit, role skills kept, path match first, context-bundle `SKILL.md` never selected

## 2. Worker and contract

- [ ] 2.1 Gather the query: title, description, labels, context file names; changed paths for a reader and for Round 2 onwards
- [ ] 2.2 Install configured skills like role skills; leave repository skills in the clone
- [ ] 2.3 `runSupportSection`: name each selected skill, its path and reason, and tell the agent to read it first
- [ ] 2.4 Add optional `skills` to `docs/contracts/taskspec.v1.schema.json` and the Go type, with a `contract_test.go` assertion
- [ ] 2.5 Document `PLOEG_SKILL_DIRS` and `PLOEG_SKILL_LIMIT` in `docs/reference/configuration.md`

## 3. Tooling and proof

- [ ] 3.1 `ploeg-okf skills -title ... <repo>` prints the selection for a Work Item, like `ploeg-okf pack`
- [ ] 3.2 Probe on a cheap model: a repository skill named in the prompt is opened in at least 8 of 10 Runs, against today's baseline
- [ ] 3.3 Run the gate set from `.forgejo/workflows/on_pull_request.yml` and paste the output in the pull request
- [ ] 3.4 `go test ./internal/ledger/` passes
