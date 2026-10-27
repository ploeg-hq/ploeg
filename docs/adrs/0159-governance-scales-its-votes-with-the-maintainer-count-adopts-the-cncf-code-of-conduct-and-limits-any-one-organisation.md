---
status: proposed
date: 2026-10-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-03
---

# Governance scales its votes with the maintainer count, adopts the CNCF Code of Conduct, and limits any one organisation

## Context and Problem Statement

[Issue #7](https://github.com/ploeg-hq/ploeg/issues/7) asks for governance that covers decisions, conflicts, appointments, inactivity, removal, succession and the relationship to commercial consumers, plus a code of conduct with a working reporting route. Before this record, [GOVERNANCE.md](../../GOVERNANCE.md) covered roles, a decision table, conflicts, joining and inactivity. It said nothing about removal or succession, gave consumers no rules, and decided by "consensus" without saying what consensus means with one or two people. [CODE_OF_CONDUCT.md](../../CODE_OF_CONDUCT.md) named the sole maintainer as the only contact and said an independent route "is not yet available".

Ploeg has one maintainer, who also maintains Unfold, a product built on Ploeg. CNCF's Maintainer Council template asks for votes counted against every maintainer, two-thirds for removal and for governance changes, and conduct reports about a maintainer taken to the CNCF Code of Conduct Committee. A rule written for five people either cannot act with one or two, or gives one person a veto over their own removal. Which rules does Ploeg adopt so that they decide something at one, two and three or more maintainers, and give a conduct reporter a route around the maintainer?

## Decision Drivers

* Every rule yields an answer at one, two and three or more maintainers; a rule that cannot act is worse than none.
* Governance describes what is true now. A rule that waits for more maintainers says when it starts ([CNCF governance guidance](https://contribute.cncf.io/projects/best-practices/governance/)).
* Nobody judges their own case: a removal or a conduct report about a maintainer is not decided by that maintainer.
* No organisation, including the one behind a consumer, controls the project once there is more than one.
* No person is named for a role they have not accepted in public.
* It stays close enough to CNCF's templates that a Sandbox reviewer recognises it.

## Considered Options

* **Adapt the CNCF Maintainer Council template, with vote rules stated per maintainer count, the CNCF Code of Conduct, organisation limits, and a continuity table**
* Adopt the CNCF template unchanged
* Keep the existing page and add the missing sections in its style
* An elected steering committee

## Decision Outcome

Chosen option: "**Adapt the CNCF Maintainer Council template**", because it is the model CNCF expects of a project with fewer than twelve maintainers, and stating each threshold at one, two and three or more maintainers turns its rules into ones that act today.

1. **Votes.** Lazy consensus by default, and any maintainer may call a vote. A vote runs seven days in public and counts against every active maintainer. Conflicted maintainers and the subject of a removal are left out of the count. A majority is more than half; two-thirds rounds up. A failed vote changes nothing.
2. **Thresholds.** Adding a maintainer, reinstating one and an ADR without consensus need a majority. Removing a maintainer for cause and changing GOVERNANCE.md, CONTRIBUTOR_LADDER.md or CODE_OF_CONDUCT.md need two-thirds. A governance change stays open at least seven days, as before.
3. **Removal.** The causes are a conduct finding, repeatedly merging without required review, a hidden conflict, a mishandled security report, or misuse of a project account. The maintainer concerned gets the reasons in writing and 14 days to answer. With two maintainers there is no removal by vote: a conduct matter goes to the CNCF committee, and any other dispute leaves the status quo. Any maintainer may suspend a compromised or harmful account at once, and must open the matter within 72 hours.
4. **Inactivity.** The six-month rule stays. It now has a notice and a 30-day answer window, and the move to emeritus records a fact, so no vote is held. A sole maintainer is never moved to emeritus.
5. **Organisations.** Once maintainers come from more than one organisation, a vote on maintainers or governance needs yes votes from at least two organisations. From three maintainers, nobody new is nominated from an organisation that already employs half of them. A change of employer removes nobody. Independents each count as their own organisation.
6. **Consumers.** No private roadmap. No consumer named in code, documentation, contracts or defaults, and no contract only one consumer can use (ADR 0069, ADR 0077, ADR 0158 item 12). Work done for a consumer says so in its pull request. The maintainer's interest in Unfold is a standing declared conflict; GOVERNANCE.md and MAINTAINERS.md describe it without the name, because ADR 0069 keeps consumer names to dated records such as this one. With one maintainer a conflicted decision is still made, with the declaration recorded and seven days open for comment.
7. **Conduct.** Ploeg adopts the CNCF Community Code of Conduct v1.3. A report can go to the maintainers, to `conduct@cncf.io`, or to GitHub Support. A report about a maintainer goes to the CNCF committee, and so do appeals. Ploeg is not a CNCF project and the committee may decline a report or an appeal; the code of conduct says what Ploeg does then, and states the gap.
8. **Ladder.** Three roles, contributor, reviewer and maintainer, each with numeric criteria anyone can check through GitHub search. A reviewer's approval counts as the independent review in their area, including on a maintainer's own changes.
9. **Continuity.** A table of what the project holds, where it lives and what happens if the maintainer disappears. A second maintainer becomes an owner of the organisation within 30 days of joining. The table is checked every January. No successor is named until one accepts, which follows the owner's decision of 2026-10-24 on issue #7.
10. **Affiliation.** MAINTAINERS.md keeps the Name, GitHub ID and Company/Organization columns the Sandbox application requires, and adds role, areas and start date. The current maintainer's affiliation is Independent.

### Consequences

* Good, because a reader can resolve removal, inactivity, a company's nomination, a conduct report about the maintainer and the maintainer's disappearance from the documents alone; GOVERNANCE.md works eight examples.
* Good, because the organisation rules are already written down when a second organisation's maintainer arrives.
* Good, because a conduct reporter no longer has to go through the person they report.
* Bad, because until Ploeg is a CNCF project the independent route is a request the committee may decline, and while there is one maintainer a declined report about that maintainer has no project responder without a conflict. Accepted and stated in CODE_OF_CONDUCT.md; a second maintainer or CNCF acceptance closes it.
* Bad, because with two maintainers a non-conduct dispute between them can deadlock. Accepted: the status quo is safer than letting one of two remove the other.
* Bad, because Ploeg under its own name cannot continue if the sole maintainer disappears. Accepted as open until a co-maintainer is found.
* Neutral, because conduct reports to the maintainers go through GitHub's private reporting form, which is built for security reports: the maintainer chose it on 2026-10-27 over a separate mailbox, and the code of conduct tells reporters to title the report `Conduct:`.

### Confirmation

`go test ./internal/ledger/`, run by `mise run docs-check` and by `go test ./...` in `mise run verify`, checks three things. MAINTAINERS.md's first table starts with the Name, GitHub ID and Company/Organization columns, with a handle and an affiliation in every row. README.md links GOVERNANCE.md, MAINTAINERS.md, CONTRIBUTOR_LADDER.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md and SECURITY.md, and each exists. CODE_OF_CONDUCT.md links the CNCF Code of Conduct and names `conduct@cncf.io`. Each check has a case that must fail and one that must pass. The human rules are confirmed by their public record: nomination issues, vote comments, the `Decisions YYYY-MM` issues and the January continuity check.

## Pros and Cons of the Options

### Adopt the CNCF template unchanged

* Good, because a reviewer recognises every clause.
* Bad, because "a 2/3 vote of the remaining maintainers" lets one of two maintainers remove the other, and with one maintainer most clauses decide nothing.
* Bad, because it assumes mailing lists and meetings Ploeg does not have.

### Keep the existing page and add sections

* Good, because it is the smallest change.
* Bad, because "consensus" stays undefined with one or two people, and the conduct gap stays.

### An elected steering committee

* Good, because it suits a project with many organisations.
* Bad, because an election among one maintainer and no reviewers elects nobody. CNCF's template suggests it for a project where one organisation dominates.

## Re-evaluation triggers

* A second maintainer joins: the continuity steps run, and the two-maintainer rules are tried for real.
* Ploeg files its CNCF Sandbox application, no earlier than 2027-04-03 ([eligibility](../cncf/eligibility.md)), or CNCF accepts it: the CoC committee route becomes binding, and the central recordkeeping and legal-consultation rules apply.
* The CNCF committee declines a Ploeg report or appeal.
* One organisation comes to employ half the maintainers or more.
* A dispute or a decision cannot be resolved from the documented rules.

## More Information

* [CNCF project template: GOVERNANCE-maintainer.md](https://github.com/cncf/project-template/blob/main/GOVERNANCE-maintainer.md), [CONTRIBUTOR_LADDER.md](https://github.com/cncf/project-template/blob/main/CONTRIBUTOR_LADDER.md), [MAINTAINERS.md](https://github.com/cncf/project-template/blob/main/MAINTAINERS.md) and [GOVERNANCE-org-balanced-voting.md](https://github.com/cncf/project-template/blob/main/GOVERNANCE-org-balanced-voting.md), read at `e66ba44f`.
* [CNCF Community Code of Conduct v1.3](https://github.com/cncf/foundation/blob/main/code-of-conduct.md), [incident resolution procedures](https://github.com/cncf/foundation/blob/main/code-of-conduct/coc-incident-resolution-procedures.md) and [jurisdiction policy](https://github.com/cncf/foundation/blob/main/code-of-conduct/coc-committee-jurisdiction-policy.md), read at `f162799b`.
* [CNCF Sandbox application form](https://github.com/cncf/sandbox/blob/main/.github/ISSUE_TEMPLATE/application.yml), read at `8d0a6b9e`: the MAINTAINERS file needs Name, GitHub ID and Company/Organization columns, and organisation diversity is considered but not required.
* [CNCF governance guidance](https://contribute.cncf.io/projects/best-practices/governance/) and the [Maintainer Council template notes](https://contribute.cncf.io/projects/best-practices/governance/templates/governance-maintainer) from TAG Contributor Strategy.
* 2026-10-26: proposed in the pull request that adds it, open for at least seven days under the governance change rule.
