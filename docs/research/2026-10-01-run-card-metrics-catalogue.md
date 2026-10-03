# Run card metrics catalogue

Status: research record, 2026-10-01. It is the catalogue the proposed [Run cards](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/concepts/run-cards.md) pick their numbers from, not a specification. What Ploeg stores and serves is defined by [ADR-0045](../adrs/0045-keep-run-usage-and-merge-facts.md), [ADR-0046](../adrs/0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md), [ADR-0047](../adrs/0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) and [ADR-0049](../adrs/0049-a-run-card-reads-the-gateway-for-usage-so-far-while-a-run-is-running.md), all written after this record.

**Question.** Which numbers could a card show, where does each come from, what does it cost to compute, how easily is it gamed, and where on the card does it belong?

**Method.** A read of Ploeg's migrations 0001 to 0022 and harness adapters; the Forgejo API swagger ([swagger.v1.json](https://code.forgejo.org/swagger.v1.json), 17.0-dev, checked 2026-10-01), its webhook types ([type.go](https://codeberg.org/forgejo/forgejo/src/branch/forgejo/modules/webhook/type.go)) and the matching [Gitea API reference](https://docs.gitea.com/api/); and the research on DORA, AI code quality and code survival listed under Sources.

**Limitations.** A catalogue to choose from: about 115 metrics, few validated. Costs are estimates. Survival research on AI code is from 2026 preprints.

## How to read the tables

| Column | Values |
| --- | --- |
| **Now?** | `P` computable with SQL over Ploeg's tables. `P+R` the data sits in the forge, git or tracker and can be read and backfilled any time. `NEW` thrown away at Run time; once a Run is gone, it is gone. |
| **Cost** | `$` a field or SQL. `$$` 1–3 API calls. `$$$` paginated API, or a clone plus static analysis. `$$$$` a recurring job (blame or survival snapshots). |
| **Fresh** | `once` frozen when the Run ends. `to-merge` moves during review, frozen at merge or close. `live` keeps changing. `@N` snapshots at 1/7/30/90/180/365 days. |
| **Game** | How easily a number moves without more value: `L`, `M`, `H`. |
| **Place** | `F` front, `B` back, `T` timeline event, `X` keep off the card. |
| **Grain** | `R` Run, `S` Shift or pull request, `W` Work Item. |

## What Ploeg stored on 2026-10-01

* `agent_runs`: times, outcome, summary, stuck and failure reasons, links, `usage` (input and output tokens, models, cost), Shift, Role, Round, writes, state, authorized amount, verdict, free-text findings, problem and solution.
* `shifts`: budget, spent, Round, close reason, branch, Team. `run_llm_accounts`: authorized, models, observed and reconciled spend. `work_items`: creation, pull request URL, Work Target, routing, infra failures, Follow-Up lineage, budget. `work_item_reviews`: human requests for changes. `audit_log`: claims, expiries, Shift closes, gateway and delivery events.

**Captured by harnesses and then discarded** (the `NEW` items to grab first; ADR-0045 has since kept the harness figures):

* Claude Code's JSON result carries `num_turns`, `duration_ms`, `duration_api_ms`, cache read and creation tokens, server tool use and per-model `modelUsage` ([field cheatsheet](https://takopi.dev/reference/runners/claude/stream-json-cheatsheet/)). The adapter kept only tokens, cost and session id.
* The ACP adapter tallied tool calls and context-window fill in memory only.
* The LiteLLM gateway logs every request per key with tokens, cache tokens, latency and spend, so these can be backfilled while LiteLLM keeps its logs ([prompt caching](https://docs.litellm.ai/docs/completion/prompt_caching), [custom pricing](https://docs.litellm.ai/docs/proxy/custom_pricing)).

## Forge data available

| Need | Forgejo endpoint | Useful fields |
| --- | --- | --- |
| Pull request | `GET /repos/{o}/{r}/pulls/{index}` | created, merged, closed, `merged_by`, additions, deletions, changed files, comments, merge commit, labels |
| Files | `…/pulls/{index}/files` | per-file status and counts (a pagination bug on empty pull requests is reported: [tektoncd issue](https://github.com/tektoncd/pipelines-as-code/issues/3011)) |
| Commits | `…/pulls/{index}/commits` | author, committer, stats, files |
| Reviews | `…/pulls/{index}/reviews` | state, submitted, comment count, stale, dismissed, official |
| CI | `…/commits/{ref}/status`, `…/actions/runs?head_sha=` | combined state, per-context status; run duration; job `attempt` (> 1 is a rerun) |
| Timeline | `…/issues/{index}/timeline` | review requests, pushes, labels, references |
| Reverse lookup | `…/commits/{sha}/pull` | which pull request introduced a commit |
| Releases | `…/releases`, `git/refs` | release time, a proxy for deploys |
| Webhooks | pull request, review, sync, push, release, workflow events | event-driven freshness |

**Forgejo has no blame endpoint.** Survival needs a clone (`git blame --porcelain`), so a scheduled "librarian" job on a mirror is the natural home. GitHub has the same endpoints; GitLab uses merge-request diffs, approvals and pipelines, which a provider adapter can normalise.

## A. Agent and Run economics

| ID | Metric | Definition | Grain | Cost | Fresh | Game | Place | Now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A1 | **Cost** | settled US$, reconciled first, provisional until settled | R/S | $ | once | L | **F** | P |
| A2 | **Budget share** | cost ÷ authorized; spent ÷ Shift budget | R/S | $ | once | M | **F** | P |
| A3 | Tokens in and out | from `usage` | R | $ | once | M | B | P |
| A4 | Output to input ratio | generative versus read-heavy | R | $ | once | M | B | P |
| A5 | **Cache hit ratio** | cache read ÷ all input; the main cost lever on long loops | R | $ | once | L | B | NEW |
| A6 | Cache savings | what caching saved | R | $ | once | L | B | NEW |
| A7 | Model mix | cost or tokens per model | R/S | $ | once | L | B | P (list) / NEW (shares) |
| A8 | Turns | agent loop iterations | R | $ | once | M | B | NEW |
| A9 | Tool calls | by kind, with failure rate | R | $ | once | M | B | NEW |
| A10 | Test executions | tool calls that ran the project's checks, and how many passed | R | $$ | once | M | B | NEW |
| A11 | Peak context fill | close to 100 % predicts compaction | R | $ | once | L | B | NEW |
| A12 | Wall-clock duration | finish − start | R/S | $ | once | L | B | P |
| A13 | Active versus wall-clock | model time versus tool time versus idle | R | $ | once | L | B | NEW |
| A14 | Idle stops | Runs stopped for silence | R/S | $ | once | L | B | P |
| A15 | **Rounds used** | "2 of 3 fix Rounds" | S | $ | to-merge | M | **F** | P |
| A16 | Runs and retries | by outcome | S | $ | to-merge | L | B | P |
| A17 | **Attempts per merged pull request** | Shifts ÷ merged pull requests | W | $ | to-merge | L | B | P |
| A18 | Infra failures | failures not the agent's fault | W | $ | once | L | B | P |
| A19 | Cost per changed line | padding improves it | S | $$ | once | H | B | P+R |
| A20 | Cost per surviving line at 90 days | much harder to game | S | $$$$ | @N | M | B | P+R |
| A21 | Cost by Role | writer, reviewer, fix Rounds | S | $ | once | L | B | P |
| A22 | Review overhead | reviewer cost ÷ writer cost | S | $ | once | L | B | P |
| A23 | Unsettled hold | reserved − settled | R | $ | live | L | X | P |
| A24 | Token Charge | proposed billing | S | $ | once | L | B (hosted) | NEW |
| A25 | Equivalent human hours | speculative ([DX AI Measurement Framework](https://getdx.com/blog/ai-measurement-framework-guide/)) | S | $ | once | H | X | n/a |

## B. Change anatomy

Counted lines exclude tests, lockfiles and generated files, like the Diff Limit; show raw and counted side by side.

| ID | Metric | Definition | Grain | Cost | Fresh | Game | Place | Now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| B1 | **Lines +/−** | raw and counted | S | $$ | to-merge | H as output, L as size | **F** (as size) | P+R |
| B2 | Files changed | by status | S | $$ | to-merge | M | B | P+R |
| B3 | Diff Limit usage | counted lines ÷ the Size's limit | S | $$ | to-merge | M | B | P+R |
| B4 | Languages | lines per language | S | $$ | to-merge | L | B | P+R |
| B5 | Test versus code lines | by path pattern | S | $$ | to-merge | H | B | P+R |
| B6 | Test-to-code ratio | pair with A10 and B21 | S | $ | to-merge | H | B | P+R |
| B7 | Intra-PR churn | lines written then rewritten before review | S | $$$ | to-merge | M | B | P+R |
| B8 | Dependency changes | manifests and lockfiles | S | $$ | to-merge | L | B badge | P+R |
| B9 | Migrations | migration paths | S | $$ | to-merge | L | B badge | P+R |
| B10 | Config and infra | values, workflows, Kustomize | S | $$ | to-merge | L | B badge | P+R |
| B11 | Attention Paths touched | `.unfold/attention` or CODEOWNERS | S | $$ | to-merge | L | B badge | P+R |
| B12 | Public API delta | `apidiff`, `oasdiff`, api-extractor; breaking or not | S | $$$ | to-merge | L | B | P+R |
| B13 | Cyclomatic complexity delta | changed functions only ([lizard](https://github.com/terryyin/lizard), [gocyclo](https://github.com/fzipp/gocyclo), [radon](https://radon.readthedocs.io/en/latest/)) | S | $$$ | to-merge | M | B | P+R |
| B14 | Cognitive complexity delta | ([SonarSource](https://www.sonarsource.com/docs/CognitiveComplexity.pdf), [gocognit](https://github.com/uudashr/gocognit)) | S | $$$ | to-merge | M | B | P+R |
| B15 | Functions over threshold | complexity above 15 | S | $$$ | to-merge | M | B | P+R |
| B16 | Duplication introduced | agents duplicate more ([Huang et al., MSR '26](https://arxiv.org/abs/2601.21276)) | S | $$$ | to-merge | M | B | P+R |
| B17 | Moved versus copied lines | GitClear-style classes | S | $$$ | to-merge | M | B | P+R |
| B18 | SLOC, comment, blank delta | ([scc](https://github.com/boyter/scc)) | S | $$ | to-merge | M | B | P+R |
| B19 | Binary and size delta | | S | $$ | to-merge | L | B | P+R |
| B20 | Static-analysis findings delta | | S | $$$ | to-merge | M | B | P+R |
| B21 | Patch coverage | needs CI to publish coverage | S | $$$ | to-merge | M | B | NEW |
| B22 | Scope adherence | files outside the named area | S | $$ | to-merge | M | B | P+R |
| B23 | COCOMO "value" | meaningless per change | S | $ | once | H | X | n/a |
| B24 | Maintainability Index | outdated, Python only | S | $$ | once | M | X | n/a |

## C. Review and flow

Exclude Unfold's reviewer bot from human figures and report agent review separately.

| ID | Metric | Definition | Grain | Cost | Fresh | Game | Place | Now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| C1 | **Time to merge** | open pull requests show their age | S | $$ | to-merge | M | **F** | P+R |
| C2 | Time to first human review | | S | $$ | to-merge | L | B | P / P+R |
| C3 | Human review rounds | requests for changes | S | $$ | to-merge | M | B | P+R |
| C4 | Review comments | | S | $$ | to-merge | H as quality | B | P+R |
| C5 | Approvals and reviewers | | S | $$ | to-merge | L | B | P+R |
| C6 | Review depth | comments per 100 counted lines; inspection rate ([SmartBear/Cisco study](https://static1.smartbear.co/support/media/resources/cc/book/code-review-cisco-case-study.pdf), [best practices](https://smartbear.com/learn/code-review/best-practices-for-peer-code-review/)) | S | $$ | to-merge | M | B | P+R |
| C7 | **CI green on first push** | | S | $$ | once | L | F candidate | P+R |
| C8 | CI duration | | S | $$ | to-merge | L | B | P+R |
| C9 | CI runs and failed checks | each extra failed check lowers an agentic pull request's merge odds by about 15 % ([Ehsani et al.](https://arxiv.org/html/2601.15195)) | S | $$ | to-merge | L | B | P+R |
| C10 | Flaky reruns | attempt > 1 with a flipped result | S | $$ | to-merge | L | B | P+R |
| C11 | Wait on human versus agent | what really explains time to merge | S | $$$ | to-merge | L | B | P+R |
| C12 | Merge conflicts hit | | S | $ | to-merge | L | B | P |
| C13 | **Reviewer Verdict** | per Round | R/S | $ | once | M | **F** | P |
| C14 | Reviewer findings | free text today | R | $ | once | H | B | P / NEW |
| C15 | **Reviewer false-positive rate** | needs per-finding ids and a disposition | S | $$ | to-merge | M | B | NEW |
| C16 | Reviewer escape rate | later bugs the agent reviewer approved | Role | $$$$ | live | L | X | P+R |
| C17 | Human agreement with agent reviewer | | S | $$ | to-merge | L | B | P+R |
| C18 | **Human touch %** | lines in the merged diff written by people | S | $$$ | to-merge | L | B (F candidate) | P+R |
| C19 | Acceptance type | explicit, merge without review, silent after 10 days | S | $$ | to-merge | L | B badge | P+R |
| C20 | Description accuracy | LLM-judged ([description alignment study](https://arxiv.org/html/2601.17627v1)) | S | $$$ | once | M | X | NEW |
| C21 | Per-reviewer speed | a person-level metric; needs an ethics and works council check | person | $$ | live | H | X | P+R |

## D. Production life

DORA's five ([DORA metrics](https://dora.dev/guides/dora-metrics/), [RedMonk on DORA 2025](https://redmonk.com/rstephens/2025/12/18/dora2025/)): three map onto a card; deployment frequency only makes sense in aggregate.

| ID | Metric | Definition | Grain | Cost | Fresh | Game | Place | Now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| D1 | Deployed at | first deploy containing the merge commit | S | $$ | once | L | T | P+R / NEW |
| D2 | **Change lead time** | deploy − first commit | S | $$ | once | M | B | P+R |
| D3 | Work Item to production | | W | $$ | once | M | B | P+R |
| D4 | Days live | ticks daily | S | $ | live | L | B | P+R |
| D5 | **Reverted** | a revert of the merge; within 14 days with an in-scope defect it is a Reversal | S | $$$ | live | L | T + B | P+R |
| D6 | Hotfix or follow-up fix | low confidence when only blame matched | S | $$$$ | live | L | T + B | P+R |
| D7 | Linked bugs and incidents | | S | $$ | live | L | T + B | P+R |
| D8 | Change failed | reverted, hotfixed within 7 days, or linked incident | S | $ | live | L | B | P+R |
| D9 | Recovery time | | S | $$$ | once | L | B | NEW |
| D10 | Rework deploys | | S | $$$ | live | L | X | NEW |
| D11 | Error-rate delta | confounded by concurrent deploys | S | $$$ | once | L | B | NEW |
| D12 | Latency and resource delta | | S | $$$ | once | L | B | NEW |
| D13 | Feature-flag exposure | | S | $$ | live | L | B | NEW |
| D14 | Preview usage | | S | $$ | to-merge | L | B (hosted) | NEW |

## E. Code survival

**Method (proposed).** At 1/7/30/90/180/365 days after merge, run `git blame --porcelain -w -M -C` over the touched files on a mirror and count lines still attributed to the change. Kaplan–Meier gives a half-life per cohort. This follows [Rahman & Shihab 2026](https://arxiv.org/html/2601.16809v1) and the cohort views of [git-of-theseus](https://github.com/erikbern/git-of-theseus), ["The half-life of code"](https://erikbern.com/2016/12/05/the-half-life-of-code.html) and [Hercules](https://blog.sourced.tech/post/hercules/).

**Caveat.** Agent lines survive *longer* than human lines (46.1 % versus 30.7 %), yet when modified they are more often corrected. Long survival can mean stable code or code nobody dares touch. Pair survival with who changed the lines and why.

| ID | Metric | Definition | Grain | Cost | Fresh | Game | Place | Now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| E1 | **Line survival @N** | lines still blamed ÷ lines added | S | $$$$ | @N | L | **F** + T | P+R |
| E2 | Code half-life | | S | $$$$ | live | L | B | P+R |
| E3 | Early churn, 21 days | GitClear: 3.1 % in 2020 to 5.7 % in 2024 ([2025 research](https://www.gitclear.com/ai_assistant_code_quality_2025_research), [PDF](https://gitclear-public.s3.us-west-2.amazonaws.com/GitClear-AI-Copilot-Code-Quality-2025.pdf), [2026 maintainability gap](https://www.gitclear.com/the_ai_code_quality_maintainability_gap)) | S | $$$$ | @21d | L | B | P+R |
| E4 | File survival | | S | $$$ | @N | L | B | P+R |
| E5 | Rework by others | | S | $$$$ | live | L | B | P+R |
| E6 | Rework by Unfold | | S | $$$$ | live | L | B | P+R |
| E7 | Rework intent | corrective, perfective, adaptive, preventive | S | $$$$ | live | M | B | P+R |
| E8 | Surviving lines at 90 days | feeds A20 | S | $$$$ | @N | L | B | P+R |
| E9 | Moved, not killed | moved lines count as surviving | S | $$$$ | @N | L | X | P+R |

## F. Ownership and knowledge

The glossary had no steward term then; the assigner and the accepting reviewer were the natural owners.

| ID | Metric | Definition | Grain | Cost | Fresh | Game | Place | Now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| F1 | Assigner and accepter | | S | $$ | to-merge | L | B | P+R |
| F2 | Bus factor of touched files | ([Avelino et al.](https://arxiv.org/pdf/1604.06766)); exclude the bot | S | $$$ | to-merge | L | B | P+R |
| F3 | First-time file touch | "new territory" | S | $$$ | to-merge | L | B badge | P+R |
| F4 | Hotspot overlap | churn × complexity | S | $$$ | to-merge | L | B | P+R |
| F5 | Docs updated | | S | $$ | to-merge | M | B badge | P+R |
| F6 | Tests touched with code | | S | $$ | to-merge | M | B | P+R |
| F7 | Follow-Ups spawned | | R/S | $ | live | L | B + T | P |
| F8 | Knowledge left behind | problem and solution text, description, ADR | R | $ | once | M | B | P |

## G. Tracker and business

Vikunja tasks have dates, priority, labels, assignees and parent and subtask relations, but no estimate field ([Vikunja task model](https://pkg.go.dev/code.vikunja.io/api/pkg/models)). The proposed Size replaces story points.

| ID | Metric | Definition | Grain | Cost | Fresh | Game | Place | Now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G1 | Work type | | W | $$ | once | L | B chip | P+R |
| G2 | Epic, parent, project | | W | $$ | once | L | B chip | P / P+R |
| G3 | Priority | | W | $$ | once | L | B | P+R |
| G4 | **Cycle time** | assignment to merge | W | $$ | to-merge | M | B (F candidate) | P+R |
| G5 | Lead time | ticket open to merge | W | $$ | to-merge | L | B | P+R |
| G6 | Size versus actual | | W | $ | once | M | B | NEW |
| G7 | Estimate versus actual | | W | $$ | once | H | B | P+R |
| G8 | Acceptance Conditions met | | W | $$ | to-merge | M | B | NEW |
| G9 | Delivery | | W | $ | to-merge | L | frame | P+R |
| G10 | Price versus cost | agency-internal | W | $ | once | L | X | NEW |
| G11 | Reopened | | W | $$ | live | L | T + B | P+R |
| G12 | Due date hit | | W | $$ | once | M | B | P+R |

## Timeline, gameability and placement

**Timeline:** Work Item created → assigned → Shift → each Run → pull request opened → CI → reviews → fix Rounds → merged → deployed → survival snapshots → reverts, hotfixes, linked bugs, Follow-Ups, reopen.

**Gameability.** Lines, comment counts, finding counts and test ratios are the easiest to pump; show them as anatomy, never as a score. Goodhart pairs: size with survival, speed with change failed, findings with reviewer false positives, test ratio with tests executed and coverage. Hard to game, so good headlines: cost, CI on first push, survival, reverts, human touch %. Person-level numbers stay off cards.

**Recommended front:** cost with budget share ("US$ 1,84 of US$ 5,00"); size ("+214 / −37 · 6 files"); Rounds and Verdict, or CI on first push before merge; time to merge (age while open); survival at 30 days, or human touch % before the first snapshot.

**Recommended back, six tabs:** Economics (tokens, cache hit, model mix, cost by Role, cost per surviving line, retries); Agent (turns, tool calls, test executions, peak context, active time, attempts per merged pull request); Change (files, languages, test lines, churn, badges, API and complexity deltas, duplication); Review & CI (first human review, rounds, depth, CI duration, flaky reruns, wait split, reviewer false positives); Life (lead time, days live, reverts, linked bugs, half-life, early churn, rework); Context (work type, epic, cycle time, Size versus actual, docs, new territory, Follow-Ups).

## Build order (proposed)

1. **Capture before it is lost:** widen the Claude Code envelope, persist the ACP tally and context fill, give reviewer findings ids. (ADR-0045 has since kept the usage figures; findings still have no ids.)
2. **Forge snapshot at merge:** diff, files, commits, reviews, statuses and Actions runs. (Diff and combined status done since by ADR-0046.)
3. **Librarian job:** daily survival blame, revert and hotfix scans, release lookup, stored as `(subject, metric, value, as_of, source)` rows.
4. **Static analysis at merge** on changed files only.

## Sources not cited inline

* [SZZ variants evaluation (Rosa et al. 2023)](https://www.sciencedirect.com/science/article/pii/S0164121223001243), [SZZUnleashed](https://github.com/wogscpar/SZZUnleashed)
* ["How Do AI Coding Agents Contribute to Software Development?"](https://arxiv.org/html/2607.21832v1)
* ["AI Writes Code, Humans Pay the Debt"](https://arxiv.org/abs/2609.04208)
* Ploeg sources read: `pkg/store/migrations` 0001–0022, `pkg/store/usage.go`, `pkg/harness/contract.go`, `pkg/harness/adapters/claudecode/claudecode.go`, `pkg/harness/adapters/acp/{state,wire}.go`, `pkg/provider/provider.go`
