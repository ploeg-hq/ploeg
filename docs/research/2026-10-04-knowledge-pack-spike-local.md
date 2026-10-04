# Knowledge pack spike, local run

Status: research record, 2026-10-04. A reduced local run of spike VIK-1859 ("do knowledge packs change Run cost, rounds and verdicts?"). It does not meet that ticket's criteria, which ask for at least 8 Work Items on two repositories run through deployed Ploeg. The knowledge-pack code (ADR-0065, proposed) is not deployed, and deploying an unmerged branch would change production desired state.

**Question.** On the same model and the same instructions, does an agent given a knowledge pack produce better or cheaper changes to Ploeg than one without?

**Method.**
- Three Work Items where the pack's conventions should matter. Each ran twice, with and without the pack, in its own clean checkout of `development` at `f81c929`:
  - t1: add `reviewDurationMs` to the OutcomeReport
  - t2: add a store column and method
  - t3: add a worker setting
- Six agents ran on Claude Sonnet with the same prompt. The pack arm added the worker's "Knowledge pack" section and directory, selected by `ploeg-okf pack` from the 10-concept bundle at `sha256:2238c0e05ac88b70`.
- Each agent saw only its own prompt file. Both arms had the repository's `AGENTS.md`.
- Afterwards, on every checkout: `gofmt -l .`, `go vet ./...`, `go test ./...`, `mise run docs-check`, and for t1 a trace test of whether the new field reaches the report the worker posts.

**Limitations.**
- Three tasks, one run each: no statistics.
- A subagent is not a Ploeg harness. It has no budget, no delivery contract and no review Round.
- Cost is subagent tokens, not gateway spend.
- Task 3's base had no `ClaimResponse.Context`, so its description allowed a minimal field.

## Results

| Task | Arm | Tokens | Tool calls | Seconds | gofmt, vet, tests | docs-check | Correct end to end |
| --- | --- | --- | --- | --- | --- | --- | --- |
| t1 field | pack | 69,574 | 12 | 59 | pass | pass | No: kept by `MergeDropBox`, dropped by `resolveOutcome` |
| t1 field | bare | 63,816 | 14 | 59 | pass | pass | No: dropped by both |
| t2 store | pack | 51,836 | 7 | 44 | pass | pass | Yes |
| t2 store | bare | 55,342 | 8 | 44 | pass | pass | Yes |
| t3 setting | pack | 64,019 | 8 | 63 | pass | **fail**: stale configuration reference | Yes, but not mergeable |
| t3 setting | bare | 63,937 | 11 | 73 | pass | pass | Yes |

Totals: pack 185,429 tokens and 27 tool calls; bare 183,095 tokens and 33 tool calls.

## Findings

1. **Cost did not move.** Tokens were within 1.3 %. The pack arm used 6 fewer tool calls over three tasks, too few to mean anything.
2. **Packs of known conventions add little where `AGENTS.md` already says them.** Both t2 runs followed the append-only migration rule; the bare run cited `AGENTS.md`.
3. **The one quality gain came from a concept pointing at the outcome path.** The t1 pack run found that `MergeDropBox` copies fields one by one, after reading the harness-contract concept's note on `PLOEG_OUTCOME_FILE`. It still missed `resolveOutcome`. The bare run missed both. All four t1 test suites passed, because no test traced the value end to end.
4. **The one quality loss was knowledge the bundle lacked.** The t3 pack run did not regenerate `docs/reference/configuration.md`. Nothing in the bundle says to, and the bare run happened to.
5. **Selection is too loose.** Common words matched ("run", "only", "not", "check"), so t1 and t3 got 8 of 10 concepts. After the bundle grew, t2 still missed the migration pitfall that mattered most to it.
6. **Both t2 runs numbered their migration 0038.** That is the number #61 takes, so whichever lands second must renumber.

## What changed because of this run

Three pitfalls were added to `knowledge/`, each verified against the code:

- "A new OutcomeReport field must be carried through every copy"
- "A new PLOEG_ setting needs the configuration reference regenerated"
- "Migration numbers collide across open branches"

They are what a reviewed learning (ADR-0066) would have produced from these Runs.

## Recommendation

- Keep ADR-0065 and ADR-0066 proposed.
- Before the full spike: tighten selection (VIK-1860). That means a stopword list fitted to Work Item prose, splitting hyphenated words, and a higher minimum score.
- Then run VIK-1859 as written, on deployed Ploeg with real Runs, so that review Rounds and verdicts are measured too.
- The early signal: packs pay off through pitfalls that are not in `AGENTS.md`, not through conventions that are. That makes the learnings loop the part worth building first.
