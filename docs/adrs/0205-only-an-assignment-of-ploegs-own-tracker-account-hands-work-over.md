---
status: accepted
date: 2026-11-20
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-20
---

# Only an assignment of Ploeg's own tracker account hands work over

## Context and Problem Statement

[ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md) makes assigning Ploeg's tracker user the start signal, so that starting paid work stays a deliberate act. The ClickUp adapter did not hold to that. With `trackers.clickup.agents` unset, every assignee dispatched: a person assigning themselves, or a colleague, on a routed List started a Shift. A runtime connection with `handOverAgent: true` ([ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) section 9) added its own account as an agent but did not stop anyone else's assignment, so binding a real team's List ([ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md)) turned every routine assignment into spend. The only guard was a list of names a deployment had to remember to configure. How does Ploeg know which assignment is a hand-over, with no configuration a deployment can forget?

## Decision Drivers

* Starting paid work stays a deliberate act (ADR-0095).
* Safe by default: a deployment that configures nothing must not dispatch on people.
* As little configuration as possible. Ploeg already holds the credentials that say who it is.
* Existing boards that assign a Team's own account (`teams.<team>.assignees`) keep working.

## Considered Options

* Ploeg's own accounts only, learned from the credentials it holds
* Require `trackers.clickup.agents` and refuse to start without it
* Keep the default of every assignee and document `agents` more loudly

## Decision Outcome

Chosen option: "Ploeg's own accounts only, learned from the credentials it holds", because it closes the hole without asking anyone for configuration.

Rules, for the ClickUp adapter:

1. An assignment or removal hands work over only when the member is one of Ploeg's own accounts:
   * the account the static token `PLOEG_CLICKUP_TOKEN` belongs to, by user id, read once from `GET /user` and cached; a failed read is retried on the next assignment and counts as no match meanwhile;
   * a Team's own account under `teams.<team>.assignees` or `PLOEG_TEAM_MAP`, by username or email (ADR-0095 rule 5);
   * a name in `trackers.clickup.agents`, which stays as an optional extra;
   * a hand-over connection's account on a List bound through that connection, as ADR-0195 section 9 already defines.
2. Any other member's assignment or removal is ignored, whether or not `agents` is set. "Empty dispatches on every assignee" is gone.
3. Which Team runs is unchanged: the List's pin, else the Team whose account was assigned, else `PLOEG_DEFAULT_TEAM`.

### Consequences

* Good, because a person assigning themselves or a colleague never starts paid work, on any List, with or without configuration.
* Good, because a deployment with a static ClickUp token needs no agent configuration: the token's account is the agent.
* Bad, because a board that relied on any assignee dispatching stops dispatching until a person assigns Ploeg's account instead. That is the behaviour ADR-0095 asked for; the release notes say so.
* Bad, because the first assignment after start costs one `GET /user` call. It is cached for the process's life.
* Neutral, because `handOverAgent` stays a per-connection opt-in here. Making a tracker connection's account always its hand-over agent, and dropping the option, is a separate decision.

### Confirmation

* `go test ./pkg/provider/clickup/` runs `TestWithoutAgentsAPersonAssigningThemselvesStartsNothing`, `TestTheTokensOwnAccountHandsWorkOverWithoutConfiguration` (the token's account dispatches and is read once) and `TestAnUnreadOwnAccountIsRetriedAndIgnoresTheAssignmentMeanwhile`; all three fail on the code before this record.
* `go test ./pkg/provider/clickup/` keeps `TestAgentMembersDecideWhichAssignmentsCount` and the hand-over tests green.
* `go test ./internal/ledger/` keeps this record well-formed.

## Pros and Cons of the Options

### Require `trackers.clickup.agents` and refuse to start without it

* Good, because the hand-over set is explicit in reviewed configuration.
* Bad, because it adds a required setting that only repeats what the token already tells Ploeg, and a runtime-only deployment would need it too.

### Keep the default of every assignee and document `agents` more loudly

* Good, because nothing changes for existing boards.
* Bad, because the safe behaviour depends on someone reading the documentation, and the failure costs money on a customer's board.

## Re-evaluation triggers

* Another tracker adapter that dispatches on assignment (Vikunja does today) is brought under the same rule.
* A deployment needs a person's own assignment to start work on purpose.
* ClickUp changes `GET /user` or the member shape in assignee history items.
* `handOverAgent` is made implicit for every tracker connection.

## More Information

* 2026-11-20 — Found while binding a real team's ClickUp List: with no static agents, any assignee on the bound List would have started a Shift. The owner decided that only work assigned to Ploeg is picked up, without new configuration.
* Related: [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md) (hand-over by assignment), [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) (runtime bindings), [ADR-0195](0195-a-proven-operator-makes-and-manages-a-tracker-or-forge-connection-through-a-flow-ploeg-starts-and-the-consumer-only-relays-codes.md) (hand-over connections).
