# Governance

Ploeg has one maintainer today. This page says how decisions are made now and how that changes as people join. A rule that only starts once there are two or three maintainers says so; every other rule is in force today. The [scope and stewardship charter](docs/cncf/charter.md) says what the project covers, and [ADR 0159](docs/adrs/0159-governance-scales-its-votes-with-the-maintainer-count-adopts-the-cncf-code-of-conduct-and-limits-any-one-organisation.md) records why the rules have this shape.

- [Values](#values)
- [Roles](#roles)
- [How decisions are made](#how-decisions-are-made)
- [Lazy consensus and votes](#lazy-consensus-and-votes)
- [Conflicts of interest](#conflicts-of-interest)
- [Companies, commercial users and consumers](#companies-commercial-users-and-consumers)
- [Maintainer lifecycle](#maintainer-lifecycle)
- [Continuity and succession](#continuity-and-succession)
- [The monthly decision cycle](#the-monthly-decision-cycle)
- [Conduct and security](#conduct-and-security)
- [Changing this page](#changing-this-page)
- [Worked examples](#worked-examples)
- [What is not decided here](#what-is-not-decided-here)

## Values

- **In the open.** Decisions and their reasons are written down in public issues, pull requests, Discussions and ADRs. Only security reports and conduct reports are handled in private.
- **People, not employers.** Everyone takes part as an individual. A role belongs to the person who earned it and does not pass to their employer or client.
- **No single organisation in control.** Maintainers decide on the merits. Once maintainers come from more than one organisation, the rules below stop any one of them deciding alone.
- **A path in.** Anyone can move up the [contributor ladder](CONTRIBUTOR_LADDER.md) through work the project can see.

## Roles

| Role | Who | What they can do |
| --- | --- | --- |
| Contributor | Anyone who opens an issue, a pull request, a review or a Discussion post | Propose anything, review anything, take part in every public decision |
| Reviewer | Listed in [MAINTAINERS.md](MAINTAINERS.md) with an area | Their review counts as the independent review for changes in their area |
| Maintainer | Listed in [MAINTAINERS.md](MAINTAINERS.md) | Merge, release, vote, and hold the project's accounts |
| Emeritus maintainer | Listed under Emeritus in [MAINTAINERS.md](MAINTAINERS.md) | Be consulted; no merge rights and no vote |

The [contributor ladder](CONTRIBUTOR_LADDER.md) gives the criteria for each role and how to apply.

The maintainers together are the project's governing body. They are also its security response team ([SECURITY.md](SECURITY.md)) and its code of conduct responders ([CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)), and they hold the name and mark ([trademark policy](docs/brand/TRADEMARK.md)) and the accounts listed under [continuity](#continuity-and-succession). They review who does security response once a year, in the January decision cycle.

## How decisions are made

| Decision | Where | Who decides |
| --- | --- | --- |
| A code or documentation change | A pull request against `development` | A maintainer who did not author it approves and merges it. While there is one maintainer, that maintainer merges their own changes after CI passes. Once a reviewer is listed for an area, a maintainer requests that reviewer's review on their own changes to it. |
| An architectural decision | A new record in [docs/adrs/](docs/adrs/README.md), proposed in a pull request | Maintainers by lazy consensus; without it, a vote at majority. The record is append-only: a later record supersedes it. |
| Priority and roadmap | A proposal in [GitHub Discussions](https://github.com/ploeg-hq/ploeg/discussions), decided in the [monthly decision cycle](#the-monthly-decision-cycle) and tracked in [GitHub Issues](https://github.com/ploeg-hq/ploeg/issues) and its milestones | Maintainers. Anyone may propose or comment. |
| A stable release | A public release issue listing the promotion gates, then a fast-forward of `main` to the soaked candidate ([ADR 0158](docs/adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md)) | A maintainer. Once there are two, another maintainer approves the release issue first. |
| Adding a reviewer | A public nomination issue, then a pull request to MAINTAINERS.md ([ladder](CONTRIBUTOR_LADDER.md#reviewer)) | A maintainer, by lazy consensus over seven days |
| Adding a maintainer | A public nomination issue, then a pull request to MAINTAINERS.md ([ladder](CONTRIBUTOR_LADDER.md#maintainer)) | A vote at majority, with the [organisation rules](#limits-on-any-one-organisation) |
| Moving a maintainer to emeritus | A pull request to MAINTAINERS.md | The maintainer themselves, or another maintainer after the [inactivity notice](#inactivity) |
| Removing a maintainer for cause | An issue, then a pull request to MAINTAINERS.md | A vote at two-thirds; see [removal](#removal-for-cause) |
| Changing this page, the [contributor ladder](CONTRIBUTOR_LADDER.md) or the [code of conduct](CODE_OF_CONDUCT.md) | A pull request, open for at least seven days | A vote at two-thirds, with the organisation rules |

## Lazy consensus and votes

Most decisions are made by lazy consensus: a proposal stands when no maintainer objects during its comment period. The period is seven days unless the table above gives another; for an ordinary pull request it is the review. Any maintainer may call a vote instead of relying on lazy consensus.

A vote:

- takes place in the public issue or pull request, except that votes on a security report or a conduct report take place in that process's private channel;
- runs for seven days, and ends early once the remaining votes cannot change the result;
- counts the `+1`, `-1` and `abstain` comments of maintainers;
- counts against every active maintainer, not only those who voted. A maintainer who declared a conflict in the decision, and the maintainer a removal vote is about, are left out of that count.

A **majority** is more than half of the maintainers counted. **Two-thirds** is at least two-thirds of them, rounded up. When a vote fails, nothing changes.

The same thresholds give these results at each size:

| Decision | 1 maintainer | 2 maintainers | 3 or more maintainers |
| --- | --- | --- | --- |
| An ADR or roadmap decision without consensus | The maintainer decides and records why | Both agree; otherwise nothing changes | Majority |
| Adding a maintainer | The maintainer, once the nomination has been open seven days | Both | Majority |
| Changing governance | The maintainer, once the pull request has been open seven days | Both | Two-thirds |
| Removing a maintainer for cause | Not applicable | Not possible by vote; see [removal](#removal-for-cause) | Two-thirds of the other maintainers |
| Reinstating an emeritus maintainer | The maintainer | Both | Majority |

### Limits on any one organisation

A maintainer's organisation is the employer or client that pays for their time on Ploeg, as listed in [MAINTAINERS.md](MAINTAINERS.md). Each independent maintainer counts as an organisation of their own.

1. **Two organisations behind every vote that shapes the project.** When the maintainers come from more than one organisation, a vote to add or remove a maintainer, or to change this page, the ladder or the code of conduct, passes only if its yes votes come from at least two organisations.
2. **No new seat for an organisation that holds half.** Once there are three or more maintainers, the maintainers do not nominate a new maintainer from an organisation that already employs half of them or more.
3. **A change of employer moves nobody.** When a maintainer changes employer and that leaves one organisation with half the seats or more, nobody is removed. Rule 2 blocks further nominations from that organisation until the balance changes.

## Conflicts of interest

A maintainer has a conflict in a decision when it is about themselves; when it is about their employer or a client; when it is about an organisation they have a financial stake in; or when it is about a product they maintain or sell, including a product built on Ploeg. MAINTAINERS.md lists standing declarations.

A maintainer with a conflict says so in the issue or pull request before the decision, and does not count in a vote on it. When the declaration leaves no maintainer without a conflict, which is always the case while there is one maintainer, the conflicted maintainer still decides: the declaration stays in the record and the decision stays open for comment for at least seven days.

## Companies, commercial users and consumers

Anyone may use, sell, host or support Ploeg under the [Apache-2.0 licence](LICENSE). The [trademark policy](docs/brand/TRADEMARK.md) governs the name.

A **consumer** is a product that calls Ploeg's contracts. A consumer takes part the way any contributor does: through public issues, Discussions, the monthly decision cycle and ADRs. For every consumer:

- Ploeg keeps no private roadmap and makes no private promise. A request that shaped a decision is visible in the issue or Discussion that carries it.
- Ploeg names no consumer in its code, documentation, contracts or defaults, and adds no contract or feature that only one consumer can use ([ADR 0069](docs/adrs/0069-ploeg-names-none-of-its-consumers.md); [ADR 0077](docs/adrs/0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md); [ADR 0158](docs/adrs/0158-a-stable-release-is-a-soaked-candidate-promoted-by-fast-forwarding-main.md), item 12).
- A pull request or proposal made for a consumer says which consumer it serves.

Ploeg's current maintainer also maintains a commercial consumer of Ploeg. That is a standing conflict, declared in MAINTAINERS.md. A decision that mainly serves that consumer follows the [conflict rule](#conflicts-of-interest), so it stays open for comment for seven days.

People who work for a company that uses or sells Ploeg climb the same [ladder](CONTRIBUTOR_LADDER.md) as everyone else. Public work done for an employer counts like any other public work; private work does not count, because the project cannot see it. Nominations come from people, in public. An employer cannot claim, replace or reassign a seat, and money, sponsorship or hosting buy no decision rights. A maintainer whose employer changes keeps the role and updates their affiliation in MAINTAINERS.md within 30 days.

## Maintainer lifecycle

### Joining

The [contributor ladder](CONTRIBUTOR_LADDER.md#maintainer) gives the criteria and the nomination steps. A new maintainer gets their access within seven days of the merge.

### Stepping down

A maintainer may step down at any time with a pull request that moves them to the emeritus list. It takes effect when it merges, and the [offboarding steps](#offboarding) follow.

### Inactivity

A maintainer is inactive after six months with none of these on GitHub: a review, a merged pull request, a release step, a comment in a decision issue, or a response to a security report.

1. Another maintainer opens an issue that mentions the inactive maintainer and asks whether they will return, and sends the same message to any private address they gave the maintainers.
2. The inactive maintainer has 30 days to answer. If they answer with a date, within three months, by which they will be active again, they stay.
3. If they do not answer, or are not active again by their own date, a pull request moves them to emeritus. This records a fact, so it needs no vote.

A sole maintainer is never moved to emeritus for inactivity: there would be nobody left to hold the project. [Continuity](#continuity-and-succession) covers that case.

### Removal for cause

A maintainer may be removed when the code of conduct responders find a violation; when they repeatedly merge without the review this page requires; when they hide a conflict of interest; when they mishandle a security report; or when they misuse a project account.

1. A maintainer opens the matter in an issue. A conduct or security matter is handled privately as its own process says.
2. The maintainer concerned gets the reasons in writing and 14 days to answer.
3. The other maintainers vote; removal needs two-thirds of them. When the conduct responders have already found a violation, the vote decides only on removal and does not reopen the finding.
4. On a yes, the maintainer's access is removed the same day, and the pull request records whether they move to emeritus or leave the list.

With two maintainers, one cannot remove the other by vote, because one voter is too few to judge a dispute they are part of. A conduct matter goes to the [independent route in the code of conduct](CODE_OF_CONDUCT.md#when-a-maintainer-is-involved). Any other dispute stays as it is: nothing changes until the two agree, a third maintainer joins, or one steps down. Inactivity still works as above.

### Emergency suspension

Any maintainer may suspend another maintainer's access at once when that maintainer's account appears compromised, or when someone is causing active harm through it. The suspending maintainer opens the matter within 72 hours, privately if it is a conduct or security matter. The suspension ends when the matter is decided, or after 30 days if nobody opened it.

### Emeritus maintainers

Emeritus maintainers are listed for their past work and may be consulted, but they have no merge rights and no vote. An emeritus maintainer who meets the current maintainer criteria may return by the vote for reinstating in the table above.

### Offboarding

When a maintainer or reviewer leaves, a remaining maintainer, within seven days:

- removes or lowers their role in the `ploeg-hq` organisation and on the repository;
- removes them from ruleset bypass lists, environment reviewers, Discussions moderation and private security advisories;
- rotates every Actions secret they created or could read;
- updates MAINTAINERS.md, and once Ploeg is a CNCF project, CNCF's list of project maintainers.

## Continuity and succession

Ploeg's maintainer is today the only owner of the `ploeg-hq` GitHub organisation. Nobody else can grant access, publish a release or answer a private security report. **Who takes over is open.** The maintainer decided on 2026-10-24 that succession stays open until a co-maintainer is found, and [issue #7](https://github.com/ploeg-hq/ploeg/issues/7) tracks it. Ploeg does not name a successor who has not agreed to it in public.

### What the project holds

| Asset | Where it lives | If the maintainer disappears |
| --- | --- | --- |
| Source and history | [ploeg-hq/ploeg](https://github.com/ploeg-hq/ploeg), public, Apache-2.0 | Every clone keeps working, and anyone may fork. |
| The `ploeg-hq` organisation, its rulesets and Actions secrets | GitHub; one owner | Nobody else can administer it. |
| Images and the Helm chart | `ghcr.io/ploeg-hq/*` | Published versions stay pullable while the organisation exists. No new versions appear. |
| Go module | `github.com/ploeg-hq/ploeg`, mirrored by the Go module proxy | Published versions stay fetchable through the proxy. New versions need the repository. |
| Signing identity | No key exists. The identity is `.github/workflows/release.yml` in `ploeg-hq/ploeg`, through GitHub Actions OIDC ([CI and infrastructure](docs/ops/ci-and-infra.md#credentials-and-signing)) | Published signatures stay verifiable in the public Rekor log. Whoever can run that workflow signs as Ploeg; a fork signs as itself, and verifiers have to change the identity they check. |
| Name and mark | Held by the maintainers ([trademark policy](docs/brand/TRADEMARK.md)) | A fork has to take another name. |
| Domains | Ploeg uses no domain of its own today; the docs use example domains | Nothing. A domain the project registers gets a row here. |
| Consumer update token `RELEASE_CONSUMER_TOKEN_FORGEJO` | The maintainer's own credential, stored as an Actions secret ([releases](docs/ops/release-versioning.md#consumer-pull-requests)) | Consumer update pull requests stop once it lapses. Releases are unaffected. |
| Private security advisories | GitHub; visible to organisation owners and repository admins | Nobody can read or answer them. |

### What happens and when

1. **While there is one maintainer**, the maintainer keeps their own account recoverable. Gaining a second organisation owner is the work of issue #7.
2. **Within 30 days of a second maintainer joining**, they become an owner of the `ploeg-hq` organisation, the organisation turns on required two-factor authentication, and both maintainers check this table together and record the result in the next decision issue.
3. **Every January**, the maintainers check this table against the live settings and record the result in that month's decision issue.
4. **When the maintainers are unavailable**, meaning no public activity from any of them and no answer to an issue that mentions them for 60 days, the project is stalled. Contributors may say so in an issue. Apache-2.0 lets anyone continue the code in a fork under another name, with its own signing identity. Ploeg under its own name cannot continue without someone who can administer `ploeg-hq`. That gap is the main reason Ploeg needs a second maintainer.

## The monthly decision cycle

Roadmap decisions are made in public, once a month, without a meeting.

1. Anyone proposes work or a change of direction as a post in GitHub Discussions, in the Ideas category.
2. On the first working day of the month a maintainer opens an issue titled `Decisions YYYY-MM`. It lists the proposals to decide and links each.
3. The issue stays open for comments for seven days.
4. A maintainer records each decision in the issue with its reason, links the issues or milestones it creates, and closes it. A decision about architecture becomes an ADR as the table above says.
5. The next month's issue starts by reporting what happened to the previous month's decisions.

When no maintainer can run a cycle, the issue is still opened, says so, and carries the proposals to the next month. Security matters never go through this cycle; they follow [SECURITY.md](SECURITY.md).

## Conduct and security

The [code of conduct](CODE_OF_CONDUCT.md) applies everywhere the project works. It gives a reporting route to the maintainers and an independent route through the CNCF Code of Conduct Committee, which a report about a maintainer takes. Report security problems privately as [SECURITY.md](SECURITY.md) describes, never in a public issue.

## Changing this page

A change to this page, the [contributor ladder](CONTRIBUTOR_LADDER.md) or the [code of conduct](CODE_OF_CONDUCT.md) is a pull request that stays open for at least seven days, and then needs a vote at two-thirds with the [organisation rules](#limits-on-any-one-organisation). A change of reasoning also gets an ADR.

## Worked examples

1. *A contributor proposes replacing PostgreSQL leases with a different locking scheme.* This is architectural, so it needs a new ADR in a pull request. The maintainers discuss it there and accept or reject the record; the ADR ledger keeps the earlier record and links the new one.
2. *Two contributors disagree on a pull request about the default harness.* A maintainer decides in the pull request and says why. If either contributor thinks the default is a decision about direction, they open an ADR, and the decision table applies.
3. *Ploeg has two maintainers and one has done nothing for seven months.* The other opens an issue that mentions them and writes to the private address they gave. Thirty days pass without an answer. The active maintainer opens a pull request that moves the inactive one to emeritus and runs the offboarding steps. No vote is held, because [inactivity](#inactivity) records a fact. The remaining maintainer is now a sole maintainer and cannot be moved to emeritus in turn.
4. *Ploeg has four maintainers, two of them from the same company, and that company's employees nominate a colleague.* Two of four is half, so [organisation rule 2](#limits-on-any-one-organisation) says the maintainers do not make the nomination. The colleague can keep contributing and become a reviewer, and can be nominated once the balance changes.
5. *With three maintainers, two from one company and one independent, the two propose a change to this page.* The pull request stays open seven days. Two yes votes out of three meet two-thirds, but both come from one organisation, so [organisation rule 1](#limits-on-any-one-organisation) fails the vote until the independent maintainer also votes yes.
6. *Someone reports that the sole maintainer was abusive in a pull request.* The maintainer has a hard conflict and takes no part in handling the report. The reporter writes to `conduct@cncf.io` as the [code of conduct](CODE_OF_CONDUCT.md#when-a-maintainer-is-involved) says. If the committee takes the report and asks for a remedy, such as an apology or stepping back from a thread, the maintainer carries it out. If the committee declines because Ploeg is not a CNCF project, the reporter can still report the content to GitHub, and the code of conduct says what Ploeg does with the report until it has a responder without a conflict.
7. *A change helps the consumer the maintainer also maintains, and makes an option mandatory for every installer.* The pull request says which consumer it serves. The maintainer declares the standing conflict, and the change is decided in the monthly cycle after seven days open for comment, with the [consumer rules](#companies-commercial-users-and-consumers) applied: Ploeg's code and documentation cannot name the consumer, and the change has to work for any consumer. A contributor who objects can comment there or propose an ADR.
8. *The sole maintainer stops answering for three months.* No one can merge, release or administer `ploeg-hq`. Published releases, images, signatures and module versions keep working ([continuity](#continuity-and-succession)). A contributor opens an issue recording that the project has stalled. Anyone may continue in a fork under another name, with their own signing identity.

## What is not decided here

Transferring the name, domains, accounts or repositories, or signing any foundation agreement, is outside this page. The charter says those need a separate owner decision.
