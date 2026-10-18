# Briefings: versions, catch-up, usefulness and replay

A Run's briefing is its view of the blackboard ([ADR-0124](../adrs/0124-agents-communicate-through-a-blackboard-of-four-owned-sources-that-ploeg-reads-at-a-version-vector.md)): what the forge, Ploeg, the Tenant's graphs and the tracker held when it was claimed. This page says what each Run records, how a Run catches up, how to tell whether Work Item Notes help, and how to replay a briefing.

## What a Run records

| Column on `agent_runs` | Written by | When |
| --- | --- | --- |
| `board_claimed_at`, `board_ploeg_sequence`, `board_tracker_revision` | ploegd | At the claim: the highest audit event id and the Work Item's tracker revision |
| `board_forge_commit`, `board_notes_commit`, `board_concepts_commit` | worker, `briefed` checkpoint | After the prompt is composed: the commit the clone stands on and the graph commits the knowledge was read at |
| `briefed_notes`, `briefed_concepts` | worker | The keys of the Work Item Notes and the paths of the concepts the briefing showed |
| `briefing_sections` | worker | Each section's fixed budget, bytes used, items shown and left out, and digest |

Every column is written once and is `NULL` for a Run claimed before migration `0063`, which reads as unknown. Each catch-up with changes, and every `before_finish`, is a row in `run_board_catch_ups`. A catch-up names Ploeg's events, people's instructions, new notes the Run may read (with their provenance), people's answers to questions and contradictions flagged on briefed notes.

## Sections and budgets

| Section | Budget | Holds |
| --- | --- | --- |
| `findings` | 8,000 bytes | Earlier Rounds' findings, reviews, a restart, people's notes and board changes left for the Round, newest Round first |
| `work-item` | 3/8 of the knowledge budget (9,000 of 24,000 bytes) | The Work Item's `work-item` notes, newest first, and concepts from its context |
| `repository` | 3/8 | The repository's `repository` notes, its own bundle and the graph's repository concepts |
| `client` | 1/8 | The graph's Client concepts |
| `tenant` | 1/8 | The graph's Tenant concepts and configured bundles (`PLOEG_KNOWLEDGE_DIRS`) |
| `review_findings` | 8,000 bytes | The last review's findings by id, with the writer's answers for a reader (ADR-0121) |
| `questions` | 4,000 bytes | Questions earlier Runs asked and their answers (ADR-0121) |
| `active_work` | 2,000 bytes | Other live Shifts on the same repository and the paths they touch, overlaps first (ADR-0123) |

`PLOEG_KNOWLEDGE_BUDGET_BYTES` sets the knowledge budget the four levels split. A level's unused budget is not lent to another.

## Catch-up per harness

The worker keeps `PLOEG_BOARD_FILE` current every two minutes and the prompt tells the agent to read it before it pushes and before it gives its findings or verdict. When the agent's turn ends the worker asks once more (`before_finish`):

| Harness | Board file | Before finish |
| --- | --- | --- |
| `acp` (OpenCode, Qwen Code, Goose, custom) | Yes | One further prompt turn with the changes, only when something changed |
| `claude-code`, `openhands`, `exec` | Yes | Recorded as `next_round`; the next Round's briefing carries it as "board changes during round N" |
| `claude-environment` | No (the session runs elsewhere) | Recorded as `next_round` |

## Do Work Item Notes help?

`ploeg_runs_briefed_total{notes="with"}` against `{notes="without"}` shows how many Runs saw notes. To relate a note to the Rounds and cost of the Shifts it briefed, run against Ploeg's database:

```sql
-- Per note: how many Runs it briefed, and the Rounds and spend of their Shifts,
-- next to the Shifts of the same Team whose Runs were briefed with no note.
WITH briefed AS (
  SELECT unnest(r.briefed_notes) AS note, r.shift_id
  FROM agent_runs r
  WHERE r.board_briefed_at IS NOT NULL AND r.shift_id IS NOT NULL
), shift_cost AS (
  SELECT s.id, s.team, s.round AS rounds, s.spent AS spent_usd
  FROM shifts s
  WHERE s.closed_at IS NOT NULL
), without_notes AS (
  SELECT c.team, avg(c.rounds) AS rounds, avg(c.spent_usd) AS spent_usd
  FROM shift_cost c
  WHERE NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.shift_id = c.id AND cardinality(COALESCE(r.briefed_notes, '{}')) > 0)
    AND EXISTS (SELECT 1 FROM agent_runs r WHERE r.shift_id = c.id AND r.board_briefed_at IS NOT NULL)
  GROUP BY c.team
)
SELECT b.note, c.team, count(DISTINCT c.id) AS shifts,
       round(avg(c.rounds), 2) AS avg_rounds, round(avg(c.spent_usd), 2) AS avg_spent_usd,
       round(w.rounds, 2) AS team_avg_rounds_without_notes, round(w.spent_usd, 2) AS team_avg_spent_usd_without_notes
FROM briefed b JOIN shift_cost c ON c.id = b.shift_id LEFT JOIN without_notes w ON w.team = c.team
GROUP BY b.note, c.team, w.rounds, w.spent_usd
ORDER BY shifts DESC, b.note;
```

A note whose Shifts take fewer Rounds and less spend than the Team's Shifts without notes is earning its place; one that never changes either is a candidate to supersede. Treat a handful of Shifts as anecdote: the comparison needs tens of Shifts per Team before it means anything.

## Replay a briefing

`GET /api/v1/operator/runs/{id}/briefing` answers the Run's version vector, its sections, the findings section rebuilt from the records its claim read with `matches` (true when the digest equals the one the worker reported), `replayed` with the review findings, questions and active work sections rebuilt from what Ploeg holds now (active work is live, so it matches only while nothing changed), the notes it showed read back by key with their provenance, the concepts as paths at `versions.conceptsCommit`, and every catch-up it was given. `matches` false means a record the claim read changed afterwards or the worker rendered differently; the rebuilt text shows what Ploeg holds now.
