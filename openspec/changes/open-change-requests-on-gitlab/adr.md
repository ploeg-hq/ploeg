# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-09-02
- **Reviewer**: Claude (session with Ryan Grippeling)
- **Change**: `open-change-requests-on-gitlab`

## In-Force ADR Context Reviewed

Reviewed 0001–0022 and the Records index. In force = accepted and not
superseded. Those that constrained this change:

- `0014-work-target-is-a-work-item-attribute.md` — decided the shape of this
  change before it was written. The repository is a property of the work,
  resolved at ingest; the forge is part of naming a repository, so the dialect
  belongs on the same object. Deciding it per Team would have re-coupled
  capability to codebase, which is what 0014 decoupled.
- `0016-forge-registry-and-per-run-repo-scoped-credentials.md` — `Target.Forge`
  exists because of this record, and ploegd already resolves a provider through
  it. The worker reading the same field is the extension, not a new idea.
- `0013-push-rights-are-minted-per-run.md` — the binding constraint. Tier 1
  (a read-only credential for reading Roles) ports to GitLab and is configured
  and pinned by a golden. Tier 2 does NOT port: minting goes through a Forgejo
  admin endpoint with no GitLab equivalent. Recorded as a non-goal and as a
  re-evaluation trigger rather than quietly left undone.
- `0011-the-pull-request-is-the-blackboard.md` — the reason the missing worker
  half was load-bearing rather than cosmetic. If no change request exists, the
  blackboard does not exist, and findings have nowhere to land.
- `0010-shift-owns-the-item-lease-owns-the-branch.md` — unchanged. The dialect
  is read after the claim and adds no state a Run is claimed on, so no claim or
  queue predicate moves.

`0017`–`0019`, `0021` are `proposed`, not in force, and none of them constrain
this change.

## Repository-Level ADRs Created

- `docs/adrs/0023-the-forge-dialect-travels-on-the-work-item.md` — the dialect
  is a property of the Work Item and varies per Run; the forge URL and
  credential stay deployment-global because a worker pod holds one of each.
  Status `proposed`.

## Notes

**The code was written before this record, and the record is `proposed`.** The
implementation came out of a survey of what rc.31 could and could not do for
a downstream GitLab deployment; the durable commitment was extracted afterwards. That
is the opposite of the intended order, and it is why nothing here marks 0023
accepted: the schema's rule against retro-justification is exactly the risk, so
the record states the alternatives that were genuinely weighed (per-Team,
per-deployment) and what each would have cost, and a human ratifies it or does
not.

If 0023 is rejected, the change does not shrink — it changes shape. Per-Team
dialect is roughly the same amount of code with a different owner for the
answer.

**0023 carries `review-by: 2027-01-31`** and is added to that row of the review
calendar, joining the eight records that rest on assumptions only production
can test.
