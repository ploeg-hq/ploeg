## Context

The audit compares the two checkouts before a monorepo move. Ploeg's current explanations contain old implementation gaps, while De Vloer's product language assumes an execution split still under discussion.

## Goals / Non-Goals

Make current behavior discoverable and traceable to implementation evidence. Preserve the full evidence dossiers and accepted decisions under [ADR 0001](../../../docs/adrs/0001-adrs-are-the-decision-ledger.md). Do not change runtime behavior, adopt a shared engine or name the future repository.

## Decisions

Use Markdown for both human and agent readers, YAML for the domain source and existing JSON schemas for wire contracts. Regenerate domain views from YAML. A small `llms.txt` links to the current entry point; it carries no separate specification.

Replace obsolete current prose in place so inbound links survive. Preserve its baseline revision in the audit. Keep dated research intact, with scope notices where current guidance could be confused with an old observation. Replace De Vloer's generated design compilation with a generated chapter guide; its source chapters retain the detail.

## Risks / Trade-offs

- Historical evidence can still be mistaken for current behavior. Entry points and dated notices must identify its scope.
- Tests establish exercised behavior, not production qualification. Keep live provider and cluster checks explicit.
- An inventory is not proof that every claim was revalidated. Record mechanical classification separately from semantic and source review.

## Migration Plan

No deployment. Review the documentation diffs in each existing checkout. Git retains replaced prose; no repository moves or history rewrites are needed.

## Open Questions

Must a local runner work without any Ploeg service? Should a common runner be extracted, and which duplicated behavior justifies it? These remain product questions.
