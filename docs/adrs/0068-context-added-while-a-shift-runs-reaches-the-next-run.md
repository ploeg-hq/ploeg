---
status: accepted
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-04
---

# Context added while a Shift runs reaches the next Run

## Context and Problem Statement

A person steering a Work Item may attach a file while a Run is under way: a log, a specification, a correction. Ploeg's Runs take one prompt at claim (the ACP adapter sends one `session/prompt`), and the TaskSpec is fixed when the Run starts. When does a file attached during a Shift reach an agent?

## Decision Drivers

* A Run's inputs are fixed and recorded at claim, so it can be explained and replayed.
* The person learns, where they attach the file, when it takes effect.
* No harness needs a mid-Run input channel.

## Considered Options

* At the next Run's claim, with the item marked as added while steering
* Into the running Run between turns
* Restart the running Run automatically

## Decision Outcome

Chosen option: "At the next Run's claim, with the item marked as added while steering", because it keeps every Run reproducible and works on every harness today.

* An item's phase is `while_steering` when work on the Work Item has started at upload (a Run of the open Shift, or a pre-Shift Run, has started), else `before_start`. An open Shift alone does not count, because Shifts open at ingest with Runs still pending.
* A claim includes every item added before it. The running Run is unchanged; the next Run of the Shift (the next Round, a fix round or a retry) gets the item, and if the Shift has none left, the next Shift does.
* The prompt says items added while steering are newer than the Work Item description and record what the person learned or wants since.
* "Apply now" stops the running Run and retries it with every item attached so far. It is never automatic: the person sees what the running Run has spent and confirms. It is a follow-up to build, not part of the proof of concept.

### Consequences

* Good, because each Run's context is a fixed, recorded set.
* Good, because steering with files needs no harness change.
* Bad, because new context waits for the next Run; a person who needs it now must stop and retry the running Run.

### Confirmation

`TestContextBundlesEndToEnd` in `pkg/httpapi/context_e2e_test.go`, in the existing `go test ./...` CI step: an item uploaded while the first Run is running is absent from that Run's `TaskSpec.context` and prompt, and present in the next Round's Run, marked `while_steering`.

## Pros and Cons of the Options

### Into the running Run between turns

* Good, because the agent could change course immediately.
* Bad, because only ACP harnesses can take a follow-up prompt, the Run's inputs would no longer be fixed, and Ploeg sends one prompt today. Kept as a spike.

### Restart the running Run automatically

* Good, because new context always applies at once.
* Bad, because it throws away the Run's spend without the person deciding to.

## Re-evaluation triggers

* The steering-between-turns spike shows a follow-up prompt works on every harness Ploeg ships.
* People routinely stop and retry Runs after attaching context, which would justify Apply now or live delivery.

## More Information

* 2026-10-04 — accepted by the owner, who chose "steering input to the open Shift" over "a draft Brief Revision for the next Shift", and asked for Apply now with confirmation.
* Evidence: [context bundles proof of concept](../research/2026-10-04-context-bundles-poc.md).
* Brief Revisions (Unfold VIK-1835) fix what a Shift was authorized to do. Context added while steering is a steering input to the Shift, like an operator message, and does not change the Shift's revision; it can be promoted into the next draft revision when the Shift ends.

* Unfold system ADR-0021; Vloer ADR-0038 shows the timing where the file is attached.
* Vloer `docs/ploeg-front-end.md` options S1 (between Runs, chosen there too) and S2 (between turns).
