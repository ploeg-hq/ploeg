---
status: accepted
date: 2026-11-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-27
---

# Scope is frozen until an outside design partner runs real tickets on Ploeg

## Context and Problem Statement

On 2026-11-27 Ploeg had one maintainer, one GitHub star, no outside adopter,
148 release candidates of 0.2.0 and no stable 0.2.0. The scheduled quickstart
([#400](https://github.com/ploeg-hq/ploeg/issues/400)) and the attack drill
([#487](https://github.com/ploeg-hq/ploeg/issues/487)) were red. The decision
ledger holds more than two hundred records, many of them proposed and unbuilt,
and the source implements more trackers, forges and harnesses than any live
record qualifies.

The owner's red-team of the value proposition on the same day
([0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md))
found that what lasts is a governance plane people run themselves and the
evidence behind its claims. Neither is proven until someone outside the project
runs real work on it. Adding scope before then grows the surface that has to be
qualified, and delays the release an outsider needs.

What may change in Ploeg until an outside organisation uses it?

## Decision Drivers

* **An outsider needs a stable release and a green first hour.** A candidate
  line with no stable tag and a red quickstart is not something a design
  partner can adopt.
* **Every claim has to stay checkable** (0032). Each new provider, harness or
  record is another claim the support matrix and the threat model must carry.
* **One maintainer.** Time spent on new scope is time not spent on the release,
  the drills or the partner.

## Considered Options

* Freeze scope until one outside design partner runs real tickets on Ploeg
* Keep adding scope, and release when the candidates settle
* Freeze nothing, and only add release blockers for the quickstart and the
  attack drill

## Decision Outcome

Chosen option: "**Freeze scope until one outside design partner runs real
tickets on Ploeg**", because the release and the partner are the evidence the
positioning in 0032 is missing, and nothing else moves them.

### What the freeze means

1. **Ship v0.2.0 stable.** The quickstart green
   ([#400](https://github.com/ploeg-hq/ploeg/issues/400)) and the attack drill
   green ([#487](https://github.com/ploeg-hq/ploeg/issues/487)) are release
   blockers, beside the gates the stable channel already has.
2. **No new decision records**, except one that records a decision a fix or the
   design partner forces.
3. **No new research documents**, except the one exploration the owner asked
   for: Ploeg as a neutral governance layer above vendor runners.
4. **The supported profile is narrowed to one tracker and one forge** for the
   design partner.
5. **Explicitly allowed:** bug fixes, security fixes, qualification work, and
   documentation of existing behaviour.

### When the freeze ends

The freeze ends when an outside organisation has run real tickets on Ploeg for
30 days. "Outside" means an organisation that employs no maintainer and is not
the maintainer's own business; "real tickets" means work from its own tracker
on its own repositories, not a demo.

### Open question

* **Which tracker and forge pair is the supported profile for the design
  partner?** Owner: Ryan Grippeling. Recommendation: GitHub Issues and GitHub,
  the only pair qualified live
  ([support matrix](../reference/support-matrix.md)); Forgejo stays tested in
  CI. The trade-off: GitHub is where an outside organisation most likely
  already works and the only pair with live evidence, while the self-hosted
  Forgejo path, which Ploeg was built for, keeps only CI evidence until a
  partner needs it.

### Consequences

* Good, because the work that remains is the work an outsider needs: a stable
  release, a green quickstart, a green attack drill and one qualified pair.
* Good, because the ledger and the research folder stop growing faster than
  the code they describe.
* Bad, because proposed records that are not forced by a fix or the partner
  wait, and some of them describe real gaps (for example
  [0197](0197-no-credential-enters-a-run-pod-and-a-credential-gateway-outside-it-attaches-each-one-a-run-uses.md)).
  Security fixes stay allowed, so a gap that is a vulnerability is not frozen.
* Bad, because narrowing the profile to one pair means the other providers in
  the source keep only the evidence they have today.
* Bad, because the end condition depends on someone outside the project. If no
  partner comes, the freeze has no natural end; the review on 2027-05-27 is the
  backstop.

### Confirmation

* A reviewer checks every pull request that adds a file under `docs/adrs/` or
  `docs/research/` against this record: the record or document must name the
  fix, the design partner's request, or the governance-layer exploration that
  forced it.
* The maintainer pushes no `v0.2.0` tag (the reviewed act in
  [release versioning](../ops/release-versioning.md#stable-releases)) while
  #400 or #487 is open; `gh issue view 400` and `gh issue view 487` read closed
  before the tag.
* `go test ./internal/ledger/` validates this record and its Records row.

## Pros and Cons of the Options

### Keep adding scope, and release when the candidates settle

* Good, because nothing in flight stops.
* Bad, because 148 candidates show the candidates do not settle on their own.

### Freeze nothing, and only add release blockers

* Good, because it fixes the two red drills.
* Bad, because it leaves the ledger, the research and the provider surface
  growing while nobody outside uses any of it.

## Re-evaluation triggers

* An outside organisation has run real tickets on Ploeg for 30 days: the freeze
  ends.
* 2027-05-27 arrives and no outside design partner runs real tickets on Ploeg:
  0032 and this record are reviewed together.
* A security finding needs a design change larger than a fix.
* A second maintainer joins.

## More Information

* [0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md): the
  positioning this freeze serves.
* [0159](0159-governance-scales-its-votes-with-the-maintainer-count-adopts-the-cncf-code-of-conduct-and-limits-any-one-organisation.md):
  the second maintainer is sought through the design partner.
* [Support matrix](../reference/support-matrix.md): what each tracker and forge
  is qualified on.
* 2026-11-27: owner decision: scope freeze until one outside design partner
  runs real tickets on Ploeg; v0.2.0 stable with #400 and #487 as release
  blockers; the tracker and forge pair left open with the recommendation above.
