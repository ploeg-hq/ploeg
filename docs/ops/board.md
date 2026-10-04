# Tracker configuration and historical board notes

The tracker owns current work and priorities. Ploeg receives configured provider events and resolves a Work Target from the registered Scope. An operator consumer registers its own task sources and repository mapping; there is no universal project-ID default.

For current integration behavior, read [tracker execution binding](../contracts/tracker-execution.md), the [Vikunja provider](../../pkg/provider/vikunja/vikunja.go), the [ClickUp provider](../../pkg/provider/clickup/clickup.go).

To have agents implement an OpenSpec change, put a line `openspec: <change-id>` in the tracker item's description. See [OpenSpec Work Items](../contracts/README.md#openspec-work-items) for what the Run then does.

Before expecting an assignment to dispatch work, verify the actual tracker project, the assignee's access, webhook registration, provider routing and target mapping. A planning board and an execution test board may be different projects.

When the Vikunja URL and token are configured, ploegd checks webhook registration itself at startup and then hourly. For every Vikunja project in the routing config it lists the project's webhooks and looks for one that sends `task.assignee.created` to Ploeg: exactly `PLOEG_VIKUNJA_WEBHOOK_URL` when that is set, otherwise any target on the `/webhooks/tracker/vikunja` path. A project without one is logged as a warning naming the project. The unauthenticated `GET /readyz` reports only counts, under `vikunjaWebhooks.missingProjects` and `uncheckedProjects` (projects the check could not read). Readiness stays healthy, because one silent board does not stop the service. Setting `PLOEG_VIKUNJA_WEBHOOK_REGISTER=true` additionally registers a signed webhook on each missing project; it requires `PLOEG_VIKUNJA_WEBHOOK_URL` and the webhook secret, and ploegd refuses to start without them. Registration writes tracker state, so decide first whether each configured board should dispatch at all.

The [July board snapshot](https://forgejo.webgrip.dev/webgrip/ploeg/src/commit/94c7c8e2dc07037ee2fd38a69343426e40bd680d/docs/ops/board.md) records IDs and MCP failures observed then. Do not treat those IDs, counts, API errors or the old console default as current configuration.
