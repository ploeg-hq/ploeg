#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
if ! command -v go >/dev/null 2>&1; then
  go_version=$(sed -n 's/^toolchain go//p' go.mod)
  [ -n "$go_version" ] || go_version=$(sed -n 's/^go //p' go.mod)
  install-tool golang "$go_version"
fi
PLOEG_UPDATE_LICENSE_INVENTORY=1 go test ./internal/licenses/
