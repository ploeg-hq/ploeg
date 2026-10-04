---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-02
verified_by: "Read apps/ploeg pkg/flow/*.go, pkg/config/{config,flow,resolve}.go, pkg/httpapi/gates.go and pkg/store/{statuses,card_flow}.go; go test ./pkg/flow ./pkg/config ./pkg/provider/... ./pkg/store ./pkg/httpapi. Not checked against a live Vikunja or ClickUp."
---

# Configure status kinds and working hours

**Goal:** a Run card shows how long its ticket spent in every board column, its lead and cycle time, how much of the cycle was real work, and each duration in working hours as well as calendar time ([ADR-0057](../adrs/0057-a-run-cards-flow-figures-come-from-every-recorded-tracker-status-and-a-team-calendar.md)).

**How it works:** on a board that Ploeg watches, every tracker update makes Ploeg read the ticket and store its column when it changed, mapped to a gate or not. When a card is read, Ploeg sorts each column into a **kind**: `active` (someone works on it), `waiting` (it waits for someone), `blocked` (something stops it) or `done`. Flow efficiency is active time divided by active, waiting and blocked time in the cycle. Working seconds count only the team's working hours.

These figures describe the card and the team's process. Waiting and blocked time say how work flows through the team, not how the steward worked. They never change the grade or the rarity, and Ploeg never adds them up per person.

Read [Before you start](index.md#before-you-start) for names and the database session.

## 1. Choose the boards that record columns

A board records its columns when it has `gates:` ([map tracker statuses to delivery gates](map-tracker-statuses-to-gates.md)) or `statusKinds:`. A board with gates needs nothing more. For a board without gates, add an empty `statusKinds: {}` to record its columns with the default kinds.

## 2. Check the default kinds

Without configuration, Ploeg decides a column's kind from its name and its gate, ignoring case, in this order:

| Rule | Kind | Examples |
| --- | --- | --- |
| The name contains "blocked", "on hold", "on-hold" or "impeded" | blocked | Blocked, On hold, Blocked by legal |
| The column is in the `done` gate, or its name is "done", "closed", "complete", "completed", "released" or "resolved" | done | Done, Closed |
| The name contains "ready", "waiting", "awaiting", "pending", "to review", "to test", "to do", "todo", "backlog", "queue" or "icebox", or is "new" or "open" | waiting | Ready for test, Waiting for client, Merge queue, Backlog |
| The column is in the `development` or `test` gate | active | Doing, In test, Code review |
| The column is in no gate and its name contains "in progress", "doing", "progress", "develop", "review", "testing", "in test", "qa", "build" or "working" | active | In progress, QA |
| Anything else, the `acceptance` gate included | waiting | UAT, Refinement |

## 3. Override what the defaults get wrong

List columns under the kind they have on your board. A listed column always takes that kind.

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
            acceptance: ["UAT"]
            done: ["Done"]
          statusKinds:
            active: ["UAT", "Refinement"]   # the client tests actively in UAT
            waiting: ["In test"]            # nobody tests until a tester picks it up
            blocked: ["Parked"]
            done: ["Won't do"]
```

Names compare without surrounding space and without case. ploegd refuses to start when a key is not one of the four kinds, a name is empty or padded, one name is listed under two kinds, or the same board is listed twice with different `statusKinds:`. Kinds are worked out when a card is read, so a change applies to every card, old moves included.

## 4. Set the team's working hours

Working seconds use Monday to Friday, 09:00 to 17:00 in Europe/Amsterdam unless the team sets its own calendar:

```yaml
config:
  teams:
    bronze:
      workingHours:
        timezone: Europe/Amsterdam        # an IANA zone name
        days: [mon, tue, wed, thu]        # a four-day week
        start: "08:30"
        end: "16:30"
        holidays: ["2026-12-25", "2026-12-26", "2027-01-01"]
```

Every field is optional; a left-out field keeps the default. `start` must come before `end`. Holidays are dates in the team's zone, at most 1000. Without holidays, every card lists `holidays` under `flow.notCollected`, because public holidays count as working days.

A daylight saving change falls on a Sunday night in Europe/Amsterdam, outside the default hours. When a team's hours do cross one, the day counts the real elapsed time.

## 5. Verify

1. Move a ticket on a recording board and look for `status move recorded` in the ploegd log. The ticket needs a Work Item.
2. List the recorded moves:

   ```sql
   SELECT i.external_id, t.status, t.gate, t.observed, t.at
   FROM status_transitions t JOIN work_items i ON i.id = t.work_item_id
   ORDER BY t.id DESC LIMIT 20;
   ```

   `observed` is true when the tracker gave no time, or an impossible one, and Ploeg used the time it received the webhook.
3. Open the ticket's Run card: `flow.statuses` lists every column with its kind and time, and `flow.calendar` names the calendar its working seconds used.

## Not implemented yet

- Ploeg does not poll. A move whose webhook was lost is not recorded, and its time counts towards the previous column.
- The first recorded column starts when Ploeg first read the ticket. `flow.statusesSince` says when the record begins.
- Vikunja keeps no time estimate, so `flow.estimateSeconds` stays null there. ClickUp's `time_estimate` is read.

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| ploegd does not start: `statusKinds: ... is already active` | One column is listed under two kinds | Keep it under one |
| ploegd does not start: `teams.<team>.workingHours: timezone` | The zone is not an IANA name | Use a name such as `Europe/Amsterdam` |
| `flow.notCollected` lists `statuses` | The board has neither `gates:` nor `statusKinds:`, or no move arrived yet | Add `statusKinds: {}` or gates, then move the ticket |
| Log says `tracker reports several statuses; status move not recorded` | The ticket sits in unmapped buckets of two Kanban views | Map one view's buckets as gates or list one under `statusKinds:` |
| Efficiency looks too high or too low | A column has the wrong default kind | List it under the right kind |
| Every status entry says `observed: true` | The tracker sends no change time in its webhook | Nothing to fix; the times are when Ploeg received each change |
