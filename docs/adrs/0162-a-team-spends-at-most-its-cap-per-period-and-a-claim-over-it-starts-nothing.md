---
status: proposed
date: 2026-10-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# A Team spends at most its cap per period, and a claim over it starts nothing

## Context and Problem Statement

Ploeg bounds Shift work per Run (the key's `max_budget`), per Work Item (the Shift pool, [ADR-0012](0012-two-level-budgets-authorized-and-settled.md)), per Role (the Role cap) and by concurrency (`maxRunning`). Nothing bounds a Team's total over a period. Dispatch is open by design to anyone who can assign or label in a configured tracker project (attack tree AT3), so total spend grows with the number of Work Items, not with any figure a person authorized: N Work Items each draw a full Shift pool, and a replayed webhook multiplies it. The monthly allowances cover Asks and Chats only, a consumer's `maxBudgetUsd` is checked per admission, and a LiteLLM team budget bounds a Team only when the installer sets `litellmTeamId` and a budget on that team, which profile P1 does not require. This is finding F-42 ([#352](https://github.com/ploeg-hq/ploeg/issues/352)). What bounds a Team's total spend, where is it checked, and what does the supported profile set it to?

## Decision Drivers

* The bound must hold before money is committed: a Run that cannot be afforded must never mint a key, as ADR-0012 already requires for the Shift pool.
* It must count money already promised, not only money settled: a hold is spend until it is settled or released ([ADR-0093](0093-the-working-state-is-active-and-budget-kept-back-is-a-hold.md)).
* Concurrent claims of one Team must not each see the same headroom.
* A refusal must name the Team, the period and the figures, where an operator reads it.
* Unset must keep today's behaviour in ploegd, so the change breaks no deployment that does not use the chart's default.
* The per-Run, per-Shift and per-Role budgets stay as they are.

## Considered Options

* **A per-Team cap per calendar period, checked by ploegd at claim against settled plus held spend**
* Require a LiteLLM team budget on every Team (`litellmTeamId` plus a budget set in LiteLLM)
* A rolling window (the last 24 hours) instead of a calendar period
* Clamp the Run's authorization to the headroom left instead of refusing it
* An alert on spend per Team, without a gate

## Decision Outcome

Chosen option: "**A per-Team cap per calendar period, checked by ploegd at claim against settled plus held spend**", because ploegd already owns every figure it needs (holds and settlements, ADR-0012 and [ADR-0155](0155-an-operator-settles-an-account-the-gateway-cannot-answer-for-from-evidence-and-never-below-what-ploeg-observed.md)) and already serialises claims, so the bound holds before any key is minted and in every deployment, whatever the gateway is configured to do.

1. A Team's cap is `spendCap: {usd, period}` with `period` one of `day`, `week` (from Monday) or `month`, each a calendar period in UTC. `usd` of 0 or no `spendCap` is no cap. ploegd reads it from `PLOEG_TEAM_SPEND_CAPS` and from `teams.<name>.spendCap` in the `PLOEG_CONFIG` file, which wins for that Team. A negative or non-finite `usd`, or an unknown period, stops ploegd at boot.
2. Every claim path checks it: the Shift-scoped claim, the role-less claim of a uniform-dispatch Shift, a client-carried claim and the pre-Shift claim. Inside the claim transaction, after the Run's authorization is computed, ploegd takes a per-Team advisory lock and refuses the claim when the Team's period spend plus the Run's authorization would exceed `usd`. The pre-Shift claim authorizes no budget, so it counts the 0.05 USD floor below which ADR-0012 calls a Run not worth starting.
3. Period spend is the spend settled since the period started plus every hold the Team's Runs keep now. Settled spend is the sum of managed reconciliation deltas (`llm.reconciled`, operator settlements included) and the harness-reported cost of finished Runs without a managed account, the same figures the operator summary's `settledUsd` reads. Held spend is the hold of every running Run and of every account not yet reconciled, as the summary's `heldUsd`. A hold is counted whenever its Run started, so a new period starts with the holds still open. Ask and chat Runs belong to no Work Item and stay with their own allowances.
4. A refused claim starts nothing: no Run moves to running, no key is minted and no attempt is burned. The worker gets `204` and exits 0; a client-carried claim gets `409 spend_cap_reached` with the reason. The Run stays pending, so it starts as soon as a hold is settled or released or the next period begins, with no person needed. ploegd audits `team.spend_capped` on the Work Item once per Run and period, with the Team, the period, its start, the cap, the settled and held spend, the requested authorization and a reason sentence that the operator events and Work Item detail show.
5. The Teams read shows each capped Team's `spendCap` (`usd`, `period`, `periodStart`, `periodEnd`, `settledUsd`, `heldUsd`, `remainingUsd`), null for a Team without one, and `/metrics` exports `ploeg_team_spend_cap_usd{team,period}` and `ploeg_team_period_spend_usd{team,period,kind}`.
6. The chart gives every executor team `executor.spendCap: {usd: 20, period: day}` unless the team sets `executor.teams[].spendCap` or the config file sets one, and renders the result into `PLOEG_TEAM_SPEND_CAPS`. Under `supportProfile: P1` ([ADR-0154](0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md)) the render fails for an executor team that resolves no cap. The changed default is recorded against `v0.2.0-rc.83` in [breaking-changes.yaml](../contracts/breaking-changes.yaml).

The P1 default is 20 USD per UTC day per Team. The chart's own examples give a Shift a pool of 1 to 2 USD, so a day's cap funds ten to twenty Shifts, more than one Team's reviewers merge in a day today. It bounds the worst case a dispatch flood or a webhook replay can cause to 20 USD per Team per day, about 600 USD a month, where today the bound is the number of Work Items anyone can create. A day is short enough that a Team that hits it waits hours, not weeks, and it matches the deployment-wide `spendSpike` alert's 20 USD per hour, which fires before one flooded Team has spent its day. An installer with more traffic raises it per Team; one that wants no cap sets `usd: 0` and `supportProfile: unsupported`.

### Consequences

* Good, because a Team's spend has a figure a person chose, checked before a key is minted, in every deployment, with or without a LiteLLM team.
* Good, because headroom comes back on its own: a settlement, a released hold or a new period lets the pending Run start without a person.
* Good, because the refusal is visible where operators look: the events read, the Work Item detail, the Teams read and a metric.
* Bad, because counting holds is conservative: a Run that will settle far below its hold blocks headroom until it settles. Accepted, because a hold is money the gateway may still spend; the managed settlement sweeps release it within minutes of the Run ending.
* Bad, because a capped Team's pending Runs still look like work to KEDA, so workers keep starting, finding no claim and exiting 0 until headroom returns. The pods cost cluster time, not model spend. The scaler query stays the claim's mirror; teaching it the cap would put spend arithmetic into the scaler.
* Bad, because deployments rendered from the chart now stop a Team at 20 USD per day unless they raise it, and a P1 render with `usd: 0` fails. The break is recorded and named in the render message.
* Neutral: the cap counts across Shifts and Roles, not per person; a per-person cap remains a later decision if Teams are shared widely.

### Confirmation

* `go test ./pkg/store/ -run 'TeamSpendCap|TeamWithoutSpendCap|LegacyClaimRefused'` runs against embedded PostgreSQL: a Team capped at 1.00 USD per day starts a 0.60 USD Run and is refused the second, which stays pending with 0 attempts and leaves one `team.spend_capped` audit row naming the team, `day` and the period start, and a repeated claim adds none; two 0.50 USD Runs start under the same cap and a third is refused; a Team without a cap claims three 0.60 USD Runs; settling the first Run at 0.30 USD, or releasing it with no cost, lets the second start; a managed account holds its authorization until it is reconciled and then counts its reconciled spend; a Run refused over a day's settled spend starts once the clock is in the next day; six concurrent 0.30 USD claims under 1.00 USD start exactly three; and the pre-Shift claim is refused at the cap and admitted after settlement. With the check disabled seven of those tests fail and the uncapped Team's test passes; with the check always refusing the same seven fail and the uncapped test still passes.
* `go test ./pkg/httpapi/ -run 'SpendCap'` validates the Teams read against [operator-api.v1.schema.json](../contracts/operator-api.v1.schema.json) with a capped and an uncapped Team, and requires both metrics on `/metrics`.
* `go test ./pkg/spendcap/ ./pkg/config/` checks the UTC periods, the refused caps and the file overlay.
* `scripts/helm-golden.sh check` requires a P1 render to fail with `resolves no spend cap` for `executor.spendCap.usd=0`, a team's own `usd: 0`, `config.teams.<name>.spendCap.usd=0` and `executor.spendCap=null`, and to render a team's own cap and a cap set only in `config`; it also requires a team's own cap to reach `PLOEG_TEAM_SPEND_CAPS`.

## Pros and Cons of the Options

### Require a LiteLLM team budget on every Team

* Good, because the gateway enforces it per call, also against spend a Run makes after its key was minted.
* Bad, because the chart cannot see a budget configured inside LiteLLM, so P1 could only ask for `litellmTeamId`, not prove a bound.
* Bad, because the gateway refuses a call mid-Run, after a Run started and a key was minted, where ADR-0012 wants the gate before dispatch. It stays a useful second layer for installers who set it.

### A rolling window

* Good, because there is no cliff at midnight.
* Bad, because headroom returns spend by spend at unpredictable times, which an operator cannot read from one figure, and the reset time stops being a date a person can state.

### Clamp the authorization to the headroom left

* Good, because one more Run starts, smaller.
* Bad, because a Run minted for a fraction of its Role cap often dies without finishing, costing an attempt and a pod, which is why ADR-0012 refuses below a floor; and the Team's last Runs would be the worst-funded.

### An alert without a gate

* Good, because nothing is refused.
* Bad, because an alert reaches a person after the money is spent, and F-42 is about spend nobody authorized.

## Re-evaluation triggers

* A Team hits its cap on ordinary traffic more than twice a month, which says the default is too low.
* The KEDA scaler's empty-handed pods from a capped Team cost noticeable cluster time.
* LiteLLM exposes a team budget Ploeg can read and verify, which would let P1 accept it as the bound.
* Teams are shared by enough people that a per-person or per-consumer cap is asked for.
* A settlement or hold is found counted twice or not at all in a Team's period spend.

## More Information

* Finding: threat model F-42; issue [#352](https://github.com/ploeg-hq/ploeg/issues/352). Related: AT3, F-05 ([#294](https://github.com/ploeg-hq/ploeg/issues/294)), F-09.
* Code: `pkg/spendcap`, `pkg/store/team_spend_cap.go`, the claim paths in `pkg/store/shift.go`, `pkg/store/store.go` and `pkg/store/client_carried.go`, `pkg/httpapi/metrics.go`, `ops/helm/ploeg/templates/_helpers.tpl` (`ploeg.teamSpendCap`) and `_support_profile.tpl`.
* 2026-10-27: proposed.
