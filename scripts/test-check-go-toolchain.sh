#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
fail() { echo "check-go-toolchain: $*" >&2; exit 1; }
required=$(awk '$1 == "toolchain" { sub(/^go/, "", $2); print $2; exit }' "$root/go.mod")
[ -n "$required" ] || fail "go.mod has no toolchain line to test against"
IFS=. read -r major minor patch <<<"$required"
older="$major.$minor.$((${patch:-0} - 1))"
[ "${patch:-0}" -gt 0 ] || older="$major.$((minor - 1)).99"
newer="$major.$minor.$((${patch:-0} + 1))"

mkdir -p "$fixture/bin" "$fixture/tree"
cat > "$fixture/bin/go" <<'FAKE'
#!/usr/bin/env bash
[ "$1" = version ] || exit 1
printf '%b' "$FAKE_GO_VERSION_OUTPUT"
FAKE
chmod +x "$fixture/bin/go"

check() { PATH="$fixture/bin:$PATH" FAKE_GO_VERSION_OUTPUT="$1" bash "$root/scripts/check-go-toolchain.sh" --dir "$fixture/tree" > "$fixture/out" 2>&1; }
passes() { check "$2" || fail "$1 was refused: $(cat "$fixture/out")"; }
refused() {
  if check "$2"; then fail "$1 passed"; fi
  grep -q "$3" "$fixture/out" || fail "$1 was refused for another reason: $(cat "$fixture/out")"
}

passes 'binaries at the toolchain' "$fixture/tree/usr/local/bin/ploegd: go$required\n$fixture/tree/usr/local/bin/ploeg-mcp: go$required\n"
passes 'a binary newer than the toolchain' "$fixture/tree/usr/local/bin/ploegd: go$newer\n"
passes 'a binary with a GOEXPERIMENT suffix' "$fixture/tree/usr/local/bin/ploegd: go$required X:nocoverageredesign\n"
refused 'one binary older than the toolchain' "$fixture/tree/usr/local/bin/ploegd: go$required\n$fixture/tree/usr/local/bin/ploeg-worker: go$older\n" "ploeg-worker built with go$older"
refused 'an image without Go binaries' "" 'carries no Go binary'
refused 'a development build' "$fixture/tree/usr/local/bin/ploegd: godevel-abc\n" 'unrecognised'
echo "check-go-toolchain refuses an image whose Go binaries are older than go.mod's toolchain go$required"
