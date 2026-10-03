# Changelog

## 0.1.0 — independent baseline

New repository and module: `github.com/ploeg-hq/ploeg`. Source extracted from Unfold as documented in [PROVENANCE.md](PROVENANCE.md); standalone GitHub CI and independent releases. This version is experimental.

## Historical release record

The entries below belong to the previous Ploeg module and repositories, not this new release line. They are retained for provenance.

## [0.3.0-rc.7](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.3.0-rc.6...v0.3.0-rc.7) (2026-09-11)

### Fixed

* **ci:** stop the licence gate depending on a network tool install ([94c7c8e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/94c7c8e2dc07037ee2fd38a69343426e40bd680d))

### Docs

* **release:** drop the 1.x candidate from the record ([6dfb43f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6dfb43f6f1367645a56de6eb1bc6bc8e7503f26f))
* **release:** point test deployments at the corrected publisher ([166e85c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/166e85caa72de0a5941d13fc8f24e2b608f9f91a))

### Internal

* **licence:** name the copyright holder and hold Apache-2.0 in CI ([5681fb4](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5681fb4577318a8dfb5bf094ce6ba3075cd50d07))

## [0.3.0-rc.6](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.3.0-rc.5...v0.3.0-rc.6) (2026-09-11)

### Fixed

* **release:** use available outputs to enable Forgejo publishers ([fff3d3d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fff3d3daec3ef06626751b48622db42437f180bc))

## [0.3.0-rc.5](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.3.0-rc.4...v0.3.0-rc.5) (2026-09-11)

### ⚠ BREAKING CHANGES

* **control-plane:** managed worker authentication is now the default. Configure
  controller signing/bootstrap Secret references and LLM policies, remove
  administrative worker credentials, and roll controller and workers together.
  Compatibility requires explicit legacy authentication and static-compatibility
  inference. See docs/ops/managed-workers.md for the existing-deployment upgrade.

### Added

* **control-plane:** unify managed execution and verified delivery ([9028cf0](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9028cf0718c65acbfc4f8bfd1be71f32417221ce))

### Fixed

* **llm:** scope gateway keys and preserve accounting identities ([762c27d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/762c27d5a170dbefdcacb1e3a7f50f60838acedb))
* **release:** keep experimental Ploeg releases on zero major ([46056cf](https://forgejo.webgrip.dev/webgrip/ploeg/commit/46056cf7b91b2ea8c75e5bbf9b33ac56e3a24f0d))
* **tracker:** retain authoritative scope and expose fresh execution state ([e00209e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e00209ed147d131af260bd99524f3f56d3dab499))

### Docs

* **agents:** adopt the estate no-comments rule ([259a817](https://forgejo.webgrip.dev/webgrip/ploeg/commit/259a817a3f058346b3d96def361610dadc9f3bc5))
* **architecture:** define unified execution and delivery authority ([1a8b00c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1a8b00c2160930ca56638ccdb595e6c194efa532))
* **domain:** ground work in tickets and useful research ([cf02ac7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/cf02ac7dbc2f2e8c562ab69cfae3114d1f994017))

## [0.3.0-rc.4](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.3.0-rc.3...v0.3.0-rc.4) (2026-09-02)

### Added

* **ingest:** a container's pinned team decides, as the config always claimed ([cdb8d19](https://forgejo.webgrip.dev/webgrip/ploeg/commit/cdb8d195f9f3b00d836509209f8ce09e2d00382a))

## [0.3.0-rc.3](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.3.0-rc.2...v0.3.0-rc.3) (2026-09-02)

### Added

* **config,chart:** route ClickUp Lists through the config file ([e0a4823](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e0a48230180283a7e8c2c66d46d1d8c33b237ebe))

## [0.3.0-rc.2](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.3.0-rc.1...v0.3.0-rc.2) (2026-09-02)

### Fixed

* **chart:** the worker node selector is a value, not a constant ([53f8ad6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/53f8ad69692863d2af24ff4dfd60b2c263af541f))

## [0.3.0-rc.1](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.1-rc.1...v0.3.0-rc.1) (2026-09-02)

### Added

* **worker:** open change requests on GitLab, not only Forgejo ([a2a5547](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a2a5547ce49fdf531f5910a225c7c0ec2d9da684))

### Fixed

* **harness:** taskspec.v1 carries the forge dialect ([08826e4](https://forgejo.webgrip.dev/webgrip/ploeg/commit/08826e4dcb8e01916bb13ebb5d7dbdd0dbbd4b40))

### Changed

* **worker:** name things instead of explaining them ([79407f1](https://forgejo.webgrip.dev/webgrip/ploeg/commit/79407f1ac1c475bf9102d7e482511c5d28889a42))

### Docs

* **agents:** add CLAUDE.md as a symlink to AGENTS.md ([7996ce8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7996ce86cd365147faa80e0af0ef95688943be27))
* **changelog:** backfill v0.2.1-rc.1 — cut on the old toolchain after the first backfill ([fd8ecb8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fd8ecb8f316f2addc8d2e706ea8ce02d2cb62435))
* onboarding field report — five manual acts, all failing silently ([4f2b008](https://forgejo.webgrip.dev/webgrip/ploeg/commit/4f2b0084ad994e435ad867feef0caf8e2fa4c7ae))
* **openspec:** record the forge dialect decision as ADR-0023 ([8770c29](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8770c297766ffadeeb428c27131f9a26e969b401))
* **skills:** team-silver gates run in CI — the dispatched harness is daemonless ([e15c9d1](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e15c9d1842e5cbdfff7de9d122686f5cad2656e6))
* stop prescribing a worktree per change ([70bd3fd](https://forgejo.webgrip.dev/webgrip/ploeg/commit/70bd3fdd4e73310d4ae0c7ff9a29f156ba55ec11))

### CI

* **release:** back to toolchain image 0.1.2 — 0.3.1 fails the release job ([3026c31](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3026c31b8282a95b0881129da5e4ef3a3974857a))
* **release:** cut releases in toolchain image 0.3.1 — notes render, dep bumps release ([594593e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/594593e1943eb4484ffcc15a8e190dd6bc0c8d31)), references [#131](https://forgejo.webgrip.dev/webgrip/ploeg/issues/131)
* **release:** rerun the release job on toolchain image 0.3.1 ([76823cf](https://forgejo.webgrip.dev/webgrip/ploeg/commit/76823cffc1a869691889e44ff9b07f9d12053364))
* **release:** toolchain image 0.3.2 — the alpine base now ships bash ([8b1d2cf](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8b1d2cfad04b7283d7ea8c5ab8b72018195c700c)), references [#132](https://forgejo.webgrip.dev/webgrip/ploeg/issues/132)
* **release:** toolchain image 0.3.3 — the publish path now resolves got 11 ([d1f9c73](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d1f9c730cf347475bfb87115fa20be26cff2cf28))

## [0.2.1-rc.1](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0...v0.2.1-rc.1) (2026-08-28)

### Fixed

* **brand:** clip the Klei under the steel so it can't bleed through the edges ([c7414e5](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c7414e5096a8beafac67f06d2b37693eb3b31786))

### Docs

* **agents:** stop telling agents to docker-pull the gate toolchain ([a75e7d2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a75e7d296db702bcc2ca0935847f2a4a9eb07fe0))
* **changelog:** backfill 28 empty entries — the notes toolchain dropped every commit line ([29ea7cf](https://forgejo.webgrip.dev/webgrip/ploeg/commit/29ea7cf941deb6ebdbc1096f75a0af263c7fabb1)), references [#10](https://forgejo.webgrip.dev/webgrip/ploeg/issues/10) [#57](https://forgejo.webgrip.dev/webgrip/ploeg/issues/57) [#131](https://forgejo.webgrip.dev/webgrip/ploeg/issues/131)

## [0.2.0](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0...v0.2.0) (2026-08-27)

### Added

* **api:** role-scoped claim, findings on the outcome, role-filtered depth ([3cbfd63](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3cbfd6341e4ebb1096cbea532a88113afce45dfa))
* bind the work target to the work item, not to the team ([d2085b3](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d2085b39fe68feeaa12f08725b231547d52fac4c)), references [#97](https://forgejo.webgrip.dev/webgrip/ploeg/issues/97) [97/#103](https://forgejo.webgrip.dev/webgrip/ploeg/issues/103) [#104-108](https://forgejo.webgrip.dev/webgrip/ploeg/issues/104-108)
* **chart:** one workload per (team, Role), and a waiver keyed to the hazard ([e6b3f92](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e6b3f92fbcbc1f1b9e233d959c70ae2eb3a2b291))
* **chart:** worker ServiceAccount and per-Role resources ([2a9b184](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2a9b1848103e875127c13c237c9832feec33a4d8))
* **config:** routing and roster as a file, and push rights minted per Run ([45b343f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/45b343f6eaecf624e479ad32329f8b599b8a4d15)), references [#26](https://forgejo.webgrip.dev/webgrip/ploeg/issues/26)
* **deps:** update docker.io/golang docker tag ( 1.24 ➔ 1.26 ) ([739bd80](https://forgejo.webgrip.dev/webgrip/ploeg/commit/739bd806b1b0ef2f4d7e761a07a1c34d5f0358fd))
* **deps:** Update postgres Docker tag ( 17 ➔ 18 ) ([6fe8454](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6fe84548e06d44ab22eb444a009247dcad621e85))
* **dispatch:** every queued item gets a Shift, behind a kill switch ([988c496](https://forgejo.webgrip.dev/webgrip/ploeg/commit/988c4963551f01f30bfd8bd335230a0fbefbb093))
* **harness:** ACP driver, client half, and the coder/acp-go-sdk dependency ([6a06793](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6a067933d0f3a1227b320d8a6c53a229b21aca24))
* **harness:** ACP event and stop-reason semantics (no SDK, no process) ([2bcd9ce](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2bcd9ce6092712f703e06d4a25d0d44dd25a18eb)), references [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64)
* **harness:** ACP permission policy for unattended runs ([a8e705a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a8e705ab332d032c1711c12b1dd2c86166f147f3))
* **harness:** ACP subprocess layer — process groups, stdout demux, async stdin ([a238a66](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a238a668bb1703e2077cf1aec23f11c6fa80a2ef))
* **httpapi:** forge webhook ingest — verified, deduplicated, audited ([7befbce](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7befbceecbf9a7ecda46faf4e60ab2a7f2752a36)), references [#2](https://forgejo.webgrip.dev/webgrip/ploeg/issues/2) [#3](https://forgejo.webgrip.dev/webgrip/ploeg/issues/3) [#107](https://forgejo.webgrip.dev/webgrip/ploeg/issues/107) [#9](https://forgejo.webgrip.dev/webgrip/ploeg/issues/9)
* **plan:** team plan config, parsed at boot, rendered dark from the chart ([bff5540](https://forgejo.webgrip.dev/webgrip/ploeg/commit/bff5540227b4aa3310850cf3e6a285d0e1ab6360))
* pluggable harness, agent image, LLM broker, and executor seams ([1789c7a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1789c7a74ec7470c127178ec07cb572471243ce6)), references [66/#69](https://forgejo.webgrip.dev/webgrip/ploeg/issues/69)
* **provider:** findings reach the pull request, and a person is asked to merge ([2e9f6ed](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2e9f6edfd503115c5d003c40852d0ec940376d94))
* **provider:** GitLab forge and ClickUp tracker providers ([e96cce7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e96cce76c8c68e3eff660434b52530186b23d9e0))
* run forensics survive pod/job cleanup — node+pod identity in logs+checkpoints, failure-reason taxonomy, VIK-586 fix ([72db36b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/72db36bcbc964e18461d21a9b2e4a2523eca8a0a))
* **shiftengine:** open, advance, close and park Shifts ([9e538d0](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9e538d077b05aa070fd7936db9feb3c1e0ae52ee))
* **shiftengine:** verdict-driven fix rounds, bounded by pool then cap ([6beaaa1](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6beaaa1739d26a89bbe1419c8634f43b5e10917d))
* **store:** settlement, per-Run liveness, and the round-completion signal ([9d2d39b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9d2d39be359e1129ce483d9e05773e3d0fa02f51))
* **store:** shift lifecycle completions and shift-run plumbing fixes ([c3db764](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c3db7641b74829b73b8c8b990cf5e9745104bddb))
* **store:** Shifts — rounds, reader/writer runs, and pooled budgets ([450a8ae](https://forgejo.webgrip.dev/webgrip/ploeg/commit/450a8ae25d0ce3f0a3961fba65510c35f9e96e98))
* **worker:** role-aware runs — claim, prompt, budget, findings drop box ([8e170fa](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8e170fac79771df29880d28de7e7a3e7aab7494f)), references [#9](https://forgejo.webgrip.dev/webgrip/ploeg/issues/9)
* **worker:** select the ACP harness from the registry, env and chart ([a33646d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a33646d4dbcb14d9722e67a6fb85f83f29b74d24))

### Fixed

* **adrs:** revert the ADR-0017 index edit — upstream had already resolved it ([444c313](https://forgejo.webgrip.dev/webgrip/ploeg/commit/444c313f65aebc344bbc897c55f1eade83229b55))
* apply PR review round 2 — ExpectsLLM, VIK-586 heuristic, gofmt, FailureReason naming ([84c8ced](https://forgejo.webgrip.dev/webgrip/ploeg/commit/84c8ceddd053e8eac5a72ab4754d274284d499e3))
* assignment webhooks revive finished work items ([8b15d2e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8b15d2e5fae575f6dbea93f59026521f53989ca9))
* **chart:** three defects found by running rc.13 in production ([1cb5fdd](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1cb5fddafc72a2b4127feb1970855f5fce9f26bf))
* **ci:** adopt the shared forgejo-distribute reusable for the Forgejo mirror ([9047b3a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9047b3a520c94c4ac3df7e198776f07458e14d06))
* **ci:** assert image labels on parsed JSON, not on rendered text ([dfdc714](https://forgejo.webgrip.dev/webgrip/ploeg/commit/dfdc714af816b2240c00badbec8b5e2373f20066))
* **ci:** bypass the dead Docker Hub proxy so a release can distribute again ([615948d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/615948dabd66f180cfffd41b2e4bc8049f4ccfa0))
* **ci:** correct the stale single-reusable-chain comment; cut v0.1.0-rc.11 ([09eeea0](https://forgejo.webgrip.dev/webgrip/ploeg/commit/09eeea0677794acb6ed8dcbace2fd72829538276))
* **ci:** finish the proxy bypass — syft scanner and buildkit come direct too ([646a6e7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/646a6e741cae386c5a271a6d76f654d0fa02e68f))
* **ci:** mirror image and chart to the Forgejo registry and link them to the repo ([621e77a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/621e77ae035ec64368dd3c2becbf7fee9bb63351))
* **ci:** pin semantic-release to v1.2.0 now that PR [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) is released ([1ca543a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1ca543af2c9161c94a701b049538a89b585abb31))
* **ci:** pin webgrip/workflows to v1.0.0 instead of [@main](https://forgejo.webgrip.dev/main) ([2c5fbec](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2c5fbecd65fca3b5e94c39bdd0b11666929066eb)), references [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40)
* **ci:** prove the container release path end to end, on a probed toolchain ([673896a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/673896af6a85024f0bf71560a538b6939978f190))
* **ci:** re-pin the semrel action to a commit the server can still resolve ([a5c8026](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a5c80261266e91152fdce9053088db51a99de84b))
* **ci:** release and publish as the webgrip-ci bot, not the per-job token ([647a49c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/647a49cd2a75421d66defd83eb52482e3d53b000))
* **ci:** reopen the GitHub track — mirror, Releases and GHCR via github-distribute ([0b59582](https://forgejo.webgrip.dev/webgrip/ploeg/commit/0b595828044f64e4ddff2999efacccdd593fcbf4)), references [#48](https://forgejo.webgrip.dev/webgrip/ploeg/issues/48)
* **ci:** skip the Harbor build when the version is already published ([71e6436](https://forgejo.webgrip.dev/webgrip/ploeg/commit/71e64367b26083318a0185a5580f27fc000f407d))
* **ci:** substitute the chart version out of the helm goldens ([bc24da2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/bc24da27c5af757b960a92f2b6b8301fb6c75161))
* **config:** per-team routing on one project is valid, not a duplicate ([0b5a05b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/0b5a05b9660bbc6bfe5198d31bed8b1c01ec55d3))
* **config:** reject an assignee shared by two teams ([3ec972f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3ec972f794207ae8e768b760f934b3fd5d20bb31))
* **deps:** clear the nine CVEs Harbor flags on the ploegd image ([6670482](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6670482149c3b385a73048c82d1d339125d00147))
* **deps:** update harbor.webgrip.dev/webgrip/agent-runner docker tag ( 1.0.1 ➔ 1.0.2 ) ([2fa0985](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2fa0985ce2ae60f66daefc7c23a6985cddab4e75))
* Guaranteed QoS for every factory pod — out of the OOMController's kill zone ([9e708ee](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9e708ee7b4a6a55db0047c98d53ab6fc19aaae30))
* **harness,worker:** a reading Run's review must survive every harness ([fc0293f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fc0293f61662b270c8543c688b54a7a8dad9047f))
* **harness:** flush the agent's stderr before building an ACP failure reason ([de03342](https://forgejo.webgrip.dev/webgrip/ploeg/commit/de03342713aa73549939c43864b8f66682837198))
* **helm:** default worker CPU to 1 core (single-threaded cold import ([540c3c2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/540c3c2aa2b248c2799dfb7259f1bca1db90aa5f))
* **httpapi:** close the failure taxonomy at the API boundary ([f72f9ac](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f72f9ac741fb59413492c10caac1a78f81ae1641))
* infra failures don't burn attempt budget (backoff + infra_failures) ([a9615a8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a9615a87c4d834e8d2c5cbe52f0631a102c7cc07))
* ploeg-worker owns the per-run LiteLLM key lifecycle (mint + always-revoke) ([aa5fc39](https://forgejo.webgrip.dev/webgrip/ploeg/commit/aa5fc397c463d95a6cf18944594ee22cdfd0e585))
* **ploegd:** register a forge under the ID its Work Target carries ([9ccb035](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9ccb035124471f4fa841517c4272c38ca0888b8b))
* **ploegd:** safe Alias() helper, sweeper key revoke, boot orphan sweep ([1c39d96](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1c39d96824d3a51a31398ad470d6edb950e31e0b))
* **release:** annotate the index and mirror cosign's accessories to GHCR ([b11e343](https://forgejo.webgrip.dev/webgrip/ploeg/commit/b11e3434b40f89d6cb30b6663a8f02799319926b)), references [#53](https://forgejo.webgrip.dev/webgrip/ploeg/issues/53)
* **release:** drop the yq appVersion prepareCmd — the shared config bumps both keys ([565cb1f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/565cb1fcf926c522bfdaea33bb3ea01563238857))
* **release:** link the image and chart to the repo on GHCR ([3c833c2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3c833c2fa755724693b5430c5e97c95548c422bb))
* **release:** reject zero-time release timestamps, not just Go's spelling ([9a1ba9f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9a1ba9f509178daf2abbc2e1760b23a347e44bc4))
* **release:** sign the Forgejo mirror too ([f8099c9](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f8099c9fef8aaec3763545e7f054a4cf21827d41))
* **shiftengine,store:** a failed writing Run re-opens its Round ([b0ad282](https://forgejo.webgrip.dev/webgrip/ploeg/commit/b0ad28266faa4ebdc501d547204170034c9252d6)), references [#35](https://forgejo.webgrip.dev/webgrip/ploeg/issues/35)
* **shiftengine,worker,litellm:** close the loop the reviews were falling out of ([eb6c8dc](https://forgejo.webgrip.dev/webgrip/ploeg/commit/eb6c8dc91575a5414ceaa3808c260b9447eb3059)), references [erfbeeld#9](https://forgejo.webgrip.dev/erfbeeld/issues/9)
* **shiftengine:** a successful review must not read as a stoppage ([848c911](https://forgejo.webgrip.dev/webgrip/ploeg/commit/848c911f07fbd8563f0e4eb379f86ba1edc26cd1))
* **shiftengine:** write back to the tracker on every terminal settle ([93ade62](https://forgejo.webgrip.dev/webgrip/ploeg/commit/93ade6266a5204199b773ae05cc7855e9db140a0)), references [#30](https://forgejo.webgrip.dev/webgrip/ploeg/issues/30)
* worker owns the per-run LiteLLM key lifecycle (mint + always-revoke) ([9bcc0f8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9bcc0f84f1bdad3e68a4513eeba6ccf77be95fa1))
* worker targets a configurable base branch end to end ([6ffdfe6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6ffdfe636cf19f2c4be7ea51eedbf2fb6dbe295a)), references [#6](https://forgejo.webgrip.dev/webgrip/ploeg/issues/6)
* **worker,httpapi:** tell the truth about the forge credential, log routing ([51817e2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/51817e2a6d9af32d5e836057f7b6ed4ec1410b78))
* **worker,shiftengine:** a killed run reports its own death, and does not spend the agent's budget ([771a18b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/771a18b6c2344e414985422c68f2610530c589e8))
* **worker:** a reading Round may run before any branch exists ([f0cd7bd](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f0cd7bde77058aca2dccad37824e164a20212473))
* **worker:** give a reader the work, and take away the credential ([e2d4f25](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e2d4f2571670eb00c31331a472a82c3c29045f1b))
* **worker:** stop a failed run inheriting the previous run's PR ([5195737](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5195737f67bead50826cacad2465fa6cbcac4891))

### Changed

* **ci:** bring the composite pins onto the plain-text diagnostics ([5ecd134](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5ecd1345bed92f5f691edfc9f78adffe058f2cae))
* **ci:** drop the last GitHub-only annotation command ([7d883b2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7d883b2eac4e3b1644bcd7d02fdb26c7b9f3add5))
* **harnesstest:** make the conformance kernel adapter-shaped ([dc0dcb2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/dc0dcb261adb969f0976dfe61ea82ad33c22714e)), references [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64)

### Docs

* **adr:** consolidate docs/adr into docs/adrs — one gated ledger ([28b82b7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/28b82b767e00a0cfeda9fea925bdd733231abd32)), references [#97](https://forgejo.webgrip.dev/webgrip/ploeg/issues/97)
* **adr:** record why published artifacts name the mirror as their source ([11dee3e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/11dee3e713027fba03736511a796a3bd241327bb))
* **adrs:** ADR-0018 — the drop box is every harness's return path ([f50edaa](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f50edaab66ba7a694adb9ef2b8bf09f2aaa366aa))
* **adr:** Shift owns the item, Lease owns the branch (0010-0012) ([d601888](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d601888a529f4d67151e1800b3fd93849aaf892a))
* **adrs:** migrate design.md §8/§9 into an enforced MADR 4.0 ledger ([f5f9596](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f5f959686e2e55b09a309305ca56f8a23ec35963))
* **adr:** the Lease becomes a capability, not a note (0013) ([14fcac6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/14fcac62a54dd71d2a645dca4e096ec9bb5d9781)), references [forgejo#8837](https://forgejo.webgrip.dev/forgejo/issues/8837)
* **agents:** correct migrations path to pkg/store/migrations ([7fc9446](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7fc9446c806aea292df72df2c6327ebb4635582c))
* **agents:** record the multi-session staging discipline ([6b7ccf7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6b7ccf7d4477d59490e2cd85dee50725a5894f56))
* archive run-multi-agent-shifts and correct the divergence list ([8bf5210](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8bf5210be06dd2ac481d4fb6ec9a467053ee659a))
* **brand:** a visual identity for Ploeg, and terms for its mark ([98d6a3a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/98d6a3a25ca7449d003c30d38f9f79cef7121327)), references [#E4572E](https://forgejo.webgrip.dev/webgrip/ploeg/issues/E4572E)
* **brand:** transparent PNG exports of every logo variant ([511e0db](https://forgejo.webgrip.dev/webgrip/ploeg/commit/511e0db728c9d5177442de617a6c31a84d6a2bee))
* **ci:** name the helm-version trap in the golden check's own advice ([3ec294c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3ec294cc99bfc74c92a1d35d49f3c5fdabe91d89))
* cite model.yaml entities by name, not by line number ([db2b4e5](https://forgejo.webgrip.dev/webgrip/ploeg/commit/db2b4e58cf9701e5e281c47339d2749f7ebba344))
* close out the ACP work in the backlog, design §5 and the divergence list ([4cfd890](https://forgejo.webgrip.dev/webgrip/ploeg/commit/4cfd8901f1e72cbe625329eb266f39992b9ca3ae)), references [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64) [#63](https://forgejo.webgrip.dev/webgrip/ploeg/issues/63) [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64) [#44](https://forgejo.webgrip.dev/webgrip/ploeg/issues/44) [#69](https://forgejo.webgrip.dev/webgrip/ploeg/issues/69)
* current-state architecture of the dark factory (mermaid: context, run sequence, states, key layers) ([3e868ec](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3e868ecd3b7e9afca1efb23c58b412a3aff7f8a9))
* **domain:** model the Work Target, Forge, Scope and Routing Rule axes ([8227da7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8227da76ee23b628bd9fc51b050dffca56011718))
* **domain:** regenerate the domain views for Shift and Round ([0c8f417](https://forgejo.webgrip.dev/webgrip/ploeg/commit/0c8f4178b6829e12f39790612c35ef986b1b0706))
* make docs/adrs the only ledger, and gate it in go test ([fcbd0b7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fcbd0b7b6325e7751359ecc779fe7ae34ca9ecd5))
* **openspec:** adopt the spec-driven-with-adr workflow ([18cb967](https://forgejo.webgrip.dev/webgrip/ploeg/commit/18cb9678be152f285e4cb26577a3297f327f8e4d))
* **openspec:** design, adr manifest and tasks for run-multi-agent-shifts ([b9a197c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/b9a197c90054fb45e69fa99862a9ab2d80fadae9))
* **openspec:** propose close-the-review-loop, and ADR-0017 behind it ([f9b157c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f9b157c2d200c82c74476f05e8dd8999e7dd8219)), references [#107](https://forgejo.webgrip.dev/webgrip/ploeg/issues/107)
* **openspec:** propose run-multi-agent-shifts ([70abf24](https://forgejo.webgrip.dev/webgrip/ploeg/commit/70abf24df514c3df5aea1a7c4014a426aca4a677))
* reconcile ADR-0010/0012 with the implementation; architecture §10 with diagrams ([e589ed6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e589ed65dd8fbb813ba38e8dc2eb6f03b7e1de64))
* record 2026-07-27 AHP sweep verdict — session-sync layer above ploeg, ACP stays the harness seam ([d595dff](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d595dff2f7969511c5673ef13a7c4928c56d88e5))
* record 2026-07-28 A2A sweep — wrong layer for the factory, north-facade watchlisted ([139d497](https://forgejo.webgrip.dev/webgrip/ploeg/commit/139d497560b6924a96545f9495465eb011c352b2)), references [#102](https://forgejo.webgrip.dev/webgrip/ploeg/issues/102) [#31](https://forgejo.webgrip.dev/webgrip/ploeg/issues/31)
* **research:** correct the rc.15 claim — it published; the release job is what broke ([3a1d248](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3a1d248bc9fc64d19da7755f4c05345b28122fee))
* **research:** how many trials, computed rather than asserted ([06d9964](https://forgejo.webgrip.dev/webgrip/ploeg/commit/06d996459ee75b904b563aae5a2b23768ba0bcdf))
* **research:** probe results — the gateway keeps its aliases, and rc.14 keeps no cost ([11fcead](https://forgejo.webgrip.dev/webgrip/ploeg/commit/11fceadd96b767e99ec3532c7cc83ed75e51327c))
* **research:** survey and design for benchmarking the whole loop ([d565449](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d565449519cbccc83b026293597a57863891256d))
* rewrite AGENTS.md as a router, land research and ops knowledge in-repo ([3fd1744](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3fd1744cca83d71cd0e6ac69d8cf46436468decb)), references [#103](https://forgejo.webgrip.dev/webgrip/ploeg/issues/103)
* update README status — executors ship in the chart ([078efca](https://forgejo.webgrip.dev/webgrip/ploeg/commit/078efcad020aa30e0ffae145abff6b2e261276ef))

### Tests

* **acp:** a zombie grandchild is not a surviving one ([7bd42ed](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7bd42ed0c5600bcd8dd076bd8b25aca13765f659))
* **litellm:** strict fake emits [] not null for empty lists; gofmt ([f2ca405](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f2ca4051dd4ce0d9ab48b1ab15afe17b43e3d759))
* **store:** unused var + gofmt — reviewer gate pass ([a544a41](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a544a41b2db88eea5abb52e38b6f63e3f89fd9f5))
* **worker:** pin the spend-settling loop in both directions ([cdcb2b1](https://forgejo.webgrip.dev/webgrip/ploeg/commit/cdcb2b11d59a09765ccd64e9904f61ad9d95520a))

### CI

* **actions:** Pin dependencies ([e26493a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e26493a946cfe21c60243b124005c8bdbaa08245))
* **actions:** Update dependency helm ( v3.18.4 ➔ v4.2.3 ) ([5591eab](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5591eab6521ec8623b8a27a974c6e18deb576efc))
* **actions:** Update https://github.com/actions/setup-go action ( v6.5.0 ➔ v7.0.0 ) ([5ce8533](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5ce85333de80d9a666742f471bd69bbe7089e768))
* adopt @webgrip/semantic-release-config ([7b5f87c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7b5f87ce01d403d619e51c67108eca1b7941ec3c))
* drop the manual release dispatch — bot-cut releases fire the release event natively ([2faef42](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2faef424d906a803d1ecb190a35d38dcf4ba7dff))
* **release:** build the image once — Forgejo distribute mirrors Harbor by digest ([04ff380](https://forgejo.webgrip.dev/webgrip/ploeg/commit/04ff380edb5aca206b0fbc8a62d8c349fc254784))
* **release:** bump cosign-sign-attest to v1.11.2 ([eb2a8db](https://forgejo.webgrip.dev/webgrip/ploeg/commit/eb2a8db2fdac0faa4e751e8282b9e80a8531e24b))
* **release:** bump github-distribute to v1.11.1 ([3adcb19](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3adcb196274bb5f5e22774c90220b0ade9490099))
* **release:** bump github-distribute to v1.9.1 ([1e157c8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1e157c836eeb29a08deb5b529482cb7b065d7656))
* **release:** bump github-distribute to v1.9.2 ([97e3f9f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/97e3f9fc3608c6b7e10a3ec9a36ea599988e9369))
* **release:** bump reusables to v1.10.0 and publish the chart to GHCR ([ad2df3c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/ad2df3cbbe8ba579167b3f4626927c6e9a32e7b9))
* **release:** drop the local semantic-release toolchain — the shared config pins it ([592ebdd](https://forgejo.webgrip.dev/webgrip/ploeg/commit/592ebdd9e0697c61fa12604729220c10153587e4))
* **release:** run the release in the toolchain image ([1a343b2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1a343b2da761e8727bd83eb9560dd0a47d37fa7e))
* **release:** sign and attest ploegd on Harbor via the shared cosign composite ([d0c4398](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d0c4398a324d81dea6824724fa9ffa136a6b63df))
* retire the pin comment that outlived the pin ([c4d9b75](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c4d9b75034c9b076efe9af089b45b3ed192f6224)), references [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40)
* retrigger release job (composite now falls back to setup-node on node<22.14 hosts) ([e50b844](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e50b8443b8dca7f6f4d61b7acfd7aa1f9393b3a0))
* retrigger release train (rc release died on missing yq, now fixed) ([8a6c8db](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8a6c8db24883e6e6e58dd18d195c0b7a5ca83090))

### Style

* **worker:** gofmt the appended regression tests ([38603ff](https://forgejo.webgrip.dev/webgrip/ploeg/commit/38603ffd5816f5f6fc1c7807cfe6a3e775d60a7a))

### Internal

* **helm:** refresh chart goldens for v0.2.0-rc.10 ([b3b5089](https://forgejo.webgrip.dev/webgrip/ploeg/commit/b3b50899c725ec5bed2ef8b2f2934fc3502071b7))
* retrigger the release ([715fc59](https://forgejo.webgrip.dev/webgrip/ploeg/commit/715fc5922ff5ee74c171dd6153c8c7c5a6c5c3ed))

## [0.2.0-rc.31](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.30...v0.2.0-rc.31) (2026-08-26)

### Fixed

* **release:** sign the Forgejo mirror too ([f8099c9](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f8099c9fef8aaec3763545e7f054a4cf21827d41))

### CI

* **release:** bump cosign-sign-attest to v1.11.2 ([eb2a8db](https://forgejo.webgrip.dev/webgrip/ploeg/commit/eb2a8db2fdac0faa4e751e8282b9e80a8531e24b))
* **release:** bump github-distribute to v1.11.1 ([3adcb19](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3adcb196274bb5f5e22774c90220b0ade9490099))

## [0.2.0-rc.30](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.29...v0.2.0-rc.30) (2026-08-26)

### Fixed

* **release:** reject zero-time release timestamps, not just Go's spelling ([9a1ba9f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9a1ba9f509178daf2abbc2e1760b23a347e44bc4))

## [0.2.0-rc.29](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.28...v0.2.0-rc.29) (2026-08-26)

### Fixed

* **release:** annotate the index and mirror cosign's accessories to GHCR ([b11e343](https://forgejo.webgrip.dev/webgrip/ploeg/commit/b11e3434b40f89d6cb30b6663a8f02799319926b)), references [#53](https://forgejo.webgrip.dev/webgrip/ploeg/issues/53)

## [0.2.0-rc.28](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.27...v0.2.0-rc.28) (2026-08-26)

### Fixed

* **adrs:** revert the ADR-0017 index edit — upstream had already resolved it ([444c313](https://forgejo.webgrip.dev/webgrip/ploeg/commit/444c313f65aebc344bbc897c55f1eade83229b55))
* **worker,shiftengine:** a killed run reports its own death, and does not spend the agent's budget ([771a18b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/771a18b6c2344e414985422c68f2610530c589e8))

### Style

* **worker:** gofmt the appended regression tests ([38603ff](https://forgejo.webgrip.dev/webgrip/ploeg/commit/38603ffd5816f5f6fc1c7807cfe6a3e775d60a7a))

## [0.2.0-rc.27](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.26...v0.2.0-rc.27) (2026-08-25)

### Fixed

* **release:** link the image and chart to the repo on GHCR ([3c833c2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3c833c2fa755724693b5430c5e97c95548c422bb))

### Docs

* **adr:** record why published artifacts name the mirror as their source ([11dee3e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/11dee3e713027fba03736511a796a3bd241327bb))

## [0.2.0-rc.26](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.25...v0.2.0-rc.26) (2026-08-25)

### Added

* **provider:** GitLab forge and ClickUp tracker providers ([e96cce7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e96cce76c8c68e3eff660434b52530186b23d9e0))

## [0.2.0-rc.25](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.24...v0.2.0-rc.25) (2026-08-25)

### Changed

* **ci:** bring the composite pins onto the plain-text diagnostics ([5ecd134](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5ecd1345bed92f5f691edfc9f78adffe058f2cae))

## [0.2.0-rc.24](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.23...v0.2.0-rc.24) (2026-08-25)

### Changed

* **ci:** drop the last GitHub-only annotation command ([7d883b2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7d883b2eac4e3b1644bcd7d02fdb26c7b9f3add5))

### CI

* **release:** bump github-distribute to v1.9.2 ([97e3f9f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/97e3f9fc3608c6b7e10a3ec9a36ea599988e9369))
* **release:** bump reusables to v1.10.0 and publish the chart to GHCR ([ad2df3c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/ad2df3cbbe8ba579167b3f4626927c6e9a32e7b9))

## [0.2.0-rc.23](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.22...v0.2.0-rc.23) (2026-08-25)

### Fixed

* **ci:** assert image labels on parsed JSON, not on rendered text ([dfdc714](https://forgejo.webgrip.dev/webgrip/ploeg/commit/dfdc714af816b2240c00badbec8b5e2373f20066))

## [0.2.0-rc.22](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.21...v0.2.0-rc.22) (2026-08-25)

### Fixed

* **ci:** skip the Harbor build when the version is already published ([71e6436](https://forgejo.webgrip.dev/webgrip/ploeg/commit/71e64367b26083318a0185a5580f27fc000f407d))

## [0.2.0-rc.21](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.20...v0.2.0-rc.21) (2026-08-25)

### Fixed

* **deps:** clear the nine CVEs Harbor flags on the ploegd image ([6670482](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6670482149c3b385a73048c82d1d339125d00147))

### CI

* **release:** bump github-distribute to v1.9.1 ([1e157c8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1e157c836eeb29a08deb5b529482cb7b065d7656))

## [0.2.0-rc.20](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.19...v0.2.0-rc.20) (2026-08-25)

### Fixed

* **ci:** finish the proxy bypass — syft scanner and buildkit come direct too ([646a6e7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/646a6e741cae386c5a271a6d76f654d0fa02e68f))

## [0.2.0-rc.19](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.18...v0.2.0-rc.19) (2026-08-25)

### Fixed

* **ci:** bypass the dead Docker Hub proxy so a release can distribute again ([615948d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/615948dabd66f180cfffd41b2e4bc8049f4ccfa0))

## [0.2.0-rc.18](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.17...v0.2.0-rc.18) (2026-08-24)

### Fixed

* **ci:** reopen the GitHub track — mirror, Releases and GHCR via github-distribute ([0b59582](https://forgejo.webgrip.dev/webgrip/ploeg/commit/0b595828044f64e4ddff2999efacccdd593fcbf4)), references [#48](https://forgejo.webgrip.dev/webgrip/ploeg/issues/48)

### CI

* retire the pin comment that outlived the pin ([c4d9b75](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c4d9b75034c9b076efe9af089b45b3ed192f6224)), references [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40)

## [0.2.0-rc.17](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.16...v0.2.0-rc.17) (2026-08-09)

### Fixed

* **ci:** pin semantic-release to v1.2.0 now that PR [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) is released ([1ca543a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1ca543af2c9161c94a701b049538a89b585abb31))

## [0.2.0-rc.16](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.15...v0.2.0-rc.16) (2026-08-09)

### Fixed

* **ci:** pin webgrip/workflows to v1.0.0 instead of [@main](https://forgejo.webgrip.dev/main) ([2c5fbec](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2c5fbecd65fca3b5e94c39bdd0b11666929066eb)), references [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40) [#40](https://forgejo.webgrip.dev/webgrip/ploeg/issues/40)
* **ci:** re-pin the semrel action to a commit the server can still resolve ([a5c8026](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a5c80261266e91152fdce9053088db51a99de84b))
* **harness,worker:** a reading Run's review must survive every harness ([fc0293f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fc0293f61662b270c8543c688b54a7a8dad9047f))
* **shiftengine,store:** a failed writing Run re-opens its Round ([b0ad282](https://forgejo.webgrip.dev/webgrip/ploeg/commit/b0ad28266faa4ebdc501d547204170034c9252d6)), references [#35](https://forgejo.webgrip.dev/webgrip/ploeg/issues/35)
* **shiftengine,worker,litellm:** close the loop the reviews were falling out of ([eb6c8dc](https://forgejo.webgrip.dev/webgrip/ploeg/commit/eb6c8dc91575a5414ceaa3808c260b9447eb3059)), references [erfbeeld#9](https://forgejo.webgrip.dev/erfbeeld/issues/9)

### Docs

* **adrs:** ADR-0018 — the drop box is every harness's return path ([f50edaa](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f50edaab66ba7a694adb9ef2b8bf09f2aaa366aa))
* **ci:** name the helm-version trap in the golden check's own advice ([3ec294c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3ec294cc99bfc74c92a1d35d49f3c5fdabe91d89))
* **research:** correct the rc.15 claim — it published; the release job is what broke ([3a1d248](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3a1d248bc9fc64d19da7755f4c05345b28122fee))
* **research:** how many trials, computed rather than asserted ([06d9964](https://forgejo.webgrip.dev/webgrip/ploeg/commit/06d996459ee75b904b563aae5a2b23768ba0bcdf))
* **research:** probe results — the gateway keeps its aliases, and rc.14 keeps no cost ([11fcead](https://forgejo.webgrip.dev/webgrip/ploeg/commit/11fceadd96b767e99ec3532c7cc83ed75e51327c))
* **research:** survey and design for benchmarking the whole loop ([d565449](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d565449519cbccc83b026293597a57863891256d))

### Tests

* **worker:** pin the spend-settling loop in both directions ([cdcb2b1](https://forgejo.webgrip.dev/webgrip/ploeg/commit/cdcb2b11d59a09765ccd64e9904f61ad9d95520a))

### Internal

* retrigger the release ([715fc59](https://forgejo.webgrip.dev/webgrip/ploeg/commit/715fc5922ff5ee74c171dd6153c8c7c5a6c5c3ed))

## [0.2.0-rc.15](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.14...v0.2.0-rc.15) (2026-07-31)

### Fixed

* **ci:** prove the container release path end to end, on a probed toolchain ([673896a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/673896af6a85024f0bf71560a538b6939978f190))

### CI

* **release:** run the release in the toolchain image ([1a343b2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1a343b2da761e8727bd83eb9560dd0a47d37fa7e))

## 0.2.0-rc.14 (2026-07-30)

* Merge pull request 'fix(worker,shiftengine,chart): make a reading Role able to review — and unable t ([5371fff](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5371fff)), closes [#32](https://forgejo.webgrip.dev/webgrip/ploeg/issues/32)
* fix(chart): three defects found by running rc.13 in production ([11d2284](https://forgejo.webgrip.dev/webgrip/ploeg/commit/11d2284))
* fix(shiftengine): a successful review must not read as a stoppage ([7deab6a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7deab6a))
* fix(worker): a reading Round may run before any branch exists ([1c55e74](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1c55e74))
* fix(worker): give a reader the work, and take away the credential ([8045b6d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8045b6d))

## 0.2.0-rc.13 (2026-07-30)

* fix(worker,httpapi): tell the truth about the forge credential, log routing ([f58c261](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f58c261))
* Merge pull request 'fix(shiftengine): tell the board when a Shift finishes' (#31) from fix/tracker-w ([0e9c3e1](https://forgejo.webgrip.dev/webgrip/ploeg/commit/0e9c3e1)), closes [#31](https://forgejo.webgrip.dev/webgrip/ploeg/issues/31)
* feat(chart): worker ServiceAccount and per-Role resources ([12896f5](https://forgejo.webgrip.dev/webgrip/ploeg/commit/12896f5))
* fix(config): reject an assignee shared by two teams ([60c836b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/60c836b))
* fix(shiftengine): write back to the tracker on every terminal settle ([30d9ce3](https://forgejo.webgrip.dev/webgrip/ploeg/commit/30d9ce3)), closes [#30](https://forgejo.webgrip.dev/webgrip/ploeg/issues/30)

## 0.2.0-rc.12 (2026-07-30)

* fix(ci): substitute the chart version out of the helm goldens ([c827f9f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c827f9f))

## 0.2.0-rc.11 (2026-07-30)

* Merge pull request 'fix(config): per-team routing on one project is valid, not a duplicate' (#29) fr ([c725b8d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c725b8d)), closes [#29](https://forgejo.webgrip.dev/webgrip/ploeg/issues/29)
* chore(helm): refresh chart goldens for v0.2.0-rc.10 ([c55f446](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c55f446))
* fix(config): per-team routing on one project is valid, not a duplicate ([ab8536b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/ab8536b))

## 0.2.0-rc.10 (2026-07-29)

* Merge pull request 'feat(config): routing and roster as a file, and push rights minted per Run' (#27 ([cda5095](https://forgejo.webgrip.dev/webgrip/ploeg/commit/cda5095)), closes [#27](https://forgejo.webgrip.dev/webgrip/ploeg/issues/27)
* feat(config): routing and roster as a file, and push rights minted per Run ([3c455da](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3c455da))

## [0.2.0-rc.9](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.8...v0.2.0-rc.9) (2026-07-29)

### Added

* **chart:** one workload per (team, Role), and a waiver keyed to the hazard ([e6b3f92](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e6b3f92fbcbc1f1b9e233d959c70ae2eb3a2b291))
* **dispatch:** every queued item gets a Shift, behind a kill switch ([988c496](https://forgejo.webgrip.dev/webgrip/ploeg/commit/988c4963551f01f30bfd8bd335230a0fbefbb093))
* **httpapi:** forge webhook ingest — verified, deduplicated, audited ([7befbce](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7befbceecbf9a7ecda46faf4e60ab2a7f2752a36)), references [#2](https://forgejo.webgrip.dev/webgrip/ploeg/issues/2) [#3](https://forgejo.webgrip.dev/webgrip/ploeg/issues/3) [#107](https://forgejo.webgrip.dev/webgrip/ploeg/issues/107) [#9](https://forgejo.webgrip.dev/webgrip/ploeg/issues/9)
* **provider:** findings reach the pull request, and a person is asked to merge ([2e9f6ed](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2e9f6edfd503115c5d003c40852d0ec940376d94))
* **shiftengine:** verdict-driven fix rounds, bounded by pool then cap ([6beaaa1](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6beaaa1739d26a89bbe1419c8634f43b5e10917d))

### Fixed

* **ploegd:** register a forge under the ID its Work Target carries ([9ccb035](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9ccb035124471f4fa841517c4272c38ca0888b8b))

### Docs

* archive run-multi-agent-shifts and correct the divergence list ([8bf5210](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8bf5210be06dd2ac481d4fb6ec9a467053ee659a))
* **openspec:** propose close-the-review-loop, and ADR-0017 behind it ([f9b157c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f9b157c2d200c82c74476f05e8dd8999e7dd8219)), references [#107](https://forgejo.webgrip.dev/webgrip/ploeg/issues/107)

## [0.2.0-rc.8](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.7...v0.2.0-rc.8) (2026-07-29)

### Added

* **api:** role-scoped claim, findings on the outcome, role-filtered depth ([3cbfd63](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3cbfd6341e4ebb1096cbea532a88113afce45dfa))
* **harness:** ACP driver, client half, and the coder/acp-go-sdk dependency ([6a06793](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6a067933d0f3a1227b320d8a6c53a229b21aca24))
* **harness:** ACP event and stop-reason semantics (no SDK, no process) ([2bcd9ce](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2bcd9ce6092712f703e06d4a25d0d44dd25a18eb)), references [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64)
* **harness:** ACP permission policy for unattended runs ([a8e705a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a8e705ab332d032c1711c12b1dd2c86166f147f3))
* **harness:** ACP subprocess layer — process groups, stdout demux, async stdin ([a238a66](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a238a668bb1703e2077cf1aec23f11c6fa80a2ef))
* **plan:** team plan config, parsed at boot, rendered dark from the chart ([bff5540](https://forgejo.webgrip.dev/webgrip/ploeg/commit/bff5540227b4aa3310850cf3e6a285d0e1ab6360))
* **shiftengine:** open, advance, close and park Shifts ([9e538d0](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9e538d077b05aa070fd7936db9feb3c1e0ae52ee))
* **store:** settlement, per-Run liveness, and the round-completion signal ([9d2d39b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9d2d39be359e1129ce483d9e05773e3d0fa02f51))
* **store:** shift lifecycle completions and shift-run plumbing fixes ([c3db764](https://forgejo.webgrip.dev/webgrip/ploeg/commit/c3db7641b74829b73b8c8b990cf5e9745104bddb))
* **store:** Shifts — rounds, reader/writer runs, and pooled budgets ([450a8ae](https://forgejo.webgrip.dev/webgrip/ploeg/commit/450a8ae25d0ce3f0a3961fba65510c35f9e96e98))
* **worker:** role-aware runs — claim, prompt, budget, findings drop box ([8e170fa](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8e170fac79771df29880d28de7e7a3e7aab7494f)), references [#9](https://forgejo.webgrip.dev/webgrip/ploeg/issues/9)
* **worker:** select the ACP harness from the registry, env and chart ([a33646d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a33646d4dbcb14d9722e67a6fb85f83f29b74d24))

### Fixed

* **harness:** flush the agent's stderr before building an ACP failure reason ([de03342](https://forgejo.webgrip.dev/webgrip/ploeg/commit/de03342713aa73549939c43864b8f66682837198))
* **httpapi:** close the failure taxonomy at the API boundary ([f72f9ac](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f72f9ac741fb59413492c10caac1a78f81ae1641))
* **worker:** stop a failed run inheriting the previous run's PR ([5195737](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5195737f67bead50826cacad2465fa6cbcac4891))

### Changed

* **harnesstest:** make the conformance kernel adapter-shaped ([dc0dcb2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/dc0dcb261adb969f0976dfe61ea82ad33c22714e)), references [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64)

### Docs

* **adr:** consolidate docs/adr into docs/adrs — one gated ledger ([28b82b7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/28b82b767e00a0cfeda9fea925bdd733231abd32)), references [#97](https://forgejo.webgrip.dev/webgrip/ploeg/issues/97)
* **adr:** Shift owns the item, Lease owns the branch (0010-0012) ([d601888](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d601888a529f4d67151e1800b3fd93849aaf892a))
* **adrs:** migrate design.md §8/§9 into an enforced MADR 4.0 ledger ([f5f9596](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f5f959686e2e55b09a309305ca56f8a23ec35963))
* **adr:** the Lease becomes a capability, not a note (0013) ([14fcac6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/14fcac62a54dd71d2a645dca4e096ec9bb5d9781)), references [forgejo#8837](https://forgejo.webgrip.dev/forgejo/issues/8837)
* **agents:** record the multi-session staging discipline ([6b7ccf7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6b7ccf7d4477d59490e2cd85dee50725a5894f56))
* close out the ACP work in the backlog, design §5 and the divergence list ([4cfd890](https://forgejo.webgrip.dev/webgrip/ploeg/commit/4cfd8901f1e72cbe625329eb266f39992b9ca3ae)), references [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64) [#63](https://forgejo.webgrip.dev/webgrip/ploeg/issues/63) [#64](https://forgejo.webgrip.dev/webgrip/ploeg/issues/64) [#44](https://forgejo.webgrip.dev/webgrip/ploeg/issues/44) [#69](https://forgejo.webgrip.dev/webgrip/ploeg/issues/69)
* **domain:** regenerate the domain views for Shift and Round ([0c8f417](https://forgejo.webgrip.dev/webgrip/ploeg/commit/0c8f4178b6829e12f39790612c35ef986b1b0706))
* make docs/adrs the only ledger, and gate it in go test ([fcbd0b7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fcbd0b7b6325e7751359ecc779fe7ae34ca9ecd5))
* **openspec:** adopt the spec-driven-with-adr workflow ([18cb967](https://forgejo.webgrip.dev/webgrip/ploeg/commit/18cb9678be152f285e4cb26577a3297f327f8e4d))
* **openspec:** design, adr manifest and tasks for run-multi-agent-shifts ([b9a197c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/b9a197c90054fb45e69fa99862a9ab2d80fadae9))
* **openspec:** propose run-multi-agent-shifts ([70abf24](https://forgejo.webgrip.dev/webgrip/ploeg/commit/70abf24df514c3df5aea1a7c4014a426aca4a677))
* reconcile ADR-0010/0012 with the implementation; architecture §10 with diagrams ([e589ed6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e589ed65dd8fbb813ba38e8dc2eb6f03b7e1de64))

### Tests

* **acp:** a zombie grandchild is not a surviving one ([7bd42ed](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7bd42ed0c5600bcd8dd076bd8b25aca13765f659))

## [0.2.0-rc.7](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.6...v0.2.0-rc.7) (2026-07-29)

### Added

* bind the work target to the work item, not to the team ([d2085b3](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d2085b39fe68feeaa12f08725b231547d52fac4c)), references [#97](https://forgejo.webgrip.dev/webgrip/ploeg/issues/97) [97/#103](https://forgejo.webgrip.dev/webgrip/ploeg/issues/103) [#104-108](https://forgejo.webgrip.dev/webgrip/ploeg/issues/104-108)

### Docs

* cite model.yaml entities by name, not by line number ([db2b4e5](https://forgejo.webgrip.dev/webgrip/ploeg/commit/db2b4e58cf9701e5e281c47339d2749f7ebba344))
* **domain:** model the Work Target, Forge, Scope and Routing Rule axes ([8227da7](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8227da76ee23b628bd9fc51b050dffca56011718))
* rewrite AGENTS.md as a router, land research and ops knowledge in-repo ([3fd1744](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3fd1744cca83d71cd0e6ac69d8cf46436468decb)), references [#103](https://forgejo.webgrip.dev/webgrip/ploeg/issues/103)
* update README status — executors ship in the chart ([078efca](https://forgejo.webgrip.dev/webgrip/ploeg/commit/078efcad020aa30e0ffae145abff6b2e261276ef))

## [0.2.0-rc.6](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.5...v0.2.0-rc.6) (2026-07-28)

### Added

* run forensics survive pod/job cleanup — node+pod identity in logs+checkpoints, failure-reason taxonomy, VIK-586 fix ([72db36b](https://forgejo.webgrip.dev/webgrip/ploeg/commit/72db36bcbc964e18461d21a9b2e4a2523eca8a0a))

### Fixed

* apply PR review round 2 — ExpectsLLM, VIK-586 heuristic, gofmt, FailureReason naming ([84c8ced](https://forgejo.webgrip.dev/webgrip/ploeg/commit/84c8ceddd053e8eac5a72ab4754d274284d499e3))

### Docs

* record 2026-07-28 A2A sweep — wrong layer for the factory, north-facade watchlisted ([139d497](https://forgejo.webgrip.dev/webgrip/ploeg/commit/139d497560b6924a96545f9495465eb011c352b2)), references [#102](https://forgejo.webgrip.dev/webgrip/ploeg/issues/102) [#31](https://forgejo.webgrip.dev/webgrip/ploeg/issues/31)

## [0.2.0-rc.5](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.4...v0.2.0-rc.5) (2026-07-28)

### Added

* pluggable harness, agent image, LLM broker, and executor seams ([1789c7a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1789c7a74ec7470c127178ec07cb572471243ce6)), references [66/#69](https://forgejo.webgrip.dev/webgrip/ploeg/issues/69)

### Docs

* **agents:** correct migrations path to pkg/store/migrations ([7fc9446](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7fc9446c806aea292df72df2c6327ebb4635582c))
* current-state architecture of the dark factory (mermaid: context, run sequence, states, key layers) ([3e868ec](https://forgejo.webgrip.dev/webgrip/ploeg/commit/3e868ecd3b7e9afca1efb23c58b412a3aff7f8a9))
* record 2026-07-27 AHP sweep verdict — session-sync layer above ploeg, ACP stays the harness seam ([d595dff](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d595dff2f7969511c5673ef13a7c4928c56d88e5))

## [0.2.0-rc.4](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.3...v0.2.0-rc.4) (2026-07-28)

### Fixed

* infra failures don't burn attempt budget (backoff + infra_failures) ([a9615a8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a9615a87c4d834e8d2c5cbe52f0631a102c7cc07))

### Tests

* **store:** unused var + gofmt — reviewer gate pass ([a544a41](https://forgejo.webgrip.dev/webgrip/ploeg/commit/a544a41b2db88eea5abb52e38b6f63e3f89fd9f5))

## [0.2.0-rc.3](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.2...v0.2.0-rc.3) (2026-07-27)

### Fixed

* **ploegd:** safe Alias() helper, sweeper key revoke, boot orphan sweep ([1c39d96](https://forgejo.webgrip.dev/webgrip/ploeg/commit/1c39d96824d3a51a31398ad470d6edb950e31e0b))

### Tests

* **litellm:** strict fake emits [] not null for empty lists; gofmt ([f2ca405](https://forgejo.webgrip.dev/webgrip/ploeg/commit/f2ca4051dd4ce0d9ab48b1ab15afe17b43e3d759))

## [0.2.0-rc.2](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.2.0-rc.1...v0.2.0-rc.2) (2026-07-27)

### Fixed

* **helm:** default worker CPU to 1 core (single-threaded cold import ([540c3c2](https://forgejo.webgrip.dev/webgrip/ploeg/commit/540c3c2aa2b248c2799dfb7259f1bca1db90aa5f))

### CI

* **release:** build the image once — Forgejo distribute mirrors Harbor by digest ([04ff380](https://forgejo.webgrip.dev/webgrip/ploeg/commit/04ff380edb5aca206b0fbc8a62d8c349fc254784))
* **release:** sign and attest ploegd on Harbor via the shared cosign composite ([d0c4398](https://forgejo.webgrip.dev/webgrip/ploeg/commit/d0c4398a324d81dea6824724fa9ffa136a6b63df))

## [0.2.0-rc.1](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0...v0.2.0-rc.1) (2026-07-27)

### Added

* **deps:** update docker.io/golang docker tag ( 1.24 ➔ 1.26 ) ([739bd80](https://forgejo.webgrip.dev/webgrip/ploeg/commit/739bd806b1b0ef2f4d7e761a07a1c34d5f0358fd))
* **deps:** Update postgres Docker tag ( 17 ➔ 18 ) ([6fe8454](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6fe84548e06d44ab22eb444a009247dcad621e85))

### Fixed

* assignment webhooks revive finished work items ([8b15d2e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8b15d2e5fae575f6dbea93f59026521f53989ca9))
* **ci:** adopt the shared forgejo-distribute reusable for the Forgejo mirror ([9047b3a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9047b3a520c94c4ac3df7e198776f07458e14d06))
* **ci:** correct the stale single-reusable-chain comment; cut v0.1.0-rc.11 ([09eeea0](https://forgejo.webgrip.dev/webgrip/ploeg/commit/09eeea0677794acb6ed8dcbace2fd72829538276))
* **ci:** mirror image and chart to the Forgejo registry and link them to the repo ([621e77a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/621e77ae035ec64368dd3c2becbf7fee9bb63351))
* **ci:** release and publish as the webgrip-ci bot, not the per-job token ([647a49c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/647a49cd2a75421d66defd83eb52482e3d53b000))
* **deps:** update harbor.webgrip.dev/webgrip/agent-runner docker tag ( 1.0.1 ➔ 1.0.2 ) ([2fa0985](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2fa0985ce2ae60f66daefc7c23a6985cddab4e75))
* Guaranteed QoS for every factory pod — out of the OOMController's kill zone ([9e708ee](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9e708ee7b4a6a55db0047c98d53ab6fc19aaae30))
* ploeg-worker owns the per-run LiteLLM key lifecycle (mint + always-revoke) ([aa5fc39](https://forgejo.webgrip.dev/webgrip/ploeg/commit/aa5fc397c463d95a6cf18944594ee22cdfd0e585))
* **release:** drop the yq appVersion prepareCmd — the shared config bumps both keys ([565cb1f](https://forgejo.webgrip.dev/webgrip/ploeg/commit/565cb1fcf926c522bfdaea33bb3ea01563238857))
* worker owns the per-run LiteLLM key lifecycle (mint + always-revoke) ([9bcc0f8](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9bcc0f84f1bdad3e68a4513eeba6ccf77be95fa1))
* worker targets a configurable base branch end to end ([6ffdfe6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6ffdfe636cf19f2c4be7ea51eedbf2fb6dbe295a)), references [#6](https://forgejo.webgrip.dev/webgrip/ploeg/issues/6)

### CI

* **actions:** Pin dependencies ([e26493a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e26493a946cfe21c60243b124005c8bdbaa08245))
* **actions:** Update dependency helm ( v3.18.4 ➔ v4.2.3 ) ([5591eab](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5591eab6521ec8623b8a27a974c6e18deb576efc))
* **actions:** Update https://github.com/actions/setup-go action ( v6.5.0 ➔ v7.0.0 ) ([5ce8533](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5ce85333de80d9a666742f471bd69bbe7089e768))
* adopt @webgrip/semantic-release-config ([7b5f87c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/7b5f87ce01d403d619e51c67108eca1b7941ec3c))
* drop the manual release dispatch — bot-cut releases fire the release event natively ([2faef42](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2faef424d906a803d1ecb190a35d38dcf4ba7dff))
* **release:** drop the local semantic-release toolchain — the shared config pins it ([592ebdd](https://forgejo.webgrip.dev/webgrip/ploeg/commit/592ebdd9e0697c61fa12604729220c10153587e4))
* retrigger release job (composite now falls back to setup-node on node<22.14 hosts) ([e50b844](https://forgejo.webgrip.dev/webgrip/ploeg/commit/e50b8443b8dca7f6f4d61b7acfd7aa1f9393b3a0))
* retrigger release train (rc release died on missing yq, now fixed) ([8a6c8db](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8a6c8db24883e6e6e58dd18d195c0b7a5ca83090))

## [0.1.0-rc.9](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.8...v0.1.0-rc.9) (2026-07-26)

### Fixed

* **ci:** release and publish as the webgrip-ci bot, not the per-job token ([647a49c](https://forgejo.webgrip.dev/webgrip/ploeg/commit/647a49cd2a75421d66defd83eb52482e3d53b000))

## [0.1.0-rc.8](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.7...v0.1.0-rc.8) (2026-07-25)

### Fixed

* **ci:** mirror image and chart to the Forgejo registry and link them to the repo ([621e77a](https://forgejo.webgrip.dev/webgrip/ploeg/commit/621e77ae035ec64368dd3c2becbf7fee9bb63351))

## [0.1.0-rc.7](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.6...v0.1.0-rc.7) (2026-07-25)

### Fixed

* ploeg-worker owns the per-run LiteLLM key lifecycle (mint + always-revoke) ([aa5fc39](https://forgejo.webgrip.dev/webgrip/ploeg/commit/aa5fc397c463d95a6cf18944594ee22cdfd0e585))

## [0.1.0-rc.6](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.5...v0.1.0-rc.6) (2026-07-25)

### Fixed

* Guaranteed QoS for every factory pod — out of the OOMController's kill zone ([9e708ee](https://forgejo.webgrip.dev/webgrip/ploeg/commit/9e708ee7b4a6a55db0047c98d53ab6fc19aaae30))

## [0.1.0-rc.5](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.4...v0.1.0-rc.5) (2026-07-25)

### Fixed

* assignment webhooks revive finished work items ([8b15d2e](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8b15d2e5fae575f6dbea93f59026521f53989ca9))
* worker targets a configurable base branch end to end ([6ffdfe6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6ffdfe636cf19f2c4be7ea51eedbf2fb6dbe295a)), closes [#6](https://forgejo.webgrip.dev/webgrip/ploeg/issues/6)

### Docs

* AGENTS.md + team-silver repo skill — make the repo factory-workable ([15b28b6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/15b28b6d0ee7ecacf1d054671067cf3ff8f72887))

## [0.1.0](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.0.0...v0.1.0) (2026-07-25)

### Added

* **executor:** OpenHands worker, Helm chart, and chart publishing ([2f48634](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2f48634794595ef5d774dc6979feebb86d477532))
* **ploegd:** working dispatch-plane prototype — ingest, leases, run API ([6b16c77](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6b16c77d168e40ae1630290981462ee3b4cb950c)), closes [#31](https://forgejo.webgrip.dev/webgrip/ploeg/issues/31) [#49](https://forgejo.webgrip.dev/webgrip/ploeg/issues/49)
* **work:** align WorkItem with domain model — needs_human state, origin, priority ([90f48e6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/90f48e6e29a7c39dd80374b339fcecf26d310af3)), closes [#12](https://forgejo.webgrip.dev/webgrip/ploeg/issues/12)

### Fixed

* **chart:** default the KEDA scaler host to a namespace-qualified FQDN ([ef51368](https://forgejo.webgrip.dev/webgrip/ploeg/commit/ef51368f9982c1150f3c57257d8fdbd0da53f7f9))
* never lose a run's outcome to the links constraint ([5893e03](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5893e03cd35d2e657b725512511a19be5201376c))
* **ploegd:** retry database connectivity at startup instead of crash-looping ([fc5d879](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fc5d8793e0a2b3a712600704a4089a23ea46b168))
* **release:** pin notes toolchain so release notes render sections ([5c75197](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5c75197ce5766da97ef72e009abf8bc948015462))

## [0.1.0-rc.4](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.3...v0.1.0-rc.4) (2026-07-24)

### Fixed

* **chart:** default the KEDA scaler host to a namespace-qualified FQDN ([ef51368](https://forgejo.webgrip.dev/webgrip/ploeg/commit/ef51368f9982c1150f3c57257d8fdbd0da53f7f9))
* never lose a run's outcome to the links constraint ([5893e03](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5893e03cd35d2e657b725512511a19be5201376c))
* **ploegd:** retry database connectivity at startup instead of crash-looping ([fc5d879](https://forgejo.webgrip.dev/webgrip/ploeg/commit/fc5d8793e0a2b3a712600704a4089a23ea46b168))

## [0.1.0-rc.3](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.2...v0.1.0-rc.3) (2026-07-23)

### Added

* **executor:** OpenHands worker, Helm chart, and chart publishing ([2f48634](https://forgejo.webgrip.dev/webgrip/ploeg/commit/2f48634794595ef5d774dc6979feebb86d477532))

## [0.1.0-rc.2](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.1.0-rc.1...v0.1.0-rc.2) (2026-07-23)

### Added

* **ploegd:** working dispatch-plane prototype — ingest, leases, run API ([6b16c77](https://forgejo.webgrip.dev/webgrip/ploeg/commit/6b16c77d168e40ae1630290981462ee3b4cb950c)), closes [#31](https://forgejo.webgrip.dev/webgrip/ploeg/issues/31) [#49](https://forgejo.webgrip.dev/webgrip/ploeg/issues/49)

### Fixed

* **release:** pin notes toolchain so release notes render sections ([5c75197](https://forgejo.webgrip.dev/webgrip/ploeg/commit/5c75197ce5766da97ef72e009abf8bc948015462))

## [0.1.0-rc.1](https://forgejo.webgrip.dev/webgrip/ploeg/compare/v0.0.0...v0.1.0-rc.1) (2026-07-23)

### Added

* **work:** align WorkItem with domain model — needs_human state, origin, priority ([90f48e6](https://forgejo.webgrip.dev/webgrip/ploeg/commit/90f48e6e29a7c39dd80374b339fcecf26d310af3)), references [#12](https://forgejo.webgrip.dev/webgrip/ploeg/issues/12)

### CI

* development branch cuts rc prereleases; :latest reserved for stable ([db7e41d](https://forgejo.webgrip.dev/webgrip/ploeg/commit/db7e41d0e0377794d391873b4fe7ae8f9e5a783f))
* park GHCR publish while GitHub is out of scope ([71e46dd](https://forgejo.webgrip.dev/webgrip/ploeg/commit/71e46dd70f97b1cf50b9e469ad852e0977bc977a))
* set up release train (semantic-release, ploegd image, publish workflows) ([8a16bda](https://forgejo.webgrip.dev/webgrip/ploeg/commit/8a16bda56c0e4f483185ba263a2608b88d19bc56))
