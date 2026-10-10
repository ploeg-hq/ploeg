---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-10
verified_by: "Read pkg/gate/gate.go, pkg/config/{config,resolve}.go, pkg/httpapi/{server,gates}.go, pkg/provider/{vikunja,clickup}/*.go and pkg/store/{gates,statuses,facts}.go; go test ./pkg/gate ./pkg/config ./pkg/provider/... ./pkg/store ./pkg/httpapi. Not checked against a live Vikunja or ClickUp."
---

# Map tracker statuses to delivery gates

**Goal:** an operator consumer can read where a ticket stands after the pull request (in development, in test, in acceptance or done), every time it was sent back with a reason, and every column it passed through.

**How it works:** you tell ploegd which of a board's columns belong to which gate. When the tracker tells Ploeg a ticket changed, Ploeg reads the ticket, records its column when it changed, mapped to a gate or not, and records a gate move when its column belongs to another gate than last time ([ADR-0051](../adrs/0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md), [ADR-0080](../adrs/0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)). A move back to an earlier gate is a bounce. Ploeg never moves a ticket itself, and it computes nothing from the moves: the delivery facts carry them as recorded.

Read [Before you start](index.md#before-you-start) for names and the database session.

## 1. Map the board's columns

Add `gates:` to the board under the chart's `config:` value. In Vikunja, list Kanban bucket titles. In ClickUp, list the List's status names.

```yaml
config:
  trackers:
    vikunja:
      projects:
        - name: "App"
          default: app
          gates:
            development: ["Doing"]
            test: ["In test"]
            acceptance: ["Acceptance", "UAT"]
            done: ["Done"]
    clickup:
      projects:
        - id: "901234"
          repo: webgrip/site
          gates:
            test: ["qa"]
            done: ["complete"]
```

| Gate | Meaning |
| --- | --- |
| `development` | Being built or reworked |
| `test` | A tester checks it |
| `acceptance` | The client or product owner accepts it |
| `done` | Delivered |

Gates are ordered as in the table. You may leave a gate out. A column you don't list (a backlog, for example) records a status move without a gate and no gate move. A board without `gates:` records neither.

Names are compared without surrounding space and without case, so `In test` matches `in test`. ploegd refuses to start when:

- a key under `gates:` is not one of the four gates,
- a name is empty or has surrounding space,
- one name is listed under two gates,
- `gates:` lists nothing,
- the same board is listed twice (for two teams) with different `gates:`.

A board named by `name:` is resolved to its id at startup, like routing. ploegd logs `gate map loaded` for each board.

## 2. Check that Ploeg hears about moves

Ploeg learns moves from the webhooks it already receives:

- **Vikunja:** the project webhook Ploeg registers sends `task.updated`. `/readyz` lists a project whose webhook is missing.
- **ClickUp:** the webhook you registered for `POST /webhooks/tracker/clickup` must include `taskStatusUpdated`. ClickUp needs the tracker token (`tracker.clickup.tokenSecret`) for Ploeg to read the ticket.

Only tickets that Ploeg has a Work Item for are recorded. A ticket's first gate is recorded when it is assigned to Ploeg, or at its next change.

## 3. Give bounce reasons

When someone moves a ticket back, Ploeg looks for the reason in this order:

1. the newest comment that starts with `bounce:<reason>`, written since the ticket entered the gate it left,
2. otherwise a label (Vikunja) or tag (ClickUp) named `bounce:<reason>`, when the ticket has exactly one,
3. otherwise `unknown`.

| Reason | Use it when |
| --- | --- |
| `defect` | The change does not work as asked |
| `requirement` | The ask changed |
| `misunderstood` | The ask was read differently than meant |
| `environment` | The test or acceptance environment was at fault |
| `unknown` | No reason was given; stored as no reason |

Write the comment before you move the ticket, for example `bounce:defect the login form returns 500`. Ploeg reads the reason once, when it records the move, so a comment or label added afterwards is not picked up.

## 4. Verify

1. Move a mapped ticket to another gate and look for `status move recorded` and `gate move recorded` in the ploegd log.
2. List the recorded moves:

   ```sql
   SELECT i.external_id, t.gate, t.status, t.actor, t.reason, t.at
   FROM gate_transitions t JOIN work_items i ON i.id = t.work_item_id
   ORDER BY t.id DESC LIMIT 20;

   SELECT i.external_id, t.status, t.gate, t.observed, t.at
   FROM status_transitions t JOIN work_items i ON i.id = t.work_item_id
   ORDER BY t.id DESC LIMIT 20;
   ```

   A status move's `observed` is true when the tracker gave no time, or an impossible one, and Ploeg used the time it received the webhook.
3. `GET /api/v1/operator/work-items/{id}/facts` lists them as `gateTransitions` and `statusTransitions`, and its roster names whoever moved the ticket as `mover`.

## Not implemented yet

- Ploeg does not poll. A move whose webhook was lost is not recorded; the next delivered change records the gate the ticket is in by then.
- The roster shows tracker usernames for `mover`, and forge logins for `merger` and `reviewer`. One person with two different names appears twice.
- Vikunja keeps no time estimate, so `estimateSeconds` stays null there. ClickUp's `time_estimate` is read.

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| ploegd does not start: `gates: ... already mapped to` | One column is listed under two gates | Keep it under one |
| ploegd does not start: `no tracker project named` | The board's `name:` matches no project | Fix the name or pin `id:` |
| No `gate move recorded` after a move | The column is not listed, the ticket has no Work Item, or the webhook did not arrive | Add the column, assign the ticket to Ploeg, or check `/readyz` and the webhook |
| Log says `tracker status maps to no single gate` | The ticket sits in buckets of two Kanban views that map to different gates | Map one view's buckets only, or make them agree |
| Every bounce reads `unknown` | No `bounce:` comment or label, or the comment came after the move | Comment before moving the ticket |
| Log says `board status read failed` | The tracker token is missing or cannot read the ticket | Configure the tracker token |
| Log says `tracker reports several statuses; status move not recorded` | The ticket sits in unmapped buckets of two Kanban views | Map one view's buckets as gates |
| Log says `retired configuration key ignored` for `statusKinds` | The board still sets the Run card's status kinds | Remove the key; status moves are recorded for every board with `gates:` |
