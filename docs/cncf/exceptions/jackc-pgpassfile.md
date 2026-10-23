---
type: reference
audience: [owner]
owner: ploeg
last_verified: 2026-10-24
verified_by: "Issue template and process read at cncf/foundation d9616b9e on 2026-10-24; exceptions.json searched for jackc and pgpass; GitHub API figures for jackc/pgpassfile read the same day"
---

# Licence exception request: github.com/jackc/pgpassfile

This is a draft for the maintainer to file. Nothing has been filed. The [licence inventory](../license-inventory.md) records the component as `exception-needed` and points here.

## How CNCF takes the request

- **Where:** a new issue in [cncf/foundation](https://github.com/cncf/foundation/issues/new?template=license-exception-request.yaml) from the *License Exception Request* template. The [allowlist policy](https://github.com/cncf/foundation/blob/main/policies-guidance/allowed-third-party-license-policy.md#process-for-applying-for-an-exception) and the [how-to](https://contribute.cncf.io/resources/how-to/submit-license-exception-request/) describe the steps.
- **Who:** a maintainer of the CNCF project that uses the component. The form's first question is "For which CNCF project are you requesting exceptions?", so file it once Ploeg is a CNCF project. Before acceptance, point the Sandbox application at the [licence inventory](../license-inventory.md), which already names this gap.
- **What happens:** staff triage it (`licensing`, `needs-review`) and put it on the [Licensing Exception Board](https://github.com/orgs/cncf/projects/44). Counsel reviews it, the Legal Committee (which meets every one to two months) recommends, and the Governing Board decides. Staff then label the issue `license-exception/approved`, `denied` or `not-eligible`. A workflow turns each row of the component table into an entry on [exceptions.cncf.io](https://exceptions.cncf.io/).
- **Format rules:** one component per table row, SPDX identifiers, and no `|` inside a cell. The workflow reads the five-column table, so keep the column order. Several packages can share one issue only when every other answer is identical.
- **Already decided?** On 2026-10-24 neither `exceptions.json` nor the cncf/foundation issues mentioned `jackc/pgpassfile` or any other `jackc` module.

## Why it needs one

MIT is on the allowlist, and the module comes from its upstream through the Go module proxy, so allowlist conditions 1 and 2 hold. Condition 3 fails: `github.com/jackc/pgpassfile` was created on 2019-03-30 but had 6 stars and 4 forks on 2026-10-24. The policy asks for at least 10 of either. The licence isn't the problem; the use test is.

## Form answers

**Title:** `[License Exception Request] [Ploeg] [MIT]`

**For which CNCF project are you requesting exceptions?** Ploeg (https://github.com/ploeg-hq/ploeg)

**Are you an official maintainer of this project?** Yes

**List of components requiring an exception**

| Component | Upstream URL | Project Usage URL | License(s) | Purpose |
|-----------|-----------|-----------|-----------|-----------|
| github.com/jackc/pgpassfile v1.0.0 | https://github.com/jackc/pgpassfile | https://github.com/ploeg-hq/ploeg/blob/development/go.mod | MIT | Indirect dependency of the pgx PostgreSQL driver, unmodified; pgx uses it to read a PostgreSQL password file when the connection string carries no password |

**Distribution and integration model:** CNCF-Distributed

> Ploeg links the module into the `ploegd`, `ploeg-worker` and `ploeg-mcp` binaries. Those ship in the release tarball and in the `ghcr.io/ploeg-hq/ploegd` image. The licence text travels in `THIRD-PARTY-NOTICES`, which the tarball and the image carry.

**Modification status:** Unmodified

> Ploeg uses v1.0.0 exactly as published, resolved through the Go module proxy and pinned in `go.sum`. Ploeg never imports it directly. `github.com/jackc/pgx/v5/pgconn` imports it.

**Structural separation:** Separated Component

> The module is never stored in the Ploeg repository. Go fetches it at build time from upstream into the module cache, and it keeps its own module path.

**Communication mechanism:** Static Linking

> Go links every dependency into one static binary. Ploeg calls pgx, and `pgconn.ParseConfig` calls `pgpassfile.ReadPassfile` and `FindPassword` only when the connection string has no password. Ploeg's own code never calls the package.

**Data exchange:** Arms-Length Only

> The only data that crosses the boundary is a file path in and a password string out. No shared internal structures, pointers or memory layouts are involved.

**Additional information**

> The package is 110 lines of Go that parse the libpq `.pgpass` format. Its author, Jack Christensen, also maintains the pgx driver, which has 14,352 stars and 1,145 forks (2026-10-24) and needs no exception. pgx is MIT as well. pgpassfile is a helper the same author publishes as its own module, so its low star count understates how widely it is used: every program built on pgx v5 links it.
>
> Ploeg can't drop it cheaply. pgx imports it unconditionally, so removing it means forking pgx (a modified downstream copy, which is worse under the policy) or replacing the PostgreSQL driver under the 51 store and API files written against pgx. Asking upstream to fold the package back into pgx would also clear the use test. That is up to the pgx maintainers, so Ploeg is asking for the exception instead.
>
> The sibling module `github.com/jackc/pgservicefile` (MIT, also imported by pgconn) had exactly 10 stars and 6 forks on 2026-10-24, the threshold itself. It passes today, and one lost star would put it in the same position; the maintainer can add it as a second row now, since every other answer is the same.

## After the decision

- **Approved:** set the module's `disposition` to `allowlisted` in [license-inventory.yaml](../license-inventory.yaml), put the issue URL in its `note`, and remove the row from the inventory's findings table.
- **Denied or not eligible:** the remaining routes are an upstream merge into pgx or a different driver. Record whichever the maintainer picks as an ADR.
