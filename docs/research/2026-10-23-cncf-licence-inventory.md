# CNCF licence policy and the Ploeg licence inventory

Evidence for [the licence inventory](../cncf/license-inventory.md), recorded on 2026-10-23 against `development` at the parent of this change. Issue: [#9](https://github.com/ploeg-hq/ploeg/issues/9).

## Policy as read

All files were read from [cncf/foundation](https://github.com/cncf/foundation) on 2026-10-23, `main` at `e46b23301ccb3a7426dc9fa0426437590196286f`.

| Document | Last changed | What it says that applies |
| --- | --- | --- |
| [allowed-third-party-license-policy.md](https://github.com/cncf/foundation/blob/f196b95408adb03991ab04554a3d2b315924406b/policies-guidance/allowed-third-party-license-policy.md) | 2026-09-07, `f196b954` | Apache-2.0 dependencies need no review. Others are automatically approved only if (1) fully licensable under the list 0BSD, BSD-2-Clause, BSD-2-Clause-FreeBSD, BSD-3-Clause, MIT, MIT-0, ISC, OpenSSL, OpenSSL-standalone, PSF-2.0, Python-2.0, Python-2.0.1, PostgreSQL, SSLeay-standalone, UPL-1.0, X11, Zlib and the Go patent grant; (2) stored unmodified in a third-party folder, or fetched from upstream at build or install time; (3) part of the language's standard library, or created on GitHub at least 12 months ago with at least 10 stars or 10 forks. Anything else needs a Governing Board exception, requested by a project maintainer through the [exception issue template](https://github.com/cncf/foundation/issues/new?template=license-exception-request.yaml). |
| [charter.md section 11](https://github.com/cncf/foundation/blob/fc06ea4db7db02f7cb9db26f18089fdc4e3c1508/charter.md#11-ip-policy) | 2025-07-02, `fc06ea4d` | (a)(i) trademark and logo assets transfer to the Linux Foundation; (a)(iv) an API specification for the project's own software may be Apache-2.0; (b)(ii) inbound code carries a DCO sign-off under Apache-2.0; (b)(iv) documentation is received and made available under CC-BY-4.0. |
| [container-image-guidance.md](https://github.com/cncf/foundation/blob/3a0e5c00e9d90d20715392968ecf3771a05f1f41/policies-guidance/container-image-guidance.md) | 2025-06-05, v1.0 | (A) use the most minimal base image, with a public Dockerfile, that keeps licence notices. (B)(1) put only the project's own build output and standard-package-manager content in an image; avoid copying executables from third-party websites. (B)(2) make compliance artifacts available; a binary blob without source must at least allow free redistribution and should go to the Legal Committee and Governing Board. (B)(3) publish an SBOM. |
| [cncf-dependency-licensing-glossary.md](https://github.com/cncf/foundation/blob/df9775d3c866cca2a72870891e779bf03a432ca4/policies-guidance/cncf-dependency-licensing-glossary.md) | 2026-04-08, `df9775d3` | Defines CNCF-distributed, user-fetched, system component and "not distributed and not needed by the end user" dependencies. The inventory's dispositions follow these. |
| [license-exceptions/README.md](https://github.com/cncf/foundation/blob/e46b23301ccb3a7426dc9fa0426437590196286f/license-exceptions/README.md) | current | The database at [exceptions.cncf.io](https://exceptions.cncf.io/exceptions.json) is the record of decisions. Version 1.1.0, updated 2026-09-07, was searched for every module below. |

## Go modules

`go list -deps ./cmd/...` gives the shipped set; `go list -deps -test ./...` adds the test-only modules. Licences come from google/licensecheck v0.3.1 over each module's top-level licence files. go-licenses v2.0.1 was tried first and failed on every standard-library package ("does not have module info") under Go 1.27.3, so it was not adopted. GitHub figures were read through the API on 2026-10-23.

| Module | Shipped | Licence | Repository created | Stars / forks | Exceptions database |
| --- | --- | --- | --- | --- | --- |
| github.com/coder/acp-go-sdk | yes | Apache-2.0 | 2025-09-26 | 243 / 45 | none needed |
| github.com/golang-jwt/jwt/v5 | yes | MIT | 2021-05-18 | 9249 / 457 | no entry |
| github.com/google/jsonschema-go | yes | MIT | 2025-08-05 | 481 / 43 | no entry |
| github.com/jackc/pgpassfile | yes | MIT | 2019-03-30 | **6 / 4** | no entry |
| github.com/jackc/pgservicefile | yes | MIT | 2020-03-07 | 10 / 6 | no entry |
| github.com/jackc/pgx/v5 | yes | MIT | 2013-03-30 | 14349 / 1146 | no entry |
| github.com/jackc/puddle/v2 | yes | MIT | 2018-12-23 | 426 / 38 | no entry |
| github.com/modelcontextprotocol/go-sdk | yes | Apache-2.0 AND MIT (CC-BY-4.0 docs) | 2025-04-23 | 5263 / 598 | no entry; go-openapi/spec with CC-BY-4.0 AND Apache-2.0 AND MIT is recorded allowlisted |
| github.com/segmentio/asm | yes | MIT | 2021-04-13 | 929 / 38 | no entry |
| github.com/segmentio/encoding | yes | MIT | 2019-12-03 | 1055 / 65 | no entry |
| github.com/yosida95/uritemplate/v3 | yes | BSD-3-Clause | 2016-11-28 | 30 / 5 | no entry |
| go.yaml.in/yaml/v3 | yes | Apache-2.0 AND MIT | 2025-04-25 (yaml/go-yaml) | 542 / 81 | no entry |
| golang.org/x/oauth2, sync, sys, text, time | yes | BSD-3-Clause (+ Go patent grant except oauth2) | 2014-2015 | 427-5905 | allowlisted for all CNCF projects |
| github.com/fergusstrange/embedded-postgres | test | MIT | 2019-11-16 | 1252 / 105 | - |
| github.com/lib/pq | test | MIT | 2012-03-12 | 9967 / 973 | allowlisted |
| github.com/santhosh-tekuri/jsonschema/v6 | test | Apache-2.0 | 2017-05-09 | 1289 / 146 | - |
| github.com/xi2/xz | test | public domain | 2015-07-17 | 83 / 14 | - |
| github.com/google/licensecheck | test | BSD-3-Clause | 2019-04-15 | 517 / 105 | - |

The jackc/pgservicefile figure sits exactly on the threshold of 10 stars and can drop below it.

## Images

The distroless base was exported from its pinned digest and its `/var/lib/dpkg/status.d` and `/usr/share/doc/*/copyright` read: base-files 13.8+deb13u7 (GPL-2+), ca-certificates 20250419 (GPL-2+, MPL-2.0), media-types 13.0.0, netbase 6.5 (GPL-2), tzdata and tzdata-legacy 2026c (public domain).

For `claude-runner`, `debian:bookworm-slim` resolved to `sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8`. Installing the Dockerfile's five packages gave 124 packages, each with a copyright file. Licence fields read: curl 7.88.1-10+deb12u15 (curl, BSD, ISC, X11, GPL with autoconf exceptions for build files), git 1:2.39.5-0+deb12u3 (GPL-2, LGPL-2.1+, Apache-2.0, BSD, Expat, Zlib and others), jq 1.6-2.1+deb12u3 (MIT, CC-BY-3.0), openssh-client 1:9.2p1-2+deb12u10 (BSD and OpenSSH variants), bash and coreutils (GPL-3+).

The Claude Code CLI: `npm pack @anthropic-ai/claude-code@2.1.296` (the version the Dockerfile pins) contains `LICENSE.md`, whose whole text is "© Anthropic PBC. All rights reserved. Use is subject to the Legal Agreements outlined here: https://code.claude.com/docs/en/legal-and-compliance." The npm `license` field is `SEE LICENSE IN README.md`.

## Fonts

[Omnibus-Type/Archivo](https://github.com/Omnibus-Type/Archivo) and [JetBrains/JetBrainsMono](https://github.com/JetBrains/JetBrainsMono) are OFL-1.1. Neither file is in the repository. The [OFL FAQ](https://openfontlicense.org/ofl-faq/) answers 1.1 and 1.13 say a logo built from an OFL font is the designer's artwork and not subject to the OFL.
