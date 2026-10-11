## Context

`skills.ForRun` returns the role skills; `skills.Install` writes them under the
Run's home (`.agents/skills`, plus the harness directory from
`pkg/worker/adapters.go`); `runSupportSection` in `pkg/worker/verify.go` lists
their paths. Repository skills in the clone are discovered by each harness on
its own: OpenHands and opencode read `.agents/skills`, Claude Code reads
`.claude/skills`. The knowledge pack (ADR-0065, `pkg/knowledge.Select`) already
scores OKF concepts against a `Query` of title, description and labels with
weighted term matches and a stopword list.

## Goals / Non-Goals

**Goals:** tell each Run, by name and path, the few skills that bear on its
Work Item; keep the choice deterministic, bounded and recorded.

**Non-Goals:** see the proposal. In particular no model call, and no skill from
untrusted content.

## Decisions

### Score skills with the knowledge pack's matcher

A skill becomes a scored item with three fields: its name (weight 4), its
description (weight 2) and its `metadata.ploeg-tags` (weight 2). The query is
the knowledge `Query` plus the names of attached context files. Reusing
`pkg/knowledge` keeps one notion of relevance; the alternative, a separate
matcher for skills, would drift from it.

### Paths select by glob, not by score

`metadata.ploeg-paths` holds comma-separated globs (the Agent Skills
specification allows only string values in `metadata`). A skill whose glob
matches a changed path is selected ahead of scored skills, with the path as the
reason. Changed paths are known for a reader (`git diff --name-only
base...branch`) and for a writer from Round 2 on; a first writer has none.

### Name the skill in the prompt; do not move it

A repository skill stays in the clone, where the harness already finds it, and
the prompt names its clone path. A configured skill is installed like a role
skill. Naming beats installing because the measured failure is a model not
loading a skill it could find, not a skill missing from disk.

### Bound the list

`PLOEG_SKILL_LIMIT` (default 5) caps the selected skills besides the role
skills. A long list is a list the model skims.

## Risks / Trade-offs

- [A weak description selects nothing] → `ploeg-tags` lets a repository add the
  words its Work Items use; `ploeg-okf pack`'s equivalent for skills
  (`ploeg-okf skills`) shows the selection for a title before a Run.
- [Common words match everything] → the stopword list is shared with the
  knowledge pack; extending it improves both.
- [A malicious repository skill] → a repository's skills are as trusted as its
  `AGENTS.md`, which the instruction-file scan already covers; selection adds
  no new trust.

## Migration Plan

Additive. With no repository skills and no configured directories, only the
TaskSpec changes. Roll back by unsetting nothing: removing the code restores
the fixed role skills.

## Open Questions

- Should a Team be able to require a skill for every Run (a `skills:` list in
  Team configuration)? Left out until a Team asks.
- Should `ploeg-paths` reuse Attention Paths once Unfold's ADR-0010 is built?
