---
status: accepted
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Work is handed to Ploeg by assigning one Ploeg user, and a team label picks the team

## Context and Problem Statement

A tracker assignment is both Ploeg's start signal and its routing key: each team owns tracker usernames (`teams.<name>.assignees`), and assigning one of them dispatches the Work Item to that team. So every team needs its own tracker account, created in the customer's identity provider, logged in once and shared on every board it should work on. Adding team `pewter` on 2026-10-13 meant an Authentik user, a Vikunja login and a project share before one Run could start. For a customer with several teams, or one who wants to try a different team on a single ticket, that is too much work. How does a person pick the team without an account per team, while keeping the start signal deliberate?

## Decision Drivers

* A customer creates one tracker account for Ploeg, ever.
* Starting paid work stays a deliberate act. Adding a label is a lighter gesture than assigning, and an accidental label must not spend money.
* Routing stays deterministic: the same ticket goes to the same team on every boot (the rc.13 duplicate-assignee refusal stays).
* No new privilege: whoever can assign on a board can already start work there.

## Considered Options

* Assign the one Ploeg user to start; a team label picks the team
* A team label alone starts the work
* Keep one tracker account per team

## Decision Outcome

Chosen option: "Assign the one Ploeg user to start; a team label picks the team", because it cuts setup to one account without making a label a spend trigger.

Rules:

1. The roster names Ploeg's tracker user once, `trackers.<kind>.dispatchUser` (for example `ploeg`). Assigning it is the start signal, like assigning a team user today.
2. A team may declare `labels`, for example `teams.pewter.labels: [claude]`. Label names are compared case-insensitively and trimmed; one label belongs to exactly one team, and ploegd refuses to start otherwise, as it does for assignees.
3. When the Ploeg user is assigned, ploegd reads the ticket's labels. Exactly one team label picks that team. No team label falls back to the board's `team`, then to `PLOEG_DEFAULT_TEAM`. Two or more team labels make the Work Item `needs_human` with "pick one team label", and nothing runs.
4. The team is fixed when the Shift starts. Changing labels during a Shift does not move it; the next Shift reads them again.
5. Assigning a team's own username still works and wins over labels, so existing boards keep working. Assigning a team username and adding another team's label is `needs_human`, not a guess.
6. A board can opt out with `labelRouting: false`, for boards whose labels mean something else.

### Consequences

* Good, because a new team is a roster entry and a label, with no account, login or share.
* Good, because a person can try a different team on one ticket by changing a label before assigning.
* Good, because conflicting signals stop the work instead of guessing which team pays.
* Bad, because the start signal and the routing are now two fields a person must get right. The operator API answers which team a ticket will go to, and why, so a consumer can show it before the ticket is assigned.
* Bad, because label names become a contract with the roster: renaming a board label silently drops its routing. ploegd logs a warning when a configured team label is absent from a board it watches.

### Confirmation

* `pkg/config` tests: a label on two teams, and a label equal to another team's assignee, are refused at load.
* Provider tests for Vikunja, ClickUp and GitHub Issues: Ploeg user plus one team label routes to that team; no label falls back to the board's team; two team labels and a team username plus another team's label produce `needs_human`; a label change after the Shift starts does not move it.

## Pros and Cons of the Options

### A team label alone starts the work

* Good, because it is one gesture.
* Bad, because labels are added casually, in bulk and by automation, so a label would start paid work by accident. Trackers also do not all send a webhook for label changes.

### Keep one tracker account per team

* Good, because nothing changes.
* Bad, because every team costs an identity, a login and a share per board, which fails the first driver.

## Re-evaluation triggers

* A tracker gains a first-class "agent" or "delegate" field that is a better start signal than assignment.
* People start most work through an operator consumer instead of the tracker.

## More Information

* 2026-10-17 — The owner accepted this record as written; the per-team tracker user `copper` is lent to team `pewter` until it is built.

* Related: [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md) (a label already selects the repository), [ADR-0094](0094-a-person-connects-a-teams-claude-login-from-unfold-and-the-teams-worker-signs-in.md).
