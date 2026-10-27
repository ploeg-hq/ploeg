# Contributor ladder

Ploeg has three roles: contributor, reviewer and maintainer. Each step up asks for work the project can see on GitHub, so anyone can check whether a nominee meets the criteria. [GOVERNANCE.md](GOVERNANCE.md) says what each role decides; this page says how to get there.

## Terms used below

- A **non-trivial pull request** changes behaviour, tests or documentation content. A typo fix, formatting, a link fix, a regenerated file or a dependency bump does not count.
- A **substantive review** is a GitHub review on someone else's pull request that either comments on the change itself or approves it after checking it.
- An **area** is a set of paths, for example `pkg/worker/`, `ops/helm/ploeg/` or `docs/`.
- **Months** count from the date of the first merged pull request or review that the criterion uses.

Evidence is a set of GitHub search links that anyone can open, for example `is:pr author:<handle> is:merged repo:ploeg-hq/ploeg` and `is:pr reviewed-by:<handle> repo:ploeg-hq/ploeg`.

## Contributor

Anyone who opens an issue, a pull request, a review or a Discussion post. Nobody grants this role.

- **Responsibilities:** follow the [code of conduct](CODE_OF_CONDUCT.md) and [CONTRIBUTING.md](CONTRIBUTING.md), including the DCO sign-off.
- **Privileges:** propose anything, review anything, and comment on every public decision, including the [monthly decision cycle](GOVERNANCE.md#the-monthly-decision-cycle) and nominations.

## Reviewer

A reviewer is responsible for reviewing changes to one or more areas.

**Requirements**, all of them:

1. Contributing for at least three months.
2. At least five non-trivial pull requests merged, two of them in the area.
3. At least ten substantive reviews on other people's pull requests.
4. Two-factor authentication on their GitHub account.
5. Agreement, in the nomination issue, to the responsibilities below and to publishing their name, GitHub handle and affiliation in [MAINTAINERS.md](MAINTAINERS.md).

**Responsibilities:**

- Give a first response to a pull request in their area within seven days of being asked.
- Help new contributors get useful pull requests into shape.

**Privileges:**

- The Triage role on the repository.
- Their review counts as the independent review on changes in their area, including a maintainer's own changes ([decisions](GOVERNANCE.md#how-decisions-are-made)). Merging stays with a maintainer.
- They may nominate other reviewers.

**How to become one:**

1. Anyone, including the nominee, opens an issue titled `Nominate @<handle> as reviewer for <area>`, with the evidence links.
2. The nominee says in the issue that they accept.
3. The issue stays open for seven days. A maintainer then approves it by lazy consensus. If a maintainer objects, the maintainers vote, and a majority decides.
4. A pull request adds the reviewer to MAINTAINERS.md, and a maintainer grants the Triage role.

## Maintainer

A maintainer is responsible for the whole project.

**Requirements**, all of them:

1. A reviewer for at least three months. While Ploeg has fewer than two maintainers, nobody can have held the reviewer role for long, so a nominee may instead show the reviewer requirements met over at least three months without having held the role.
2. Contributing for at least six months.
3. At least ten non-trivial pull requests merged, and at least twenty substantive reviews, across at least two areas.
4. At least one piece of operating work: running or co-running a [monthly decision cycle](GOVERNANCE.md#the-monthly-decision-cycle); carrying out the gate checks on a release issue ([ADR 0158](docs/adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md)); handling a security report together with a maintainer; or running the restore or attack drill and recording the result.
5. Time to give: at least eight hours a month on Ploeg.
6. Two-factor authentication, and willingness to become an owner of the `ploeg-hq` organisation ([continuity](GOVERNANCE.md#continuity-and-succession)).
7. Their affiliation and any conflicts of interest disclosed in the nomination, and agreement to publish their name, handle and affiliation.
8. Judgment for the good of the project, independent of their employer, clients or friends. The other maintainers assess this from the nominee's public record.

**Responsibilities:** review across the project, mentor reviewers, take part in decisions and the monthly cycle, respond to security and conduct reports, and keep their affiliation in MAINTAINERS.md current within 30 days of a change.

**Privileges:** merge to any area, cut releases, vote, represent Ploeg in public, and, once Ploeg is a CNCF project, speak to CNCF for it.

**How to become one:**

1. Anyone, including the nominee, opens an issue titled `Nominate @<handle> as maintainer`, with the evidence links and the nominee's affiliation.
2. The nominee says in the issue that they accept, and lists any conflicts of interest.
3. The issue stays open for seven days. The maintainers then vote; the [vote rules](GOVERNANCE.md#lazy-consensus-and-votes) and the [limits on any one organisation](GOVERNANCE.md#limits-on-any-one-organisation) apply.
4. A pull request adds the maintainer to MAINTAINERS.md. Access follows within seven days.

## Moving down or out

- **Stepping down:** anyone may leave a role at any time. A maintainer moves to emeritus; a reviewer is taken off the list.
- **Inactivity:** a reviewer with no review for six months is asked in an issue whether they want to stay. After 30 days without an answer, a pull request takes them off the list. Maintainers follow [inactivity in GOVERNANCE.md](GOVERNANCE.md#inactivity).
- **Removal:** a reviewer may be removed for the same causes as a maintainer, by a majority of maintainers. Maintainers follow [removal for cause](GOVERNANCE.md#removal-for-cause).
- **Coming back:** a former reviewer who still meets the requirements may be nominated again. An emeritus maintainer may be reinstated by vote.
