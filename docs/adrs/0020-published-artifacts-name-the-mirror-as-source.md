---
status: accepted
date: 2026-08-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Published artifacts name the GitHub mirror as their source, and Forgejo as their URL

## Context and Problem Statement

[0004](0004-forgejo-leading-home-github-mirror-module-path.md) settled that
Forgejo is the single writable origin and GitHub is a read-only mirror that
exists to be the publicly resolvable host in the module path. Every release now
publishes two OCI artifacts — the `ploegd` image and the `ploeg` Helm chart — to
Harbor, the Forgejo registry and GHCR, and those artifacts carry the same
question in a second place: what does `org.opencontainers.image.source` say?

It said `https://forgejo.webgrip.dev/webgrip/ploeg` for the image and nothing at
all for the chart, and both answers turned out to be wrong for the audience that
reads them. GHCR links a package to a repository by matching that annotation
against a `github.com` URL, so both packages published unlinked — the image
naming a host GitHub cannot resolve, the chart naming nothing, because Helm
derives the annotation from `Chart.yaml` `sources[0]` and there was no `sources`
field.

The constraint that removes most of the option space: the image is built **once**
and digest-copied to the other two registries, so all three serve byte-identical
manifests. One label value therefore serves all three audiences; per-registry
values would mean per-registry builds, giving up the identical-digest property
the mirroring design rests on.

## Decision Drivers

* **The annotation is a testable claim, not a statement of governance.** OCI
  defines `image.source` as *"URL to get source code for building the image"*.
  Paired with `image.revision` it is checkable: clone, check out the sha, get the
  source. Which forge is authoritative is a different assertion.
* **One label, three registries.** Digest-identical mirroring is deliberate, so
  the value cannot vary by destination.
* **The external reader has no fallback.** `forgejo.webgrip.dev` does not resolve
  off the VPN, so for every reader outside the LAN a Forgejo value is a dead
  hostname, not merely a non-canonical one.
* **Linking must survive package deletion.** GHCR's manual "Connect repository"
  is a click recorded nowhere and lost if a package is recreated.

## Considered Options

* `image.source` = the GitHub mirror, `image.url` = the Forgejo home
* `image.source` = the Forgejo home, link GHCR packages by hand
* `image.source` = the GitHub mirror plus a bespoke `canonical-source` label
* Stop publishing to GHCR

## Decision Outcome

Chosen option: **`image.source` names the GitHub mirror and `image.url` names the
Forgejo home**, because it is the only option whose claim is true for every
holder of the artifact, and it keeps the linkage declarative.

Concretely, in `ops/docker/ploegd/Dockerfile`:

```
org.opencontainers.image.source="https://github.com/webgrip/ploeg"
org.opencontainers.image.url="https://forgejo.webgrip.dev/webgrip/ploeg"
```

and in `ops/helm/ploeg/Chart.yaml`, from which Helm derives the same two
annotations:

```yaml
home: https://forgejo.webgrip.dev/webgrip/ploeg
sources:
  - https://github.com/webgrip/ploeg
```

This is [0004](0004-forgejo-leading-home-github-mirror-module-path.md)'s
asymmetry applied to a second artifact class. That record accepted that the
module path names the mirror because Go resolves modules over a public host;
published artifacts name the mirror because OCI consumers resolve source over a
public host. Same reason, same trade-off, already argued and accepted once.

Forgejo is not demoted. It moves to `image.url` — *"URL to find more information
on the image"* — which is where a canonical home belongs, and the governance
claim itself lives in 0004 and here, not in a label.

### Consequences

* Good, because both GHCR packages link to `webgrip/ploeg` automatically on the
  next release. Linking is package-level, so one correctly annotated push links
  the package and the already-orphaned rc.25/rc.26 versions sit under it.
* Good, because the linkage is two lines in git — reviewable, and it survives a
  package being deleted and recreated.
* Good, because the annotation is now verifiable rather than aspirational: the
  commit an image was built from resolves at the URL it names.
* Bad, because a reader inside the LAN sees the mirror rather than the writable
  origin. Accepted: `image.url` carries the home, and the alternative hands every
  external reader a hostname that does not resolve at all.
* Neutral, because nothing consumes the annotation today — the estate was
  searched before the change, and the only match was a comment in
  `webgrip/workflows` `docker-mirror.yml`. The cost of being wrong either way is
  currently documentation, which is exactly why it should be documentation that
  is correct for its reader.

### Confirmation

The release workflow's `Verify image metadata labels` step
([.forgejo/workflows/on_release_published.yml](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/.forgejo/workflows/on_release_published.yml))
asserts `org.opencontainers.image.source` against `EXPECTED_SOURCE` for every
platform of every published image, alongside created/version/revision, and fails
the Harbor job if it drifts. A regression to the Forgejo value fails the release
rather than silently orphaning the packages again.

The chart has no equivalent gate; check it after a release with:

```
helm show chart oci://ghcr.io/webgrip/charts/ploeg --version <v>
```

## Pros and Cons of the Options

### `image.source` = the Forgejo home, link GHCR packages by hand

* Good, because the annotation names the writable origin, which is what a reader
  inside the LAN expects.
* Bad, because it is a dead hostname for every reader outside the LAN — the
  entire audience GHCR publication exists to serve.
* Bad, because linking becomes a UI click per package, repeated whenever a
  package is recreated, recorded nowhere in the repo.

### `image.source` = the mirror plus a bespoke `canonical-source` label

* Good, because both facts are stated explicitly on the artifact.
* Bad, because a non-standard label is read by nothing and maintained by hand;
  the same fact is already carried by `image.url` and by this record.

### Stop publishing to GHCR

* Good, because the question disappears, and Ploeg is pre-alpha — the README says
  "watch, don't install".
* Bad, because it reverses a decision taken deliberately days earlier (the
  GitHub track was reopened on 2026-08-24), and public artifacts are the point of
  an Apache-2.0 project per [0003](0003-apache-2-0-license.md).

## More Information

* 2026-08-25 — decided and implemented while fixing both packages publishing
  unlinked to GHCR; the Helm annotation mapping was verified by pushing the
  packaged chart to a throwaway local registry and reading the manifest back,
  rather than assumed.
* 2026-08-26 — **the decision stands; its mechanism was wrong for the image.**
  The chart linked, the image did not. GHCR resolves a package's repository from
  the **manifest**, and a Dockerfile `LABEL` lands in each platform's image
  *config* — which `docker inspect` reads and a registry does not. The chart
  linked because Helm writes a real OCI annotation; the image stayed orphaned
  through rc.24–rc.27 with the label set correctly the whole time. The Confirmation
  gate below shared the mistake: it asserted the label, so it was green on every
  one of those releases. The build now also sets `index:` annotations via the
  `annotations` input added to `docker-build-push-registry-fast` (webgrip/workflows
  v1.11.0), and the gate additionally asserts
  `.annotations["org.opencontainers.image.source"]` on the raw index — verified to
  fail against an un-annotated index, so it would have caught the original bug.
  The `LABEL` is kept: it is what `docker inspect` surfaces, and the two serve
  different readers.
* 2026-08-26 — measured, rather than assumed, that annotating at BUILD time is
  sufficient: a plain `buildx imagetools create` preserves index annotations and
  leaves the index digest byte-identical, so Harbor, Forgejo and GHCR all inherit
  them and this record's identical-digest property is untouched. Annotating at
  copy time would have diverged GHCR's index digest from Harbor's.
* 2026-09-22 — **the decision stands; the repositories moved.** Ploeg is now
  built from the Unfold monorepo, and the `ploegd` image already names
  `github.com/webgrip/glide` as its source and the Forgejo Unfold repository as
  its URL. The chart's `sources` and `home` now name the same two repositories,
  so image and chart link to the repository their bytes are built from.
  `github.com/webgrip/ploeg` remains the Go module export, not the chart's
  source.
* Refines [0004](0004-forgejo-leading-home-github-mirror-module-path.md), which
  established the Forgejo-leading/GitHub-mirror split and the precedent that
  publicly resolvable metadata names the mirror.
* Related: [0003](0003-apache-2-0-license.md) — the adoption bet that makes
  publicly consumable artifacts worth having at all.
