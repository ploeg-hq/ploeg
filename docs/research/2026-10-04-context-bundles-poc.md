# Context bundles proof of concept

Status: research record and proof of concept, 2026-10-04. Built on branch `poc/okf-knowledge-pack` with the knowledge pack, then split into its own branch `feat/context-bundles` once ADR-0067 and ADR-0068 were accepted. In the split, OKF concepts inside a bundle no longer join knowledge selection; that link returns with the knowledge pack.

**Question.** Can a person attach files (a zip, a tar.gz or a single file) to a Work Item, at the start and while steering, and can every later Run receive them verified, unpacked outside the clone and listed in its prompt, without any harness change?

**Method.** Built storage, operator and Run routes, claim references, worker delivery and safe unpacking against a written contract, with Unfold's Vloer built in parallel against the same contract. Checked wire compatibility by feeding real ploegd responses from the test server to Vloer's parser.

**Limitations.** Only tests and a fake-Ploeg browser check; no live cluster Run yet. Bytes live in Postgres. ploegd's 30-second read timeout bounds uploads. Not yet in `operator-api.v1.schema.json` or the Helm chart.

## Results

* `TestContextBundlesEndToEnd` (`pkg/httpapi/context_e2e_test.go`) runs the real server against embedded Postgres: an operator upload before the Run reaches the first Run's TaskSpec, prompt and context directory; an upload while that Run runs is marked `while_steering`, is absent from the first Run and reaches the next Round's Run.
* Safe unpacking refuses traversal, absolute paths, links, special files, duplicates, zip bombs, and archives over the file, size and path limits, at upload and in the worker.
* A digest mismatch makes the Run stuck before the harness starts.
* Real responses (created 201, idempotent 200, zip, list, and 400 refusals naming the rule) parse with Vloer's `parseContextItem`.
* `go test ./...`: 38 packages pass.

## Differences from the first contract

* Errors use the operator API's envelope with codes, and success bodies carry `schemaVersion`.
* `while_steering` means a Run had started, not only that a Shift was open, because Shifts open at ingest.
* tar.gz is stored as `application/gzip`; empty files and archives are refused.

## Follow-ups

* Own timeouts for the context routes and the worker download.
* `operator-api.v1.schema.json` and Helm values.
* The storage spike (Postgres or object storage) and the secret-scanning spike, both on the Glide board under the epic "agents learn from a knowledge base and take context files".
