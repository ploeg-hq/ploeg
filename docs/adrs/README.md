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
| [0024](0024-operator-work-uses-one-execution-authority.md) | Ploeg admits operator work and owns execution while De Vloer provides interaction | superseded by 0102 | 2026-09-10 |
| [0025](0025-management-authority-stays-in-the-control-plane.md) | Management authority stays in the control plane and unresolved accounting retains authorization | proposed | 2026-09-10 |
| [0026](0026-tracker-selections-bind-the-canonical-work-item.md) | Tracker selections bind canonical Work Items under durable operator ownership | proposed | 2026-09-11 |
| [0027](0027-candidate-delivery-uses-trusted-evidence-and-a-publication-barrier.md) | Candidate delivery uses trusted evidence and a publication barrier | accepted | 2026-10-10 |
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
| [0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) | A Run card is assembled per Work Item from stored facts | rejected | 2026-10-10 |
| [0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) | Ploeg learns where a merged change is deployed from a generic deploy endpoint | proposed | 2026-10-01 |
| [0048](0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md) | A pool held by unsettled Runs waits for settlement before it parks | accepted | 2026-10-04 |
| [0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md) | Ploeg reads the gateway for the usage so far of a running Run (narrowed by 0080; was the Run card's live usage) | proposed | 2026-10-10 |
| [0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md) | A Run card's grade is a versioned formula over stored facts | rejected | 2026-10-10 |
| [0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) | Delivery gates are mapped per board from tracker statuses, and Ploeg records gate moves and bounce reasons (narrowed by 0080) | proposed | 2026-10-10 |
| [0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md) | A crack needs the fixer and a second person, and Ploeg only proposes candidates | rejected | 2026-10-10 |
| [0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md) | Ploeg records the parents a tracker reports for a Work Item and when it first saw them (narrowed by 0080; was the epic set) | proposed | 2026-10-10 |
| [0054](0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md) | A card list finds cards by roster login, newest activity first | rejected | 2026-10-10 |
| [0055](0055-ploeg-keeps-one-card-comment-with-a-static-card-image-on-the-pull-request.md) | Ploeg keeps one card comment with a static card image on the pull request | rejected | 2026-10-10 |
| [0056](0056-a-run-cards-rarity-is-its-challenge-predicted-at-mint-and-frozen-at-release.md) | A Run card's rarity is its challenge, predicted at mint and frozen at release | rejected | 2026-10-10 |
| [0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md) | Ploeg records every tracker status move of a board with gates, and the tracker's creation time and estimate (narrowed by 0080; was the Run card's flow figures) | proposed | 2026-10-10 |
| [0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md) | Ploeg reads pull request activity and CI runs from the forge, and keeps per-file indentation at the merge (narrowed by 0080; was the Run card's pipeline and shape figures) | proposed | 2026-10-10 |
| [0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) | Delivery facts come from the forge, never from the agent's outcome | proposed | 2026-10-03 |
| [0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md) | Authenticated webhooks go through a durable inbox, and required publications through an outbox | proposed | 2026-10-03 |
| [0061](0061-a-run-cards-grade-penalizes-rework-not-review-and-says-which-inputs-it-missed.md) | A Run card's grade penalizes rework, not review, and says which inputs it missed | rejected | 2026-10-10 |
| [0062](0062-github-is-the-independent-project-home.md) | GitHub is the independent project home | accepted | 2026-10-03 |
| [0063](0063-independent-releases-start-at-zero-point-one.md) | Independent releases start at 0.1.0 | superseded by 0115 | 2026-10-03 |
| [0064](0064-development-is-trunk-and-release-candidates-come-from-it.md) | Development is trunk, and release candidates come from it | superseded by 0116 | 2026-10-04 |
| [0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md) | A Run is briefed with an OKF knowledge pack written outside the clone | proposed | 2026-10-04 |
| [0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md) | A Run's learnings are proposals a person reviews before any Run sees them | proposed | 2026-10-04 |
| [0067](0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md) | Context bundles are stored per Work Item and unpacked by the worker under fixed limits | accepted | 2026-10-04 |
| [0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md) | Context added while a Shift runs reaches the next Run | accepted | 2026-10-04 |
| [0069](0069-ploeg-names-none-of-its-consumers.md) | Ploeg names none of its consumers | accepted | 2026-10-04 |
| [0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md) | A pull request is ready for review only when its checks passed on the pushed commit | superseded by 0119 | 2026-10-04 |
| [0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md) | Ploeg keeps its state in PostgreSQL and is not a Kubernetes operator | proposed | 2026-10-05 |
| [0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md) | KEDA stays, and ploegd reserves a Run for each launcher | proposed | 2026-10-06 |
| [0073](0073-ploeg-stays-in-go-and-admits-rust-only-as-a-separately-deployed-component.md) | Ploeg stays in Go, and admits Rust only as a separately deployed component | proposed | 2026-10-05 |
| [0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md) | Ploeg exposes its execution facts, and consumers collect everything else | superseded by 0079 | 2026-10-06 |
| [0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md) | A stopped writing Run leaves a checkpoint its retry starts from | proposed | 2026-10-05 |
| [0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md) | Run health is observable by Team and Role through one correlation key | proposed | 2026-10-05 |
| [0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md) | Ploeg knows Work Sources and Change Destinations, never vendors | proposed | 2026-10-06 |
| [0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md) | Runs reach read-only MCP tools only through the gateway, bounded by a LiteLLM team | proposed | 2026-10-08 |
| [0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) | Run cards belong to the operator consumer, and Ploeg supplies delivery facts | accepted | 2026-10-10 |
| [0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md) | Ploeg keeps no Run card code; its card routes, tables and settings go in one release | accepted | 2026-10-10 |
| [0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md) | Ploeg serves its operator reads as MCP tools through a separate ploeg-mcp command | accepted | 2026-10-10 |
| [0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md) | An Ask is a read-only Run outside the Shift, paid from a periodic allowance | accepted | 2026-10-13 |
| [0083](0083-a-run-is-told-which-skills-bear-on-its-work-item.md) | A Run is told which skills bear on its Work Item | accepted | 2026-10-11 |
| [0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md) | A claude-code Run can execute in its Team's self-hosted Claude environment, paid by the Team's plan | accepted | 2026-10-11 |
| [0085](0085-a-team-label-on-an-issue-in-a-configured-github-repository-dispatches-it.md) | A team label on an issue in a configured GitHub repository dispatches it, read and written back with a repository-scoped GitHub App token | proposed | 2026-10-11 |
| [0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md) | GitHub is a forge, and a Run pushes with its own GitHub App installation token | proposed | 2026-10-11 |
| [0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md) | ploegd holds a Tenant's knowledge graphs and serves a Run only its Work Item's scopes | proposed | 2026-10-11 |
| [0088](0088-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md) | A Run writes Work Item Notes without review, and a wider scope is a Promotion a person accepts | proposed | 2026-10-11 |
| [0089](0089-operator-consumers-read-knowledge-and-write-notes-for-a-named-person-within-scopes-ploegd-checks.md) | Operator consumers read knowledge and write notes for a named person within scopes ploegd checks | proposed | 2026-10-11 |
| [0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md) | A Run's agent session is kept as a bounded, redacted transcript | proposed | 2026-10-11 |
| [0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md) | Each Run records the model, harness, plan and ploegd version it was configured with | proposed | 2026-10-11 |
| [0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md) | A Team pins its skills in a catalogue that ploegd fetches once and serves by digest | proposed | 2026-10-13 |
| [0093](0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md) | The working state is active, budget kept back is a Hold, and every cost has one Cost Status | accepted | 2026-10-13 |
| [0094](0094-a-person-connects-a-teams-claude-login-from-unfold-and-the-teams-worker-signs-in.md) | A person connects a team's Claude login from Unfold, and the team's own worker signs in | rejected | 2026-10-17 |
| [0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md) | Work is handed to Ploeg by assigning one Ploeg user, and a team label picks the team | accepted | 2026-10-17 |
| [0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md) | A shadow reader records its verdict and never acts | proposed | 2026-10-13 |
| [0097](0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md) | A Run records its Harness version, and the Teams read shows each Role's Harness and model | accepted | 2026-10-13 |
| [0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md) | An operator admission may choose each Run's Harness from its Team's allowlist | accepted | 2026-10-14 |
| [0099](0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md) | Each allowed Harness runs in its own workload, and the claim selects on it | accepted | 2026-10-14 |
| [0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md) | An attended Run relays its Harness's requests through ploegd and falls back to its Team's policy | proposed | 2026-10-13 |
| [0101](0101-a-paused-run-keeps-its-pod-and-session-for-a-bounded-time-and-is-never-retried-automatically.md) | A paused Run keeps its pod and session for a bounded time and is never retried automatically | proposed | 2026-10-13 |
| [0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md) | An Operator Consumer admits Work Items that ploeg-worker executes | accepted | 2026-10-14 |
| [0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md) | An operator admission may choose each Run's model from the models its Role's policy lists | accepted | 2026-10-14 |
| [0104](0104-a-message-to-an-attended-run-reaches-it-at-the-next-prompt-turn-and-context-items-still-wait.md) | A message to an attended Run reaches it at the next prompt turn, and context items still wait for the next Run | proposed | 2026-10-13 |
| [0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md) | A credential leak parks the Work Item and closes its pull request | accepted | 2026-10-17 |
| [0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) | A Run declares an isolation floor, and a cell runs it at the strictest class it can honour | proposed | 2026-10-17 |
| [0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md) | Execution runs in cells that pull from one hub | proposed | 2026-10-17 |
| [0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) | Every operator action names its person and channel, and an MCP action needs the person's confirmation | accepted | 2026-10-17 |
| [0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md) | A pull request someone else opened is read by a Team on the pull request, and what the reader delivers is the route's instruction | proposed | 2026-10-17 |
| [0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) | A Team is Ploeg's own manifest of charter, crew, permissions, intake and skills, with borrowed shapes and no borrowed schema | proposed | 2026-10-17 |
| [0111](0111-a-spent-litellm-budget-wider-than-the-runs-key-parks-the-work-item.md) | A spent LiteLLM budget wider than the Run's key parks the Work Item | accepted | 2026-10-17 |
| [0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md) | A claude-environment Run is started by the person's own Claude Code, and Ploeg stores no Claude login | proposed | 2026-10-18 |
| [0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md) | A Run's Harness exports OpenTelemetry to a collector set per Team and Role | proposed | 2026-10-17 |
| [0114](0114-each-run-records-the-limits-that-stop-its-prompt.md) | Each Run records the limits that stop its prompt | proposed | 2026-10-18 |
| [0115](0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md) | Candidates are calculated from conventional commits, and a stable release stays a reviewed tag | accepted | 2026-10-18 |
| [0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md) | Every releasable merge to development cuts a candidate | accepted | 2026-10-18 |
| [0117](0117-every-release-publishes-a-contract-bundle-for-consumers.md) | Every release publishes a contract bundle for consumers | accepted | 2026-10-18 |
| [0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md) | The unmaintained OpenHands CLI is retired as a Harness, for OpenCode, Qwen Code and Goose | proposed | 2026-10-18 |
| [0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) | A pull request is ready for review only when the forge's CI passed on its head | accepted | 2026-10-18 |
| [0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md) | A worker container meets the restricted Pod Security Standard by default | accepted | 2026-10-18 |
| [0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md) | Findings carry ids, writers answer them, and a Run asks instead of guessing | accepted | 2026-10-18 |
| [0122](0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md) | Every note carries its provenance, untrusted text is quoted, and a wider level needs evidence or a person | accepted | 2026-10-18 |
| [0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md) | Dispatch waits for tracker dependencies and an opt-in free premise check, and a briefing names overlapping active work | accepted | 2026-10-18 |
| [0124](0124-agents-communicate-through-a-blackboard-of-four-owned-sources-that-ploeg-reads-at-a-version-vector.md) | Agents communicate through a blackboard of four owned sources that Ploeg reads at a version vector | accepted | 2026-10-18 |
| [0125](0125-a-writing-run-shows-its-change-in-a-walkthrough-a-person-can-repeat.md) | A writing Run shows its change in a walkthrough a person can repeat | proposed | 2026-10-18 |
| [0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md) | A recurring attack drill runs every harness against a hostile repository | accepted | 2026-10-18 |
| [0127](0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md) | A release opens each consumer's update by running the consumer's own recipe | accepted | 2026-10-18 |
| [0128](0128-a-chat-message-is-a-read-only-run-about-no-work-item-paid-from-a-teams-chat-allowance.md) | A chat message is a read-only Run about no Work Item, paid from a Team's Chat Allowance | proposed | 2026-10-18 |
| [0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md) | Ploeg reports how each board and repository it routes to is wired | proposed | 2026-10-19 |
| [0133](0133-a-run-is-told-its-limits-at-the-start-and-its-own-status-on-every-model-call.md) | A Run is told its limits at the start and its own status on every model call | proposed | 2026-10-20 |
| [0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) | Ploeg keeps each release compatible with the one before and removes only after a deprecated minor | proposed | 2026-10-23 |
| [0135](0135-a-bake-off-runs-one-work-item-on-several-configurations-and-a-shadow-judge-recommends-while-a-person-picks.md) | A bake-off runs one Work Item on several configurations and a shadow judge recommends while a person picks | proposed | 2026-10-23 |
| [0136](0136-the-chart-default-is-the-strict-isolation-profile-and-privileged-dind-is-an-acknowledged-opt-in.md) | The chart default is the strict isolation profile, and privileged DinD is an acknowledged opt-in | proposed | 2026-10-23 |
| [0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md) | The chart renders a default-deny NetworkPolicy for every pod, with an allowlist derived from its URLs | proposed | 2026-10-23 |
| [0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) | A harness runs in its own container, and no credential enters it | proposed | 2026-10-23 |
| [0140](0140-documentation-is-cc-by-4-0-and-code-and-api-specifications-stay-apache-2-0.md) | Documentation is CC-BY-4.0, and code and API specifications stay Apache-2.0 | accepted | 2026-10-24 |
| [0141](0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md) | The claude-runner image carries no Claude Code and installs Anthropic's signed release at start | proposed | 2026-10-24 |

## Review calendar

Derived from `review-by`; the validator guarantees every dated entry has named
triggers.

| Due | ADRs |
| --- | --- |
| 2027-04-30 | [0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md) — or sooner, when MCP standardizes proof of a person's confirmation, a deployment needs per-person authorization finer than groups, or a consumer needs Ploeg to verify the person itself |
| 2027-04-30 | [0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md) — or sooner, when a new harness or ACP profile is added, a supported agent's major version changes how it loads repository config, plugins or instructions, `OPENCODE_DISABLE_PROJECT_CONFIG` changes meaning, or the credential-isolation mechanism changes shape |
| 2026-10-22 | [0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md) — after the first `claude-code` and reviewing Runs against a repository with its own AGENTS.md |
| 2026-10-31 | [0006](0006-ahp-is-the-wrong-layer.md), [0007](0007-a2a-adopt-nothing-watchlist-a-facade.md), [0008](0008-litellm-is-the-credential-and-metering-seam.md), [0009](0009-paperclip-mine-for-design-never-integrate.md), [0024](0024-operator-work-uses-one-execution-authority.md), [0025](0025-management-authority-stays-in-the-control-plane.md), [0041](0041-the-openai-agents-api-stays-outside-the-run-until-it-takes-an-authorized-budget.md) — the quarterly market re-scan (`design.md` §10) |
| 2026-11-30 | [0031](0031-runs-create-work-items-held-for-approval-within-limits.md) — or sooner, when the owner answers whether created Work Items are written back to the tracker and whether a person approves them |
| 2026-12-31 | [0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) — or sooner, when worker pods can reach a Go module proxy |
| 2026-12-31 | [0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md) — or sooner, when the first `registries` Run's verification passes `go test` or a denial alert fires that no build explains |
| 2026-12-31 | [0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md) — or sooner, when the target registry passes 15 entries or a misroute reaches a merged pull request |
| 2026-12-31 | [0039](0039-a-run-calls-only-its-roles-model-and-the-advisor-waits-for-metering.md) — or sooner, when LiteLLM prices advisor iterations per model or a Team runs `claude-code` |
| 2026-12-31 | [0040](0040-a-conflicted-pull-request-becomes-a-priority-ticket-ploeg-resolves.md) — or sooner, when the homelab Forgejo reaches v17 or conflict results start being rejected |
| 2027-01-04 | [0065](0065-a-run-is-briefed-with-an-okf-knowledge-pack-written-outside-the-clone.md), [0066](0066-a-runs-learnings-are-proposals-a-person-reviews-before-any-run-sees-them.md), [0067](0067-context-bundles-are-stored-per-work-item-and-unpacked-by-the-worker-under-fixed-limits.md), [0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md) — or sooner, when the measurement spike reports, selection moves to Omnigraph, context storage passes its limits, or steering between turns works on every shipped harness |
| 2027-01-11 | [0087](0087-ploegd-holds-a-tenants-knowledge-graphs-and-serves-a-run-only-its-work-items-scopes.md), [0088](0088-a-run-writes-work-item-notes-without-review-and-a-wider-scope-is-a-promotion-a-person-accepts.md), [0089](0089-operator-consumers-read-knowledge-and-write-notes-for-a-named-person-within-scopes-ploegd-checks.md) — or sooner, when Omnigraph policy gains row or branch-prefix rules, a wrong Work Item Note costs two pull requests in a month, the first 20 Promotions are reviewed, or consumers can forward a signed person token |
| 2027-01-18 | [0115](0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md), [0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md) — or sooner, when the owner automates stable releases from `main`, the shared release configuration changes its analyzer rules, GitHub lets a workflow token's tag push start other workflows, or a candidate's publication fails twice in a month |
| 2027-01-18 | [0117](0117-every-release-publishes-a-contract-bundle-for-consumers.md) — or sooner, when the codes in the Go files get their own schema, a second consumer needs content the bundle leaves out, or GHCR stops serving anonymous pulls of the package |
| 2027-01-18 | [0127](0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md) — or sooner, when a second forge needs a consumer, Forgejo changes `merge_when_checks_succeed`, or a stable release needs to reach a consumer |
| 2027-01-31 | [0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [0011](0011-the-pull-request-is-the-blackboard.md), [0012](0012-two-level-budgets-authorized-and-settled.md), [0013](0013-push-rights-are-minted-per-run.md), [0017](0017-the-review-loop-is-verdict-driven-and-capped.md), [0018](0018-the-outcome-drop-box-is-every-harnesss-return-path.md), [0019](0019-a-failed-writing-run-reopens-its-round.md), [0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md), [0023](0023-the-forge-dialect-travels-on-the-work-item.md) — after the first real Shifts have run; all nine rest on assumptions only production can test |
| 2027-01-31 | [0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md) — or sooner, when a Team's premise check bounces over 20 % of what people requeue unchanged, the free endpoint's terms or price change, or typed questions (ADR-0121) land |
| 2027-01-31 | [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) — or sooner, when a qualified harness needs credentials from inside DinD |
| 2027-01-31 | [0033](0033-board-control-planes-are-mined-for-design-never-depended-on.md) — or sooner, when Paperclip ships bring-your-own ticket system or Multica publishes a stable daemon protocol |
| 2027-01-31 | [0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md) — or sooner, after 20 escalation briefs, or when Ploeg writes to the tracker as its own user |
| 2027-01-31 | [0042](0042-a-writing-run-reports-the-problem-and-solution-a-reviewer-reads.md) — or sooner, after the first 20 writer accounts, or when Vloer gains read access to the forge |
| 2027-01-31 | [0043](0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md), [0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md) — or sooner, when a reading Role reaches `MaxRunAttempts` twice in a month, or restarts from one close reason pass five in a month |
| 2027-01-31 | [0045](0045-keep-run-usage-and-merge-facts.md) — or sooner, when a third forge provider is added, a harness changes its usage shape, or the first card or KPI query is built on these facts |
| 2027-01-31 | [0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md) — or sooner, when self-hosted environments leave beta, Anthropic changes its terms for plan logins, or exposes session usage through an API |
| 2027-01-31 | [0085](0085-a-team-label-on-an-issue-in-a-configured-github-repository-dispatches-it.md) — or sooner, when the ADR-0060 inbox is built, the GitHub forge provider lands, a deployment needs Projects columns or status labels, or a repository rename duplicates a Work Item |
| 2027-01-31 | [0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md) — or sooner, when GitHub scopes installation tokens to a ref or lists them, ploegd runs more than one replica, or a proxied Run loses push rights at the hour mark |
| 2027-01-31 | [0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md) — or sooner, when transcripts pass 10 GiB, a stored transcript carries a missed credential, a consumer needs faster than polling, a non-ACP harness needs a transcript, or ACP v2 changes `session/update` |
| 2027-01-31 | [0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md) — or sooner, when the ADR-0076 metrics are built, the gateway reports served models under another name than the alias, ploegd learns each Role's harness itself, or a Role is configured with more than one model |
| 2027-01-31 | [0092](0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md) — or sooner, when the Agent Skills specification merges a version or index format, APM reaches 1.0, a harness reads OCI skills natively, or the skill store passes 100 MiB |
| 2027-01-31 | [0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md) — or sooner, when 20 bronze Work Items have a shadow review, a Shift is refused a Round on budget a shadow held, a harness can post from a reading Run, or a consumer needs findings agreement scored |
| 2027-01-31 | [0097](0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md) — or sooner, when ACP makes `agentInfo` required, a Team's Harness prints no usable version within 5 s, or ploegd selects the Harness per Run (0098) |
| 2027-01-31 | [0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [0099](0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md) — or sooner, when a consumer needs a Harness per Role in one session, tracker work needs a per-Work-Item Harness, an allowlist passes four entries, or Harness cold starts pass 60 s at p50 |
| 2027-01-31 | [0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md), [0101](0101-a-paused-run-keeps-its-pod-and-session-for-a-bounded-time-and-is-never-retried-automatically.md) — or sooner, when ACP v2 adds multi-client sessions, backlog #101's second gate is met, agent-sandbox ships pause, or a fifth of pauses in a month end `paused_too_long` |
| 2027-01-31 | [0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md) — or sooner, when a consumer needs a plan override per Admission, polling the operator event feed measurably loads ploegd, ADR-0100 or ADR-0101 is rejected, or Operator Executions are still admitted |
| 2027-01-31 | [0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md) — or sooner, when a Role's policy lists more than four models, a consumer needs a model no Role of its Team lists, LiteLLM prices advisor iterations per model, or a chosen model regularly exhausts its Role's cap |
| 2027-01-31 | [0104](0104-a-message-to-an-attended-run-reaches-it-at-the-next-prompt-turn-and-context-items-still-wait.md) — or sooner, when people weekly ask an attended Run to read a file they just attached, a non-ACP Harness gains mid-Run input, or ADR-0100 is rejected |
| 2027-01-31 | [0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md) — or sooner, when ADR-0060's outbox is built, a leak-guard false positive parks a Work Item twice in a month, a second failure reason is proposed to park, or a forge refuses a leak close ploegd could fix |
| 2027-01-31 | [0111](0111-a-spent-litellm-budget-wider-than-the-runs-key-parks-the-work-item.md) — or sooner, when LiteLLM reports when a spent budget resets, a Work Item parks `shared_budget_spent` wrongly twice in a month, LiteLLM changes the wording that names a budget's owner, or a third failure reason is proposed to park |
| 2027-01-31 | [0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) — or sooner, when a GitOps controller should report deploys directly, deploys keep filling the 50-pull-request batch, or a third forge provider is added |
| 2027-01-31 | [0048](0048-a-pool-held-by-unsettled-runs-waits-for-settlement-before-it-parks.md) — or sooner, when a fix round closes on unsettled holds or a Shift parks as held more than once a month |
| 2027-01-31 | [0049](0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md) — or sooner, when a card read during a Run passes 1 s at p95, LiteLLM offers a per-key token total, or the worker reports usage during a Run |
| 2027-01-31 | [0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) — or sooner, when a lost webhook explains a missing gate stay, Ploeg registers ClickUp webhooks, or tracker users are linked to forge logins |
| 2027-01-31 | [0053](0053-an-epic-is-a-set-of-the-work-items-declared-its-children-before-their-first-shift.md) — or sooner, when Size points exist, a team wants tracker-only subtasks to count, or Ploeg polls tracker relations |
| 2027-01-31 | [0057](0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md) — or sooner, when a lost webhook explains a missing status stay, most boards override the default status kinds, a card list with flow passes 1 s at p95, or a tracker offers readable status history |
| 2027-01-31 | [0058](0058-a-run-cards-pull-request-ci-and-change-shape-figures-are-read-from-the-forge-and-kept-per-play.md) — or sooner, when Forgejo exposes jobs per run, capture reads show in forge rate limits, the owner wants complexity in rarity, a card list passes 1 s at p95, or measured cards mislead |
| 2027-01-31 | [0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md) — or sooner, when a worker shares a process with its agent, `infra_forge` sends five Work Items a week to a human, a third forge provider is added, or no older worker reports for two weeks |
| 2027-01-31 | [0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md) — or sooner, when inbox or outbox throughput passes 50 rows a second, a provider signs its delivery id, a tracker strips both markers, a second deployment shares a forge or tracker, or five required publications go dead in a month |
| 2027-01-31 | [0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md) — or sooner, when KEDA is not acceptable on a target cluster, Runs in flight pass 200, warm starts drop below 30 s, or agent-sandbox drops warm adoption |
| 2027-01-31 | [0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md) — or sooner, when a Ploeg decision needs a dropped feed, two consumers need the same collected facts, or the collector loses status history twice in a quarter |
| 2027-01-31 | [0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md) — or sooner, when a Ploeg decision needs a figure the consumer computes, a deployment cannot remove a retired key before the next minor release, or a second consumer needs a fact the facts endpoints lack |
| 2027-01-31 | [0075](0075-a-stopped-writing-run-leaves-a-checkpoint-its-retry-starts-from.md) — or sooner, when agent-sandbox ships pause and resume, a forge drops refs outside heads and tags, or checkpointed retries fail more than cold ones |
| 2027-01-31 | [0076](0076-run-health-is-observable-by-team-and-role-through-one-correlation-key.md) — or sooner, when the worker sandbox can reach an OTLP collector, a Team passes 200 series, or an incident is diagnosed from logs alone |
| 2027-01-31 | [0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md) — or sooner, when a team asks for a write-capable access group, LiteLLM stops bounding a key's access groups by its team, `/key/generate` accepts a team alias, or an opted-in Run takes an instruction from a tool result |
| 2027-01-31 | [0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md) — or sooner, when a consumer needs a Client or Tenant scope, more than one call per Ask, or to raise a limit inside a period |
| 2027-01-31 | [0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md) — or sooner, when the first ten readings have been read, a GitHub or GitLab target is routed, a push without a title change should have been read, a target's `maxOpen` is reached in a month, a per-route `autoDispatch` is asked for, or a route needs to pick pull requests by label or title |
| 2027-01-31 | [0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) — or sooner, when a foundation-governed spec adds a team object, a permission grant or a money budget, agents.yaml or OpenEnvelope ships a consumer, kagent or Ark re-adds a Team CRD with a budget or permissions, Agent Skills' `allowed-tools` leaves experimental, OTel GenAI adds a cost attribute, or Paperclip publishes its Team schema |
| 2027-01-31 | [0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md) — or sooner, when a tracker gains a first-class agent or delegate field, or most work starts through an operator consumer |
| 2027-01-31 | [0112](0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md) — or sooner, when Anthropic answers the 2026-10-18 terms question, offers delegated or session-only authorization, or waiting for a person proves too slow for tracker work |
| 2027-01-31 | [0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md) — or sooner, when a Harness Ploeg runs gains native OTLP export, ploegd emits a trace per Run, or a collector needs authentication headers from a Secret |
| 2027-01-31 | [0114](0114-each-run-records-the-limits-that-stop-its-prompt.md) — or sooner, when ploegd learns each Role's timeouts itself, a Harness gains a limit of its own or runs more than one prompt turn per Run, or an operator consumer needs the agent's last activity from Ploeg |
| 2027-01-31 | [0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md) — or sooner, when the OpenHands CLI is maintained again, the OpenHands SDK ships an ACP agent of its own, or OpenCode, Qwen Code or Goose fails qualification or stops being maintained |
| 2027-01-31 | [0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md) — or sooner, when Forgejo reports cancelled CI as its own state, more than five Work Items a month close as `ci_not_finished` or as `ci_not_passed` on a red base, or a route needs review while its CI cannot run |
| 2027-01-31 | [0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md) — or sooner, when a supported harness image needs root at run time, Kubernetes changes what `restricted` requires, or a Team needs a different uid per Role |
| 2027-01-31 | [0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md) — or sooner, when 20 fix Rounds have run with findings by id, a reviewer model routinely rewrites findings instead of raising them again by id, people forget to requeue after answering, or a consumer needs findings agreement scored by ploegd |
| 2027-01-31 | [0122](0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md) — or sooner, when Ploeg records Clients, a hosted Tenant asks for a Client's knowledge to be erased, Omnigraph gains per-key preconditions or row-level policy, notes wait on a 412 more than five times a day, or an agent follows a quoted note as an instruction |
| 2027-01-31 | [0124](0124-agents-communicate-through-a-blackboard-of-four-owned-sources-that-ploeg-reads-at-a-version-vector.md) — or sooner, when a harness other than ACP can take a further prompt turn, Omnigraph can read at a given commit, the usefulness query shows Work Item Notes change nothing over 50 Shifts or a level omits items in a quarter of briefings, or one catch-up in twenty changes nothing |
| 2027-01-31 | [0125](0125-a-writing-run-shows-its-change-in-a-walkthrough-a-person-can-repeat.md) — or sooner, when ten opted-in writing Runs have reported a walkthrough, a reviewer finds a credential or personal data in a screenshot, someone asks for a preview environment per pull request or for the screenshots on the forge, or object storage replaces bytea for context files |
| 2027-01-31 | [0128](0128-a-chat-message-is-a-read-only-run-about-no-work-item-paid-from-a-teams-chat-allowance.md) — or sooner, when a Team wants chats on another model or cap than its Asks, a consumer needs a chat scope other than a Team, a chat message regularly needs more than its budget, or a consumer needs Ploeg to keep the conversation |
| 2027-01-31 | [0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md) — or sooner, when Ploeg gains runtime registration of boards or targets, its forge credentials can read repository webhooks, a second tracker carries production work, or the hourly share check costs noticeably |
| 2027-01-31 | [0133](0133-a-run-is-told-its-limits-at-the-start-and-its-own-status-on-every-model-call.md) — or sooner, when 20 writing Runs have been told their limits, the gateway drops its pre-call hooks or a Harness bypasses it, trailing text breaks a provider's cached prefix, an agent acts on an advisory as an instruction, or a kind of Work Item needs more than 10 minutes of reading |
| 2027-01-31 | [0135](0135-a-bake-off-runs-one-work-item-on-several-configurations-and-a-shadow-judge-recommends-while-a-person-picks.md) — or sooner, when twenty bake-offs have ended with a choice, a Team asks for more than six attempts or for attempts that differ in prompt, skills or effort, a judge's reading costs more than a writing attempt, a person asks for unchosen diffs to be merged, or an editor lets an outside host supply attempts or a judge to its own comparison with cost shown |
| 2027-01-31 | [0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md) — or sooner, when the chart or its installers adopt a CNI-specific policy type for FQDN rules, Kubernetes network policy gains a cluster-level or FQDN form the chart can render, a rendered policy breaks an installer that already carries policies, the ADR-0037 registries profile lands in the chart, or five installers set `networkPolicy.enabled: false` |
| 2027-04-30 | [0081](0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md) — or sooner, when a person asks to propose, approve or cancel work from an AI client, someone needs the tools without a route to ploegd, or the MCP Go SDK ships a major version |
| 2027-04-01 | [0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) — or sooner, when the owner opens the 1.0 decision, a consumer breaks on a release the compatibility check passed, a minor collects five recorded breaks, an adopter asks for a longer window or wider skew, or the worker version handshake ships |
| 2027-04-01 | [0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) — or sooner, when per-Run credential injection outside the pod ships, a qualified harness cannot run behind the launcher or ten Runs a month fail with a launcher error, an installation still sets `shared` as the minor after 0.2 is prepared, the F-24 fix assumes a shared container, or a reviewer finds an unlisted same-pod channel |
| 2027-04-01 | [0141](0141-the-claude-runner-image-carries-no-claude-code-and-installs-anthropics-signed-release-at-start.md) — or sooner, when Anthropic allows redistribution or publishes Claude Code in a registry CNCF image guidance accepts, changes the manifest, signing key or bucket path, the worker pod template gains an init container for the harness image, or a runner fails to start because `downloads.claude.ai` was unreachable |
| 2027-04-01 | [0005](0005-build-a-dedicated-dispatch-plane.md), [0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md), [0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md), [0073](0073-ploeg-stays-in-go-and-admits-rust-only-as-a-separately-deployed-component.md), [0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md) — the project review gate (`design.md` §10) |
