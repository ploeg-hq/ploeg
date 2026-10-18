#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
fail() { echo "contract bundle: $*" >&2; exit 1; }
mkdir -p "$fixture/repo/docs/contracts" "$fixture/repo/docs/adrs" "$fixture/repo/pkg/store" "$fixture/repo/cmd/ploegd" "$fixture/repo/ops"
cd "$fixture/repo"
git init --quiet -b development
git config user.name 'Contract bundle test'
git config user.email contract-bundle-test@example.invalid
printf '{"enum":["queued"]}\n' > docs/contracts/operator-api.v1.schema.json
printf '# 0001\n' > docs/adrs/0001-ledger.md
printf 'package store\n\nconst reasonDone = "done"\n' > pkg/store/store.go
printf 'package store\n\nconst onlyInTests = "test"\n' > pkg/store/store_test.go
printf 'package main\n' > cmd/ploegd/main.go
printf 'kind: Deployment\n' > ops/deploy.yaml
git add -- docs pkg cmd ops
git commit --quiet -m fixture
git tag v0.1.0-rc.1
build() { CONTRACT_BUNDLE_OUTPUT="$fixture/$1" bash "$root/scripts/contract-bundle.sh" build "$2" "$fixture/repo" > /dev/null; }
build first v0.1.0-rc.1
archive="$fixture/first/ploeg-contract-0.1.0-rc.1.tar.gz"
listing=$(tar -tzf "$archive" | grep -v '/$' | LC_ALL=C sort)
expected=$(printf '%s\n' cmd/ploegd/main.go docs/adrs/0001-ledger.md docs/contracts/operator-api.v1.schema.json files.txt pkg/store/store.go ploeg-contract.json)
[[ "$listing" == "$expected" ]] || fail "unexpected contents:"$'\n'"$listing"
mkdir "$fixture/unpacked"
tar -xzf "$archive" -C "$fixture/unpacked"
grep -qx 'pkg/store/store_test.go' "$fixture/unpacked/files.txt" || fail 'files.txt must list every tracked file, tests included'
grep -qx 'ops/deploy.yaml' "$fixture/unpacked/files.txt" || fail 'files.txt must list files the bundle leaves out'
commit=$(git rev-parse HEAD)
python3 -c 'import json,sys; m=json.load(open(sys.argv[1])); assert m=={"schemaVersion":1,"name":"ploeg-contract","version":"0.1.0-rc.1","commit":sys.argv[2],"source":"https://github.com/ploeg-hq/ploeg"}, m' "$fixture/unpacked/ploeg-contract.json" "$commit" || fail 'ploeg-contract.json does not name the version and commit'
build second v0.1.0-rc.1
cmp -s "$archive" "$fixture/second/ploeg-contract-0.1.0-rc.1.tar.gz" || fail 'two builds of one commit differ, so a re-run could not prove it publishes the same bundle'
for tag in v1.0.0 v0.1.0-rc.0 0.1.0 invalid; do
  if build rejected "$tag" 2>/dev/null; then fail "accepted invalid tag $tag"; fi
done
printf 'package main\n\nfunc main() {}\n' > cmd/ploegd/main.go
git commit --quiet -am later
if build moved v0.1.0-rc.1 2>/dev/null; then fail 'built a tag from a checkout that is not at the tag'; fi
printf 'changed\n' >> docs/adrs/0001-ledger.md
if build dirty v0.1.0-rc.2 2>/dev/null; then fail 'built from uncommitted changes'; fi
echo 'Contract bundle contents, determinism and tag checks passed'
