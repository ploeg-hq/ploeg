---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-17
verified_by: "Read cmd/ploeg-mcp, pkg/operatorclient, pkg/httpapi/operator.go and ops/helm/ploeg/templates/deployment.yaml; go test ./cmd/ploeg-mcp ./pkg/operatorclient ./pkg/httpapi (action tools, 2026-10-17); go install of v0.2.0-rc.14 on darwin/arm64 and the release assets of v0.2.0-rc.14 checked. Not checked against a live deployment or a running client."
---

# Ask Ploeg from an AI client over MCP

**Goal:** a person asks their own AI client (Claude Code, Codex, Cursor, any MCP client) "what is waiting for me, why, and what did it cost", and the answer comes from Ploeg's operator API ([ADR-0081](../adrs/0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md)).

`ploeg-mcp` is a local process that the client starts over stdio. It reads the operator API as one Operator Consumer and changes nothing. Over HTTPS it can also act for a signed-in person who confirms each change; see [Act on work over HTTPS](#act-on-work-over-https). To give people one HTTPS URL with a browser sign-in instead, see [Serve Ploeg's MCP tools over HTTPS](serve-ploeg-mcp-over-https.md).

## 1. Give it a read-only Operator Consumer

Use a consumer of its own, never another consumer's token, so you can revoke it alone and its reads show up under its own name.

1. Generate a random value of at least 32 bytes and put it in OpenBao, following [Rotate credentials](rotate-credentials.md#steps-shared-by-every-rotation). Git holds only the reference.
2. Sync it into a Kubernetes Secret in the `ploeg` namespace, for example `ploeg-mcp-reader` with key `token`.
3. Add the consumer to the chart values. Leave `execute` false: `ploeg-mcp` only reads, and a reader cannot start, cancel or approve work.

   ```yaml
   operator:
     consumers:
       - name: mcp-reader
         teams: [silver]        # leave out to read every Team
         execute: false
         tokenSecret:
           name: ploeg-mcp-reader
           key: token
   ```

4. Roll out ploegd. A consumer whose token is missing or shorter than 32 bytes stops ploegd at boot, so check the rollout.

## 2. Install the binary

Releases attach no `ploeg-mcp` binary, and the Ploeg image is built for `linux/amd64` only, so on macOS or on an arm64 machine build it from the release tag:

```sh
go install -ldflags "-X main.version=<version>" github.com/ploeg-hq/ploeg/cmd/ploeg-mcp@v<version>
ploeg-mcp --version
```

Go fetches the toolchain the module asks for. Without `-ldflags`, `--version` prints `0.0.0-dev`. On a `linux/amd64` host you can also run it from the image, which ships it at `/usr/local/bin/ploeg-mcp`: `docker run --rm -i -e PLOEG_URL -e PLOEG_MCP_TOKEN --entrypoint /usr/local/bin/ploeg-mcp ghcr.io/ploeg-hq/ploegd:<version>`. A container cannot reach a port-forward on the host's `127.0.0.1` without extra network flags, so prefer the binary on a laptop.

## 3. Connect a client

`ploeg-mcp` needs `PLOEG_URL` (ploegd's base URL) and `PLOEG_MCP_TOKEN` (the consumer's token). Without both it refuses to start. On a laptop, reach ploegd through a port-forward:

```sh
kubectl -n ploeg port-forward svc/ploeg 8080:8080
```

Never write the token into a client's configuration file. Put it in the environment of the process that starts the client, read from your secret store, and let the configuration refer to the variable.

Claude Code, for every project of the current user:

```sh
claude mcp add --scope user --transport stdio \
  --env PLOEG_URL=http://127.0.0.1:8080 \
  --env 'PLOEG_MCP_TOKEN=${PLOEG_MCP_TOKEN}' \
  ploeg -- ploeg-mcp
```

The single quotes keep `${PLOEG_MCP_TOKEN}` literal in `~/.claude.json`; Claude Code expands it from its own environment when it starts the server. Export `PLOEG_MCP_TOKEN` in the shell before you run `claude`.

VS Code reads environment variables in `mcp.json` only when it was started from a shell that has them. A client started from the Dock does not have that shell environment, so let a shell read the token from the OS keychain when the server starts. On macOS, with the token cached in the keychain as `ploeg-mcp-reader`, add to the user `mcp.json` (command **MCP: Open User Configuration**):

```json
{
  "servers": {
    "ploeg": {
      "type": "stdio",
      "command": "/bin/sh",
      "args": ["-c", "PLOEG_MCP_TOKEN=\"$(security find-generic-password -s ploeg-mcp-reader -w)\" exec \"$HOME/go/bin/ploeg-mcp\""],
      "env": { "PLOEG_URL": "http://127.0.0.1:8080" }
    }
  }
}
```

A client started from the Dock also lacks your shell's `PATH`, so name the binary by its full path; `go env GOPATH` shows the directory whose `bin` holds it.

Any other client takes the same command, arguments and two environment variables in its MCP configuration.

## Tools

| Tool | Answers |
| --- | --- |
| `ploeg_overview` | Counts, Runs and spend per Team for 24h, 7d or 30d, plus the Work Items that are `needs_human`, `awaiting_review` or `proposed` |
| `ploeg_find_work` | Work Items by state or Team, paged |
| `ploeg_get_work` | One Work Item by Ploeg id, `provider:externalId` or tracker task URL: why it waits, its latest Shift's budget and spend, its Runs, pull request and recent events. `detail: full` adds descriptions, findings and event details |
| `ploeg_recent_runs` | Runs newest first with outcome, verdict, failure reason and cost |
| `ploeg_changes_since` | Audit events since a cursor the previous call returned |

Every tool above is read-only and returns its full answer as structured content and as text. Amounts are USD rounded to cents. Text that came from a tracker, a forge or an agent sits under an `untrusted` key, and the server tells the client to treat it as data.

## Act on work over HTTPS

Over stdio `ploeg-mcp` only reads: it has no verified person to name. Over HTTPS ([Serve Ploeg's MCP tools over HTTPS](serve-ploeg-mcp-over-https.md)), a person whose group is marked `"execute": true` in `PLOEG_MCP_GROUPS`, and whose group's Operator Consumer has `execute: true` in ploegd, also gets the action tools of ADR 0108 ([ploeg-hq/ploeg#146](https://github.com/ploeg-hq/ploeg/pull/146)). Other people never see them in the tool list.

| Tool | Does | Operator route |
| --- | --- | --- |
| `ploeg_submit_work` | Submits a new Work Item to a Team on a registered target, with an optional budget | `POST /work-items` |
| `ploeg_approve_work` | Approves a proposed Work Item | `POST /work-items/{id}/approve` |
| `ploeg_reject_work` | Rejects a proposed Work Item with a reason | `POST /work-items/{id}/reject` |
| `ploeg_retry_work` | Restarts a `needs_human`, `stale` or `awaiting_review` Work Item, from Round 1 or a `fromRound`, with an optional note | `POST /work-items/{id}/requeue` |
| `ploeg_stop_work` | Stops a Work Item: withdraws it, cancels pending Runs, stops running ones | `POST /work-items/{id}/cancel` |
| `ploeg_add_note` | Leaves an instruction for the next Run of a live Work Item | `POST /work-items/{id}/notes` |
| `ploeg_steer_execution` | Sends a message to, pauses, resumes or cancels an attended execution the person owns | `POST /executions/{id}/commands` |

**Every action asks first.** Before it calls ploegd, the tool asks the person through an MCP elicitation that states the exact effect: which Work Item (id, title, Team and state), what will happen and what it may cost. Only an accept with the box ticked goes ahead. A decline, a cancel or an unticked box answers "Not done: you did not confirm. Nothing changed." If the Work Item changes while the person decides, nothing happens and the tool asks to be called again. A client that cannot show an elicitation is refused before anything is called. Over stateless HTTP that means the client must speak MCP 2026-07-28, which carries the confirmation inside the tool call; a 2025-11-25 client can still read but cannot act.

**Every action names the person.** ploegd records each change with the person (`X-Ploeg-Actor`: Ploeg's person id, `oidc-` and the first 32 hex characters of the SHA-256 of `<issuer>|<subject>`, from the verified token's issuer with one trailing `/` removed, or `PLOEG_MCP_SUBJECT_NAMESPACE` when set, and its subject), the channel (`X-Ploeg-Via`: `mcp:` and the client's name and version, or `mcp:ploeg-mcp` when the client sends none) and the confirmation (`X-Ploeg-Confirmation`: the elicitation id and the SHA-256 of the text the person agreed to). They appear as `detail.attribution` on the change's events; `GET /api/v1/operator/events?actor=<person>` lists what one person did. The username is not sent: `X-Ploeg-Acting-User` names a different person acting on the actor's behalf, so `ploeg-mcp` writes the username next to the person id in its own request log instead.

**A repeated call acts once.** Each call gets a fresh `commandId`, returned in the answer. Pass it back as `commandId` to repeat a call that timed out: submit, retry, stop and notes answer `replayed: true` and change nothing again. Approve and reject need no command id: a second decision on the same item is refused because it is no longer proposed. An execution command repeated after it acted is refused as stale.

### Steer your attended executions

A person steers any attended execution they own, whichever Operator Consumer admitted it, as long as the Team is in their group's consumer's Teams. `ploeg_get_work` lists a Work Item's executions with their id, state, owner and `yours: true` for the ones the signed-in person owns, and its summary names yours. Pass that id to `ploeg_steer_execution` with `message` (and the text), `pause`, `resume` or `cancel`. ploegd checks the owner: an execution of another person, or of a Team outside the consumer's scope, answers as if it did not exist and nothing changes. The executor's own commands (start, heartbeat, report, close, credentials, blocking) stay with the consumer that admitted the execution.

This depends on one person id everywhere: every consumer that admits executions names its people in `X-Ploeg-Actor` with the same `oidc-` form, from the same provider subject. When `ploeg-mcp` signs people in through a provider application whose issuer differs from the issuer person ids were recorded under (a provider that puts the application's name in its issuer does this), set `PLOEG_MCP_SUBJECT_NAMESPACE` to that issuer so the ids match.

## Verify

Ask the client "what needs a person in Ploeg?". It should call `ploeg_overview` and list the same `needs_human` Work Items as `GET /api/v1/operator/work-items?needsHuman=true`.

## Symptom → cause → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| The client says the server failed to start | `PLOEG_URL` or `PLOEG_MCP_TOKEN` is missing, or the token is shorter than 32 bytes | Set `PLOEG_URL` in the client's MCP configuration and export `PLOEG_MCP_TOKEN` where the client starts |
| Every tool answers "Ploeg refused PLOEG_MCP_TOKEN" | The token belongs to no configured Operator Consumer | Check the chart's `operator.consumers` and that ploegd restarted after the change |
| A tool answers "did not answer within 5 seconds" | The port-forward dropped or ploegd is overloaded | Restart the port-forward; narrow the request with `team` or `limit` |
| `ploeg_get_work` answers "not found" for an id you can see elsewhere | The Work Item belongs to a Team outside the consumer's `teams` | Add the Team to the consumer, or leave `teams` out |
