---
status: accepted
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-04
---

# Context bundles are stored per Work Item and unpacked by the worker under fixed limits

## Context and Problem Statement

Unfold system ADR-0021 (proposed) lets a person attach files to a Work Item for agents: a zip, a tar.gz or a single file. Ploeg has no inbound file path, no object storage and no binary columns. Where does Ploeg keep the files, how do they reach the worker, and how is an archive kept from escaping its directory, filling a disk or becoming executable?

## Decision Drivers

* Durable state lives in Postgres and git (R6); a new store needs a reason.
* The claim response stays small (it is capped at 1 MiB) and carries references, not bytes.
* A Run verifies what it received; it never runs on context it could not verify.
* The same archive rules apply at upload, where a person can fix the file, and in the worker.
* A writing Run cannot commit context into the pull request.

## Considered Options

* Postgres rows per Work Item, references in the claim, download by Run capability, one safe-unpack package used at upload and in the worker
* Object storage from the start
* Bytes inline in the claim response
* Files committed to the work branch

## Decision Outcome

Chosen option: "Postgres rows per Work Item, references in the claim, download by Run capability, one safe-unpack package used at upload and in the worker", because it adds no service, keeps the claim small and makes every Run's context verifiable.

* **Storage.** A new append-only migration adds `work_item_context`: id, Work Item, digest, name, media type, size, file count, content (`bytea`), note, added by, added at, the Shift open at upload and the phase. One row per digest per Work Item.
* **Limits.** 20 MiB per upload (`PLOEG_CONTEXT_MAX_BYTES`), 50 MiB per Work Item (`PLOEG_CONTEXT_MAX_TOTAL_BYTES`). Archives: at most 2,000 files, 100 MiB uncompressed, 25 MiB per file, a 200:1 ratio per entry and 300-character paths. Refused: absolute paths, `..`, links, devices, non-UTF-8 and duplicate names. Skipped: `__MACOSX/`, `.DS_Store`, `Thumbs.db`. Nested archives stay files. Extracted files are 0644. Sizes are counted while streaming.
* **Errors** use the operator API's envelope, `{"schemaVersion":"1.0","error":{"code","message"}}`, with codes `invalid_name`, `invalid_note`, `unsafe_bundle` (the message names the rule), `too_large`, `total_too_large`, `not_found`, `work_item_finished` and `execution_forbidden`.
* **Routes.** Operator: `POST` and `GET /api/v1/operator/work-items/{id}/context`, `POST /api/v1/operator/executions/{id}/context`. Run: `GET /api/v1/runs/{token}/context/{id}`, only for the Run's own Work Item.
* **Claim.** `context`: references (id, name, media type, digest, size, files, note, added at, phase) for items added before the claim.
* **Worker.** Downloads, verifies digest and size (a mismatch makes the Run stuck), unpacks into `scratch/context/NN-<name>`, writes `index.md`, sets `PLOEG_CONTEXT_DIR`, records `TaskSpec.context`, and adds the prompt section "Context from people" after the Work Item description, framed as evidence below the delivery contract.

### Consequences

* Good, because no new service is needed and backups already cover the data.
* Good, because every Run's context is verified and recorded by digest.
* Bad, because binary content grows the database and its backups; 50 MiB per Work Item bounds it, and object storage is the next step when volume demands.
* Bad, because the worker downloads every item on every Run of the Work Item; caching by digest is a later optimisation.
* Bad, because ploegd's 30-second read timeout and the worker's 30-second HTTP client bound a 20 MiB transfer to links of about 5.5 Mbit/s or faster; context routes need their own timeouts before a pilot.
* Neutral, because a zip entry declares its own compressed size, so the ratio rule can be fooled for zip; the per-file and total limits still bound what is written.

### Confirmation

`go test ./...` in the existing CI step covers the safe-unpack rules (traversal, absolute paths, links in zip and tar, bombs, too many files, too large, duplicates, nested archives, single files, empty archives), the operator and Run routes including 400, 404, 409 and 413, the claim references, digest verification and the prompt section. `TestTaskSpec_MatchesSchema` and the run API schema cover the new fields.

## Pros and Cons of the Options

### Object storage from the start

* Good, because it scales without growing the database.
* Bad, because it adds a service, credentials and a retention policy before a pilot has shown the volume.

### Bytes inline in the claim response

* Good, because the worker makes no extra request.
* Bad, because the claim is capped at 1 MiB and would carry every file on every claim.

### Files committed to the work branch

* Good, because agents see them with no new mechanism.
* Bad, because they enter the pull request and the client's history, and cannot hold confidential material.

## Re-evaluation triggers

* A Tenant's context exceeds 5 GiB or the database backup grows by more than a quarter because of context.
* The storage spike recommends object storage.
* Tracker attachments become a source, which may need larger limits.
* Tenant isolation (Unfold ADR-0017) lands and the routes gain a Tenant check.

## More Information

* 2026-10-04 — accepted by the owner with the proof of concept. The owner kept 20 MiB per upload and 50 MiB per Work Item for the first pilot, let clients attach context to their own Work Items as well as agency members, and decided that a file the secret scan flags is held: stored, but given to no Run until a person removes it or confirms it is safe (spike VIK-1866 measures the scanner).
* Evidence: [context bundles proof of concept](../research/2026-10-04-context-bundles-poc.md).
* Unfold system ADR-0021 and the RFC "people give a Work Item context files" (Unfold `docs/research/2026-10-04-rfc-context-bundles-and-steering.md`).
* OpenSpec change `add-knowledge-and-context-bundles`, which lands with the knowledge pack (ADR-0065, proposed) in a later pull request.
