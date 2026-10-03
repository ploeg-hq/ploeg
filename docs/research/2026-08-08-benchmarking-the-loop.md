# Benchmarking the whole loop — survey and design

> **What this is.** Evidence and a build plan for a re-runnable, deterministically
> graded end-to-end benchmark of Ploeg: ticket → Shift → Rounds → PR → review →
> verdict → "a person is asked to merge". Written 2026-08-08 against `development`
> (post `a1bccea`, chart v0.2.0-rc.15). Survey first, design second, phased plan
> last. No decision is ratified here — the verdicts this produces belong in ADRs.

## 1. The thing being measured is four things

The single most common way an agent benchmark goes wrong is scoring one number
over a pipeline that has several independent failure modes. Ploeg's loop has four,
and they need different graders because only two of them can be deterministic in
the strict sense:

| # | Layer | Question | Gradeable how | Deterministic? |
|---|---|---|---|---|
| L1 | **Dispatch plane** | Did the machinery behave? Rounds in order, one writer per Round, budget settled, verdict honoured, tracker told | SQL over `shifts` / `agent_runs` / `leases` / `audit_log` | **Yes, fully.** Same rows every time or it's a bug |
| L2 | **Patch correctness** | Does the produced diff actually solve the ticket? | Hidden fail-to-pass + pass-to-pass suites, SWE-bench style | **Yes**, given a sealed test suite |
| L3 | **Review quality** | Did the reading Roles catch what's wrong, without drowning the signal? | Seeded-defect detection: precision / recall / F1 / signal-to-noise | **Yes, if** the PR under review is frozen and the matcher is keyed, not an LLM judge |
| L4 | **Cost & latency** | What did it cost, how long, how many Rounds | LiteLLM `/spend/logs` joined on `key_alias`, `audit_log` timestamps | Measurable, but inherently variable — report distributions |

L1 is a **conformance suite**, not a benchmark: it should be 100% every run, and a
failure is a Ploeg defect, not a model score. L2 and L3 are the benchmark. L4 is
the axis that makes the benchmark interesting — a harness that resolves 60% for
€0.40 beats one that resolves 65% for €4.00 in this setting.

Keeping L1 separate is what stops "the model was bad today" from masking "the
sweeper ate the run", which is exactly the confound the runs on 2026-07-24/25
produced.

## 2. What the field does, and what to take from it

### 2.1 Execution-based grading is the settled standard

[SWE-bench Verified](https://openai.com/index/introducing-swe-bench-verified/) is
the reference shape: 500 human-validated issues, each with a hidden test suite
acting as a deterministic pass/fail oracle. The harness restores the repo to the
pre-fix state, applies the candidate patch, and runs the project's real tests in
a sandboxed Docker container with pinned dependencies. An instance is **resolved**
only when every **fail-to-pass (F2P)** test passes *and* every **pass-to-pass
(P2P)** test still passes. P2P is the anti-regression half and it is not optional:
without it, deleting the failing test is a winning strategy.

Take: F2P/P2P, container isolation, pinned deps, "restore the test files before
grading". That last one is the mechanism that makes the oracle un-gameable, and
it costs one `git checkout <golden-ref> -- <test-paths>` before the run.

Known caveat to design around:
[UTBoost](https://arxiv.org/pdf/2506.09289) and the
[2026 methodology reviews](https://benchmarkingagents.com/swe-bench/) both find
that flaky tests inflate or deflate scores run to run, and that vendors each run
their own scaffold around the raw model — so cross-vendor numbers are barely
comparable. In our case *we are the scaffold*, which is the point, but it means
our numbers are only ever comparable **to each other**.

### 2.2 Terminal-Bench: the containerised task template

[Terminal-Bench 2.0](https://arxiv.org/abs/2601.11868) (89 tasks, ICLR 2026) is
worth copying structurally rather than adopting. Each task is exactly four
artefacts: a natural-language instruction, a Docker environment, a verification
test suite, and an **oracle solution**. The oracle is the part people skip and
shouldn't — it's how you prove the task is solvable and how you calibrate the
grader before any agent touches it.

Take: the four-artefact task layout, and the discipline of an oracle patch that
must score 1.0 against your own grader.

### 2.3 Standards for the run harness — and why we shouldn't adopt one

[Inspect AI](https://neurlcreators.substack.com/p/inspect-ai-evaluation-framework-review)
(UK AISI; used by METR and Apollo) and
[HAL, the Holistic Agent Leaderboard](https://arxiv.org/abs/2510.11977) are the
two credible pieces of shared infrastructure. HAL's contribution is the
argument, not the code: agent evaluation should be **multidimensional and
process-level** — accuracy *plus* token and dollar cost, operational reliability,
error frequency, robustness — and **all evaluation logs published** so results can
be re-derived by someone else.

But both frameworks assume *they* orchestrate the agent. Ploeg is the
orchestrator; that is the system under test. Wrapping Ploeg in Inspect would mean
either mocking out the dispatch plane (deleting L1, the half we most want graded)
or running Inspect as a puppet that only pokes the Vikunja API — all ceremony, no
benefit.

Take the **metric vocabulary and the log-publication norm**, not the runtime.
Concretely: emit results as JSON with cost and reliability fields alongside
accuracy, and keep every raw artefact.

One thing worth borrowing outright is METR's holistic criterion —
[judge whether the PR would be mergeable without meaningful extra work](https://metr.org/blog/2025-08-12-research-update-towards-reconciling-slowdown-with-time-horizons/).
That is a *human* rubric and it should stay one: use it as an occasional
calibration check on whether the automated score is measuring anything real, never
as the headline number.

### 2.4 Reviewer grading is where published work is weakest

This is the layer Ploeg has that SWE-bench does not, and the literature is thin
and mostly vendor-authored.

- [CR-Bench](https://arxiv.org/abs/2603.11078) (Nutanix, Mar 2026) is the
  academic entry: a dataset plus a fine-grained evaluation pipeline, scored on
  precision, recall, F1, usefulness rate and **signal-to-noise ratio**. Its
  central finding is the trade-off to design for: agents either prioritise
  precision and miss real defects, or prioritise recall and produce noisy,
  low-actionable feedback. A reviewer scored on recall alone will learn to say
  everything.
- [CodeFuse-CR-Bench](https://arxiv.org/pdf/2509.14856) adds a
  comprehensiveness axis over end-to-end Python review.
- Vendor benchmarks — Qodo (60.1 F1), Augment (59), Propel (64), Macroscope (48%
  detection over 118 bugs / 45 repos) — all use **LLM-injected defects into real
  PRs, then double-verified**, which is a sound method. DeepSource's survey of
  them,
  [*Every AI code review vendor benchmarks itself, and wins*](https://deepsource.com/blog/ai-code-review-benchmarks),
  names the actual problem: the ground truth is not published, so no one can
  reproduce the scoring; and a pure-LLM grader run twice gives two answers.

Take: seeded defects with published ground truth, precision *and* recall *and*
noise, and a **keyed deterministic matcher** rather than an LLM judge for the
headline figure.

The mutation-testing literature supplies the vocabulary for the seeding half —
[Google's practical mutation testing at scale](https://research.google/pubs/practical-mutation-testing-at-scale-a-view-from-google/)
and [Meta's LLM-driven mutant generation](https://engineering.fb.com/2025/09/30/security/llms-are-the-key-to-mutation-testing-and-better-compliance/)
— where a mutation score is simply killed mutants over seeded mutants. A reviewer
that catches 9 of 12 seeded defects has a mutation score of 0.75, and that number
means the same thing every run because the mutants are frozen in git.

Adjacent and useful if we ever want to generate defects at volume rather than
hand-write them: [SWE-smith](https://github.com/SWE-bench/SWE-smith) turns any
repository into a task factory via LLM function rewrites, AST transformations
(dropping conditionals, flipping operators), PR-undo, and bug combination. Not
needed for v1 — twelve hand-written, hand-verified defects beat a thousand
synthetic ones for a benchmark you will read the results of personally.

### 2.5 One run tells you nothing

[*Beyond pass@1: A Reliability Science Framework for Long-Horizon LLM Agents*](https://arxiv.org/pdf/2603.29231)
and [ReliabilityBench](https://arxiv.org/abs/2601.06112) both land on the same
finding: an agent at **60% pass@1 can be 25% consistent across trials**. The two
metrics to report together:

- **pass@k** — at least one of k i.i.d. trials succeeds. Measures capability with
  retries. Ploeg retries (3 attempts, sweeper-driven), so this is the honest
  "does the factory eventually deliver" number.
- **pass^k** ("pass-hat-k") — *all* k trials succeed. Measures reliability. This
  is the number that decides whether you can leave the factory unattended.

Both papers use k ≥ 3 for variance estimation; the long-horizon work notes
variance grows with task duration. Temperature 0 does not make this go away —
non-determinism enters through tool ordering, timing, and batch effects, not just
sampling.

Practical floor for a homelab: **n = 5 trials per configuration** for a signal,
n = 10 before believing a gap smaller than ~15 points. Report Wilson 95% intervals
so it is visible when two configs are indistinguishable.

## 3. Design

### 3.1 Two modes, because the loop's halves need different ground truth

**Mode A — full loop (grades L1, L2, L4).** Fresh ticket → assign → Shift →
Rounds → PR → review → close → tracker comment. Graded by hidden tests on the PR
head plus SQL conformance.

**Mode B — planted PR (grades L3, L4).** A frozen branch carrying N known
defects, dispatched to a review-only plan. The writer never runs.

Mode B exists because **Mode A cannot grade review quality**. What the reviewer
sees in Mode A is whatever the writer happened to produce, which differs every
trial — so there is no ground truth, and a reviewer that finds nothing might be
lazy or might be looking at clean code. Freezing the diff is the only way to ask
"did it catch the race condition on line 88" and get a repeatable answer. This is
the same move Qodo and Macroscope make, and it is the reason their numbers are at
least internally comparable even where their data is not published.

### 3.2 The bench repository

A new Forgejo repo, `webgrip/ploeg-bench`, structured as:

```
tasks/<task-id>/
  ticket.md            # verbatim Vikunja title + description for the trial
  repo/                # the codebase at its pre-fix state (the agent's world)
  hidden/              # F2P + P2P suites — restored by the grader, never trusted
  oracle.patch         # reference solution; must score 1.0 or the task is broken
  grade.yaml           # gates, weights, timeouts, tamper paths
review/<case-id>/
  base.sha             # frozen; the diff under review
  defects.yaml         # ground truth: key, file, line span, accept patterns
  allowlist.yaml       # triaged-once findings that are true but not seeded
```

Two rules do the heavy lifting:

1. **The grader never runs inside the agent's pod.** It runs as a separate
   container against a fresh clone of the PR head. The agent has Docker-in-pod
   and could otherwise edit the very tests scoring it.
2. **`hidden/` is restored from the golden ref before every grading run**
   (`git checkout <golden> -- hidden/`). Whether the agent edited it becomes an
   *observation* (gate G6, "tamper detected") rather than an exploit.

### 3.3 The hard problem

The task has to be hard for the right reason. Puzzle-hard (obscure algorithm)
separates models on knowledge; **constraint-hard** (many simultaneous invariants,
concurrent, with an obvious-but-wrong shortcut) separates *harnesses* — and the
harness is what you are actually comparing. It should also produce something for
the reviewer Role to legitimately catch, so the multi-Role plan is exercised
rather than decorative.

Proposed task **PB-01 — fair-share claiming in a lease broker**. A small Go
service (~1,200 LOC, no external deps, in-memory store) modelling Ploeg's own
domain: tenants submit items, workers claim them under a TTL lease, spend settles
against a pool. The ticket asks for fair-share claiming across tenants plus TTL
reclaim.

Why this one:

- **Concurrency is not optional.** `go test -race` over a 200-goroutine stress
  test is a free, fully deterministic detector of the mistake LLMs make most.
- **Several invariants at once.** Fair-share ordering, no starvation, idempotent
  renew, renew-after-expiry rejected, `sum(authorized) ≤ pool` at all times. The
  obvious implementation satisfies three of five.
- **Fast.** Full suite under 90 s, so a 10-trial × 4-config matrix is an
  afternoon, not a week.
- **Go, matching `agent-runner`'s toolchain**, so a failure is the agent's, not
  the image's.
- **The domain is ours**, so what the benchmark teaches transfers directly.

Calibrate before trusting it: `oracle.patch` must score 1.0, and a deliberately
naive patch must score < 1.0 on a named gate. If the naive patch passes, the tests
are too weak and the benchmark measures nothing.

### 3.4 Gates — Mode A

All gates are pass/fail and run in a fixed order in a clean container. `resolved`
= all of G1–G7.

| Gate | Check | Guards against |
|---|---|---|
| G1 | `go build ./... && go vet ./...` | Non-compiling patch |
| G2 | P2P suite green | Regression / breaking what worked |
| G3 | F2P suite green | Not actually solving the ticket |
| G4 | `go test -race ./...` on the stress test | The concurrency bug the ticket invites |
| G5 | Property test, **fixed seed**, invariant `sum(authorized) ≤ pool` | Plausible-looking accounting |
| G6 | No diff under `hidden/`, `go.mod`, `grade.yaml` | Grading the grader |
| G7 | Test-function count ≥ baseline; zero new `t.Skip` | Deleting or muting tests to pass |

Scoring is **binary at the instance level** (SWE-bench's choice, and it survives
contact with reality better than partial credit), with the per-gate vector kept
so a failure is diagnosable. Partial credit would let a patch that compiles and
does nothing accumulate points.

### 3.5 Grading — Mode B (review quality)

`defects.yaml` per seeded defect:

```yaml
- key: RACE-CLAIM-MAP
  file: internal/broker/claim.go
  lines: [84, 96]
  severity: high
  accept:                       # ANY match, case-insensitive, on the finding text
    - "data race"
    - "concurrent map (write|access)"
    - "unsynchron[iz]sed access"
  requires_location: true       # must also cite the file (line ±10 optional)
```

A finding counts as a **detection** when it cites the file and matches one accept
pattern. Everything else lands in `unmatched`. Three deliberate choices:

- **The headline matcher is regex + location, never an LLM judge.** Same input →
  same score, forever. An LLM judge may run as a *second* column for insight, and
  disagreement between the two is itself a useful signal, but it never moves the
  number.
- **Triage once, then freeze.** Real reviewers find real things that weren't
  seeded. On the first run, triage `unmatched` by hand into `allowlist.yaml`
  (true-but-unseeded) or leave it as noise. Later runs are then fully automatic,
  and the allowlist is a published artefact — the thing the vendor benchmarks
  refuse to publish.
- **Report all three**: recall = detected / seeded; precision = detected /
  (detected + noise); signal-to-noise = total findings / detections. CR-Bench's
  finding is that optimising one alone is trivially gameable in both directions.

Twelve seeded defects across classes with genuinely different detectability:
data race on a map write; inverted TTL comparison; swallowed error; off-by-one in
the fair-share index; unchecked overflow in budget arithmetic; missing
`context` cancellation; unclosed resource; N+1 in a hot loop; a hardcoded
credential; a deleted P2P test; a plausible-but-wrong comment about isolation
level; a silent `int64`→`float64` precision loss in money.

That last cluster matters: a reviewer that only catches lint-grade issues and
misses the money bug is exactly the reviewer this benchmark should mark down.

### 3.6 L1 conformance — SQL, not vibes

Every trial, against the run's `work_item_id`. Any failure is a Ploeg bug:

| Assertion | Query shape |
|---|---|
| Exactly one Shift, closed, with a `close_reason` | `shifts WHERE work_item_id = $1` |
| Rounds monotonic from 0, no gaps | `SELECT DISTINCT round FROM agent_runs WHERE shift_id = $1` |
| Every Round is readers-only or exactly one writer | `GROUP BY round HAVING count(*) FILTER (WHERE writes) > 1` |
| No two writing Runs overlap in time | self-join on `[claimed_at, finished_at)` where `writes` |
| Lease held only while a writer ran | `leases` vs writing `agent_runs` windows |
| `spent ≤ budget`; every finished Run settled | `shifts.spent`, `agent_runs.authorized` |
| Reserved never exceeded the pool | `SUM(authorized) FILTER (state='running') ≤ budget − spent` |
| Verdict honoured | `request_changes` in last Round ⇒ writer Round re-opened, unless capped/pool-blocked (`close_reason` says which) |
| Fix rounds ≤ `maxFixRounds` | `shifts.round` vs plan length |
| Audit trail complete | `audit_log` has `work_item.queued`, `round.opened`×n, `run.claimed`×n, `shift.closed` |
| No orphan credentials | LiteLLM `/key/list` has no `ploeg-*` alias after settle |
| Tracker was told | Vikunja comment on the trial ticket contains the PR link |

`close_reason` is the single most valuable field here — `plan_exhausted`,
`review_approved`, `fix_round_cap_reached`, `budget_exhausted_before_fix_round`
turn "why did this stop" into a `GROUP BY` across the whole matrix.

### 3.7 Trial isolation and reset

**One fresh ticket per trial.** Do not reuse a ticket and re-assign it: the
`done → queued` re-dispatch path resets `attempts` and opens a *second* Shift, so
the trial would measure the re-mandate path instead of a cold start. A fresh
ticket also yields a fresh branch, since the branch is `agent/vik-<externalID>`.

Per-trial teardown, in order: record PR head SHA → grade → close PR → delete
branch → close ticket → assert no live Shift, no live lease, no `ploeg-*` key.

Trial identity to carry through every artefact: `trial_id = <task>-<config>-<n>`,
joined to the run via the trace alias `ploeg-<token12>`, which is already the
LiteLLM `key_alias`, the `LLM_TRACE_ID` and the `Agent-Trace-Id` commit trailer.
That alias is the join key for the whole scorecard — nothing new needs building.

### 3.8 The configuration matrix

The axes worth varying, each one a values change and nothing else:

1. **Harness adapter** — `openhands` (current default) vs `claude-code` vs
   `acp`/opencode vs `exec`. The headline comparison.
2. **Model per Role** — cheap reader + expensive writer vs uniform, and
   deliberately mismatched families for the reviewer (a reviewer on the writer's
   own family is closer to self-grading than the plan admits).
3. **Plan shape** — one writer; analyst → builder → reviewer; two readers
   fanned out then builder then reviewer.
4. **`maxFixRounds`** — 0, 1, 2, 3. Directly tests ADR-0017's default and feeds
   its own re-evaluation triggers.
5. **Pool size** — where budget starts binding before the cap does, which is the
   other ADR-0017 trigger.

Start with 4 configs × 5 trials × 2 modes = 40 runs. At the measured ~€0.01–0.05
per deepseek run and a couple of euro per frontier-model run, the first full
matrix is small money; the constraint is wall clock and the single
`maxReplicaCount: 1` per team.

### 3.9 Scorecard

One JSON per trial, appended to `bench/results/<date>/`, with the raw artefacts
(audit rows, spend logs, PR diff, findings text, container logs) beside it —
HAL's publish-the-logs norm, which is what makes a result re-derivable in six
weeks.

```json
{
  "trial_id": "PB-01-openhands-sonnet5-03",
  "mode": "A",
  "task": "PB-01",
  "config": {"harness": "openhands", "plan": "analyst-builder-reviewer",
             "models": {"analyst": "...", "builder": "...", "reviewer": "..."},
             "maxFixRounds": 2, "pool": "6.00"},
  "trace_alias": "ploeg-a1b2c3d4e5f6",
  "resolved": true,
  "gates": {"G1": true, "G2": true, "G3": true, "G4": false, "G5": true,
            "G6": true, "G7": true},
  "conformance": {"passed": 12, "failed": 0, "failures": []},
  "review": {"seeded": 12, "detected": 9, "noise": 4,
             "recall": 0.75, "precision": 0.69, "f1": 0.72, "snr": 1.44,
             "missed": ["MONEY-PRECISION", "..."]},
  "loop": {"rounds": 4, "fix_rounds": 1, "close_reason": "review_approved",
           "attempts": 1, "sweeper_interventions": 0},
  "cost": {"usd": 1.83, "input_tokens": 412000, "output_tokens": 38000,
           "usd_per_role": {"analyst": 0.21, "builder": 1.32, "reviewer": 0.30}},
  "latency": {"queued_to_claim_s": 41, "total_s": 1870,
              "per_round_s": [220, 980, 410, 260]}
}
```

Aggregate per config: pass@1 with Wilson 95% CI, pass@5, **pass^5**, mean and p95
cost, cost per resolved instance, conformance pass rate, mean recall/precision/F1.

### 3.10 External calibration — worth one afternoon

Run a 20–30 instance slice of SWE-bench Verified through Ploeg with the same model
a public leaderboard reports, and emit predictions in the standard
`{instance_id, model_name_or_path, model_patch}` format so the
[official harness](https://github.com/swe-bench/SWE-bench) grades them.

This answers a question the in-house benchmark structurally cannot: **is Ploeg's
scaffold losing points the model would otherwise score?** If Ploeg lands far below
the published figure for the same model, the gap is prompt composition, task
delivery, gate wiring or timeouts — not the model. Without this, every in-house
number is unanchored and a bad harness looks like a bad model forever.

## 4. Instrumentation gaps to close first

Found while reading the current code; each is small and each blocks part of the
scorecard.

1. **No metrics at all** (architecture.md §9.11 — no OTel/Prometheus in Go). All
   latency must come from `audit_log` timestamps. Check that
   `round.opened` / `run.claimed` / `shift.closed` carry enough resolution for
   `per_round_s`; if not, that's the smallest possible fix.
2. **`agent_runs` has no target column** (§9.17), so cost rolls up by team only.
   For the benchmark, `trial_id` must be recoverable from the trace alias — verify
   the alias is written into `agent_runs` and not only into LiteLLM.
3. **Forge webhooks are unreachable in-cluster** (§9.1): the `ploeg` namespace has
   no Forgejo ingress rule and Forgejo's egress excludes the pod/service CIDRs.
   So the *human* review path can't be benchmarked yet — v1 measures the
   agent-verdict path only, and the human step is the Vikunja comment. Worth
   stating in the results rather than discovering mid-matrix.
4. **`readTokenSecret` is unset** (values.yaml, ADR-0013 tier 1): readers today
   run with the read-write builder token, and only scheduling separates them.
   Add a conformance assertion that no reading Run pushed — it will pass, but it
   should pass *provably*, and if it ever fails that's the finding of the year.
5. **Checkpoints are written and never read** (§9.5) — every run starts fresh, so
   a crash mid-Shift costs the whole Round. Relevant to interpreting L4 numbers.
6. **KEDA `historyLimit` deleted finished Jobs before logs shipped** (memory of
   the 2026-07-24 crash drill). Confirm this is fixed before running 40 trials,
   or half the artefacts vanish.
7. **Branch `agent/vik-<id>` is not unique per target** (§9.16). Fine while the
   bench uses one repo; it forecloses running the same task across repos
   concurrently.

## 5. Phased plan

| Phase | Deliverable | Depends on | Rough size |
|---|---|---|---|
| **0** | Instrumentation gaps 1, 2, 6 closed; `trial_id` join proven end to end on one throwaway run | — | half a day |
| **1** | `webgrip/ploeg-bench` repo; PB-01 task at pre-fix state; `hidden/` F2P+P2P; `oracle.patch` scoring 1.0 and a naive patch scoring < 1.0 | 0 | 1–2 days, mostly writing good tests |
| **2** | Grader container: clone PR head → restore `hidden/` → G1–G7 → gate vector JSON. Runs outside the agent pod | 1 | half a day |
| **3** | L1 conformance suite (§3.6) as SQL + a small Go runner; green on the Phase 0 run | 0 | half a day |
| **4** | Trial driver: create ticket → assign → poll to terminal → collect artefacts → grade → teardown → scorecard JSON | 2, 3 | 1 day |
| **5** | Mode B: freeze `review/RB-01` branch, seed 12 defects, write `defects.yaml`, keyed matcher, first triage pass into `allowlist.yaml` | 1 | 1 day |
| **6** | Matrix runner + aggregation (pass@1/pass@5/pass^5, Wilson CIs, cost) + a results page | 4, 5 | half a day |
| **7** | SWE-bench Verified calibration slice (§3.10) | 4 | half a day |

Phases 1 and 3 are independent and can run in parallel. Phase 5 is the one that
will take longest in practice because seeding defects that are *fair* — real, not
lint-noise, and each genuinely detectable from the diff — is judgement work.

The output that makes it worth it: after Phase 6, "swap the reviewer to Sonnet 5
and raise `maxFixRounds` to 3" becomes a 40-minute question with a number and a
confidence interval attached, and ADR-0017's re-evaluation triggers stop being
things you notice by accident.

## 6. Open questions

- **Task count.** One deep task (PB-01) gives a sharp, cheap signal but a
  fragile one — a single quirk of PB-01 could dominate every conclusion. Three
  tasks would be robust and triple the wall clock. Recommendation: build PB-01
  properly, run the full matrix once, and only then decide whether the variance
  justifies PB-02/03.
- **Where the bench lives.** Its own Forgejo repo (clean, and the agent can't see
  the grader) vs a `bench/` directory in `ploeg` (one checkout, but `hidden/` sits
  in the tree the agent may clone). Separate repo is the safer default.
- **Whether L1 conformance belongs in CI.** It is a genuinely good integration
  test of the dispatch plane, entirely deterministic, and does not need an LLM if
  you fake the harness with the `exec` adapter. Arguably it should run on every PR
  regardless of the benchmark.
- **Human calibration cadence.** METR's mergeable-without-substantial-extra-work
  judgement is the check on whether the automated score means anything. Once per
  matrix, on a sample of 5, is probably enough.

## 8. Corrections after code verification (2026-08-08)

Everything above §7 was written from the architecture docs and a partial read of
the code. This section records what changed when every load-bearing claim was
checked against `development` @ `c505e69`. It is **additive** — the sections
above are left as written, because what a survey got wrong is part of the
evidence.

Fourteen corrections. Three of them (C1, C2, C9) would have produced wrong SQL;
two (C12, C13) are live product defects the benchmark design surfaced.

### Schema and query corrections

**C1 — Rounds start at 1, not 0.** §3.6 asserts "rounds monotonic from 0".
`OpenRound` does `UPDATE shifts SET round = round + 1` *before* inserting the
roster ([pkg/store/shift.go:90](../../pkg/store/shift.go)), so the first Round is
**1**. Round `0` on `agent_runs` is the column default and means a pre-Shift
legacy claim leaked into a Shift — worth asserting against, not for. The
assertion is `{1..shifts.round}`, contiguous.

**C2 — There is no `claimed_at`.** §3.6's overlap query names it. The column is
`agent_runs.started_at`, set to `now()` at the pending→running flip
([shift.go:203](../../pkg/store/shift.go)) and left `NULL` on a pending row
([shift.go:106](../../pkg/store/shift.go)). Runs cancelled at Shift close keep
`started_at IS NULL` with `finished_at` set, so **every window query must exclude
`started_at IS NULL`** or it reports phantom overlaps.

**C7 — Latency does not have to come from `audit_log`.** §4.1 says "all latency
must come from `audit_log` timestamps" because there is no OTel. Too pessimistic:
`agent_runs.started_at`/`finished_at` give exact per-Run and per-Round latency
already. `audit_log` is needed only for the `queued → first round.opened` leg.
§9.11's "no metrics" is true and remains a gap for *live* observability; it is
not a gap for post-hoc measurement.

**C8 — `attempts` is not a retry counter under Shifts.** §3.9's scorecard has
`"attempts": 1`. `work_items.attempts` is incremented **per role claim**
([shift.go:227](../../pkg/store/shift.go)), so a three-role plan reads `3` after
one clean pass. Rename the field to `runs_claimed`; derive real retries from
`run.expired` audit rows plus `agent_runs.failure_reason`.

**C9 — Lease history does not exist.** §3.6 assertion 5 self-joins `leases`
against run windows. `leases` rows are `DELETE`d at settle
([store.go:426](../../pkg/store/store.go)) and at expiry
([shift.go:357](../../pkg/store/shift.go)), and the Shift claim path inserts a
lease with **no audit row** — only `run.claimed` is audited
([shift.go:240](../../pkg/store/shift.go)); `lease.acquired` exists solely on the
legacy `store.Claim` path ([store.go:280](../../pkg/store/store.go)). Post-hoc,
the provable form is "a Lease was acquired for every writing Run (from audit) and
none is live at teardown". Auditing `lease.acquired` on the Shift path is a
small, separately worthwhile fix.

**C10 — The orphan-key assertion flaps.** §3.6 asserts no `ploeg-*` alias after
settle. The boot/periodic orphan sweep is not immediate, so this needs a bounded
wait and an observation field, not an instant hard failure.

### Benchmark-design corrections

**C3 — The trace alias is per Run, not per trial.** §3.7 makes
`ploeg-<token12>` "the join key for the whole scorecard". `litellm.Alias` derives
from `agent_runs.run_token` ([pkg/litellm/client.go:25-30](../../pkg/litellm/client.go)),
so a three-role Shift has three or more aliases. The **trial** key is
`work_items.external_id` (the fresh ticket); the alias is the **per-Run cost**
join only, `'ploeg-' || left(run_token,12) = key_alias`. The alias is never
persisted — it is always derived.

**C4 — The review metrics were malformed.** §3.5 gives
`precision = detected / (detected + noise)`, mixing units: `detected` counts
*defects*, `noise` counts *findings*. And `signal-to-noise = total findings /
detections` is noise-per-detection, so higher is worse — the opposite of what the
name promises. Corrected: recall is over defects, precision is over **finding
units**, and `snr = signal_units / max(noise_units, 1)`.

**C5 — Freeze the commit, not the branch.** §3.1 and §3.5 assume a fixed branch
carrying the seeded defects. The branch is `agent/vik-<externalID>`
([pkg/shiftengine/engine.go:70-75](../../pkg/shiftengine/engine.go)) and `findPR`
matches `head.ref` exactly ([pkg/worker/forge.go:15-20](../../pkg/worker/forge.go)),
so a fixed branch name cannot survive one-fresh-ticket-per-trial. A branch created
from the frozen ref points at the **identical commit object**, so the fix is to
anchor ground truth to `frozen_sha` and assert it at setup and teardown.

**C6 — `hidden/` cannot be a directory in the agent's tree.** §3.2 puts
`tasks/<id>/{repo,hidden}` side by side and §3.4's G6 checks "no diff under
`hidden/`". The agent clones the *target* repo, so a task world cannot be a
subdirectory of the bench repo, and hidden tests must live on a ref the agent's
clone does not contain. G6 becomes a **reserved-path assertion over the PR
diff**.

**C11 — The harness axis is only half real.** §3.8 calls harness adapter "the
headline comparison". Only `openhands` and `exec` can deliver a reading Run's
`findings`/`verdict` — see C12.

### Two live defects the design surfaced

**C12 — A reading Run cannot return findings on `claude-code` or `acp`.**
`ComposePrompt` tells every reading Run, unconditionally, to write its review to
`$PLOEG_OUTCOME_FILE` ([pkg/worker/task.go:66-88](../../pkg/worker/task.go)).
That variable is set in exactly two adapters:
[openhands.go:48-57](../../pkg/harness/adapters/openhands/openhands.go) and
[execbin.go:67-75](../../pkg/harness/adapters/execbin/execbin.go).
`claude-code` sets no `OutcomeFile` and no such env var, and its `ParseOutcome`
returns only `Usage` ([claudecode.go:37-68, 89-107](../../pkg/harness/adapters/claudecode/claudecode.go));
the `acp` package contains zero references to `Findings`, `Verdict` or
`OUTCOME_FILE`. Consequences on those harnesses: `agent_runs.findings` and
`verdict` stay empty, `requestsChanges()`
([reviewloop.go:120](../../pkg/shiftengine/reviewloop.go)) is always false, so
**ADR-0017's review loop is inert — every Shift closes `review_approved`
regardless of what the reviewer found** — and `publishRound` posts nothing, so
the human sees no review either. `ops/helm/ploeg/values.yaml` documents
`harness: {name: claude-code}` on a role as supported configuration. This is a
product bug, not bench scaffolding.

**C13 — A structured reader report loses the PR link.** In `resolveOutcome`, the
`case report.Outcome.Valid()` arm returns the harness report untouched, with no
`Links` ([pkg/worker/worker.go:437-448](../../pkg/worker/worker.go)). The arm
that *does* attach the PR ([worker.go:449-461](../../pkg/worker/worker.go)) is
only reached when the harness returned nothing structured. So a reader that
correctly writes a drop box yields a report with no link, while a reader that
returns nothing gets one. In a full Shift this is masked — the writer's Run
carries the link and `pullRequest()` scans all reports — but in a **review-only
Shift the findings are never published to the pull request**.

### One operational hazard

**C14 — `ScratchDir` is process-global.** `RunEnv.ScratchDir` is `os.TempDir()`
([worker.go:249](../../pkg/worker/worker.go)), so `taskspec.json`, `task.md` and
the default `outcome.json` are shared paths. `RunCommand` only reports
`OutcomeFile` when the file exists
([adapter.go:148-152](../../pkg/harness/adapter.go)), so a run whose agent dies
before writing one can **inherit the previous run's outcome file and report its
outcome**. In-cluster each Run is its own pod, so this has never bitten; it bites
immediately in any harness that runs two Runs in one process or one container.

### What this changes about the plan

- L1 conformance gains three assertions the survey did not have: writers carry no
  verdict, no reading Run pushed, and findings reached the pull request.
- The review benchmark anchors on a frozen **commit** with a per-trial branch.
- The harness axis needs the C12 fix before a reader-harness comparison is
  meaningful; a writer-harness comparison is exercisable today.
- Mode B grades review **content**, not review **delivery** — C13 means a
  review-only Shift publishes nothing even once C12 is fixed. Delivery is
  asserted in the full-loop mode only.

## 9. Probe results (2026-08-08)

Measured against the live cluster, not reasoned about. §4 listed four probes
that block parts of the scorecard; three are now answered.

### P1 — Does LiteLLM keep `key_alias` in its spend logs after the key is
### deleted? **Yes.**

This was the one that decided whether a crashed run's cost is ever
recoverable. `revokeKey` deletes the key at settle, so the worry was that
deletion takes the alias→spend mapping with it.

At the time of the probe `/key/list` held **zero** live `ploeg-*` keys — every
past run's key had already been revoked — and `/spend/logs` still carried
**35 distinct `ploeg-*` aliases** with their spend intact. Joining
`'ploeg-' || left(agent_runs.run_token, 12)` against
`metadata.user_api_key_alias` matched **34 of 40** recent runs; the six misses
are runs that made no LLM call at all.

**Consequence for the design.** §3.9 proposed a layered cost model with the
database as primary and the gateway as a fallback that might not exist. Invert
it: **the gateway is authoritative and always available**, and
`agent_runs.usage` becomes a cross-check. `cost.complete` can then be true for
every run, including swept ones, and the `unattributed_usd` bucket §3.9
reserved is unnecessary.

### P2 — At what threshold does `-race` fire reliably? **200 goroutines ×
### 5000 ops, 5 runs of 5.**

Measured on PB-01's `naive-unlocked` calibration patch (the oracle minus its
mutex) and recorded in `tasks/PB-01/grade.json` and
`calibration.expected.json`. `-race` is probabilistic; an unrecorded threshold
is a silent flake generator.

### P4 — Does the bench project resolve a Target? **Yes** — `route_rule=11/bench`,
so `publishRound` has the Target it needs and the blackboard is not silently
dead.

### An unplanned finding: production has no cost data at all

Not the swept-run hole (#109) — **every** run. In the cluster database:

```
agent_runs : 45 rows, 0 with usage   (2026-07-24 … 2026-07-31)
shifts     :  6 rows, 0 with spend,  total 0.0000
```

...while the gateway holds the spend for those same runs, down to `$0.1373`
for the most expensive.

The cause is not a defect in current code. The deployed image is
`ploegd:0.2.0-rc.14`, and `settleSpend` landed in `a1bccea` (2026-07-31),
**after** that tag — verified with `git merge-base --is-ancestor`.

> **Correction (2026-08-09).** This section first said "rc.15 never published
> an image". That was wrong, and the error was mine: the release pipeline
> strips the leading `v`, so the image is
> `harbor.webgrip.dev/webgrip/ploegd:0.2.0-rc.15` and it **exists**. I probed
> for `v0.2.0-rc.15`, got a 404, and concluded too much from it.
>
> rc.15 published normally — Forgejo run 1441, a `release` event, succeeded on
> 2026-07-31T04:48. The real blocker is different and worse: **the `release`
> job has failed on every push to `development` since then**, so no rc has been
> cut at all since rc.15 and the two fixes merged on 2026-08-08 have no image.
> Handed off separately for investigation.

It still has a live consequence worth stating plainly. `ClaimRole` authorizes
`budget − spent − reserved`. With `spent` permanently 0, only concurrently
*running* Runs constrain a claim — so a three-Round Shift with a pool of €6 can
authorize €6 in each Round. **ADR-0012's pool is not bounding total spend in
the deployed cluster**; it is bounding concurrency. Deploying current trunk
closes it, so the action is a release, not a code change.

## 10. How many trials, honestly (2026-08-09)

§2.5 set a floor of "n = 5 for a signal, n = 10 before believing a gap smaller
than ~15 points". The second half of that is wrong by more than an order of
magnitude, and computing it rather than asserting it is the difference.

Worst-case 95% Wilson width, and the trials per configuration needed before a
difference in pass@1 could clear the intervals:

| gap in pass@1 | trials per config |
|---|---|
| 60 points | 7 |
| 50 points | 12 |
| 40 points | 21 |
| 30 points | 39 |
| 25 points | 58 |
| 20 points | 93 |
| **15 points** | **167** |
| 10 points | 381 |

And what a given n can separate at all:

| n | widest 95% interval |
|---|---|
| 5 | 65 points |
| 10 | 53 points |
| 20 | 40 points |

At n = 5 only a near-total difference is real — 0/5 against 5/5. Two
configurations at 60% and 80% are **not** distinguishable there, and
`stats.Distinguishable` says so rather than letting the larger number win.

**This does not sink the design; it renames what the matrix is for.** Three
things stay sharp at small n, because they are per-trial and deterministic
rather than sampled:

- **L1 conformance** — 17 assertions, pass or fail on a single run. A
  violation is a defect on the first trial, not a rate.
- **L2 gate vectors** — G1–G7 per trial. "This configuration fails G4" is a
  fact about that run, and a configuration that fails G4 three times out of
  three needs no interval to be worth acting on.
- **L3 review metrics** — recall against twelve seeded defects is measured on
  one trial. Its variance across trials is worth reporting, but a reviewer
  that misses the money bug five times out of five has told you something at
  n = 5.

And **pass^k is informative at small n in a way pass@1 is not**: 5/5 against
3/5 is a visible reliability difference even where the pass@1 intervals
overlap, because it is a statement about consistency rather than a rate
estimate.

So the honest protocol:

1. **n = 5 per configuration** to shake out infrastructure, rank
   configurations, and catch large effects. Report every interval; let
   overlapping ones read as "not separated" rather than as a ranking.
2. **Escalate to n ≈ 20–40 only on a specific hypothesis** worth paying for —
   and note that even 40 only resolves a 30-point gap.
3. **Never publish a winner from overlapping intervals.** The matrix's job at
   homelab scale is to rank, to surface conformance and gate failures, and to
   price configurations — not to settle 10-point differences.

The money makes this concrete. At roughly €0.50–2 per trial, resolving a
single 15-point comparison at n = 167 costs €170–670 for two configurations.
That is not a homelab experiment, and the design should not pretend otherwise.

## 7. Sources

- [Introducing SWE-bench Verified — OpenAI](https://openai.com/index/introducing-swe-bench-verified/)
- [SWE-bench Verified Explained: 2026 Methodology, Tiers, Caveats](https://benchmarkingagents.com/swe-bench/)
- [SWE-bench (harness)](https://github.com/swe-bench/SWE-bench) · [SWE-smith](https://github.com/SWE-bench/SWE-smith)
- [UTBoost: Rigorous Evaluation of Coding Agents on SWE-Bench](https://arxiv.org/pdf/2506.09289)
- [Terminal-Bench: Benchmarking Agents on Hard, Realistic Tasks in CLIs](https://arxiv.org/abs/2601.11868) · [2.0 overview](https://explainx.ai/blog/terminal-bench-2-0-ai-agent-benchmark-evaluation)
- [Holistic Agent Leaderboard (HAL)](https://arxiv.org/abs/2510.11977)
- [Inspect AI framework review](https://neurlcreators.substack.com/p/inspect-ai-evaluation-framework-review)
- [METR: Algorithmic vs. Holistic Evaluation](https://metr.org/blog/2025-08-12-research-update-towards-reconciling-slowdown-with-time-horizons/)
- [CR-Bench: Evaluating the Real-World Utility of AI Code Review Agents](https://arxiv.org/abs/2603.11078)
- [CodeFuse-CR-Bench](https://arxiv.org/pdf/2509.14856)
- [Every AI code review vendor benchmarks itself, and wins — DeepSource](https://deepsource.com/blog/ai-code-review-benchmarks)
- [How Qodo Built a Real-World Benchmark for AI Code Review](https://www.qodo.ai/blog/how-we-built-a-real-world-benchmark-for-ai-code-review/)
- [Beyond pass@1: A Reliability Science Framework for Long-Horizon LLM Agents](https://arxiv.org/pdf/2603.29231)
- [ReliabilityBench](https://arxiv.org/abs/2601.06112)
- [Practical Mutation Testing at Scale — Google](https://research.google/pubs/practical-mutation-testing-at-scale-a-view-from-google/)
- [LLMs Are the Key to Mutation Testing — Meta](https://engineering.fb.com/2025/09/30/security/llms-are-the-key-to-mutation-testing-and-better-compliance/)
