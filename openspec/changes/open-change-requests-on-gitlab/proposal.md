# Open change requests on GitLab

## Why

ploegd has spoken GitLab since rc.31: `pkg/provider/gitlab` comments on a merge
request and verifies inbound webhooks. `ploeg-worker` never learned. It polled
`/api/v1/repos/{owner}/{name}/pulls` and its briefing named the Forgejo API as
the way to open a change request, so against a GitLab target nothing was ever
opened.

That one absence removes most of the review loop. With no change request the
Shift has nothing for a reviewing Role to comment on, `publishRound` logs
"findings not published: no pull request on this shift yet", no forge webhook
can arrive, and `close-the-review-loop`'s fix Rounds never open. Ingest,
routing, Rounds and budgets all work; the work simply never leaves the factory.
Every step of that failure is silent — the writing Run reports success.

Less was missing than it looks, which is why this is one change and not three.
Three things are already forge-agnostic and are untouched here:

- `pkg/worker/git.go` builds clone and push URLs from owner and name, which is
  already correct for a GitLab subgroup path;
- `pkg/shiftengine`'s `prPathRe` already matches `/merge_requests/(\d+)`;
- the Shift takes its change-request URL from the OutcomeReport links, not from
  the forge poll — the poll is ground truth for a Run that died before
  reporting.

The gap is exactly two places that name a forge: the poll, and the briefing.

The driving consumer is a downstream deployment that routes a GitLab project.
It cannot reach phase 4 until this lands.

## What Changes

**Seam:** Executor — the worker→forge edge (`pkg/worker`, `ops/helm/ploeg`),
plus the additive Task Spec field that carries the answer to it.

- **A forge DIALECT on `harness.RepoRef`**, additive and optional on
  `taskspec.v1`. Empty means `forgejo`, so every Task Spec and every stored
  target written before this field keeps its exact meaning. The value travels
  on the Work Item — `pkg/work.Target` has carried `Forge` since ADR-0016 —
  and falls back to a deployment default.
- **`findOpenChangeRequest` dispatches on the dialect.** GitLab filters
  `source_branch` server-side, so unlike the Forgejo call it cannot be defeated
  by a repository with more than fifty open change requests. The project is
  addressed by URL-encoded full path, which is what makes a subgroup work.
- **The briefing dispatches too, in vocabulary as well as endpoint.** An agent
  told to open a "pull request" against GitLab looks for an endpoint that is
  not there. Noun and endpoint come out of one switch so they cannot drift.
- **An unknown dialect fails loudly**, rather than falling back to Forgejo and
  reporting "no change request" forever — which is indistinguishable from an
  agent that never opened one.
- **`executor.forge` selects one active forge**; `executor.gitlab` configures
  it. A new `ploeg.forge` helper resolves whichever is active into one shape,
  so no template reads `.forgejo` or `.gitlab` directly.
- **A fourth golden render.** Selecting a forge changes the worker pod — a
  different credential Secret and a different API — and the worker pod is the
  boundary the goldens exist to police.

## Capabilities

**New Capabilities**

- `forge-dialect` — which forge a Run acts against, where that is decided, and
  what happens when the answer is unknown.

**Modified Capabilities**

- none. `forge-provider-forgejo` describes ploegd's provider seam and is
  untouched: this change is the worker half, which has no ForgeProvider.

## Non-goals

- **ADR-0013 tier 2 on GitLab.** Per-run push credentials are minted through
  `/api/v1/admin/users/forge`, a Forgejo admin endpoint with no GitLab
  equivalent. GitLab's nearest analogue is a project access token — a different
  escalation with a different blast radius, and one that deserves its own
  record rather than an implied one. GitLab uses the shared token, which is the
  documented pre-tier-2 behaviour and not a regression. Tier 1, the
  reader/writer credential split, DOES port and is configured.
- **Two forges from one deployment.** The dialect varies per Run; the forge URL
  and credential do not, because a worker pod holds one of each. A Work Item
  routed to a forge the release is not configured for fails at run time.
- **A ForgeProvider SPI on the worker side.** Two dialects justify a switch.
  Three would justify an SPI, and ADR-0023 names that as the trigger to
  supersede it.
- **Changing anything about how ploegd publishes findings.** It already picks a
  provider from `Target.Forge`; this change makes the worker read the same
  field, and touches neither `pkg/provider` nor `pkg/shiftengine`.
