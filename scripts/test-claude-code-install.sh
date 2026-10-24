#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
server=""
cleanup() {
  [ -z "$server" ] || kill "$server" 2>/dev/null || true
  rm -rf "$fixture"
}
trap cleanup EXIT
fail() { echo "claude code install: $*" >&2; exit 1; }

mkdir -p "$fixture/bin" "$fixture/site/keys" "$fixture/gnupg-a" "$fixture/gnupg-b"
chmod 0700 "$fixture/gnupg-a" "$fixture/gnupg-b"
ln -s "$root/ops/claude-runner/install-claude-code" "$fixture/bin/ploeg-install-claude-code"
ln -s "$root/ops/claude-runner/claude" "$fixture/bin/claude"

new_key() {
  GNUPGHOME="$1" gpg --batch --quiet --passphrase '' --quick-gen-key "$2 <release@example.invalid>" ed25519 sign never 2>/dev/null
  GNUPGHOME="$1" gpg --batch --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" { print $10; exit }'
}
trusted=$(new_key "$fixture/gnupg-a" 'Release signing')
other=$(new_key "$fixture/gnupg-b" 'Someone else')
GNUPGHOME="$fixture/gnupg-a" gpg --batch --armor --export "$trusted" > "$fixture/site/keys/release.asc" 2>/dev/null

release() {
  local version=$1 signer=$2 body=$3
  local dir="$fixture/site/releases/$version"
  mkdir -p "$dir/linux-x64" "$dir/linux-arm64"
  printf '#!/bin/sh\necho "fake claude %s $* autoupdater=$DISABLE_AUTOUPDATER"\n' "$version" > "$dir/linux-x64/claude"
  cp "$dir/linux-x64/claude" "$dir/linux-arm64/claude"
  local sum
  sum=$(sha256sum "$dir/linux-x64/claude" | cut -d' ' -f1)
  printf '{"version":"%s","platforms":{"linux-x64":{"binary":"claude","checksum":"%s"},"linux-arm64":{"binary":"claude","checksum":"%s"}}}\n' "$version" "$sum" "$sum" > "$dir/manifest.json"
  rm -f "$dir/manifest.json.sig"
  GNUPGHOME="$fixture/gnupg-$signer" gpg --batch --quiet --detach-sign -o "$dir/manifest.json.sig" "$dir/manifest.json" 2>/dev/null
  [ -z "$body" ] || printf '%s' "$body" >> "$dir/linux-x64/claude"
  [ -z "$body" ] || printf '%s' "$body" >> "$dir/linux-arm64/claude"
  printf '%s' "$sum"
}
pinned_sum=$(release 1.2.3 a "")
override_sum=$(release 2.0.0 a "")
release 3.0.0 a 'tampered' > /dev/null
release 4.0.0 b "" > /dev/null
release 5.0.0 a "" > /dev/null
printf '{"version":"5.0.0","platforms":{}}\n' > "$fixture/site/releases/5.0.0/manifest.json"
[ -n "$override_sum" ] || fail 'fixture wrote no checksum'

port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
python3 -m http.server --bind 127.0.0.1 --directory "$fixture/site" "$port" >/dev/null 2>&1 &
server=$!
for _ in $(seq 50); do
  curl -fsS -o /dev/null "http://127.0.0.1:$port/keys/release.asc" 2>/dev/null && break
  sleep 0.1
done

printf 'CLAUDE_CODE_VERSION=1.2.3\nCLAUDE_CODE_SHA256=%s\n' "$pinned_sum" > "$fixture/pin.env"
run() {
  env -i PATH="$fixture/bin:/usr/bin:/bin" HOME="$fixture/home" \
    PLOEG_CLAUDE_CODE_PIN="$fixture/pin.env" \
    PLOEG_CLAUDE_CODE_RELEASES_URL="http://127.0.0.1:$port/releases" \
    PLOEG_CLAUDE_CODE_KEY_URL="http://127.0.0.1:$port/keys/release.asc" \
    PLOEG_CLAUDE_CODE_KEY_FINGERPRINT="$trusted" \
    PLOEG_CLAUDE_CODE_DIR="$fixture/install" "$@"
}
refused() {
  local what=$1
  shift
  if run "$@" > "$fixture/out" 2> "$fixture/err"; then fail "installed $what"; fi
}

out=$(run claude --version 2>/dev/null)
[ "$out" = "fake claude 1.2.3 --version autoupdater=1" ] || fail "the wrapper did not run the pinned build: $out"
[ -x "$fixture/install/1.2.3/claude" ] || fail 'the pinned build is not cached'
[ -z "$(find "$fixture/install" -name '.install.*')" ] || fail 'a finished install left its work directory'

kill "$server"
wait "$server" 2>/dev/null || true
server=""
out=$(run claude -p hello 2>/dev/null) || fail 'a cached build needed the network'
[ "$out" = "fake claude 1.2.3 -p hello autoupdater=1" ] || fail "the cached build did not run: $out"
python3 -m http.server --bind 127.0.0.1 --directory "$fixture/site" "$port" >/dev/null 2>&1 &
server=$!
for _ in $(seq 50); do
  curl -fsS -o /dev/null "http://127.0.0.1:$port/keys/release.asc" 2>/dev/null && break
  sleep 0.1
done

out=$(run env DISABLE_AUTOUPDATER=0 claude --version 2>/dev/null)
[ "$out" = "fake claude 1.2.3 --version autoupdater=0" ] || fail "an operator's DISABLE_AUTOUPDATER was overridden: $out"

out=$(run env CLAUDE_CODE_VERSION=2.0.0 claude --version 2>/dev/null) || fail 'an override without a checksum did not install from the signed manifest'
[ "$out" = "fake claude 2.0.0 --version autoupdater=1" ] || fail "the override ran the wrong build: $out"

refused 'a binary the signed manifest does not match' env CLAUDE_CODE_VERSION=3.0.0 ploeg-install-claude-code
grep -q 'does not match the signed manifest' "$fixture/err" || fail "a tampered binary failed for another reason: $(cat "$fixture/err")"
[ ! -e "$fixture/install/3.0.0" ] || fail 'a tampered binary was kept'

refused 'a manifest signed by another key' env CLAUDE_CODE_VERSION=4.0.0 ploeg-install-claude-code
grep -q 'good signature\|signed by' "$fixture/err" || fail "a foreign signature failed for another reason: $(cat "$fixture/err")"

refused 'a manifest changed after signing' env CLAUDE_CODE_VERSION=5.0.0 ploeg-install-claude-code
grep -q 'good signature' "$fixture/err" || fail "a changed manifest failed for another reason: $(cat "$fixture/err")"

refused 'a build whose pinned checksum disagrees with the signed manifest' env CLAUDE_CODE_VERSION=2.0.0 CLAUDE_CODE_SHA256="$pinned_sum" PLOEG_CLAUDE_CODE_DIR="$fixture/pinned" ploeg-install-claude-code
grep -q 'disagrees with the signed manifest' "$fixture/err" || fail "a wrong pin failed for another reason: $(cat "$fixture/err")"

refused 'a manifest signed by a key other than the pinned fingerprint' env PLOEG_CLAUDE_CODE_KEY_FINGERPRINT="$other" PLOEG_CLAUDE_CODE_DIR="$fixture/fresh" ploeg-install-claude-code
grep -q "not by $other" "$fixture/err" || fail "an unpinned signer failed for another reason: $(cat "$fixture/err")"

refused 'a version that is not a release number' env CLAUDE_CODE_VERSION=../../etc ploeg-install-claude-code
grep -q 'no Claude Code version' "$fixture/err" || fail "a path-like version failed for another reason: $(cat "$fixture/err")"

if [ "$(id -u)" != 0 ]; then
  mkdir -p "$fixture/readonly"
  chmod 0555 "$fixture/readonly"
  refused 'into a directory it cannot write' env PLOEG_CLAUDE_CODE_DIR="$fixture/readonly" ploeg-install-claude-code
  grep -q 'is writable' "$fixture/err" || fail "an unwritable directory failed for another reason: $(cat "$fixture/err")"
fi

echo "Claude Code installs only a build the release key signed, from its pin or an override, and reuses it"
