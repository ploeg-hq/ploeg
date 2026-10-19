---
status: proposed
date: 2026-10-20
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Shift ends when its pull request merges or closes, or when its writer changed nothing

## Context and Problem Statement

Two Shifts on 2026-10-20 parked their Work Items at `needs_human` with a reason that was not true.

On Work Item 238 the builder opened homelab-cluster pull request #817, and the review Round opened. Its reviewer sandboxes failed on infrastructure twice, and the Round reopened each time. Meanwhile the owner merged #817 and Forgejo deleted the branch. The next reviewer found the branch missing and reported stuck: "branch agent/vik-2238 is absent on the forge although a writing Round ran before this reader". The Shift closed `run stuck: reviewer round 2`. The review watch turns a merge into `done`, but it only read Work Items in `awaiting_review`, and a Work Item whose Shift is still open is not in that state. Work Item 232 (#784) was merged after its Shift closed and reached `done` as designed. Ploeg charges for merged code, so a merge must always reach `done`.

On Work Item 188 the builder finished `no_change_needed`: it rightly refused to commit secrets and pushed nothing. The plan still opened its review Round. Both readers reported the same "branch is absent" stuck, and the writer's own explanation was lost.

Which forge facts end a Shift before its plan runs out, and what does a writer that changed nothing end it with?

## Decision Drivers

* A merged pull request is accepted work. Recording it is the billing fact, so it must not depend on when the merge happened during the Shift.
* A close reason says what happened. "Branch absent" describes a symptom and hides the cause.
* No Run should start for a branch that does not exist: it costs a sandbox and it reports a false stuck.
* A writer that finds nothing to do may be wrong. A person should confirm it before the tracker task is closed.

## Considered Options

* The review watch also reads the pull request of every live Shift, and the shift engine stops after a writing Round that delivered nothing
* The reader looks up the pull request's state on the forge when its branch is missing, and reports `no_change_needed` for a merged one
* Only the review watch changes; a writer that changed nothing still opens the review Round

## Decision Outcome

Chosen option: "The review watch also reads the pull request of every live Shift, and the shift engine stops after a writing Round that delivered nothing", because ploegd already holds forge credentials and the merge-to-`done` path, and the worker can only see open pull requests.

1. **A pull request merged during a Shift.** The review watch reads the pull request of every live Shift that one of its writing Runs opened or updated, of a Shift restarted past its writer ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md)), and of a Work Item that reads someone else's pull request ([ADR-0109](0109-a-pull-request-someone-else-opened-is-read-by-a-team-on-the-pull-request.md)). It reads on the forge's merge webhook, on every reconcile, and when a Round completes, before the shift engine applies any other rule. A merged pull request closes the Shift `pull_request_merged` and cancels its pending Runs. The Work Item goes to `done` with the same audit record as a merge in review (`ploegd:review`, `work_item.done`, "pull request merged"), the forge facts with the merge commit are recorded, and the tracker gets the same merge comment.
2. **A pull request closed without merging during a Shift.** The same paths close the Shift `pull_request_closed_unmerged`. The Work Item goes to `needs_human` with "pull request closed without merging".
3. **A forge Ploeg cannot read** leaves the Shift to its other rules, as before.
4. **A writer that changed nothing.** When the writing Run of the current Round reports `no_change_needed`, and no writing Run of the Shift, or of a Shift it was restarted past, put a pull request on the forge, no reading Round opens. The Shift closes `writer_found_no_change`. The Work Item goes to `needs_human`, and the message quotes the writer's summary. A fix-round writer that pushed nothing over an existing pull request is not affected: that pull request is still reviewed. A Team on a synthesized single-writer plan keeps settling by the Run's outcome.

### Consequences

* Good, because a merge reaches `done` whether it happens before, during or after the Shift.
* Good, because a merge or close cancels readers that have not started yet, so they cost nothing and report nothing false.
* Good, because the writer's own explanation reaches the person who has to decide.
* Bad, because every completed Round of a Shift with a pull request costs one more forge read. Rounds complete a few times per Shift, so the reads stay few.
* Bad, because a reader that already started when the branch was deleted still records its stuck report. The Shift's close reason and the Work Item's state no longer depend on it.
* Bad, because a writer that is right that nothing needs doing still waits for a person. The trigger below reopens that.

### Confirmation

`go test ./pkg/shiftengine/` in CI (`mise run verify`), against a Forgejo faked with `net/http/httptest`:

* `TestAPullRequestMergedWhileItsReadersRanSettlesTheItemDoneInsteadOfParkingTheirStuck`, `TestAMergeWebhookDuringTheReviewRoundClosesTheShiftBeforeAnyReaderRuns`, `TestTheReconcileSweepClosesALiveShiftWhosePullRequestMergedWithoutAWebhook`, `TestAPullRequestClosedUnmergedDuringTheShiftSaysSoInsteadOfBranchAbsent` and `TestAnUnreadableForgeLeavesTheShiftToItsOwnRules` in `pkg/shiftengine/live_pull_request_test.go`;
* `TestAWriterThatChangedNothingEndsTheShiftWithItsOwnReasonInsteadOfOpeningAReviewOfNoBranch` and the fix-round and restart cases in `pkg/shiftengine/writer_no_change_test.go`.

## Pros and Cons of the Options

### The reader looks up the pull request's state when its branch is missing

* Good, because the reader would never report a false stuck.
* Bad, because the worker finds only open pull requests on three forges, and teaching it merged and closed ones duplicates what ploegd already reads.
* Bad, because the readers still start, and the merge is still recorded only when a reader happens to run.

### Only the review watch changes

* Good, because it is the smaller change.
* Bad, because a writer that changed nothing still pays for readers that report a missing branch, and the writer's reason is still lost.

## Re-evaluation triggers

* Twenty Shifts have closed `writer_found_no_change` and a person agreed with the writer in at least eighteen of them. Closing them as `done` is then the better default.
* The worker gains a forge read for merged and closed pull requests.
* A forge offers no merge webhook and no pull request read, so a merge during a Shift cannot be seen.

## More Information

* Work Items 238 (homelab-cluster #817), 188 (erfbeeld) and 232 (#784), 2026-10-20.
* [ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md): delivery is what the forge observed.
* [ADR-0043](0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md): failed readers are retried.
