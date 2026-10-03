# OpenVEX statements

Reviewed exploitability statements for findings the release gate should not count. Each statement is scoped to one image at one digest and carries a status and a justification from the OpenVEX vocabulary. Harbor's project allowlist stays empty; a suppression without a statement here does not exist.

`ploegd` starts at a budget of zero critical and zero high findings in `enforce` mode, as recorded in [ops/security/cve-budgets.yaml](../security/cve-budgets.yaml). A local Grype scan of an AMD64 build of the [release Dockerfile](../docker/ploegd/Dockerfile) on 2026-09-22 reported no findings, so this directory is empty. Add a statement only after a rebuild on a patched base cannot remove the finding, and reference the [infrastructure VEX guide](https://forgejo.webgrip.dev/webgrip/infrastructure/src/branch/main/ops/vex/README.md) for the file shape.
