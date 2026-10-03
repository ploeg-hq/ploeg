# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-07-29
- **Reviewer**: Claude (session with Ryan Grippeling)
- **Change**: `run-multi-agent-shifts`

## In-Force ADR Context Reviewed

Reviewed the full corpus (0001–0016; highest number in use: 0016; supersession
graph is empty — no record supersedes another yet). In force = accepted and not
superseded per the Records index: 0001–0014. Records that constrained this
design:

- `0010-shift-owns-the-item-lease-owns-the-branch.md` — the entire concurrency
  model: writer takes the Lease, readers take none, a Round is a reader fan-out
  or a single writer; agents in one Round never observe each other. D2/D3/D9
  design inside it.
- `0011-the-pull-request-is-the-blackboard.md` — findings ride the existing
  `OutcomeReport`, Ploeg publishes to the PR and injects into the next prompt;
  agents gain no tooling. Fixes D4's shape (prose, not a findings schema).
- `0012-two-level-budgets-authorized-and-settled.md` — pool on the Shift,
  authorization per Run as a sum over running Runs, settlement on report. D7
  wires the already-built authorization into the minted key.
- `0013-push-rights-are-minted-per-run.md` — tier 1 (readers read-only) is in
  scope; tier 2 (per-writing-Run mint/revoke) is an explicit non-goal. D8
  implements tier 1 by chart knob given a private-repo constraint the record
  did not weigh (no anonymous clone), documented as a gap until the read-only
  token exists in ops.
- `0014-work-target-is-a-work-item-attribute.md` — the Target rides the Work
  Item; the `ClaimRole` RETURNING fix exists to honour it on the new claim
  path.
- `0008-litellm-is-the-credential-and-metering-seam.md` — per-run keys and the
  `ploeg-<12hex>` alias are untouched; D7 only changes the ceiling the mint
  requests.
- `0015`/`0016` are **proposed**, not in force — nothing here designs against
  them; `TrackerEvent.Team` removal and the forge registry stay out of scope.

## Repository-Level ADRs Created

none — no durable architectural decision was introduced by this change. Every
design.md decision (D1–D11) is a change-local application of 0010–0013:
hook placement, engine call sites, config plumbing, additive contract fields,
and delivery order. The one decision that would qualify — verdict-driven round
advancement with a cap — is deliberately excluded from this change and owes
the ledger ADR-0017 as part of `close-the-review-loop`, before any engine code
learns the word "verdict".

## Supersessions

none

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/webgrip/ploeg/internal/ledger	0.019s
```

(run via `docker run --rm -v "$PWD":/src -w /src golang:1.25`, per mise.toml)

## Notes

For a human to ratify, not the agent's to accept:

- **D8's interim posture**: until ops mints a read-only forge token
  (`homelab-cluster` work), reading Runs carry the read-write builder token
  and the writer/reader boundary is scheduling-plus-prompt only. ADR-0013
  tier 1 is delivered as a chart knob (`executor.forgejo.readTokenSecret`);
  turning the credential boundary on is one secret, zero code. If this gap is
  acceptable for milestone A, no action; if not, the ops token becomes a
  blocker before the first multi-role run.
- ADR-0015 and ADR-0016 remain `proposed`; this change neither advances nor
  contradicts them.
