# Design — execute-openspec-work-items

Change-local rationale only. No repository-level ADR is created (see adr.md).

## Context

The survey's integration shape 4 asks for a Work Item that names an OpenSpec
change to run from `openspec instructions apply --json`, with a reviewer gated
on `openspec validate --json`. The worker already clones, scans instruction
files, composes a prompt from the Task Spec and resolves an Outcome from forge
ground truth (`pkg/worker/worker.go`). A reading Run's verdict drives the fix
loop (ADR-0017), and a stuck Outcome parks the Work Item at `needs_human`
under both dispatch shapes (`pkg/shiftengine/engine.go`).

## Goals / Non-Goals

**Goals:**

- Work on Unfold itself: the change lives in `apps/ploeg/openspec`, not at the
  root.
- Deterministic gate: Ploeg runs validation, not the agent, so an agent cannot
  claim it passed.
- Nothing new over the network; runs in today's sandbox.

**Non-Goals:** see the proposal.

## Decisions

### A description line, not a structured field

A structured `openSpecChange` column would need a migration, a field in every
Tracker Provider and in the operator API, and a way to set it in each tracker's
UI. A line in the description needs none of that, works for Vikunja (HTML),
ClickUp (markdown), GitLab and operator admission alike, and keeps the content
authoritative at the tracker. The parser lives in `pkg/work` beside
`Reference` because it reads only the Work Item.

The identifier is validated as kebab-case before it reaches a path or an
argument vector, which closes path traversal and flag injection in one check.

### The worker finds the root

`openspec` resolves its root as the nearest `openspec/` directory from its
working directory. The worker walks the clone (depth-bounded, no symlinks) for
`openspec/changes/<id>` and runs the CLI in that directory's parent. Two
matches park the Run rather than guess.

### The brief rides on the Task Spec

Putting the brief on `TaskSpec.OpenSpec` rather than only into the prompt text
keeps the prompt a pure function of the spec (`ComposePrompt`), makes it
visible to every adapter, and makes the contract test pin it. The CLI's JSON is
not copied through verbatim: its paths are absolute to the worker's clone and
its instruction text is the target repository's, so the worker renders a
repository-relative, size-capped brief and labels the change's own
instruction as ranked below the delivery contract (ADR-0030).

The fallback reads only three well-known files, each capped, so a change with
large specs cannot crowd out the Work Item (the same reason
`maxBriefingBytes` exists for Round findings).

### Where the gate sits

The gate runs in the worker after the harness exits, on the pushed commit:
the worker fetches the Run's branch and checks it out detached before
validating, so uncommitted edits in the agent's working tree cannot pass a
gate the pull request would fail.

- **Writer:** `stuck`. Considered: keep `pr_opened` and add findings. Under
  uniform dispatch a single-writer Shift settles by its last Outcome, so a
  finding alone would still reach `awaiting_review`. `stuck` is the only
  Outcome that stops the hand-off in both dispatch shapes, and it carries the
  output as its reason (R4). Considered: `failed`, which retries. A retry does
  not see why it failed, so it would repeat the mistake until attempts run out.
- **Reader:** `request_changes` with the output ahead of the agent's findings.
  That reuses ADR-0017's loop: the writer's next Round is briefed with the
  validation output.
- **Gate cannot run** (no CLI, timeout, unparsable output): `stuck` for both.
  Downgrading to "no verdict" would let a plan exhaust and settle
  `awaiting_review` without the gate ever passing, which is what the gate
  exists to prevent.

### Environment

The CLI gets `PATH`, a scratch `HOME`, `OPENSPEC_TELEMETRY=0` and
`DO_NOT_TRACK=1`, nothing else: no forge token, no model key. It is looked up
with `exec.LookPath`; there is no `npx` path, so a missing binary never turns
into a registry pull that times out in the sandbox.

## Risks / Trade-offs

- **Images without the CLI park every gated Run.** Harness images are built
  outside this repository, so an OpenSpec Work Item on such an image always
  ends at `needs_human` with the reason "openspec CLI not available". That is
  deliberate: a gate that silently does not run is worse. Proposed follow-up:
  bake `@fission-ai/openspec` into the runner images.
- **Validation is structural.** A writer can make a change "valid" by deleting
  requirements. The reader prompt tells the reviewer to diff the change's own
  files against the base branch and treat such edits as findings, the same
  stance ADR-0030 takes for `AGENTS.md`.
- **Repository-owned schema.** The CLI reads the repository's
  `openspec/config.yaml` and schema. These are YAML and templates, not code,
  and the CLI runs without credentials.

## Open Questions

- Should the directive also select the Team (for example routing every
  OpenSpec Work Item to a Team with a reviewer)? Routing Rules stay the only
  routing input today (R11); not proposed here.
- If this becomes a durable cross-repository commitment, it may deserve an ADR
  of its own after it has carried real work.
