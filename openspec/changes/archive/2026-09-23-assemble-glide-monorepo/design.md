## Context

The owner authorized Glide after reviewing the [transition proposal](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/apps/docs/migration-proposal.md). Both application trunks are `development`; current deployed image and chart names are application-specific.

## Goals / Non-Goals

Preserve both histories and audited working trees, make cross-application checks reproducible, and separate shared product prose from service references. Do not change the worker API, lease predicates, scaler queries, database schema or deployed workloads.

## Decisions

Glide uses `apps/vloer` and `apps/ploeg`, with root system documentation. Import commits preserve the original commit objects and explicit file moves. Namespace imported tags by application to prevent collisions. Keep `development` as the integration trunk and preserve component version policies.

The shared runner comparison uses the existing Vloer runtime interface in standalone and delegated modes, plus Ploeg's unattended harness contract and executable recovery tests. Extract only behavior proven equivalent. The evidence may justify keeping both engines.

Durable repository ownership is recorded in Glide's root ADR; existing Ploeg ADRs and their status remain intact. In particular, [ADR 0002](../../../docs/adrs/0002-go-as-the-implementation-language.md) and [ADR 0008](../../../docs/adrs/0008-litellm-is-the-credential-and-metering-seam.md) retain their application scope.

## Risks / Trade-offs

- Changed working directories can break Docker, generated docs and integration discovery. Run gates from imported paths and validate build contexts.
- Shared release history can release the wrong application. Use the estate's monorepo tooling and application-specific tags and path selection.
- Source links in immutable evidence must retain provenance. Rewrite current guidance and build-time resolution; preserve historical commit URLs and legal text.
- A runner abstraction can couple different lifecycles. Compare concrete execution evidence before introducing it.

## Migration Plan

Snapshot inputs, prepare and validate prefixed import commits, merge them into Glide, update root tooling and documentation, run both application gates and the cross-service qualification, then verify every source HEAD remains an ancestor. Existing repositories and production desired state remain available throughout. The local import can be discarded without changing either deployed service.

## Open Questions

The comparison determines whether any runner extraction is justified now. Remote publication and production cutover must be distinguished from successful local qualification.
