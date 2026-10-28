---
type: reference
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-27
verified_by: "Read every CREATE TABLE and ON DELETE in pkg/store/migrations, every DELETE in pkg/store and cmd/ploegd/sweep.go (deliveryRetention), pkg/store/note_reviews.go (PurgeClientNotes), pkg/transcript, cmd/ploegd and cmd/ploeg-worker main.go for PLOEG_RUN_TRANSCRIPTS, PLOEG_RUN_TRANSCRIPT_THOUGHTS, PLOEG_HARNESS_OUTPUT_IN_LOG and PLOEG_TELEMETRY, ADR-0090 and ADR-0122"
---

# Data retention

What Ploeg keeps, for how long, what you can switch off, and what it never deletes because the accounting depends on it. Everything below lives in Ploeg's PostgreSQL database unless the row says otherwise. Ploeg has no time-based expiry for Runs, their outcomes, transcripts, the audit log or the ledger: **they are kept for the life of the database**. What you control is whether sensitive content is recorded at all, and how long your database backups, log store, telemetry collector and gateway keep their copies.

## What Ploeg keeps

| Data | Where | Holds sensitive content | Kept |
| --- | --- | --- | --- |
| Work Items | `work_items` | The tracker task's title and description | For the life of the database |
| Runs and their outcomes | `agent_runs` | The agent's summary, findings, problem, solution, stuck reason and links; usage figures; the briefing's note and concept ids | For the life of the database. Ploeg deletes no Run |
| Run transcripts | `run_transcript_entries`, `run_transcript_totals` | The agent's session: messages, reasoning, tool calls and tool output, which can include file contents, after redaction ([ADR-0090](../adrs/0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)) | As long as their Run, at most 5,000 entries or 2 MiB per Run |
| Audit log | `audit_log` | Ids, states, reasons, amounts and attribution; no ticket or agent text in the `llm.*` events | For the life of the database |
| Shifts and the ledger | `shifts`, `run_llm_accounts`, `run_llm_adjustments`, `run_llm_operator_settlements` | Amounts, key aliases, the gateway key's hashed id, settlement evidence. An operator settlement keeps the evidence text the operator typed | For the life of the database; see [never deleted](#never-deleted-for-accounting) |
| Asks and chat messages | `asks`, `chats` | Only the SHA-256 of the question or message, never its text | For the life of the database |
| Notes, attached context, note reviews | `operator_notes`, `work_item_context` (the attached file's bytes), `work_item_note_outbox`, `note_reviews`, `question_answers` | What people and agents wrote, and files people attached | For the life of the database. [ADR-0122](../adrs/0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md) designs a per-Client purge of notes; no route runs it yet |
| Pull request facts | `pull_requests` and `pull_request_*` | Titles, review states, CI results, changed file paths (never file contents), labels | For the life of the database. Changed paths and events are replaced when Ploeg reads the pull request again |
| Webhook delivery ids | `forge_deliveries` | Forge and tracker delivery ids, synthetic ids derived from the payload, and each delivery's claim and completion time; no payload | 48 hours, then the sweep deletes them ([sweep.go](../../cmd/ploegd/sweep.go), `deliveryRetention`) |
| Leases | `leases` | None | Until the Lease is released, expires or its Run ends |
| Run tokens | `agent_runs.run_token` | A credential while its Run is live | For the life of the database. No operator read, metric or log returns it; operator reads show only the alias `ploeg-<first 12 hex>` |

Ploeg never stores the gateway key itself (only LiteLLM's hashed id), the forge token a Run used (only its id), the worker bootstrap token, or the text of an Ask or chat message. It never stores file contents of a repository, except where the agent printed them into a transcript or a person attached a file as context.

## Outside the database

| Data | Where | Holds sensitive content | Kept |
| --- | --- | --- | --- |
| ploegd and ploeg-worker logs | Your log store | Ids, outcomes, failure reasons, byte lengths and links without query strings; never ticket, summary or tool text. With `PLOEG_HARNESS_OUTPUT_IN_LOG=on` (chart `executor.harnessOutputInLog`, default off) the harness's whole stdout and stderr, which can carry prompts, file contents and secrets | Your log store's retention |
| Metrics | Prometheus, from `GET /metrics` | Labels name Teams, Roles, outcomes, failure reasons, verdicts, states, Harnesses and sources that configuration or Ploeg's code defines; none carries ticket, agent or person text ([alerts](alerts.md)) | Your Prometheus retention |
| Harness telemetry | Your OpenTelemetry collector, only when `PLOEG_TELEMETRY` (chart `executor.harness.telemetry`) names one | Prompt text only with `logPrompts: true`, tool arguments only with `logToolDetails: true`; both default to off | Your collector's retention |
| Gateway spend logs | LiteLLM | Per-request spend, model and key alias; whatever LiteLLM is configured to log | LiteLLM's retention |
| Knowledge graph notes | Omnigraph, when the knowledge graph is enabled | Notes Runs and people wrote | Omnigraph keeps every version until `omnigraph cleanup` ([ADR-0122](../adrs/0122-every-note-carries-its-provenance-untrusted-text-is-quoted-and-a-wider-level-needs-evidence-or-a-person.md)) |
| Database backups | Your backup store | A copy of everything above | Your backup retention |

## Settings that control sensitive content

| Setting | Default | Effect |
| --- | --- | --- |
| `PLOEG_RUN_TRANSCRIPTS` on ploegd (chart `env`) | `true` | `off` refuses new transcripts and makes every worker stop sending them. Transcripts already stored stay readable |
| `PLOEG_RUN_TRANSCRIPTS` on ploeg-worker | `true` | `off` records no transcript for that worker's Runs |
| `PLOEG_RUN_TRANSCRIPT_THOUGHTS` on ploeg-worker | `true` | `off` leaves the agent's reasoning out of the transcript |
| `PLOEG_HARNESS_OUTPUT_IN_LOG` (chart `executor.harnessOutputInLog`) | `false` | `true` copies the harness's output into pod logs. Leave it off unless your log store is as protected as the repository |
| `PLOEG_TELEMETRY` `logPrompts`, `logToolDetails` (chart `executor.harness.telemetry`) | off | `true` sends prompt text or tool details to your collector |
| `operator.consumers[].readContent` | the consumer's `execute` | Who may read transcripts, briefings, context and note reviews through the operator API ([ADR-0160](../adrs/0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)). Spend, states and the audit trail need no `readContent` |

The [configuration reference](../reference/configuration.md) describes each one. Redaction masks the Run's own credentials exactly and a fixed list of credential shapes; it is best effort for anything else, so a transcript is as sensitive as the repository it was made in.

## Never deleted, for accounting

Do not delete these rows, by hand or with a database tool. The budget arithmetic of every Shift and the evidence of every charge depend on them ([accounting invariants](../reference/accounting-invariants.md)):

- `shifts`: the pool and the settled total. `spent` must equal what the Shift's accounts settled plus the harness-reported cost of Runs without one.
- `agent_runs`: each Run's authorization and reported usage. Deleting a Run also deletes, through `ON DELETE CASCADE`, its inference account, adjustments, operator settlement and transcript, so the Shift's `spent` would no longer match anything.
- `run_llm_accounts`, `run_llm_adjustments`, `run_llm_operator_settlements`: what each Run was charged, from which evidence, and every later correction.
- `audit_log` rows with an `llm.` action: the trace [Investigate a Run's spend](../how-to/investigate-a-runs-spend.md) reads.

Keep LiteLLM's spend logs at least until every account that could still need them has settled and its correction window (`PLOEG_LLM_CORRECTION_WINDOW`, default `24h`) has ended. A key whose spend logs are gone before settlement can only be settled from other evidence.

## Changing what is kept

- **Stop recording a kind of content:** use the settings above. They apply to new Runs; they do not remove what is stored.
- **Shorten how long copies live outside Ploeg:** set the retention of your log store, Prometheus, collector, gateway and backups.
- **Remove stored content:** Ploeg has no supported command that deletes Runs, transcripts or Work Items, and no time-based expiry. A request to erase one Client's or person's data needs a database operation you plan and review yourself, after a backup, and it must leave the rows listed under [never deleted](#never-deleted-for-accounting) in place. Run text and transcripts can be removed without touching the ledger; Runs and Shifts cannot.

Open an issue if your deployment needs an expiry setting for transcripts or Run text; the decision belongs in an ADR, because it changes what the operator API can answer.
