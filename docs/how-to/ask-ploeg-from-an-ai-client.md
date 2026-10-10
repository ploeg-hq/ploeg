---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-10
verified_by: "Read cmd/ploeg-mcp, pkg/operatorclient, pkg/httpapi/operator.go and ops/helm/ploeg/templates/deployment.yaml; go test ./cmd/ploeg-mcp ./pkg/operatorclient. Not checked against a live deployment."
---

# Ask Ploeg from an AI client over MCP

**Goal:** a person asks their own AI client (Claude Code, Codex, Cursor, any MCP client) "what is waiting for me, why, and what did it cost", and the answer comes from Ploeg's operator API ([ADR-0081](../adrs/0081-ploeg-serves-its-operator-reads-as-mcp-tools-through-ploeg-mcp.md)).

`ploeg-mcp` is a local process that the client starts over stdio. It reads the operator API as one Operator Consumer and changes nothing.

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

Either build it from a release tag:

```sh
go install github.com/ploeg-hq/ploeg/cmd/ploeg-mcp@<release tag>
```

or copy it from the Ploeg image, which ships it at `/usr/local/bin/ploeg-mcp`.

## 3. Connect a client

`ploeg-mcp` needs `PLOEG_URL` (ploegd's base URL) and `PLOEG_MCP_TOKEN` (the consumer's token). Without both it refuses to start. On a laptop, reach ploegd through a port-forward:

```sh
kubectl -n ploeg port-forward svc/ploeg 8080:8080
```

Claude Code:

```sh
claude mcp add ploeg --env PLOEG_URL=http://127.0.0.1:8080 --env PLOEG_MCP_TOKEN=<the consumer token> -- ploeg-mcp
```

Any other client takes the same command, arguments and two environment variables in its MCP configuration.

## Tools

| Tool | Answers |
| --- | --- |
| `ploeg_overview` | Counts, Runs and spend per Team for 24h, 7d or 30d, plus the Work Items that are `needs_human`, `awaiting_review` or `proposed` |
| `ploeg_find_work` | Work Items by state or Team, paged |
| `ploeg_get_work` | One Work Item by Ploeg id, `provider:externalId` or tracker task URL: why it waits, its latest Shift's budget and spend, its Runs, pull request and recent events. `detail: full` adds descriptions, findings and event details |
| `ploeg_recent_runs` | Runs newest first with outcome, verdict, failure reason and cost |
| `ploeg_changes_since` | Audit events since a cursor the previous call returned |

Every tool is read-only and returns its full answer as structured content and as text. Amounts are USD rounded to cents. Text that came from a tracker, a forge or an agent sits under an `untrusted` key, and the server tells the client to treat it as data.

## Verify

Ask the client "what needs a person in Ploeg?". It should call `ploeg_overview` and list the same `needs_human` Work Items as `GET /api/v1/operator/work-items?needsHuman=true`.

## Symptom → cause → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| The client says the server failed to start | `PLOEG_URL` or `PLOEG_MCP_TOKEN` is missing, or the token is shorter than 32 bytes | Set both in the client's MCP configuration |
| Every tool answers "Ploeg refused PLOEG_MCP_TOKEN" | The token belongs to no configured Operator Consumer | Check the chart's `operator.consumers` and that ploegd restarted after the change |
| A tool answers "did not answer within 5 seconds" | The port-forward dropped or ploegd is overloaded | Restart the port-forward; narrow the request with `team` or `limit` |
| `ploeg_get_work` answers "not found" for an id you can see elsewhere | The Work Item belongs to a Team outside the consumer's `teams` | Add the Team to the consumer, or leave `teams` out |
