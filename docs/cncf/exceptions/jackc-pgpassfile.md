---
type: reference
audience: [owner]
owner: ploeg
last_verified: 2026-10-25
verified_by: "CNCF allowlist policy and issue template read at cncf/foundation main on 2026-10-25; exceptions.json (updated 2026-09-07) searched for jackc, pgpassfile and pgservicefile; go.mod of goharbor/harbor, spiffe/spire, dapr/components-contrib and openfga/openfga read the same day; GitHub API figures read the same day; jackc/pgx#2547 read for the maintainer's position"
---

# Licence exception request: two small helpers of the pgx PostgreSQL driver

The maintainer decided on 2026-10-25 to request this exception ([#9](https://github.com/ploeg-hq/ploeg/issues/9)). It is not filed yet: the form asks "For which CNCF project are you requesting exceptions?", so it can only be filed once Ploeg is a CNCF project. File it during onboarding ([#41](https://github.com/ploeg-hq/ploeg/issues/41)). Until then the Sandbox application points at the [licence inventory](../license-inventory.md), which names the gap.

The text below is written for the people who read these requests: CNCF staff, the Legal Committee and the Governing Board. Paste it into the [License Exception Request](https://github.com/cncf/foundation/issues/new?template=license-exception-request.yaml) form as it stands.

## Why an exception is needed

Both packages are MIT-licensed, which CNCF allows. The issue is the policy's third test, which asks that a package show wide use outside CNCF: at least 12 months on GitHub and at least 10 stars or 10 forks.

| Package | Created | Stars | Forks | Passes the use test |
| --- | --- | --- | --- | --- |
| github.com/jackc/pgpassfile | 2019-03-30 | 6 | 4 | No |
| github.com/jackc/pgservicefile | 2020-03-07 | 10 | 6 | Only just; one lost star fails it |

(GitHub figures on 2026-10-25.)

## What other CNCF projects do

The same two packages already ship in several CNCF projects, because every Go program that uses the pgx driver includes them. On 2026-10-25 the `go.mod` of each of these listed `github.com/jackc/pgpassfile`:

- Harbor (graduated)
- SPIFFE/SPIRE (graduated)
- Dapr, in `dapr/components-contrib` (graduated)
- OpenFGA (incubating)

The CNCF exceptions database had no entry for either package, and no request in cncf/foundation names them. Ploeg is asking openly rather than relying on that.

## Why Ploeg can't simply remove them

Ploeg never uses these packages itself. They come in through pgx, the PostgreSQL driver Ploeg is built on, which always includes them; no setting turns that off. The remaining routes are:

- Copy and change pgx so it leaves them out. That is a modified copy of a third-party library, which is worse under the same policy.
- Replace the PostgreSQL driver. Ploeg's storage and API code (51 files) is written against pgx.
- Ask the pgx author to fold both packages into pgx. He has said no to this for good reasons: he keeps them as separate modules so other programs can reuse them ([jackc/pgx#2547](https://github.com/jackc/pgx/issues/2547), May 2026).

## Form answers

**Title:** `[License Exception Request] [Ploeg] [MIT] jackc/pgpassfile and jackc/pgservicefile, used by the pgx PostgreSQL driver`

**For which CNCF project are you requesting exceptions?**

Ploeg (https://github.com/ploeg-hq/ploeg)

**Are you an official maintainer of this project?**

Yes

**List of components requiring an exception**

| Component | Upstream URL | Project Usage URL | License(s) | Purpose |
|-----------|-----------|-----------|-----------|-----------|
| github.com/jackc/pgpassfile v1.0.0 | https://github.com/jackc/pgpassfile | https://github.com/ploeg-hq/ploeg/blob/development/go.mod | MIT | Used by the pgx PostgreSQL driver to read a PostgreSQL password file. Ploeg does not call it directly. |
| github.com/jackc/pgservicefile | https://github.com/jackc/pgservicefile | https://github.com/ploeg-hq/ploeg/blob/development/go.mod | MIT | Used by the pgx PostgreSQL driver to read a PostgreSQL connection-settings file. Ploeg does not call it directly. |

**Distribution and integration model:** CNCF-Distributed

> Both packages are compiled into the `ploegd` and `ploeg-mcp` programs, which Ploeg publishes as release downloads and container images. Their licence texts ship with them in `THIRD-PARTY-NOTICES`.

**Modification status:** Unmodified

> Ploeg uses the published versions exactly as released. Go downloads them from upstream at build time and checks them against recorded checksums.

**Structural separation:** Separated Component

> Neither package is copied into the Ploeg repository. Each stays its own module, fetched at build time.

**Communication mechanism:** Static Linking

> Go builds every dependency into a single program file. The packages run only inside pgx, when it sets up a database connection.

**Data exchange:** Arms-Length Only

> A file location goes in and a password or a set of connection settings comes out. Nothing else is shared.

**Additional information**

> These are two small helpers (the password-file reader is about 110 lines) published by the author of pgx, the most widely used PostgreSQL driver for Go (14,355 stars and 1,148 forks on 2026-10-25, MIT). Their star counts are low because people star the driver, not its helpers; every program that uses pgx includes both. Harbor, SPIFFE/SPIRE, Dapr and OpenFGA ship them today. The pgx author keeps them as separate modules on purpose ([jackc/pgx#2547](https://github.com/jackc/pgx/issues/2547)). We ask for an exception that covers both, ideally for all CNCF projects, since several already depend on them.

## After the decision

- **Approved:** set both modules' `disposition` to `allowlisted` in [license-inventory.yaml](../license-inventory.yaml), put the issue URL in each `note`, and remove the finding from the inventory's findings table.
- **Denied or not eligible:** the remaining route is a different PostgreSQL driver. Record that choice as an ADR before starting.
