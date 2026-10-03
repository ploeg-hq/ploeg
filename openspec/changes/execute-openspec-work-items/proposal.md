## Why

Unfold is developed with OpenSpec, and so are other repositories Ploeg works
on. An OpenSpec change already says what to build (proposal, specs, design)
and how far it has got (tasks), and its CLI has a JSON contract for both
(`openspec instructions apply --json`, `openspec validate --json`). Today a Work
Item that means "implement change X" reaches the builder as free text: the
agent has to find the change itself, nothing checks that it kept the change
consistent, and a structurally broken change can land at `awaiting_review`.
Integration shape 4 of the
[orchestration survey](../../../docs/research/2026-09-26-agent-orchestration-landscape.md)
(§8, §10) names this as the cheapest method-level integration that does not
compete with Ploeg's dispatch.

## What Changes

**Seam touched:** Harness (worker → coding agent). One additive TaskSpec field;
no store, claim, routing or executor change.

- **A Work Item can name an OpenSpec change.** A line `openspec: <change-id>`
  in the Work Item description names it. The worker parses it after the clone;
  a malformed or conflicting directive parks the Run stuck with a reason (R4).
- **The worker locates the change in the clone** (`openspec/changes/<id>` at
  the repository root or in a nested application, as in Unfold's
  `apps/ploeg/openspec`) and refuses an absent or ambiguous one.
- **The Role is briefed from the change.** With the `openspec` CLI on the
  image's PATH the brief is `openspec instructions apply --change <id> --json`;
  without it, the change's `proposal.md`, `design.md` and `tasks.md` are read
  directly. The brief travels on a new optional TaskSpec field, `openSpec`, and
  the prompt frames it as the specification, ranked below the delivery
  contract (ADR-0030).
- **Strict validation gates the hand-off.** After a writing Run that opened or
  updated a pull request, and after a reading Run on the branch under review,
  the worker itself runs `openspec validate <id> --type change --strict --json`
  on the pushed branch. A writer that fails it, or whose image cannot run it,
  reports `stuck` with the output. A reviewer's verdict becomes
  `request_changes` on failure, so the review loop (ADR-0017) sends it back.
- **No network.** Only a preinstalled binary is used, never `npx`, with
  OpenSpec telemetry off and no credentials in its environment.

## Capabilities

### New Capabilities

- `openspec-work-items`: naming an OpenSpec change on a Work Item, briefing a
  Role from it, and gating hand-off on its strict validation.

### Modified Capabilities

- none. The blackboard, verdict and Shift semantics are used as they are.

## Non-goals

- **A structured tracker field.** Trackers keep the Work Item's content (the
  invariant in `openspec/config.yaml`); a body line works for every Tracker
  Provider and for operator admission without a migration or adapter change.
- **Creating or archiving changes.** A Run implements an existing change;
  `openspec archive` stays a human step after merge.
- **Installing the CLI in harness images.** Those images are built outside this
  repository. Without the CLI, briefing falls back to files and the gate parks
  the Run for a person.
- **Judging whether the code satisfies the specs.** Strict validation proves
  the change is well-formed, not that the implementation matches it; that is
  the reviewing Role's job.

Checked against `design.md` §2: no board UI, persistent agent, model serving,
grooming semantics or connector matrix.

## Impact

- Code: `pkg/work` (directive parsing), `pkg/worker` (locate, brief, gate,
  prompt), `pkg/harness` (`TaskSpec.OpenSpec`).
- Contracts: `docs/contracts/taskspec.v1.schema.json` gains the optional
  `openSpec` object, pinned by `pkg/harness/contract_test.go`.
- Docs: `docs/contracts/README.md` and the board guide describe the directive.
- Rules: R4 (every stuck carries a reason), R6 (the worker stays
  single-purpose: it runs a local CLI, calls no new service), R2 (crash
  behaviour unchanged).
