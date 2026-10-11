---
status: proposed
date: 2026-10-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run's agent session is kept as a bounded, redacted transcript

## Context and Problem Statement

While a Run works, the people who follow it see a handful of checkpoints and the pod's log. For an agent that speaks ACP over stdio, such as OpenHands through the `acp` harness, the pod log holds almost nothing: the agent's messages, tool calls and their output travel as JSON-RPC between the agent and the worker, and an eight-minute Run leaves about fifteen log lines. The worker already reads every `session/update` notification to build the outcome ([`pkg/harness/adapters/acp`](../../pkg/harness/adapters/acp/)), then throws the detail away. What should Ploeg keep of that session, where, and how does an operator consumer read it without the transcript becoming a way to leak a Run's credentials or to break a Run?

## Decision Drivers

* A person following a Run needs to see what the agent said, which tools it called with what input, what they returned, and its plan, while the Run is still going.
* Recording must never fail, slow or block a Run; the ACP read loop must never wait on the network.
* Command output can carry secrets. The worker isolates the Run's own credentials, but a transcript must not become the place they leak to.
* Ploeg keeps its state in PostgreSQL ([ADR-0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md)) and exposes execution facts to consumers through the operator API ([ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)); it names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* A runaway agent can stream megabytes; storage per Run must have a fixed ceiling.

## Considered Options

* The worker derives typed entries from the ACP stream, ships them in batches to ploegd, and ploegd stores and serves them per Run
* The worker uploads the raw JSON-RPC log of the session as one blob when the Run ends
* The worker writes the session to its stdout and consumers read it from the log store
* Consumers attach to the agent's session themselves

## Decision Outcome

Chosen option: "typed entries shipped in batches", because it is the only option that shows the session while the Run runs, keeps the secrets of the wire out of storage by construction, bounds what one Run can store, and reuses the run-token-authenticated worker routes and the team-scoped operator reads Ploeg already has.

What is decided:

* **Entries.** A transcript is an ordered list of typed entries. `message` and `thought` are the agent's reply and reasoning, coalesced from their chunks: an open entry closes when another kind arrives or when one flush interval passes without a chunk. `tool_call` is recorded when a call is announced and again each time its status changes (`pending`, `in_progress`, `completed`, `failed`), carrying its title, kind, paths and a one-line input summary (the command, path, query or URL it was given, else compact JSON). `tool_output` is the call's text content, its diffs rendered as removed and added lines, or its raw output when it finished without content; output identical to the call's previous output is skipped. `plan` is the agent's plan when it changed. `stage` is a step the worker or adapter took: harness started, agent session started, how the prompt turn ended, how the harness finished. `truncated` is the single marker written when a limit is reached. The echo of Ploeg's own prompt (`user_message_chunk`) is never recorded.
* **Ordering.** The worker numbers entries per Run from 1 (`seq`) and stamps when it observed them (`at`). ploegd stores them keyed by `(run_id, seq)`, so a resent batch is ignored entry by entry and readers page by `seq`.
* **Batching.** The worker ships every 2 seconds, or as soon as 100 entries are waiting, at most 100 entries and 1 MiB per request. A batch that fails is retried on the next flush and dropped after three failures; at most 1,000 entries wait, and the oldest is dropped beyond that. A gap in `seq` is how a reader sees the loss.
* **Limits.** Per entry: text 4 KiB, input summary 512 bytes, title 256 bytes, 16 paths and 32 plan steps of 256 bytes each; `clipped` marks text or input that was cut. Per Run: 5,000 entries or 2 MiB of entry JSON, after which one `truncated` entry is stored and every later entry is refused. Both the worker and ploegd enforce the limits, so storage never depends on the worker being correct. A tool kind or status outside the published enums is stored as `other` or left out.
* **Redaction.** The worker masks the exact values of the Run's model key, forge token (also after a refresh) and canary credential, then a fixed set of credential shapes: 48-hex run tokens, `sk-` keys and `Bearer` values, GitHub and GitLab tokens, AWS access key ids, Slack tokens, JWTs, private key blocks, passwords in URLs and the values of `password`, `secret`, `token`, `api_key`, `access_key`, `private_key` and `client_secret` assignments. ploegd applies the same shapes again on ingestion and also masks the Run's run token and control capability, so what is stored never depends on the worker having done it. Text is masked when an entry closes, so a secret split across chunks is still caught. Shape matching is best effort: a transcript is as sensitive as the repository and its tool output, and is served only to operator consumers allowed to read the Run's team.
* **Failure isolation.** Recording only appends to memory under a lock; shipping runs on its own goroutine. ploegd answering 404, 4xx other than 429, or the Run no longer running stops the worker sending for that Run. The worker closes the transcript right after the harness returns, with at most 10 seconds for the last batch, before it reports the outcome; ingestion is accepted only while the Run is running. Nothing about the transcript changes the outcome.
* **Thoughts.** Recorded by default; `PLOEG_RUN_TRANSCRIPT_THOUGHTS=off` leaves them out.
* **Switch.** `PLOEG_RUN_TRANSCRIPTS=off` on ploeg-worker records nothing; on ploegd it refuses ingestion with 404, which makes every worker stop, and leaves stored transcripts readable. Default on in both.
* **Retention.** Entries live and die with their Run (`ON DELETE CASCADE` on `agent_runs`). Ploeg deletes no Run today, so a transcript is kept as long as its Run, bounded at 2 MiB.
* **Routes.** `POST /api/v1/runs/{token}/transcript` takes a `transcriptRequest` under the run control capability and answers `transcriptAppendResponse` ([run API](../contracts/run-api.v1.schema.json)). `GET /api/v1/operator/runs/{id}/transcript?after=<seq>&limit=<1..500>` answers `transcriptResponse` with `entries`, `nextAfter`, `hasMore`, `truncated` and `complete`, and 404 for a Run outside the consumer's teams ([operator API](../contracts/operator-api.v1.schema.json)). A consumer polls with `after=nextAfter` until `complete` is true and `hasMore` false.
* **Harnesses.** The `acp` adapter feeds the transcript. Other harnesses get only the worker's `stage` entries until an adapter feeds them.

### Consequences

* Good, because a person can follow an ACP agent's reasoning, commands, output and plan within seconds of them happening, where the pod log showed nothing.
* Good, because the transcript reuses the run control capability and the operator scope, so no new credential or permission model is introduced.
* Good, because one Run can never store more than 2 MiB, and a broken ploegd or network costs the transcript, never the Run.
* Bad, because shape-based redaction cannot recognize every secret; a credential in an unknown format that a command prints can be stored. Accepted: the Run's own credentials are masked exactly, the rest is no more exposed than in the repository and the forge the consumer can already reach, and reads are team-scoped.
* Bad, because polling every few seconds per open view costs database reads; a consumer that needs sub-second updates would need a push channel this decision does not add.
* Bad, because transcripts are kept as long as Runs; storage grows with Run count at up to 2 MiB each.

### Confirmation

* `go test ./pkg/transcript/` covers coalescing, the quiet-interval close, tool status changes, plan and output de-duplication, redaction of exact secrets and shapes, the entry and Run limits with one marker, retry and drop, and refusal.
* `go test ./pkg/harness/adapters/acp/` turns recorded `session/update` notifications into entries and runs a scripted ACP agent over the real wire.
* `go test ./pkg/worker/` runs a scripted ACP agent through `execute`, checks the shipped transcript (the model key it printed is masked), and shows that a ploegd answering 404 or 500 leaves the Run's outcome unchanged.
* `go test ./pkg/store/ ./pkg/httpapi/` covers ordering, resends, the limits, ingestion only while running, the run capability, redaction on ingestion, pagination, team scope, and both schemas in [docs/contracts](../contracts/).

## Pros and Cons of the Options

### Upload the raw JSON-RPC log when the Run ends

* Good, because it loses nothing the agent said.
* Bad, because nobody sees it while the Run runs, which is when it is wanted.
* Bad, because the raw wire carries every byte the agent read, including files and environment dumps, so it is the largest and most secret-laden form, and a Run killed before upload leaves nothing.

### Write the session to stdout for the log store

* Good, because log shipping already exists.
* Bad, because the log store is per deployment, unauthenticated for operator consumers, has no notion of a Run's team scope, and is not part of Ploeg's contract; redaction and limits would be the log pipeline's job, not Ploeg's.

### Consumers attach to the agent's session

* Bad, because the agent speaks ACP on the worker's stdio inside the sandbox; exposing it would give a consumer a live channel into the Run and break the worker's isolation of credentials and approvals.

## Re-evaluation triggers

* `run_transcript_entries` passes 10 GiB, or Ploeg starts deleting Runs.
* A stored transcript is found to carry a credential the redaction missed.
* A consumer needs updates faster than polling every two seconds gives.
* The `claude-code` or another non-ACP harness needs a transcript.
* ACP v2 changes the shape of `session/update`.

## More Information

* [Run API](../contracts/run-api.v1.schema.json) `transcriptRequest`, `transcriptEntry`, `transcriptAppendResponse`; [operator API](../contracts/operator-api.v1.schema.json) `transcriptResponse`, `transcriptEntry`.
* Migration `pkg/store/migrations/0049_run_transcripts.sql`.
