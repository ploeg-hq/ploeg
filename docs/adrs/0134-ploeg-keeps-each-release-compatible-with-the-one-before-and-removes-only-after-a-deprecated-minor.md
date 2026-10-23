---
status: proposed
date: 2026-10-23
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Ploeg keeps each release compatible with the one before and removes only after a deprecated minor

## Context and Problem Statement

Ploeg ships a controller, workers, an MCP server, a chart, a database schema and a contract bundle, and other systems pin them at different versions ([#14](https://github.com/ploeg-hq/ploeg/issues/14)). Each seam has a partial rule: v1 schemas are frozen except for a deprecated route ([contracts](../contracts/README.md#versioning-policy), [ADR 0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)), migrations are append-only by instruction ([AGENTS.md](../../AGENTS.md)), and a breaking change raises the minor ([ADR 0115](0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md)). No rule covers Helm values, environment variables, configuration keys or MCP tool names; no rule says which version pairs work, how a rollback or a skipped upgrade behaves, or how long a deprecated element lives. None of it is checked. Comparing `v0.2.0-rc.20` with `v0.2.0-rc.47` finds seven configuration keys removed, so a file that still sets one fails to load; `executions` made required in two operator responses; and three response fields made nullable. ADR 0080 and ADR 0128 decided some of these, but no release marked them as breaks and nothing checked them. Migration `0067` merged after the released `0072`, so new and upgraded databases apply the two in different orders. What does Ploeg promise across releases during 0.x, and how is it enforced?

## Decision Drivers

* An adopter must be able to upgrade one release at a time, and roll back one, without reading the diff.
* Consumers pin Ploeg independently ([ADR 0127](0127-a-release-opens-each-consumers-update-by-running-the-consumers-own-recipe.md)) and must learn about a break before it reaches them.
* A rule nobody checks drifted already; the check has to run on every pull request and trunk push.
* 0.x must still allow removal. Semantic Versioning permits anything in 0.y.z; Ploeg wants less than that and less than 1.0 will demand.
* Established practice should carry the weight: Kubernetes' deprecation and version-skew policies, Semantic Versioning's deprecation FAQ, Prometheus's stability list.

## Considered Options

* One policy for every surface, with removal after a deprecated stable minor, enforced by comparing each change with the newest release tag
* Keep per-seam rules and rely on review
* Freeze everything until 1.0
* Follow Semantic Versioning's 0.x literally: anything may change in any minor

## Decision Outcome

Chosen option: "**One policy for every surface, with removal after a deprecated stable minor, enforced by comparing each change with the newest release tag**", because it gives adopters a rule they can plan an upgrade around, keeps 0.x able to remove things, and turns the existing rules into a gate. The full policy is [docs/ops/compatibility.md](../ops/compatibility.md).

1. **Each release is compatible with the release before it.** Candidates and patches add and fix. A break lands only in a minor and only as a `feat!` with a `BREAKING CHANGE:` footer, its own ADR and an entry in `docs/contracts/breaking-changes.yaml` under the release it breaks against.
2. **Covered surfaces:** the HTTP contracts and harness contract (`docs/contracts/*.json`), MCP tool names, the database migrations, Helm values and their schema, environment variables, configuration file keys, the contract bundle layout, and codes defined in Go. Not covered: Go package APIs, logs, metrics, HTML and tables read through `ploeg_observer`.
3. **Deprecation lasts at least one stable minor in 0.x.** An element deprecated in `0.Y` is removed no earlier than `0.(Y+1).0`. A removed value, variable or key fails the chart render or `ploegd` start with a message naming its replacement for one more minor. This tightens the contracts README's "at least one release", which with a candidate on every merge could be a day.
4. **Migrations expand, and contract a minor later.** The previous release must keep working on the new schema, so a rolling update and a one-release rollback need no restore. An older rollback restores the pre-upgrade backup. An upgrade that skips a minor stops the old release first.
5. **Version pairs:** `ploegd` N serves workers of N and the previous minor; a worker is never newer than `ploegd`; `ploeg-mcp`, `claude-runner`, the chart and its images move together; an operator consumer of N or the previous minor works with N.
6. **1.0 is a separate decision.** At 1.0 the contract rule becomes Kubernetes Rule #1 (removal only in a new contract version), operator-facing settings get 6 months or one minor, and behaviour 12 months.
7. **Follow-up, not built here:** a version handshake so an incompatible worker or client gets an error naming the supported range. `ploeg-worker` sends its version on `POST /api/v1/claim`; `ploegd` answers a worker outside the supported pair with 426 and a body naming both versions and the upgrade order. Until then an older worker gets the status of the route it misses.

### Consequences

* Good, because every pull request and trunk push now fails on an unrecorded break to a schema, migration, value schema, variable, key or tool name, with a message that names the element and the two ways forward.
* Good, because the rollback and skipped-upgrade rules make the existing migration practice (0052's trigger) a stated guarantee.
* Good, because a consumer can tell from the version alone which Ploeg it can run against.
* Bad, because the `verify` job now fetches the full history and tags. The repository is small; the cost is seconds.
* Bad, because the schema comparison is structural and conservative. Refactoring a definition into a `$ref`, or wrapping a field in `anyOf` with `null`, reports a break that a reader may consider harmless; it then needs an entry. Accepted: a false alarm costs one line of YAML, a missed break costs a consumer.
* Bad, because the configuration and MCP checks compare names, not paths or input schemas, so a key moved to another struct, or a tool whose input gains a required field, passes. Review covers those until the checks read the Go types.
* Bad, because one stable minor is short for an adopter who upgrades twice a year. Accepted for 0.x; 1.0 lengthens it.
* Neutral: the worker handshake that would satisfy "an incompatible worker gets an actionable error" end to end is decided here but unbuilt.

### Confirmation

* `go test ./internal/compat/` compares the tree with the newest `v*` tag reachable from `HEAD`: every `docs/contracts/*.json` and `ops/helm/ploeg/values.schema.json` (removed property or definition, new required property, changed `type`, `$ref`, `const` or `format`, removed enum value, newly refused unknown properties, removed or added alternatives), the migrations (edited, removed or back-dated), the environment variables in the generated configuration reference, the YAML keys in `pkg/config` and the tool names in `cmd/ploeg-mcp`. It fails on any break not recorded in `docs/contracts/breaking-changes.yaml` for that base, and on an entry whose ADR does not exist.
* The same package's rule tests break each rule one way and check it fires, and apply compatible changes (optional property, new definition, new enum value, new `oneOf` alternative, description and `deprecated` edits, reordered types) and check they pass.
* Gate: the `verify` job of `.github/workflows/ci.yml` checks out with `fetch-depth: 0` and tags and sets `PLOEG_COMPAT_REQUIRE_BASE=1`, so a missing tag fails instead of skipping; `mise run verify` runs `go test ./...`.

## Pros and Cons of the Options

### Keep per-seam rules and rely on review

* Good, because it costs nothing now.
* Bad, because it already failed: the history check above found unrecorded breaks between candidates of one minor.

### Freeze everything until 1.0

* Good, because consumers never break.
* Bad, because a project in initial development cannot remove a mistake, and the pressure moves to an early 1.0.

### Follow Semantic Versioning's 0.x literally

* Good, because it matches the letter of SemVer item 4.
* Bad, because a consumer that takes every candidate ([ADR 0116](0116-every-releasable-merge-to-development-cuts-a-candidate.md)) could break on any merge, and nothing would tell it.

## Re-evaluation triggers

* The owner opens the 1.0 decision.
* A consumer breaks on a release that `go test ./internal/compat/` passed.
* Five recorded exceptions in one minor, which means the rule or the checker is wrong.
* An adopter asks for a longer deprecation window or for support of more than one minor of skew.
* The worker version handshake ships.

## More Information

* Sources: [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html) items 4, 7 and 8 and the deprecation FAQ; [Kubernetes deprecation policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/) Rules #1, #4a, #5b and #7; [Kubernetes version skew policy](https://kubernetes.io/releases/version-skew-policy/); [Prometheus API stability](https://prometheus.io/docs/prometheus/latest/stability/); [ParallelChange](https://martinfowler.com/bliki/ParallelChange.html). All read on 2026-10-23.
* Builds on [ADR 0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md), [ADR 0093](0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md) and [ADR 0115](0115-candidates-are-calculated-from-conventional-commits-and-a-stable-release-stays-a-reviewed-tag.md).
* Code: `internal/compat/`, `docs/contracts/breaking-changes.yaml`.
* 2026-10-23: proposed. Policy and gate built; the worker handshake decided and unbuilt.
