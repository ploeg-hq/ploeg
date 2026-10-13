---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A shadow reader records its verdict and never acts

## Context and Problem Statement

The only way to compare a reviewer model on real work is to clone a Team. A second reading Role in the review Round does not help: [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) ORs every reader's `request_changes` into the review loop (`pkg/shiftengine/reviewloop.go`), so the second reviewer changes the outcome it is meant to measure. Its findings are also posted on the pull request and handed to the writer of the next fix Round ([ADR-0011](0011-the-pull-request-is-the-blackboard.md)), which changes the work.

Cost per accepted pull request per model is the open question ([Unfold research, 2026-10-11](https://forgejo.webgrip.dev/webgrip/unfold/pulls/333)), and bronze's reviewer costs about US$ 0.11 a Run on GLM 5.3 Flash. How can a Team run a second reviewer model on the same pull requests, and record what it would have said, without that changing what happens to the Work Item?

## Decision Drivers

* The measured Shift must run as it would have without the second reviewer: the same fix Rounds, close reason, pull request thread, tracker notes and briefings.
* Both reviewers read the same thing: the same pull request, the same Round, the same briefing, a clean context each.
* Money stays bounded before it is spent ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)), and the second reviewer's spend is accounted per Run like any other.
* Recorded facts are what ploegd was configured with at the time, never derived later from today's plan ([ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md)).
* Off unless a plan asks for it.

## Considered Options

* **A reading Role marked `shadow` in the plan: recorded and served, ignored by every rule that decides what happens next**
* Clone the Team and dispatch the same Work Items to both
* Run the second reviewer after the Shift closes, outside the Shift
* A second reading Role without a shadow flag, accepting that its verdict acts

## Decision Outcome

Chosen option: "**a reading Role marked `shadow` in the plan**", because it reviews the very pull request the acting reviewer reviews, in the same Round, at the cost of one reading Run, and the rule that it never acts is a filter in the places that read verdicts and findings.

What is decided:

* **Plan.** A Role takes `shadow: true` (chart `executor.teams[].plan[].roles[]`, `PLOEG_TEAM_PLANS`, or the configuration file). Validation refuses a shadow that writes or plans, a shadow without its own `cap` above zero, a Round whose readers are all shadows, and a Role that is a shadow in one Round and not in another. A plan without a shadow encodes and hashes exactly as before.
* **Record.** `agent_runs.shadow` (migration 0053, default false, never a writer) is copied from the plan when a Round opens or re-opens, so a later plan change does not relabel a Run.
* **Never acts.** For a shadow Run, ploegd:
  * leaves its verdict out of the review loop: it neither opens a fix Round nor counts as an approval, and is not the last review that `readyForReview` reads;
  * does not close the Shift when it reports `stuck`, does not retry it when it fails, and does not treat it as a missing review;
  * does not post its findings on the pull request and does not brief a later Run with them;
  * does not serve its verdict as the pull request's `agentVerdict`;
  * drops the Work Items and Work Item Notes its report proposes, and records how many it dropped in the outcome's audit event.
* **Same reading.** A shadow Run claims like any reader. It gets the same briefing as the acting reviewer of its Round, and in a fix loop it reviews again in each re-opened review Round.
* **Money.** A shadow Run holds and spends against the Shift pool like any Run, up to its own cap. The usage report on the pull request lists it as `<role> (shadow)` with its cost and without its verdict.
* **Reads.** Every Run shape on the operator API (Run detail, Run list, Work Item detail and facts) carries `shadow`, and the `ploeg-mcp` Run tools show it. `ploeg_shadow_reviews_total{team,role,primary,shadow}` counts finished shadow Runs by the verdict the acting readers of the Round gave, combined as the loop combines them, and the shadow's own. [Metrics and alerts](../ops/alerts.md#reading-shadow-reviews) gives the agreement query and a per-Work Item SQL query with both costs. Comparing findings is left to the reader of both `findings` fields ([ADR-0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md)).

The one way a shadow can still touch the Shift is money: its Holds and spend come out of the same pool, so an undersized pool can refuse a fix Round, or a sibling's claim, that it would otherwise have funded. The plan's owner sizes the pool for it: add the shadow's cap once for every review Round the Shift can run, `cap × (1 + maxFixRounds)`.

### Consequences

* Good, because a reviewer model is compared on the pull requests the Team really produces, with the acting reviewer's verdict next to it, for the cost of one capped reading Run per review Round.
* Good, because turning it off is deleting one Role from the plan, and no Shift changes behaviour while it is on, apart from the pool.
* Good, because the shadow flag is a stored fact on each Run, so a comparison survives later plan changes.
* Bad, because the review Round waits for the slower of the two readers, so a slow shadow delays the fix Round or the close. Accepted: one Round's latency, against a second Shift that would need its own branch and pull request.
* Bad, because the shadow can still change a Shift through the pool when the pool is not sized for it. Accepted and documented: refusing to fund a shadow before the acting Roles would need a reservation order the claim path does not have.
* Bad, because a shadow that fails is not retried, so its Round contributes no evidence. Accepted: a retry delays the Round for data, not for the work.
* Bad, because the worker does not know it is a shadow, so a harness that posts to the forge by itself would still post. No harness in this repository does; a reading Run holds no push credential.

### Confirmation

* `go test ./pkg/plan/` refuses a writing, planning, uncapped, lone or inconsistent shadow and keeps the hash of a plan without one.
* `go test ./pkg/shiftengine/` (`shadow_test.go`) runs a shadow `request_changes` beside an approving reviewer and checks the Shift closes `review_approved` with no fix Round and no shadow comment on the pull request; checks that the reviewer's `request_changes` still opens the fix Round and the re-opened review Round runs the shadow again; that a stuck or failed shadow is neither obeyed nor retried; that its cost counts in the Shift; and that the usage report hides its verdict.
* `go test ./pkg/store/` (`shadow_test.go`) records and serves the flag on Run detail and the Run list, drops created Work Items and notes, keeps the shadow's verdict out of `agentVerdict`, refuses a writing shadow, and counts `ShadowReviews`.
* `go test ./pkg/httpapi/` (`shadow_test.go`) leaves shadow findings out of a later Run's briefing and renders `ploeg_shadow_reviews_total`; the existing operator API tests validate Run shapes against `docs/contracts/operator-api.v1.schema.json`.
* Gate: `mise run verify` and `mise run docs-check`, run in CI by `.forgejo/workflows/on_pull_request.yml`.
* Live: one bronze Shift with a shadow reviewer on another model, its Run listed with `shadow: true`, approved or looped exactly as the reviewer decided.

## Pros and Cons of the Options

### Clone the Team and dispatch the same Work Items to both

* Good, because nothing in the review loop changes.
* Bad, because each clone writes its own branch and pull request, so the two reviewers never read the same change and the comparison mixes writer and reviewer.
* Bad, because it doubles the writer's spend, which is about four fifths of a bronze Work Item's cost.

### Run the second reviewer after the Shift closes

* Good, because it cannot affect the Shift at all, not even through the pool.
* Bad, because it reads only the last commit, not each review Round, and misses the cases where the reviewer asked for changes.
* Bad, because it needs a new kind of Run outside a Shift, with its own budget, like the Ask's allowance ([ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)).

### A second reading Role without a shadow flag

* Good, because it works today.
* Bad, because its verdict and findings act, which is the change the comparison was meant to measure.

## Re-evaluation triggers

* 20 bronze Work Items have a shadow review: the agreement data is enough to choose a reviewer model, and the shadow either becomes the reviewer or is removed.
* A Shift is refused a fix Round or a claim on budget while a shadow Run of it held or spent from the pool.
* A harness gains a way to post to the forge or tracker from a reading Run.
* A consumer needs ploegd to score findings agreement rather than verdict agreement.

## More Information

* Ticket VIK-2033, part of the cost per merged pull request epic VIK-2026.
* Migration `pkg/store/migrations/0053_run_shadow.sql`.
* [Metrics and alerts: reading shadow reviews](../ops/alerts.md#reading-shadow-reviews).
* 2026-10-13: proposed.
