# OKF knowledge pack proof of concept

Status: research record and proof of concept, 2026-10-04, on branch `poc/okf-knowledge-pack`. Nothing here is decided; the behavior it adds is proposed until an ADR accepts it.

**Question.** Can Ploeg brief a Run with what it already knows about a repository, and take back what a Run learned, using the [Open Knowledge Format](https://github.com/GoogleCloudPlatform/knowledge-catalog/tree/main/okf) (OKF v0.1, Google Cloud, 2026-06-12) as the shape and Omnigraph as the store, without giving any harness a new tool?

**Method.** Built the read path into the worker, the learning path into the outcome report, and a converter between OKF directories and the Omnigraph `memory` graph. Wrote a 10-concept bundle about Ploeg itself in [knowledge/](../../knowledge/index.md), ran packs for sample Work Items, and round-tripped a 4-concept subset through the live homelab Omnigraph (v0.11) on branch `poc/okf-knowledge-pack` of `memory`.

**Limitations.** Selection is keyword overlap with a crude plural stem (`postgres` becomes `postgre`), not meaning. No measurement yet of whether a pack changes Run cost or success. ploegd neither stores nor shows the TaskSpec's `knowledge` provenance or the report's `learnings`. The worker reads bundles from disk; nothing yet exports Omnigraph to the directory it reads.

## What was built

| Piece | Where | What it does |
| --- | --- | --- |
| OKF reader and writer | `pkg/okf` | Parses frontmatter (`type` required), resolves absolute and relative links into a graph, reports non-conforming files and dangling links, writes a bundle with an `index.md` per directory. Round trip keeps unknown frontmatter fields. |
| Pack selection | `pkg/knowledge/pack.go` | Scores each concept against the Work Item's title, description and labels (title 4, description 2, tags and type 2, body 1, label-to-tag 3), adds concepts the matches link to at a third of their score, prefers a `Pitfall` on a tie, and keeps the best within a byte budget (default 24,000). A concept with fewer than two matched terms and a score under 4 is never included, so unrelated work gets an empty pack. Each source carries a content digest. |
| Omnigraph converter | `pkg/knowledge/omnigraph.go` | OKF to NDJSON for the existing `memory` schema with no schema change: concept to `Note` (body is the whole OKF document), bundle, type and tags to `Topic` via `About`, `resource` to `Source` via `Cites`, links to `Relates`, producer to `Agent` via `Wrote`. `ExportQuery` and `FromOmnigraph` rebuild the directory. |
| Worker | `pkg/worker/knowledge.go` | Reads the repository's own `knowledge/` bundle and each `PLOEG_KNOWLEDGE_DIRS` bundle, writes the pack to the Run's scratch directory (outside the clone, so a writer cannot commit it), sets `PLOEG_KNOWLEDGE_DIR`, and puts the pack's index in the prompt ahead of the delivery contract, framed as evidence. |
| Contract | `pkg/harness/contract.go`, `docs/contracts/` | TaskSpec `knowledge`: the index, concept paths, source digests, bytes and omitted count. OutcomeReport `learnings`: at most 10 OKF-shaped proposals. |
| Learnings | `pkg/worker/knowledge.go` | With `PLOEG_KNOWLEDGE_OUTBOX` set, the prompt invites learnings and the worker writes them as an OKF bundle per Run under `learned/`, marked `status: proposed`, `proposed-by: <trace>` and `work-item: <ref>`. |
| CLI | `cmd/ploeg-okf` | `validate`, `pack`, `to-omnigraph`, `from-omnigraph`, `export-query`. |

Settings: `PLOEG_REPO_KNOWLEDGE_DIR` (default `knowledge`, `-` for none), `PLOEG_KNOWLEDGE_DIRS`, `PLOEG_KNOWLEDGE_BUDGET_BYTES`, `PLOEG_KNOWLEDGE_OUTBOX`. With none set and no `knowledge/` directory in the repository, a Run is unchanged.

## Results

**Packs for sample Work Items** against the Ploeg bundle:

| Work Item | Pack |
| --- | --- |
| "Add a reviewer timeout field to the OutcomeReport", stored in Postgres | 5 of 10 concepts, 4,348 bytes: migrations are append-only, contracts change together, the blackboard ADR, the harness contract, and the placeholders ADR through a link |
| "Helm chart: verify fails in sandbox when pulling dependencies" | 1 concept, 758 bytes: the registry-pull pitfall |
| "Update the README badges" | empty; the prompt gets no knowledge section |

**Live Omnigraph round trip** on `memory`, branch `poc/okf-knowledge-pack`:

- One `load` (merge) of 72 rows: 1 Agent, 4 Notes, 4 Sources, 22 Topics, 29 About, 4 Cites, 4 Relates, 4 Wrote; commit `01M434TQDJ00R9TFMH8M63C8QF`. The link to a concept outside the subset was dropped by the converter, not refused by the server.
- `ExportQuery` returned the 4 Notes. `FromOmnigraph` rebuilt the directory with the same content digest as the source, `sha256:183fea0aec9bd708`. The files differ only in YAML quoting style.
- A two-hop `relates{1,2}` query from the `okf-tag/taskspec` topic returned the linked concepts, the graph read a flat directory cannot answer without parsing every file.

**Gates.** `go vet ./...` clean; `go test ./...` passes (36 packages). Changing `resolveOutcome` to drop learnings fails `TestResolveOutcome_KeepsTheAgentsLearnings`.

## Findings

1. OKF costs nothing to adopt: markdown, YAML frontmatter and links, with `type` as the only rule. The value is the convention that producers and consumers agree on, not the spec text.
2. A file-based pack fits ADR-0011's driver that agent-side cost is the real cost. No adapter changed; every harness reads files.
3. The `memory` schema holds OKF without a change because the Note body keeps the whole document. A typed `Concept` node would make `type` and `timestamp` queryable without parsing, at the price of a schema change that open `glide/*` branches block.
4. Provenance is cheap: a content digest per source in the TaskSpec says exactly which knowledge a Run had. With an Omnigraph source, the read's `graphCommitId` would serve the same role.
5. Keyword selection is precise on a small bundle but will miss synonyms. Omnigraph's `recall_notes` (meaning and keywords) is the natural replacement once the worker reads the graph rather than a directory.

## Open questions for an ADR

- Where knowledge is durable. R6 names Postgres and git/forge. Repository knowledge in the repository keeps that; cross-repository learnings in Omnigraph would add a third store.
- Who reads the graph: the worker (credential stays in the worker, ADR-0034) or an export job that refreshes a mounted directory.
- One graph per tenant. The homelab `act-glide` actor can read the owner's personal `brain` graph; Runs for clients must not.
- How learnings reach a person: ploegd storing them and Vloer showing them under Needs you, or an Omnigraph branch per Run reviewed in graph-review.
- Whether to measure first: the same Work Items with and without a pack, comparing cost, rounds and verdicts.
