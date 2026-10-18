---
status: accepted
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-18
---

# Every release publishes a contract bundle for consumers

## Context and Problem Statement

A consumer that checks its client against Ploeg's schemas, builds its glossary from Ploeg's domain model, lists Ploeg's decisions or republishes Ploeg's guides has had one way to read them: a Git submodule of this repository. [Homelab ADR-0069](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0069-one-delivery-contract.md) (layer 4, owner decision D2) makes a dependency between projects a released version Renovate bumps, carried by a registry, never a source submodule. What does a Ploeg release publish so a consumer can pin it by version and digest?

## Decision Drivers

* A consumer pins one immutable artifact per release and verifies it by digest.
* A registry carries it, like the image and the chart, so Renovate can follow it.
* It holds exactly what consumers read, built from the tag alone.
* It needs no credential beyond the workflow token the release already uses.

## Considered Options

* An OCI artifact on GHCR, built from the tag and published by every release
* The Go module `github.com/ploeg-hq/ploeg` alone
* A tarball attached to the GitHub release

## Decision Outcome

Chosen option: "An OCI artifact on GHCR, built from the tag and published by every release", because GHCR already carries Ploeg's image and chart, and an OCI digest is what a consumer and Renovate pin.

* Each release publishes `ghcr.io/ploeg-hq/ploeg-contract:<version>`, artifact type `application/vnd.ploeg.contract.v1+json`, with one `application/vnd.ploeg.contract.layer.v1.tar+gzip` layer: every tracked file under `docs/`, the non-test Go files under `pkg/` and `cmd/` (which define the codes no schema lists yet), `files.txt` with every tracked path, and `ploeg-contract.json` with the version and commit. The [contracts README](../contracts/README.md#the-contract-bundle) is the format's reference.
* [scripts/contract-bundle.sh](../../scripts/contract-bundle.sh) builds it from a checkout of the tag with `git archive` and `gzip -n`, and pushes it with ORAS, setting `created` to the commit time, so a tag always yields the same layer and manifest. It refuses a checkout that is not at the tag or has local changes, and a tag that already carries a different layer; a re-run that finds the same layer succeeds without pushing.
* [The contract bundle workflow](../../.github/workflows/contract-bundle.yml) publishes it. The release candidate workflow calls it after `publish` for the candidate it cut; a successful `Release` run for a pushed stable tag starts it; a maintainer dispatches it for an existing tag, which also backfills tags cut before this record. It checks out its tooling at its own ref and the tag beside it.
* Consumers pin the bundle and the Go module at the same release. The first is [Unfold ADR-0039](https://forgejo.webgrip.dev/webgrip/unfold/src/branch/development/docs/adr/adr-0039-unfold-depends-on-a-released-ploeg-through-its-contract-bundle-and-go-module.md).

### Consequences

* Good, because a consumer no longer builds or tests Ploeg's source to read its contract, so a Ploeg test cannot block a consumer's release.
* Good, because the bundle is reproducible, so a re-run proves it publishes the same bytes.
* Bad, because the non-test Go files ride along until the codes they define get a schema of their own.
* Bad, because a release whose bundle job fails leaves a version without a bundle until the job is dispatched again.

### Confirmation

`bash scripts/test-contract-bundle.sh`, run by `mise run verify`, fails when the bundle's contents change, when `files.txt` leaves out a tracked file, when two builds of one tag differ, or when the script accepts an invalid tag, a checkout that is not at the tag, or uncommitted changes. The first candidate after this record shows a `contract-bundle` job in its `Release candidate` run and `ghcr.io/ploeg-hq/ploeg-contract:<version>` with one layer.

## Pros and Cons of the Options

### The Go module alone

* Good, because it already exists and `go.sum` verifies it.
* Bad, because a consumer without Go, such as a documentation container, cannot read it, and the module proxy fetches from the forge rather than a registry.

### A tarball attached to the GitHub release

* Good, because it needs no new tool.
* Bad, because a consumer would read a forge, which homelab ADR-0069 rules out, and a release asset has no digest Renovate pins.

## Re-evaluation triggers

The codes in the Go files get their own schema, a second consumer needs content the bundle leaves out, GHCR stops serving anonymous pulls of the package, or the bundle passes 64 MiB.

## More Information

* [Homelab ADR-0069](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0069-one-delivery-contract.md), phase P3 of its RFC, 2026-10-17.
* Builds on [ADR 0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md), whose candidate workflow calls the bundle workflow.
* 2026-10-18 — Renovate in Unfold proposed bundle `0.2.0-rc.36` with Go module `v0.2.0-rc.35`, because `proxy.golang.org` did not list `v0.2.0-rc.36` yet. The bundle workflow now waits, for up to 10 minutes, until the proxy lists the tag and serves its `.info` and `.mod` and `sum.golang.org` records it, and fails before publishing otherwise, so the bundle and the consumer update that follows it never get ahead of the Go module. Confirmation: `bash scripts/test-go-module-published.sh`, run by `mise run verify`.
