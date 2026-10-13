# RFC: live tool output in a Run's transcript

Date: 2026-10-13. Source: ploeg `development` at `d20245b`, OpenCode `v1.18.35` (the version the harness images pin), and an Unfold consumer at `3b2b19bb`. Read-only; no Run was executed. Status: **proposed**. The consumer half is Unfold's [RFC-0006](https://forgejo.webgrip.dev/webgrip/unfold/src/branch/development/docs/design/rfc-0006-live-command-output.md).

The owner asked for a crew's command output to stream live, instead of appearing when a command completes. This RFC covers the worker and ploegd side. It proposes to amend [ADR-0090](../adrs/0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md) (proposed) before it is ratified.

## Where the output goes today

1. **OpenCode does stream.** For every stdout or stderr chunk, OpenCode's shell tool updates the running part's `metadata.output`. The value is a rolling snapshot of the last 30,000 characters, prefixed with `...\n\n` once it is cut (OpenCode `packages/opencode/src/tool/shell.ts`, lines 487–529). Under `opencode acp`, each changed snapshot goes out as a `tool_call_update` with `status: "in_progress"` and the whole snapshot as text content (`src/acp/event.ts` lines 341–375, `src/acp/tool.ts` lines 140–163).
2. **The `acp` adapter records each snapshot.** `transcribe` calls `rec.ToolOutput` on every `tool_call_update` ([`pkg/harness/adapters/acp/transcribe.go`](../../pkg/harness/adapters/acp/transcribe.go) lines 36–50). `Recorder.ToolOutput` skips a snapshot only when it is identical to the previous one, so every changed snapshot becomes a new entry ([`pkg/transcript/recorder.go`](../../pkg/transcript/recorder.go) lines 184–201).
3. **Each entry keeps the head, not the tail.** `clip` cuts text to `MaxTextBytes` (4 KiB) and keeps `s[:cut]` ([`pkg/transcript/entry.go`](../../pkg/transcript/entry.go) lines 36–50 and 135–147). Once a command has printed 4 KiB, every later snapshot is recorded as the same first 4 KiB.
   * The reader never sees the tail.
   * Each copy costs up to 4 KiB of the Run's 2 MiB ceiling.
   * About 500 chunks of one chatty command can end the transcript for the rest of the Run.
4. **Terminal content is skipped.** ACP `terminal` content items are dropped, and `rawOutput` is used only for a completed or failed call without content (`transcribe.go` lines 92–117).
5. **Delivery works.** The recorder flushes every 2 s or at 100 entries (`recorder.go` line 14, `entry.go` line 48). ploegd re-redacts and re-clamps each entry ([`pkg/httpapi/transcript.go`](../../pkg/httpapi/transcript.go) lines 37–66). A consumer polls `GET /api/v1/operator/runs/{id}/transcript?after=<seq>`.
6. **Pod logs carry none of this.** JSON-RPC lines go only to the SDK pipe ([`pkg/harness/adapters/acp/acp.go`](../../pkg/harness/adapters/acp/acp.go) line 115). No chart grants `pods/log`.
7. **Claude Code produces no transcript.** It runs with `--output-format json` ([`pkg/harness/adapters/claudecode/claudecode.go`](../../pkg/harness/adapters/claudecode/claudecode.go) lines 92–97), so it has no live output.

So the output is produced live and delivered live, but recorded as copies of its first 4 KiB.

## Options

* **(a) Offset-addressed chunks in the existing transcript (recommended).**
  * The adapter turns snapshots into deltas, and the recorder sends `tool_output` entries that carry the new text and its `offset` in the call's output.
  * Batching, redaction, the operator route and the Run ceiling stay as they are.
  * The schema change is one optional field.
* **(b) A push channel from ploegd.**
  * The worker posts chunks, and ploegd fans them out over SSE and persists a bounded tail.
  * It gives sub-second output, but adds a per-consumer fan-out, held connections and a second resume protocol.
  * ADR-0090 already names this as the step to take only when polling is measured to be too slow.
* **(c) Pod log streaming.**
  * The worker would print output to stdout, and ploegd would follow `pods/log`.
  * That needs new RBAC in every Run namespace.
  * The output would leave the worker before Ploeg redacts it, into an unauthenticated log store with no Team scope.
  * ADR-0090 rejected the log store for these reasons.

## Proposal

**Deltas in the adapter.** For each tool call, `transcribe` keeps the last snapshot and the number of characters already emitted, then:

1. If the new snapshot extends the last one, the delta is the extension.
2. If OpenCode's rolling window moved, the delta is what follows the longest suffix of the last snapshot that is a prefix of the new one, after the `...\n\n` marker is removed. Prefix-function matching keeps this linear.
3. If there is no overlap, the delta is the whole new snapshot, and the offset jumps by the characters that were never seen. That jump is a gap the reader shows.

**Whole lines, redacted, coalesced.** The recorder holds a call's unfinished last line until a newline arrives, the call ends, or the line passes 4 KiB. That way, `redact.go`'s patterns and the exact secrets always see a whole line. It sends at most one chunk per call per second or per 8 KiB, inside the existing 2-second batch.

**Head and tail kept.** For each call, the first 32 KiB and the last 32 KiB are sent and the middle is dropped. The final chunk, written when the call completes or fails, carries the tail. All chunks still count against `MaxRunEntries` and `MaxRunBytes`. A command can therefore no longer spend the Run's ceiling on repeats.

**Schema.** `transcriptEntry` in [`docs/contracts/operator-api.v1.schema.json`](../contracts/operator-api.v1.schema.json) and [`docs/contracts/run-api.v1.schema.json`](../contracts/run-api.v1.schema.json) gains an optional `offset` (integer, at least 0) on `tool_output`. An entry without `offset` keeps today's meaning. ploegd validates that offsets do not go backwards within a call; it drops a regressing entry and stores the others. The Go types change together with the schema.

**Unchanged:**

* the operator route and its polling;
* team scope;
* two-pass redaction;
* retention, which follows the Run;
* the prompt echo, which stays unrecorded.

Chunks are tool output, the data class `tool_output` holds today, so Ploeg keeps nothing new of a prompt. There is no input path: the worker refuses ACP `terminal/*` requests ([`pkg/harness/adapters/acp/client.go`](../../pkg/harness/adapters/acp/client.go) lines 126–144), and this proposal adds no route towards a Run.

## Effort and acceptance

M: one to two days, covering the adapter diff, the recorder's line buffer and per-call budget, the schema and Go types, and tests with `httptest` and a scripted ACP agent.

Acceptance:

* A scripted ACP agent that streams 1,000 one-line snapshots of a growing output produces chunks whose concatenation, read by `after=<seq>`, equals the output with the middle beyond 64 KiB replaced by a single gap. The last line is always present.
* A canary secret split across two snapshots never appears in a stored entry.
* A 10 MiB command uses at most about 64 KiB of the Run's ceiling.
* An entry without `offset` from an older worker is accepted and served unchanged.
* The regression test fails on today's code, which repeats the first 4 KiB.

## Decisions for the owner

1. **Amend ADR-0090 before it is ratified**, so that `tool_output` is offset-addressed chunks with head and tail kept. Recommendation: yes. Today's head-only snapshots lose every long command's tail and spend the Run budget on copies.
2. **Is 2 to 5 seconds live enough** for now, with option (b) left until a consumer measures a need? Recommendation: yes.
3. **Claude Code.** Moving the `claude-code` adapter to `stream-json`, which is ADR-0090's own re-evaluation trigger, would give completed tool output per call. Whether Claude Code emits partial shell output at all was not verified. Recommendation: a separate ticket, after this one.
