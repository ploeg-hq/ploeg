# Security

Ploeg is experimental. The latest 0.x release is the development baseline; no long-term supported release line exists.

The deployment Ploeg supports is profile P1 of the [threat model](docs/security/threat-model.md#the-supported-deployment-profile). The [support matrix](docs/reference/support-matrix.md) lists, for each release line, which versions and components are supported, tested in CI or qualified on a live deployment, and which are unsupported. A report about a deployment outside P1 is still welcome; say which rows it leaves.

## Report a vulnerability

Report it privately through GitHub: [open a draft security advisory](https://github.com/ploeg-hq/ploeg/security/advisories/new). Do not put credentials, exploit details or private deployment data in a public issue, pull request or discussion.

If you cannot use GitHub's private reporting, open a public issue that only asks a maintainer to contact you, with no vulnerability details.

## What Ploeg stores

[Data retention](docs/ops/data-retention.md) lists what Ploeg keeps about work, Runs, transcripts and spend, for how long, and the settings that keep prompts, code and credentials out of storage and logs.

## What happens next

A [maintainer](MAINTAINERS.md) aims to acknowledge a report within seven days, agree on severity with you, fix it in a private fork when that is needed, and publish an advisory with credit once a fixed release is out. These are targets, not guarantees: Ploeg has one maintainer, and there is no backup responder yet. The maintainers are the security response team, and [continuity](GOVERNANCE.md#continuity-and-succession) says what happens to private advisories if they are unavailable.
