---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A message to an attended Run reaches it at the next prompt turn, and context items still wait for the next Run

## Context and Problem Statement

[ADR-0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md) (accepted) decided that context added while a Shift runs reaches the next Run, and rejected delivery "into the running Run between turns" because a Run's inputs are fixed at claim and no Harness needs a mid-Run input channel. The owner decided on 2026-10-13 that sessions executed by `ploeg-worker` stay interactive, including messages to a running Run ([ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)). That is the option 0068 rejected. For which Runs and which inputs does 0068 still hold?

## Decision Drivers

* The owner kept interactivity on 2026-10-13; a person must be able to steer a running attended Run.
* A Run stays explainable and replayable from what was recorded.
* Tracker work and unattended Runs keep every guarantee they have today.
* Only ACP Harnesses can take a further prompt turn today.

## Considered Options

* Narrow 0068: a message to an attended Run on a Harness that supports interaction reaches it at the next prompt turn; context items, and messages to any other Run, keep 0068's rule
* Supersede 0068: every steering input, context items included, may reach a running Run
* Keep 0068 unchanged and send messages only to the next Run

## Decision Outcome

Chosen option: "narrow 0068 for messages to attended Runs", because it gives the person live steering where the owner asked for it and changes nothing for any other Run or input.

* A `message` command to an attended Run ([ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)) whose Harness supports interaction reaches the agent as a further ACP prompt turn when the current turn ends, or interrupts the turn when the person asks.
* An attended Run's recorded inputs are its claim plus its interaction log; every delivered message is stored with its actor and sequence, so the Run can still be explained and replayed.
* A message the worker did not acknowledge before the Run ended becomes an operator note and reaches the next Run, which is 0068's rule.
* Context items (files) attached while a Shift runs keep 0068's rule for every Run, attended or not. A message to an unattended Run, or to an attended Run whose Harness has no interaction, is an operator note for the next Run, as today.

0068 is narrowed, not replaced: its decision stays in force for every input except messages to interactive attended Runs, so `supersedes` is `none`, as for [ADR-0080](0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)'s narrowings.

### Consequences

* Good, because a person steers a running attended Run without stopping and retrying it.
* Good, because unattended Runs, tracker work and context items keep fixed, recorded inputs.
* Bad, because two rules now say what reaches a running Run: messages to interactive attended Runs do, files do not.
* Bad, because replaying an attended Run needs its interaction log as well as its claim.

### Confirmation

* `go test ./pkg/harness/adapters/acp/` sends a message at a turn boundary and an interrupting message to a scripted ACP agent and checks each arrives as a further prompt turn.
* `TestContextBundlesEndToEnd` in `pkg/httpapi/context_e2e_test.go` keeps passing unchanged: an item uploaded while an attended Run runs is still absent from that Run and present in the next.
* `go test ./pkg/store/ ./pkg/httpapi/` checks that an unacknowledged message is carried over as an operator note and that a message to an unattended Run is a note for the next Run.

## Pros and Cons of the Options

### Supersede 0068 for every input

* Good, because one rule covers files and messages.
* Bad, because every context item would become eligible for mid-Run delivery on every Run, which nothing asks for and no non-ACP Harness supports.

### Keep 0068 unchanged

* Good, because every Run's inputs stay fixed at claim.
* Bad, because a person cannot steer a running Run, which the owner rejected on 2026-10-13.

## Re-evaluation triggers

* People attach files to attended Runs and then send a message asking the agent to read them, more than once a week, which would argue for delivering context items live too.
* A Harness other than ACP gains a supported mid-Run input, so the "interactive Harness" condition needs restating.
* ADR-0100 is rejected, which removes the channel this record narrows 0068 for.

## More Information

* OpenSpec change `interactive-runs`, design Decision 11 and the resolved Open Question 8.
* Narrows [ADR-0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md) (accepted); a dated note is added to it when this record is accepted. 0068's own trigger ("steering between turns works on every shipped harness") has not fired; the owner's decision, not the spike, is the reason.
* 2026-10-13 — The owner decided to keep interactivity for sessions `ploeg-worker` executes, which implies messages reach a running attended Run. Proposed.
