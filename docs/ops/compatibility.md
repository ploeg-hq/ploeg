---
type: reference
audience: [operator, integrator, contributor]
owner: ploeg
last_verified: 2026-10-24
verified_by: "go test ./internal/compat/ against v0.2.0-rc.47; read pkg/store/store.go (Migrate), pkg/config/config.go (KnownFields), ops/helm/ploeg/values.schema.json, docs/contracts/README.md"
---

# Compatibility policy

What Ploeg keeps working from one release to the next, for whom, and for how long. The decision is [ADR 0134](../adrs/0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md). Release mechanics are in [releases](release-versioning.md).

## Versions

Ploeg is `0.x`. Semantic Versioning lets anything change in `0.y.z` ([SemVer 2.0.0 item 4](https://semver.org/spec/v2.0.0.html#spec-item-4)). Ploeg promises more:

| Release | May contain |
| --- | --- |
| Candidate `v0.Y.Z-rc.N` | Additions and fixes. Nothing that breaks the release before it, unless recorded as below. |
| Patch `v0.Y.Z` | Additions and fixes. No break. |
| Minor `v0.Y.0` | Removals of what a previous stable minor deprecated, and recorded breaks. Every break is a `feat!` commit with a `BREAKING CHANGE:` footer, an ADR and a release-notes entry. |

A breaking change raises the minor version, never the major ([ADR 0115](../adrs/0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md)). "The release before it" is the newest `v*` tag reachable from the commit, which is what `go test ./internal/compat/` compares against.

1.0 is a separate decision. When it comes, the deprecation windows below become the 1.x windows in the last column, and a removal from a frozen contract needs a new contract version (`v2` schema file) instead of an exception.

## What is covered

| Surface | Promise | Checked by |
| --- | --- | --- |
| HTTP APIs: run API, worker control, operator API, deploy API, tracker execution, operator delivery (`docs/contracts/*.json`) | v1 is frozen: only optional additions. A consumer ignores unknown fields and treats an unknown enum value as unknown, never as an error. | `go test ./internal/compat/` (schemas against the last release), `go test ./pkg/harness/` (Go types against schemas), `go test ./pkg/httpapi/` (every recorded consumer expectation) |
| Harness contract: Task Spec, Outcome Report, checkpoint | The same v1 rule. | the same |
| MCP tools of `ploeg-mcp` | A tool name is never removed or renamed without deprecation. Tool inputs follow the HTTP rule. | `go test ./internal/compat/` (tool names); inputs by review |
| Database schema (`pkg/store/migrations/`) | Forward-only and append-only. A released migration is never edited, removed or preceded by a new one. Each migration leaves the previous release working on the new schema. | `go test ./internal/compat/` |
| Helm values (`values.yaml`, `values.schema.json`) | A key is removed only after a deprecated stable minor. Its schema follows the HTTP rule. | `go test ./internal/compat/` (schema) |
| Environment variables of `ploegd` and `ploeg-worker` | Removed only after a deprecated stable minor. | `go test ./internal/compat/` (the generated [configuration reference](../reference/configuration.md)) |
| Configuration file keys (`pkg/config`) | Removed only after a deprecated stable minor. The loader refuses unknown keys, so a removal fails boot. | `go test ./internal/compat/` (YAML keys) |
| Contract bundle (`ghcr.io/ploeg-hq/ploeg-contract`) | Layout `schemaVersion` 1 and artifact type `application/vnd.ploeg.contract.v1+json` stay until a bundle v2. Each release's bundle is reproducible byte for byte. | `scripts/test-contract-bundle.sh`, the `contract-bundle-reproducible` job |
| Codes defined only in Go (Shift close reasons, `needs_human` reasons, audit actions) | Treated as enum values: added freely, removed only after deprecation. | review |

Not covered, as in [Prometheus's stability promise](https://prometheus.io/docs/prometheus/latest/stability/): the Go API of packages under `pkg/` and `internal/`, log lines, metric names and labels, the HTML of any page, and tables a dashboard reads through `ploeg_observer`. They change when they must, with a release-notes entry.

## Deprecation

A deprecation is announced in a stable minor and removed no earlier than the next minor. In `0.x` that is at least one stable minor of overlap.

| Surface | 0.x: deprecated element keeps working for | 1.x: for | Modelled on |
| --- | --- | --- | --- |
| HTTP, MCP and harness contracts | one stable minor | removal only in a new contract version | Kubernetes Rule #1 and #4a (GA) |
| Helm values, environment variables, config keys | one stable minor, then one more minor of a refusing error | 6 months or one minor, whichever is longer | Kubernetes Rule #5b (admin-facing CLI) |
| Behaviour (a default, a state name) | one stable minor | 12 months | Kubernetes Rule #7 |

[Kubernetes' deprecation policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/) sets these windows for its own APIs, flags and behaviour.

A deprecation:

1. marks the element: `"deprecated": true` and a description naming the replacement in the schema, a `DEPRECATED` line in `values.yaml`, or a startup warning for an environment variable or key;
2. keeps the old form working and equal to the new one, as [ADR 0093](../adrs/0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md) did with `leased` and `active`;
3. is listed in the release notes of the minor that announces it.

The removal is a `feat!` with a `BREAKING CHANGE:` footer and an ADR, and adds an entry to [breaking-changes.yaml](../contracts/breaking-changes.yaml) under the release it breaks against. After a value, variable or key is removed, the chart render or `ploegd` start fails with a message naming the replacement for one more minor, so the error is actionable rather than a silently ignored setting.

## Database

`ploegd` applies the embedded migrations on start, in file-name order, under an advisory lock, each in its own transaction ([`Store.Migrate`](../../pkg/store/store.go)). There are no down migrations.

- **Rolling update.** With the default `RollingUpdate` strategy the new `ploegd` migrates while the old one still serves, and Runs started by the old image keep running. So each migration *expands* only: it adds, and keeps what the previous release reads and writes working. Removing or renaming happens in a later minor's migration, once no supported release uses the old form ([parallel change](https://martinfowler.com/bliki/ParallelChange.html)). Migration 0052's trigger, which rewrites `leased` written by the outgoing release, is the pattern.
- **Rollback.** Running the previous release on the current schema is supported, because every migration expanded. Running an older release than that is not: restore the database from the backup taken before the upgrade ([restore after a database outage](../how-to/restore-after-a-database-outage.md)). Take that backup before every minor upgrade.
- **Skipped versions.** Upgrading across several releases at once is supported: every migration in between runs, in order. Because the old release may not survive a contract step two minors on, [drain the workers](../how-to/drain-workers.md) and scale `ploegd` to zero before an upgrade that skips a minor, then start the new release.

## Version pairs

| Pair | Supported |
| --- | --- |
| `ploegd` N with `ploeg-worker` N or the previous minor | yes |
| `ploeg-worker` newer than `ploegd` | no; upgrade `ploegd` first, as Kubernetes requires of the kubelet ([version skew policy](https://kubernetes.io/releases/version-skew-policy/)) |
| `ploeg-mcp` and `claude-runner` with `ploegd` | same release; they ship in the same release and the chart pins all images by digest |
| Chart N with images N | same release |
| An operator consumer built against the contract bundle of N or the previous minor, with `ploegd` N | yes; checked for every consumer that records its expectations ([ADR 0146](../adrs/0146-ploeg-serves-the-major-of-each-contract-and-keeps-its-consumers-two-newest-lines-working.md)) |
| Database migrated by N with `ploegd` from the previous release | yes (rollback) |
| Database migrated by N with anything older | no; restore |

Upgrade order: database backup, then `ploegd` (which migrates), then workers (they follow the chart's image), then consumers. A consumer pins Ploeg by version and contract-bundle digest and states the Ploeg version it supports in its own repository.

A consumer asks `GET /api/v1/operator/version` which contract majors this Ploeg serves and refuses to run when one it requires is missing. Ploeg keeps each consumer's two newest release lines working: a consumer records what each line relies on under [docs/contracts/consumers/](../contracts/README.md#contract-versions-and-consumer-expectations), and every pull request runs those expectations. A change that a recorded consumer reads needs a new contract major, served beside the old one for one stable minor.

Today an older worker against a newer `ploegd` gets the HTTP status of whatever route it misses (404, 405 or 400). Ploeg does not yet name the supported range in that error; a version handshake on `POST /api/v1/claim` is the follow-up ADR 0134 records.

## Recording a break

`go test ./internal/compat/` fails with a message for each break. When a break is decided, add it:

```yaml
exceptions:
  - base: v0.2.0-rc.47
    surface: docs/contracts/operator-api.v1.schema.json
    pointer: /$defs/shift/properties/reservedUsd
    adr: "0093"
```

`base` is the release the break is against, `surface` the file or `config`, `environment`, `mcp` or `pkg/store/migrations`, `pointer` the JSON pointer or name the failure printed (a parent pointer covers everything under it), and `adr` the record that decided it. An entry applies only while its `base` is the newest release, so it never excuses a later break.

In CI the `verify` job fetches tags and sets `PLOEG_COMPAT_REQUIRE_BASE=1`, so a checkout without tags fails instead of skipping. Locally the test skips when no tag is reachable. Set `PLOEG_COMPAT_BASE=v0.x.y` to compare against another release.
