# Tasks — open-change-requests-on-gitlab

## 1. The decision

- [x] 1.1 `docs/adrs/0023-the-forge-dialect-travels-on-the-work-item.md` plus its Records row and review-calendar entry; `go test ./internal/ledger/` passes
- [ ] 1.2 A human ratifies ADR-0023 (status proposed → accepted) before this merges. The code was written before the record — see `adr.md` Notes — so this is the step that makes it a decision rather than a description

## 2. The dialect on the contract

- [x] 2.1 `harness.RepoRef.Forge`, optional, with `Dialect()` resolving empty → `forgejo` so no caller compares the raw field
- [x] 2.2 `repo.forge` in `docs/contracts/taskspec.v1.schema.json`, enum-constrained, additive per `docs/contracts/README.md`
- [x] 2.3 `fullTaskSpec` in `contract_test.go` carries the field — added FIRST and observed to fail with `additional properties 'forge' not allowed`, then the schema edited
- [x] 2.4 `RepoRef.ProjectPath()` — owner and name joined, never split. A GitLab subgroup project is three segments and the name carries a slash

## 3. The two places that name a forge

- [x] 3.1 `findOpenChangeRequest` dispatches on the dialect; Forgejo and GitLab decode into one `changeRequest` shape
- [x] 3.2 GitLab filters `source_branch` server-side, so a repository with more than one page of open requests cannot hide the Run's own
- [x] 3.3 The GitLab project is addressed by URL-encoded full path (`%2F`, not path segments)
- [x] 3.4 An unknown dialect returns an error naming it — no fallback
- [x] 3.5 `changeRequestNoun` and `openChangeRequestInstruction` switch on the same value, so vocabulary and endpoint cannot drift
- [x] 3.6 `resolveTarget` takes the dialect from the Work Item, falling back to the configured default
- [x] 3.7 Tests: both forges found; empty dialect is forgejo; subgroup path encoded; wrong base rejected on both; unknown dialect named in the error; HTTP failure surfaced
- [x] 3.8 Tests: GitLab writer told to open a merge request at the GitLab endpoint and never a Forgejo one; GitLab reader given merge-request vocabulary; already-open branch names the right noun; **the Forgejo contract byte-for-byte unchanged**

## 4. The chart

- [x] 4.1 `executor.forge` selects one active forge; `executor.gitlab` configures it (url, tokenSecret, readTokenSecret, webhookSecret)
- [x] 4.2 `ploeg.forge` helper resolves the active block; no template reads `.forgejo` or `.gitlab` directly
- [x] 4.3 The helper's `index`-with-default also removes the `forgejo: null` nil pointer that would have fired the moment `executor.enabled` went true
- [x] 4.4 ploegd renders `PLOEG_GITLAB_URL` / `_TOKEN` / `_SECRET` when a url is set, and `PLOEG_TARGET_FORGE` when gitlab is active
- [x] 4.5 One name per concept: `FORGE_URL` for the base URL and `PLOEG_TARGET_FORGE` for the dialect, the latter shared with ploegd rather than a second spelling of the same thing
- [x] 4.6 `values.schema.json` gains `forge` (enum) and `gitlab`; both forge blocks become nullable
- [x] 4.7 A fourth fixture and golden, `executor-gitlab`, rendering with `forgejo: null` on purpose; wired into `scripts/helm-golden.sh` and the PR workflow
- [x] 4.8 The golden pins ADR-0013 tier 1 on GitLab: readers draw `agent-reader-token`, the writer draws `agent-builder-token`

## 5. The downstream consumer

Not in this repository. The deployment this change exists for tracks its own
wiring, which is what proves the loop closes.

## 6. Gates and closure

- [x] 6.1 `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`, `helm lint`, all four `helm template` renderings, `./scripts/helm-golden.sh check` — output in the PR body
- [x] 6.2 `go test ./internal/ledger/` (docs/adrs changed)
- [x] 6.3 `openspec validate --all`
- [x] 6.4 `docs/architecture.md` §9: the worker speaks two dialects; tier 2 is Forgejo-only and why
- [x] 6.5 Goldens regenerated with the helm CI pins (v4.2.3). 4.2.4 disagrees about the blank line before a document separator — `scripts/helm-golden.sh` says so on failure, and it cost a diagnosis here
