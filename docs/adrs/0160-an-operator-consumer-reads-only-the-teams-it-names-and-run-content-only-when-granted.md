---
status: proposed
date: 2026-10-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# An operator consumer reads only the Teams it names, and Run content only when granted

## Context and Problem Statement

An Operator Consumer configured without `teams` read every Team, and every consumer that could read a Run could also read its transcript, its briefing, the context people attached to its Work Item and the Work Item Notes under review. A read-only consumer such as the one `ploeg-mcp` uses for people's AI clients needs Work Item metadata only, yet held the same read as the console that shows transcripts. A consumer that leaves `teams` out by mistake saw everything. What should a consumer read when its configuration says nothing?

## Decision Drivers

* A missing line of configuration must narrow access, not widen it.
* Transcripts, briefings, attached context and notes carry what agents and people wrote, including what they read from private repositories; metadata does not.
* An executing consumer such as the console already acts on Work Items and must keep showing their Runs.

## Considered Options

* **No `teams` reads no Team, `["*"]` reads every Team; a `readContent` capability, defaulting to `execute`, gates content reads**
* Keep omitted `teams` as every Team and add only `readContent`
* Split read-only consumers into a separate configuration

## Decision Outcome

Chosen option: "**No `teams` reads no Team, `["*"]` reads every Team; `readContent` gates content reads**", because both defaults now deny, and an explicit value restores the old reach in one line.

* `teams` omitted, null or empty grants no Team. `["*"]`, on its own, grants every Team. The chart requires `teams` on every consumer, so an upgrade with a consumer that leaves it out fails at render instead of silently losing access.
* `readContent` grants `GET /api/v1/operator/runs/{id}/transcript`, `/runs/{id}/briefing`, `/work-items/{id}/context` and `/knowledge/reviews`. Unset, it equals `execute`. Without it those routes answer 403 `content_forbidden`; every other read route is unchanged.

### Consequences

* Good, because a consumer configured without thought reads nothing, and a read-only consumer reads metadata only.
* Good, because the console and other executing consumers keep their reach unless configured otherwise.
* Bad, because a deployment whose consumer omitted `teams` must add `teams: ["*"]` (or its Teams) before it upgrades, and a read-only consumer that showed transcripts must add `readContent: true`. Accepted: the chart refuses the first at render, and both are one line.
* Bad, because Work Item detail still carries descriptions, findings, questions and Run summaries for any consumer in scope. Gating those is a separate decision.

### Confirmation

`go test ./pkg/httpapi/ -run 'TestAConsumerThatNamesNoTeamReadsNoTeam|TestOnlyAConsumerGrantedContentReadsIt|TestAReadOnlyConsumerReadsMetadataButNoTranscriptContextOrNotes'` and `go test ./pkg/config/ -run TestOperatorChartKeepsConsumerCredentialsInController`, both run by `mise run verify`.

## Pros and Cons of the Options

### Keep omitted `teams` as every Team and add only `readContent`

* Good, because no deployment needs a change for team scope.
* Bad, because a forgotten `teams` still grants every Team.

### Split read-only consumers into a separate configuration

* Good, because the two kinds of consumer could not be confused.
* Bad, because it duplicates token, person-token and scope handling for no gain over one capability.
