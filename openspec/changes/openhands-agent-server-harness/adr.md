# ADR Review Manifest

## ADR Review Completed

- **Date**: 2026-10-18
- **Reviewer**: Claude (for Ryan Grippeling)
- **Change**: `openhands-agent-server-harness`

## In-Force ADR Context Reviewed

- `0018-the-outcome-drop-box-is-every-harnesss-return-path.md` — the new Harness returns its Outcome through the drop-box like every other.
- `0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md` — the server gets the key placeholder and the loopback proxy, never a real credential.
- `0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md` — server events map into the bounded, redacted transcript.
- `0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md` — the Run records the server's version.
- `0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md` and `0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md` — the Harness is selected and isolated through the existing allowlist and per-Harness workload; unqualified, it is admissible only as a Role default.
- `0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md` — confirmation requests follow the same relay and fallback as ACP.
- `0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md` — telemetry reaches the server through its observability headers, not OTLP environment variables.
- `0114-each-run-records-the-limits-that-stop-its-prompt.md` — the Harness reports the prompt start and limits when the conversation starts running.
- `0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md` (proposed) — the decision this change implements: keep OpenHands through an `openhands-agent-server` Harness.

## Repository-Level ADRs Created

- none — no durable architectural decision was introduced by this change. ADR-0118 already records the decision; this change implements it within the ADRs above.

## Supersessions

- none

## Validation

```sh
$ go test ./internal/ledger/
ok  	github.com/ploeg-hq/ploeg/internal/ledger	0.022s
```
