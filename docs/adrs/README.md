# Architecture Decision Records

This directory is **the** decision ledger for Ploeg ([0001](0001-adrs-are-the-decision-ledger.md)).
If a decision outlives the change that prompted it, it is a record here — not a
row in `design.md`, not a paragraph in a research dossier, not an assistant's
memory.

Format is [MADR 4.0.0](https://adr.github.io/madr/) with two local extensions.

## The two local rules

**1. Supersession is append-only.** An accepted record's own `status:` is never
flipped. The superseding record carries `supersedes: NNNN`; the Records table
below shows `superseded by NNNN` for readers. The file is the historical
artefact; this index is the current view. Files are never renamed and numbers
are never reused.

**2. A decision that can change carries a dated review.** `review-by:
YYYY-MM-DD` in the front matter plus a `## Re-evaluation triggers` section
naming the observable events that reopen it. Prefer facts someone could check in
five minutes ("spec reaches 1.0", "issue #1029 closes") over judgements someone
has to form. `review-by: none` is a legitimate answer for a decision that only
changes if the project changes shape.

Both rules are enforced, not merely requested — see *Validation* below.

## Writing one

Copy [adr-template.md](adr-template.md) to `NNNN-kebab-title.md` with the next
number. The title names the *decision*, not the topic: "Go is the implementation
language", not "Language choice".

Every record needs a `### Confirmation` subsection naming how compliance is
actually checked — a CI gate with its command, a review step, or a script.
"Reviewers will notice" is not a confirmation, and the validator rejects a
record that omits the section.

The evidence stays out. A survey's working belongs in
`docs/research/YYYY-MM-DD-<topic>.md`, linked from *More Information*. The ADR
carries the verdict, the reasoning, and the triggers.

The [`adr-writer`](https://forgejo.webgrip.dev/webgrip/webgrip-ai-skills) skill
knows this format. Its **bundled validator does not apply here** — it assumes
vanilla status-flip supersession and would reject this corpus. Use the local
script.

## Validation

```sh
go test ./internal/ledger/
```

It runs inside the existing `go test ./...` step of
`.forgejo/workflows/on_pull_request.yml` — no extra CI step, no `python3`
dependency in the runner. It gates filename and number discipline, status
legality, `supersedes:` integrity, file↔index parity in both directions, the
status and date mirrors, the presence of `### Confirmation`, and that a dated
`review-by` is backed by a `## Re-evaluation triggers` section.

Adding a record means adding its row below in the same commit; the parity check
fails otherwise.

## Status legend

| Status | Meaning |
| --- | --- |
| `proposed` | Written, not ratified. Do not design against it yet. |
| `accepted` | In force. Design within it or supersede it. |
| `rejected` | Considered and declined. Kept so the reasoning is not re-derived. |
| `deprecated` | No longer relevant, and nothing replaced it. |
| `superseded by NNNN` | **Index-only.** The file's own status stays as it was. |

## Records

| ADR | Decision | Status | Last updated |
| --- | --- | --- | --- |
| [0001](0001-adrs-are-the-decision-ledger.md) | ADRs in `docs/adrs/` are the single decision ledger; append-only supersession, dated re-evaluation triggers | accepted | 2026-07-29 |
| [0002](0002-go-as-the-implementation-language.md) | Go is the implementation language | accepted | 2026-07-29 |
| [0003](0003-apache-2-0-license.md) | Ploeg ships under Apache-2.0 | accepted | 2026-07-29 |
| [0004](0004-forgejo-leading-home-github-mirror-module-path.md) | Forgejo-leading home, GitHub push-mirror, module path from the mirror | superseded by 0062 | 2026-07-29 |
| [0005](0005-build-a-dedicated-dispatch-plane.md) | Build a dedicated dispatch plane rather than adopt an existing orchestrator | accepted | 2026-07-29 |
| [0006](0006-ahp-is-the-wrong-layer.md) | AHP is parked: a live-run surface above Ploeg, not a seam inside it | accepted | 2026-07-29 |
| [0007](0007-a2a-adopt-nothing-watchlist-a-facade.md) | A2A: adopt nothing now; watchlist a north-facing dispatch facade | accepted | 2026-07-29 |
| [0008](0008-litellm-is-the-credential-and-metering-seam.md) | LiteLLM stays the per-run credential and metering seam | accepted | 2026-07-29 |
| [0009](0009-paperclip-mine-for-design-never-integrate.md) | Paperclip: mine it for design, never depend on it | accepted | 2026-07-29 |
| [0010](0010-shift-owns-the-item-lease-owns-the-branch.md) | A Shift owns the work item; a Lease narrows to write access on the branch | accepted | 2026-07-29 |
| [0011](0011-the-pull-request-is-the-blackboard.md) | The pull request is the blackboard; Ploeg is only the transport | accepted | 2026-07-29 |
| [0012](0012-two-level-budgets-authorized-and-settled.md) | Budgets are two-level: a Shift pool, authorized and settled per Run | accepted | 2026-07-29 |
| [0013](0013-push-rights-are-minted-per-run.md) | Push rights are minted per Run and die with the Lease; readers get none | accepted | 2026-07-29 |
| [0014](0014-work-target-is-a-work-item-attribute.md) | Bind the Work Target to the Work Item, not to the Team | accepted | 2026-07-29 |
| [0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md) | Route work in the core over provider-opaque Scopes | proposed | 2026-07-29 |
| [0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md) | Resolve forges through a registry and mint forge credentials per Run | proposed | 2026-07-29 |
| [0017](0017-the-review-loop-is-verdict-driven-and-capped.md) | A reviewing Role's verdict re-opens the writer; the pool, a cap and the verdict stop the loop | proposed | 2026-07-29 |
| [0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md) | The outcome drop box is every harness's return path for a reading Run | proposed | 2026-08-08 |
| [0019](0019-a-failed-writing-run-reopens-its-round.md) | A failed writing Run re-opens its Round; a failed reading Run does not | proposed | 2026-08-08 |
| [0020](0020-published-artifacts-name-the-mirror-as-source.md) | Published artifacts name the GitHub mirror as their source, and Forgejo as their URL | accepted | 2026-08-26 |
| [0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md) | Infrastructure failures and agent failures get separate retry budgets | proposed | 2026-08-26 |
| [0022](0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md) | The name and mark are trademarks under a usage policy, not CC-licensed artwork | accepted | 2026-08-27 |
| [0023](0023-the-forge-dialect-travels-on-the-work-item.md) | The forge dialect travels on the Work Item; the forge URL and credential stay deployment-global | proposed | 2026-09-02 |
| [0024](0024-operator-work-uses-one-execution-authority.md) | Ploeg admits operator work and owns execution while De Vloer provides interaction | proposed | 2026-09-10 |
| [0025](0025-management-authority-stays-in-the-control-plane.md) | Management authority stays in the control plane and unresolved accounting retains authorization | proposed | 2026-09-10 |
| [0026](0026-tracker-selections-bind-the-canonical-work-item.md) | Tracker selections bind canonical Work Items under durable operator ownership | proposed | 2026-09-11 |
| [0027](0027-candidate-delivery-uses-trusted-evidence-and-a-publication-barrier.md) | Candidate delivery uses trusted evidence and a publication barrier | proposed | 2026-09-11 |
| [0028](0028-automatic-releases-stay-zero-major-candidates.md) | Automatic releases stay zero-major candidates | superseded by 0063 | 2026-09-11 |

| [0029](0029-qualify-unfold-before-changing-distribution.md) | Qualify Unfold before changing Ploeg distribution | proposed | 2026-09-12 |
| [0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md) | Target repository instructions rank below the delivery contract | proposed | 2026-09-22 |
| [0031](0031-runs-create-work-items-held-for-approval-within-limits.md) | Runs create Work Items that wait for approval, within per-Team limits | proposed | 2026-09-23 |
| [0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md) | Keep the dedicated dispatch plane, and compete on authorized spend over a self-hosted stack | proposed | 2026-09-30 |
| [0033](0033-board-control-planes-are-mined-for-design-never-depended-on.md) | Board control planes (Paperclip, Multica) are mined for design and never depended on | proposed | 2026-09-26 |
| [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) | The harness gets placeholders; the worker keeps the credentials | accepted | 2026-10-04 |
| [0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) | Runs get Ploeg-owned skills, mounted toolchains and a verification the worker runs | proposed | 2026-09-27 |
| [0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md) | Stuck work reaches the owner as a cited proposal; no agent applies a decision | proposed | 2026-09-28 |
| [0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md) | Teams opt into registry egress through a logged allowlist proxy; airgapped stays the default | accepted | 2026-10-01 |
| [0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md) | A repo label selects among registered targets, and the board default is the fallback | accepted | 2026-09-28 |
| [0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md) | A Run calls only its Role's model, and the advisor waits for metering that prices it | proposed | 2026-09-30 |
| [0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md) | A conflicted pull request becomes a priority ticket that Ploeg resolves | proposed | 2026-09-30 |
| [0041](0041-the-openai-agents-api-stays-outside-the-run-until-it-takes-an-authorized-budget.md) | The OpenAI Agents API stays outside the Run until it can take an authorized budget; Unfold meets it over MCP and runs Codex itself | proposed | 2026-09-30 |
| [0042](0042-a-writing-run-reports-the-problem-and-solution-a-reviewer-reads.md) | A writing Run reports the problem and solution a reviewer reads | proposed | 2026-09-30 |
| [0043](0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md) | A failed reading Run is retried in its Round, and a review that never came closes `review_failed` | accepted | 2026-10-01 |
| [0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md) | An operator restarts stopped work by requeueing it from a Round they choose | accepted | 2026-10-01 |
| [0045](0045-keep-run-usage-and-merge-facts.md) | Ploeg keeps every usage figure a harness reports and every merge and review fact a forge reports | proposed | 2026-10-01 |
| [0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) | A Run card is assembled per Work Item from stored facts | proposed | 2026-10-01 |
| [0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) | Ploeg learns where a merged change is deployed from a generic deploy endpoint | proposed | 2026-10-01 |
| [0048](0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md) | A pool held by unsettled Runs waits for settlement before it parks | accepted | 2026-10-04 |
| [0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md) | A Run card reads the gateway for usage so far while a Run is running | proposed | 2026-10-01 |
| [0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md) | A Run card's grade is a versioned formula over stored facts | proposed | 2026-10-01 |
| [0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) | Delivery gates are mapped per board from tracker statuses | proposed | 2026-10-01 |
| [0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md) | A crack needs the fixer and a second person, and Ploeg only proposes candidates | proposed | 2026-10-01 |
| [0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md) | An epic is a set of the Work Items declared its children before their first Shift | proposed | 2026-10-01 |
| [0054](0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md) | A card list finds cards by roster login, newest activity first | proposed | 2026-10-01 |
| [0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md) | Ploeg keeps one card comment with a static card image on the pull request | proposed | 2026-10-01 |
| [0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md) | A Run card's rarity is its challenge, predicted at mint and frozen at release | proposed | 2026-10-02 |
| [0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md) | A Run card's flow figures come from every recorded tracker status and a team calendar | proposed | 2026-10-02 |
| [0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md) | A Run card's pull request, CI and change-shape figures are read from the forge and kept per play | proposed | 2026-10-02 |
| [0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) | Delivery facts come from the forge, never from the agent's outcome | proposed | 2026-10-03 |
| [0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md) | Authenticated webhooks go through a durable inbox, and required publications through an outbox | proposed | 2026-10-03 |
| [0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md) | A Run card's grade penalizes rework, not review, and says which inputs it missed | proposed | 2026-10-03 |
| [0062](0062-github-is-the-independent-project-home.md) | GitHub is the independent project home | accepted | 2026-10-03 |
| [0063](0063-independent-releases-start-at-zero-point-one.md) | Independent releases start at 0.1.0 | accepted | 2026-10-03 |
| [0064](0064-development-is-trunk-and-release-candidates-come-from-it.md) | Development is trunk, and release candidates come from it | accepted | 2026-10-04 |
| [0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md) | A Run is briefed with an OKF knowledge pack written outside the clone | proposed | 2026-10-04 |
| [0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md) | A Run's learnings are proposals a person reviews before any Run sees them | proposed | 2026-10-04 |
| [0067](0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md) | Context bundles are stored per Work Item and unpacked by the worker under fixed limits | accepted | 2026-10-04 |
| [0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md) | Context added while a Shift runs reaches the next Run | accepted | 2026-10-04 |
| [0069](0069-ploeg-names-none-of-its-consumers.md) | Ploeg names none of its consumers | accepted | 2026-10-04 |
| [0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md) | A pull request is ready for review only when its checks passed on the pushed commit | accepted | 2026-10-04 |
| [0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md) | Ploeg keeps its state in PostgreSQL and is not a Kubernetes operator | proposed | 2026-10-05 |
| [0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md) | ploegd launches each Run's sandbox, and KEDA leaves the sandbox path | proposed | 2026-10-06 |
| [0073](0073-ploeg-stays-in-go-and-admits-rust-only-as-a-separately-deployed-component.md) | Ploeg stays in Go, and admits Rust only as a separately deployed component | proposed | 2026-10-05 |
| [0074](0074-ploeg-exposes-delivery-facts-and-consumers-own-the-card-and-every-formula-over-them.md) | Ploeg exposes delivery facts, and consumers own the card and every formula over them | proposed | 2026-10-06 |
| [0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md) | A stopped writing Run leaves a checkpoint its retry starts from | proposed | 2026-10-05 |
| [0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md) | Run health is observable by Team and Role through one correlation key | proposed | 2026-10-05 |

## Review calendar

Derived from `review-by`; the validator guarantees every dated entry has named
triggers.

| Due | ADRs |
| --- | --- |
| 2026-10-22 | [0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md) — after the first `claude-code` and reviewing Runs against a repository with its own AGENTS.md |
| 2026-10-31 | [0006](0006-ahp-is-the-wrong-layer.md), [0007](0007-a2a-adopt-nothing-watchlist-a-facade.md), [0008](0008-litellm-is-the-credential-and-metering-seam.md), [0009](0009-paperclip-mine-for-design-never-integrate.md), [0024](0024-operator-work-uses-one-execution-authority.md), [0025](0025-management-authority-stays-in-the-control-plane.md), [0041](0041-the-openai-agents-api-stays-outside-the-run-until-it-takes-an-authorized-budget.md) — the quarterly market re-scan (`design.md` §10) |
| 2026-11-30 | [0031](0031-runs-create-work-items-held-for-approval-within-limits.md) — or sooner, when the owner answers whether created Work Items are written back to the tracker and whether a person approves them |
| 2026-12-31 | [0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) — or sooner, when worker pods can reach a Go module proxy |
| 2026-12-31 | [0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md) — or sooner, when the first `registries` Run's verification passes `go test` or a denial alert fires that no build explains |
| 2026-12-31 | [0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md) — or sooner, when the target registry passes 15 entries or a misroute reaches a merged pull request |
| 2026-12-31 | [0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md) — or sooner, when LiteLLM prices advisor iterations per model or a Team runs `claude-code` |
| 2026-12-31 | [0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md) — or sooner, when the homelab Forgejo reaches v17 or conflict results start being rejected |
| 2027-01-04 | [0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md), [0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md), [0067](0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md), [0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md) — or sooner, when the measurement spike reports, selection moves to Omnigraph, context storage passes its limits, or steering between turns works on every shipped harness |
| 2027-01-31 | [0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [0011](0011-the-pull-request-is-the-blackboard.md), [0012](0012-two-level-budgets-authorized-and-settled.md), [0013](0013-push-rights-are-minted-per-run.md), [0017](0017-the-review-loop-is-verdict-driven-and-capped.md), [0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md), [0019](0019-a-failed-writing-run-reopens-its-round.md), [0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md), [0023](0023-the-forge-dialect-travels-on-the-work-item.md) — after the first real Shifts have run; all nine rest on assumptions only production can test |
| 2027-01-31 | [0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md) — or sooner, when a route needs review while its checks cannot run in the sandbox, a flaky check sends five Work Items a month to `needs_human`, or every supported forge reports its check status to Ploeg |
| 2027-01-31 | [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) — or sooner, when a qualified harness needs credentials from inside DinD |
| 2027-01-31 | [0033](0033-board-control-planes-are-mined-for-design-never-depended-on.md) — or sooner, when Paperclip ships bring-your-own ticket system or Multica publishes a stable daemon protocol |
| 2027-01-31 | [0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md) — or sooner, after 20 escalation briefs, or when Ploeg writes to the tracker as its own user |
| 2027-01-31 | [0042](0042-a-writing-run-reports-the-problem-and-solution-a-reviewer-reads.md) — or sooner, after the first 20 writer accounts, or when Vloer gains read access to the forge |
| 2027-01-31 | [0043](0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md), [0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md) — or sooner, when a reading Role reaches `MaxRunAttempts` twice in a month, or restarts from one close reason pass five in a month |
| 2027-01-31 | [0045](0045-keep-run-usage-and-merge-facts.md) — or sooner, when a third forge provider is added, a harness changes its usage shape, or the first card or KPI query is built on these facts |
| 2027-01-31 | [0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) — or sooner, when the owner decides rarity, grade or finish, tracker assignees are ingested, or a card read passes 200 ms at p95 |
| 2027-01-31 | [0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) — or sooner, when a GitOps controller should report deploys directly, deploys keep filling the 50-pull-request batch, or a third forge provider is added |
| 2027-01-31 | [0048](0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md) — or sooner, when a fix round closes on unsettled holds or a Shift parks as held more than once a month |
| 2027-01-31 | [0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md) — or sooner, when a card read during a Run passes 1 s at p95, LiteLLM offers a per-key token total, or the worker reports usage during a Run |
| 2027-01-31 | [0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md) — or sooner, when crack confirmation or revert detection lands, the owner asks for a frozen grade, or twenty cards are graded |
| 2027-01-31 | [0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) — or sooner, when a lost webhook explains a missing gate stay, Ploeg registers ClickUp webhooks, or tracker users are linked to forge logins |
| 2027-01-31 | [0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md) — or sooner, when operator actors are linked to forge logins, ten disputes are resolved, twenty cracks are confirmed, or a fix outside a Ploeg Work Item must mend a crack |
| 2027-01-31 | [0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md) — or sooner, when Size points exist, a team wants tracker-only subtasks to count, or Ploeg polls tracker relations |
| 2027-01-31 | [0054](0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md) — or sooner, when a card list request passes 1 s at p95, short pages with a cursor become common, or tracker users are linked to forge logins |
| 2027-01-31 | [0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md) — or sooner, when a forge refuses SVG attachments, the owner wants today's days live in the image, a skin with its own layout is built, or a sweep tick passes 1 s at p95 |
| 2027-01-31 | [0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md) — or sooner, when a Work Target reaches 30 revealed cards in a quarter, complexity or estimates get a source, unrecorded files pass 5 % of a quarter's merged plays, or the owner asks for Team cohorts or a reveal at acceptance |
| 2027-01-31 | [0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md) — or sooner, when a lost webhook explains a missing status stay, most boards override the default status kinds, a card list with flow passes 1 s at p95, or a tracker offers readable status history |
| 2027-01-31 | [0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md) — or sooner, when Forgejo exposes jobs per run, capture reads show in forge rate limits, the owner wants complexity in rarity, a card list passes 1 s at p95, or measured cards mislead |
| 2027-01-31 | [0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) — or sooner, when a worker shares a process with its agent, `infra_forge` sends five Work Items a week to a human, a third forge provider is added, or no older worker reports for two weeks |
| 2027-01-31 | [0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md) — or sooner, when inbox or outbox throughput passes 50 rows a second, a provider signs its delivery id, a tracker strips both markers, a second deployment shares a forge or tracker, or five required publications go dead in a month |
| 2027-01-31 | [0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md) — or sooner, when the owner asks for a grade that never changes, reviewer findings or first-check CI get a source, a quarter of a month's graded cards miss an input, or twenty cards are graded under 2026.3 |
| 2027-01-31 | [0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md) — or sooner, when Runs in flight pass 200, agent-sandbox publishes a light Go client or renames `PodScheduled`, or a security review moves claim authority out of ploegd |
| 2027-01-31 | [0074](0074-ploeg-exposes-delivery-facts-and-consumers-own-the-card-and-every-formula-over-them.md) — or sooner, when a second consumer needs the same card, a fact's ingest passes 10% of ploegd's forge calls or rows, or a Ploeg decision needs a derived figure |
| 2027-01-31 | [0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md) — or sooner, when agent-sandbox ships pause and resume, a forge drops refs outside heads and tags, or checkpointed retries fail more than cold ones |
| 2027-01-31 | [0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md) — or sooner, when the worker sandbox can reach an OTLP collector, a Team passes 200 series, or an incident is diagnosed from logs alone |
| 2027-04-01 | [0005](0005-build-a-dedicated-dispatch-plane.md), [0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md), [0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md), [0073](0073-ploeg-stays-in-go-and-admits-rust-only-as-a-separately-deployed-component.md) — the project review gate (`design.md` §10) |
