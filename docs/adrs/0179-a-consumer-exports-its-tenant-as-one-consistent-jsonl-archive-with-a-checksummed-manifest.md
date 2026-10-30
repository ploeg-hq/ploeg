---
status: proposed
date: 2026-10-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# A consumer exports its Tenant as one consistent JSONL archive with a checksummed manifest

## Context and Problem Statement

[ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md) gives every Work Item, Shift and Run a Tenant and binds each Operator Consumer to one. A Tenant that leaves a deployment, an auditor and a person exercising a data-access request each need that Tenant's records as a whole. The operator reads page at most 200 records at a time, each page at a different moment, so a consumer that walks every list gets a copy no single moment ever held, with no proof that it is complete or unchanged. How does a consumer obtain all of its Tenant's records, consistently and verifiably, without Ploeg giving away a credential or another Tenant's data?

## Decision Drivers

* Every record of the Tenant (or of one Client) and nothing else, read at one moment.
* The copy proves itself: each file carries its checksum and row count, and its schema travels with it.
* No credential, token, key hash, lease token or gateway key leaves Ploeg; people's text leaves only to a consumer allowed to read content (ADR-0160 `readContent`).
* An export is a person's act and is audited like a change ([ADR-0171](0171-an-operator-change-needs-a-proven-person-and-the-chart-refuses-asserted-attribution-from-0-3-0.md)).
* PostgreSQL stays Ploeg's only state store ([ADR-0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md)), and Ploeg keeps no second copy of a Tenant's data.

## Considered Options

* **`POST /api/v1/operator/exports` streams one tar archive: a JSONL file and a JSON Schema per kind, and a manifest with sha256 last, all read in one snapshot**
* An export job whose files ploegd stores in PostgreSQL for later download
* An export job that writes to object storage
* No export route: the consumer pages the existing reads

## Decision Outcome

Chosen option: "**`POST /api/v1/operator/exports` streams one tar archive read in one snapshot**", because it gives a consistent, self-checking copy with no new store, no retention and no second copy of the Tenant inside Ploeg.

* **Who may export.** A consumer with the new `export: true` capability in `PLOEG_OPERATOR_CONSUMERS`; unset is `false`, so no existing consumer gains it. Without it the route answers 403 `export_forbidden`. The request needs exactly one `X-Ploeg-Actor` and, under `PLOEG_OPERATOR_ATTRIBUTION=proven`, a person token like every other `POST`.
* **What is exported.** The consumer's Tenant, narrowed by its `clients` and by `X-Ploeg-Acting-Client` exactly as ADR-0178 narrows reads, and by its Team scope. The body is `{"exportId": "<uuid>", "kinds": [...]}`. Kinds are `work_items` (proposed ones included), `shifts`, `runs`, `events`, `accounting` (Run accounts, settlements, holds), `notes`, `questions`, `route_refusals`, and, only with `readContent`, `transcripts`, `briefings` and `context`. An unknown or repeated kind answers 400 `invalid_export`; a content kind without `readContent` answers 403 `content_forbidden`.
* **One snapshot.** ploegd reads every kind in one `REPEATABLE READ READ ONLY` transaction, so the files agree with each other at one moment, which the manifest names as `snapshotAt`.
* **The archive.** `200` with `Content-Type: application/x-tar` streams a POSIX tar holding, per kind, `<kind>.jsonl` (one JSON object per line, ordered by id, in the shape the operator API answers for that kind, `tenant` and `client` included) and `<kind>.schema.json` (that shape's JSON Schema, generated from `docs/contracts/operator-api.v1.schema.json`). The last entry is `manifest.json`: `schemaVersion`, `exportId`, Ploeg's version, `tenant`, `client` or null, `kinds`, `snapshotAt`, and for each file its `name`, `rows`, `bytes` and `sha256`. A reader that finds no manifest at the end knows the stream broke.
* **No secrets.** Each kind selects exactly the columns its operator read selects, with the same redaction of recognisable credentials, the same removal of URL queries and fragments, and the same length bounds. Credential-bearing columns are never selected. A test fails if a new column holding a credential is added to an exported table without being excluded.
* **Audit.** `operator.export.started` when the snapshot opens and `operator.export.finished` with the manifest's sha256 and row counts when the last byte is written, or `operator.export.failed` with the reason, each with the request's attribution, `exportId`, `tenant`, `client` and `kinds`. Ploeg keeps the audit rows, never the archive. A repeated `exportId` is a new snapshot with its own rows, `detail.repeatOf` naming the first.
* **Bounds.** One export runs per consumer at a time; a second answers 409 `export_running`. The transaction runs under ploegd's statement timeout raised to `PLOEG_EXPORT_TIMEOUT`, default `30m`; past it the stream ends without a manifest and `operator.export.failed` records `timeout`.

### Consequences

* Good, because a Tenant leaving, an auditor and a data-access request get one copy that is complete for one moment and checks itself.
* Good, because Ploeg stores no archive: nothing to expire, encrypt or delete later, and no copy that outlives a Tenant's removal.
* Good, because the export reuses the read shapes and their redaction, so it shows nothing a consumer could not already read, only all of it at once.
* Bad, because a broken download means a new export and a new snapshot. Accepted: a stored job would keep a full copy of the Tenant inside Ploeg.
* Bad, because a large Tenant holds a long read-only transaction, which delays vacuum on the tables it reads. The timeout bounds it, and one export per consumer keeps it rare.
* Bad, because the export is full each time: there is no incremental form. A trigger below covers it.

### Confirmation

The implementing change adds these tests, each run by `go test ./...` in `mise run verify`:

* `go test ./pkg/httpapi/ -run TestAnExportCarriesOnlyTheConsumersTenant` seeds two Tenants, exports every kind as a consumer bound to the first, and finds no id, title or text of the second in any file.
* `TestAnExportManifestMatchesItsFiles` recomputes every file's sha256, rows and bytes from the archive and compares them with `manifest.json`, which must be the last entry, and validates every line of every `<kind>.jsonl` against its `<kind>.schema.json`.
* `TestAnExportIsOneSnapshot` commits a change in another transaction while the export streams and checks the archive does not contain it.
* `TestAnExportCarriesNoSecret` plants recognisable tokens, key hashes and lease tokens in every exported table and finds none in the archive; `TestEveryExportedColumnIsAllowed` fails on a column of an exported table that is neither selected nor listed as excluded.
* `TestExportNeedsTheCapabilityAndContentNeedsReadContent`, `TestExportIsAuditedStartedAndFinished` (against PostgreSQL) and `TestASecondExportWhileOneRunsIsRefused`.
* The operator contract test pins the request, the manifest and the error codes in `docs/contracts/operator-api.v1.schema.json` to their Go types, and `go test ./internal/compat/` finds no break.

## Pros and Cons of the Options

### An export job whose files ploegd stores in PostgreSQL

* Good, because a broken download can resume from the stored files.
* Bad, because Ploeg then keeps a full copy of the Tenant that needs a retention period, deletion and its own access check, and large binary rows in the one database every Run depends on.

### An export job that writes to object storage

* Good, because large archives stay out of the database and can be fetched again.
* Bad, because it adds a second state store and its credentials to every deployment that wants an export, against ADR-0071.

### No export route: the consumer pages the existing reads

* Good, because it needs nothing new.
* Bad, because pages are read at different moments, so the copy matches no single state, and nothing proves it complete.

## Re-evaluation triggers

* An export of a real Tenant runs past the default timeout or measurably delays vacuum: decide an incremental export or a job that stores its files.
* A consumer asks for an export since a cursor rather than of everything.
* A deployment needs to import an archive into another Ploeg: decide whether the archive is also an import format.
* ADR-0178 is rejected: an export would then cover a Team scope, which this record does not decide.

## More Information

* Depends on [ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md); kept apart from it because the export can be accepted or rejected without reopening the Tenant boundary, and because it adds a capability, an archive format and an audit trail of its own.
* Reuses [ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)'s `readContent` for content kinds and [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)'s attribution on the audit rows.
* Compatibility ([policy](../ops/compatibility.md)): a new route, a new consumer field defaulting to `false`, a new variable and new codes; no existing answer changes.
