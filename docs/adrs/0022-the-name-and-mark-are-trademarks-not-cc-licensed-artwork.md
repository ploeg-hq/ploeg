---
status: accepted
date: 2026-08-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# The name and mark are trademarks under a usage policy, not CC-licensed artwork

## Context and Problem Statement

Ploeg now has a visual identity in `docs/brand/` — a mark, a wordmark, lockups
and colour tokens. [0003](0003-apache-2-0-license.md) put the project under
Apache-2.0, which answers copyright for source. The mark raises a second
question that the code licence does not: may a fork ship under the Ploeg name
and mark, and may a vendor put them on a page that implies endorsement?

A first draft of `docs/brand/README.md` proposed CC BY-SA 4.0 for the artwork
files. That is the wrong instrument, and this record settles what replaces it
before the identity is published anywhere it can be copied.

## Decision Drivers

* **A CC licence is irrevocable.** Ploeg is pre-alpha and may yet move to a
  foundation or a different steward. A policy file can be tightened, loosened
  or reassigned; a CC grant, once made, cannot be withdrawn from anyone who
  already received it.
* **Creative Commons advises against it, on the grounds that it can cost the
  right entirely.** A mark identifies origin; a copyright permission to reuse
  and modify it undercuts precisely that function, and CC warns applying their
  licences to a trademark "could even result in a loss of your trademark rights
  altogether".
* **Share-alike is actively hostile on a mark.** It attaches a viral obligation
  to material that merely reproduces the logo, discouraging exactly the
  reproduction — README badges, integration lists, conference slides — that a
  young project wants.
* **The risk is misattribution, not copying.** Nobody is harmed by a blog post
  reproducing the mark. The harm is a fork or a vendor implying that what they
  ship is Ploeg, or that Ploeg endorses it. Copyright is the wrong tool for
  that; trademark is the right one.
* **Apache-2.0 already withholds trademark rights.** Section 6 grants no
  licence to the licensor's trade names or marks. The repo licence and a
  trademark reservation therefore compose without conflict, and no second
  copyright licence is needed for the files.

## Considered Options

* Name and mark reserved as trademarks under a usage policy; artwork files stay
  under the repo's Apache-2.0
* CC BY 4.0 on the artwork files
* CC BY-SA 4.0 on the artwork files
* CC BY-ND 4.0 on the artwork files
* All rights reserved on the artwork files

## Decision Outcome

Chosen option: **name and mark reserved as trademarks under a usage policy,
artwork files under the repo's existing Apache-2.0**, because it protects the
one thing worth protecting — that "Ploeg" and the mark mean *this* project —
without granting an irrevocable copyright permission that works against it.

Concretely:

* `docs/brand/TRADEMARK.md` is the policy. It grants nominative use outright:
  anyone may reproduce the unmodified mark to refer to Ploeg, link to it,
  report on it, or state that their software works with it, without asking.
* It withholds the two uses that cause the harm: shipping a modified or forked
  distribution under the name or mark, and any use implying endorsement,
  affiliation or certification.
* No `LICENSE` file is added under `docs/brand/`. The files are covered by the
  repository's Apache-2.0 like everything else; §6 of that licence is what
  keeps the mark out of the grant.
* The bundled font is a separate matter and stays separate: Archivo is
  SIL OFL 1.1 and is not Ploeg's to license.

### Consequences

* Good, because the uses a young project actually wants — badges, write-ups,
  ecosystem pages — need no permission and no lawyer.
* Good, because it is reversible. If Ploeg moves to a foundation, the policy
  moves with it; an irrevocable CC grant could not have been taken back.
* Good, because it removes a licence file rather than adding one: the repo keeps
  a single copyright answer.
* Bad, because an unregistered mark rests on common-law rights, which are
  weaker and territorial. Accepted — registration costs money and buys nothing
  until there is adoption worth defending. Revisit if that changes.
* Bad, because enforcement is manual; nothing detects a misuse automatically.
  Accepted, and inherent to trademark rather than to this choice.

### Confirmation

`./scripts/brand-marks.sh`, run as a step in
`.forgejo/workflows/on_pull_request.yml`. It fails the build if the policy file
is missing, if a `LICENSE`/`COPYING` file appears under `docs/brand/` (which
would re-introduce the second copyright answer this record removes), or if the
root `README.md` stops linking the policy.

## Pros and Cons of the Options

### CC BY 4.0 on the artwork files

* Good, because it is the most common answer in the ecosystem — the CNCF
  artwork repository uses it — so it surprises nobody.
* Bad, because it is irrevocable, and it grants the right to *modify* the mark.
  A permissive right to produce derivatives of an origin-identifying sign is
  the exact thing CC warns can dilute the mark out of existence.

### CC BY-SA 4.0 on the artwork files

* Good, because share-alike keeps derivative marks open.
* Bad, because share-alike is the wrong reflex here: it burdens reproduction,
  which is the behaviour to encourage, and it still grants the modification
  right that BY-4.0 grants. Strictly worse than BY for this purpose.

### CC BY-ND 4.0 on the artwork files

* Good, because "no derivatives" is closer to what a mark actually needs than
  BY or BY-SA.
* Bad, because ND also blocks legitimate technical adaptation — recolouring for
  a monochrome print, producing a platform-specific icon size — and it is still
  irrevocable. A policy can permit those case by case; ND cannot.

### All rights reserved on the artwork files

* Good, because it is maximally protective.
* Bad, because it makes ordinary reference legally uncertain and reads as
  hostile from a project asking for adoption. It also conflicts with the
  repository-wide Apache-2.0 grant unless carved out explicitly, adding the
  second copyright answer this record is trying to avoid.

## More Information

* Policy: [`docs/brand/TRADEMARK.md`](../brand/TRADEMARK.md)
* Identity and construction: [`docs/brand/README.md`](../brand/README.md)
* Creative Commons, *Frequently Asked Questions* — "Can I apply a CC license to
  my trademark?": <https://creativecommons.org/faq/>
* Apache License 2.0 §6 (Trademarks): [`LICENSE`](../../LICENSE)
* Refines [0003](0003-apache-2-0-license.md), which settled copyright for the
  source but left the mark unaddressed.
* 2026-08-27 — record created alongside the v1 identity in `docs/brand/`
