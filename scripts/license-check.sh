#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
holder="Copyright 2026 Ryan Grippeling / WebGrip"
fail=0

note() { printf '  %s\n' "$1"; fail=1; }

echo "License consistency — Apache-2.0"

[ -f LICENSE ] || note "LICENSE is missing"
grep -q 'Apache License' LICENSE 2>/dev/null || note "LICENSE is not Apache-2.0"
grep -qE '\[yyyy\]|\[name of copyright owner\]' LICENSE 2>/dev/null \
  && note "LICENSE still carries the Apache appendix placeholders; name the year and the owner"
grep -qF "$holder" LICENSE 2>/dev/null || note "LICENSE does not carry the estate copyright line: $holder"

[ -f NOTICE ] || note "NOTICE is missing; Apache-2.0 section 4(d) is how attribution travels with a fork"
grep -qF "$holder" NOTICE 2>/dev/null || note "NOTICE does not carry the estate copyright line"

[ -f CONTRIBUTING.md ] || note "CONTRIBUTING.md is missing; inbound licence terms should be stated, not assumed"

[ ! -f package.json ] || grep -q '"license": "Apache-2.0"' package.json || note "package.json does not declare Apache-2.0"

for image in $(grep -rl 'org.opencontainers.image.licenses' ops --include='Dockerfile*' 2>/dev/null || true); do
  grep -q 'org.opencontainers.image.licenses="Apache-2.0"' "$image" \
    || note "$image does not label the image Apache-2.0"
done

[ -f REUSE.toml ] || note "REUSE.toml is missing; per-file licensing would stop being declared"
grep -q 'SPDX-License-Identifier = "Apache-2.0"' REUSE.toml 2>/dev/null \
  || note "REUSE.toml does not declare Apache-2.0 for the repository"
for id in $(grep -oE 'SPDX-License-Identifier = "[^"]+"' REUSE.toml 2>/dev/null | sed 's/.*"\(.*\)"/\1/' | sort -u); do
  [ -f "LICENSES/$id.txt" ] || note "REUSE.toml declares $id but LICENSES/$id.txt is missing"
done

if [ "$fail" -ne 0 ]; then
  echo "FAIL — see docs/adrs/"
  exit 1
fi
echo "ok"
