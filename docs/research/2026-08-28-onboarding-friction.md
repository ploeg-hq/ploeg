# Onboarding a board is five manual acts across four systems — field report

Date: 2026-08-28 · Occasion: routing the `twente.dev` board (Vikunja project 50) so its
first `agent-ready` ticket (VIK-686) can dispatch · Tickets: VIK-779 (this rollout),
VIK-781 (the design decision this report argues for).

**Verdict up front:** onboarding works, but only for an operator who already knows the
whole system, and every missing step fails *silently*. If Ploeg wants to be the best
dispatch plane in the world, onboarding a board must become **one declarative act that
ploegd converges**, with every failure loud and every piece of wiring introspectable.
Nothing below requires widening the accepted problem space (design.md §2) — no board
UI, no grooming semantics; it is routing, reconciliation and observability, which are
already ours.

## What routing one board actually took

| # | Act | System | Failure mode when skipped |
|---|-----|--------|---------------------------|
| 1 | Add a `projects:` entry to `kubernetes/apps/ploeg/ploeg/app/helmrelease.yaml`, open an MR, wait for review + Flux reconcile + ploegd reboot | homelab-cluster git | board simply unknown; hours of latency for one list entry |
| 2 | Register the ploegd webhook on the Vikunja project, secret fetched by a human from OpenBao | Vikunja UI + OpenBao | assignment fires **nothing**, no error anywhere |
| 3 | Share the team user into the project with read access | Vikunja UI | assignment 403s — visible only in ploegd logs, which the board owner never sees |
| 4 | Give the team's Forgejo user access to the target repo | Forgejo | first run fails at clone/push time, after dispatch |
| 5 | Refine tickets to the board contract's dispatch marker | tracker (PO skill) | outside Ploeg's scope, but part of every real time-to-first-run |

Four systems, two credentialed UI sessions, one cluster MR — for what is conceptually a
single sentence: *"this board dispatches to that repo."*

## Friction inventory, worst class first

1. **Silence.** Steps 2–4 fail without telling the person who acted. The routing
   config's own comment documents it: *"assignment 403s (an assignee needs read access)
   and nothing fires."* The operator's mental model is a UI action; the error lands in
   a pod log. This is the class that kills trust in a dispatch plane — an assignment
   that goes nowhere is indistinguishable from a plane that is down.
2. **Declared ≠ live, and nobody can tell.** The same comment block admits: *"Today
   only Ploeg Test has both"* (webhook + share). The manifest declares eight routed
   boards; how many can actually fire is tribal knowledge with no introspection
   surface. Webhooks silently outlive or predate config — a Vikunja rebuild drops
   them and nothing notices (backlog #6 already flags Vikunja's no-retry deliveries;
   the same reconciliation logic should own webhook *existence*).
3. **Knowledge lives in YAML comments.** The helm values file is its own runbook: the
   wrong-repo incident (PR webgrip/ploeg#30, 2026-07-30), the forge-default resolution
   bug (erfbeeld#9's approve that never published), the per-board wiring caveats — all
   prose above a list. Correct, honest, and unscalable: the file teaches the next
   operator only if they read all 90 comment lines.
4. **Credential choreography.** The webhook secret lives in OpenBao, is fetched by a
   human, and pasted into a tracker UI. ploegd already holds a tracker token that can
   manage webhooks; the human in this loop adds only latency and error surface.
5. **Latency by architecture.** Config-repo MR → review → merge → Flux → reboot, for a
   routing change whose blast radius is one board. Boot-time name resolution (refuse to
   start on a dangling name) is a good guard with the wrong lever: it turns *someone
   renamed a project* into *the whole plane refuses to boot*.
6. **Idempotency gaps show up as user-visible mess.** Homelab Roadmap carries six
   byte-identical open tickets (VIK-440…445, bridge-OOM). Whatever created them lacked
   a natural-key upsert — exactly backlog #4/#10 — and the cost landed on the board as
   noise a human must now clean up. Every automation writing to a tracker needs a
   dedup key, first time, every time.
7. **Dispatch has no floor.** Assignment dispatches whatever was assigned. The board
   contract's `agent-ready` gate is convention, invisible to ploegd; a mis-click
   dispatches an unrefined ticket into an agent run. (Grooming stays out of scope —
   but *refusing to route* work that lacks the board's declared marker is routing
   policy under ADR-0015, not grooming.)

## Target UX — the standard to design against

- **One act.** A board owner declares `board: twente.dev → repo, branch, forge` once —
  CLI (`ploegctl board add`) or a small manifest — and ploegd converges everything
  else: resolves the project, registers/repairs the webhook with its own token,
  verifies share + repo access, reports READY or the first broken link. Kubernetes
  taught everyone this grammar; a dispatch plane should speak it.
- **Converge, don't instruct.** Anything ploegd can do via an API it already has a
  token for is ploegd's job, continuously — webhook existence is reconciled like a
  controller reconciles a Deployment, not re-done by hand after every tracker rebuild.
- **Loud failure, in the user's own UI.** An assignment that cannot dispatch writes
  the reason back where the user acted: a ticket comment ("Ploeg saw this assignment,
  could not dispatch: no webhook secret / no repo access / no `agent-ready` marker")
  plus an audit row. The tracker is the interface the user chose; meet them there.
- **Wiring is data.** `ploegctl boards list`: declared vs live per board — webhook
  present/signed, team shared, repo reachable, last-event-at, last-dispatch-at. The
  "only Ploeg Test has both" sentence should be a query result, never a comment.
- **Measure onboarding as TTFR** — time from "board declared" to "first PR opened".
  Target: under ten minutes, most of it the agent run itself.

## What this does *not* argue for

No kanban/board UI (boundary re-confirmed — status is CLI/API/Grafana). No grooming or
DoR semantics inside Ploeg — the marker-label guard reads a label, it does not define
one. No per-tracker special cases in core: webhook management and share verification go
through the provider SPI like everything else.

## Actioned

Backlog §L (items 117–124) carries the build-order items derived from this report.
VIK-781 on the Ploeg board owns the design decision; VIK-779 tracks the manual rollout
this report was extracted from — the last board that should ever be onboarded by hand.
