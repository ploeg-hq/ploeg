#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
fail() { echo "go module published: $*" >&2; exit 1; }
module=example.invalid/ploeg
mkdir -p "$fixture/proxy/$module/@v" "$fixture/proxy/lookup/example.invalid"
port_file="$fixture/port"
python3 -I -c '
import functools, http.server, sys
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=sys.argv[1])
handler.func.log_message = lambda *args: None
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
open(sys.argv[2], "w").write(str(server.server_address[1]))
server.serve_forever()
' "$fixture/proxy" "$port_file" &
server=$!
trap 'kill "$server" 2>/dev/null; rm -rf "$fixture"' EXIT
for _ in $(seq 50); do [[ -s "$port_file" ]] && break; sleep 0.1; done
[[ -s "$port_file" ]] || fail 'fake proxy did not start'
base="http://127.0.0.1:$(cat "$port_file")"
publish() {
  printf '{"Version":"%s","Time":"2026-10-18T00:00:00Z"}' "$1" > "$fixture/proxy/$module/@v/$1.info"
  printf 'module %s\n' "$module" > "$fixture/proxy/$module/@v/$1.mod"
  printf '%s\n' "$1" >> "$fixture/proxy/$module/@v/list"
  printf '1\n%s %s h1:x=\n%s %s/go.mod h1:y=\n' "$module" "$1" "$module" "$1" > "$fixture/proxy/lookup/$module@$1"
}
check() {
  GO_MODULE=$module GO_MODULE_PROXY=$base GO_MODULE_SUMDB=$base GO_MODULE_WAIT_SECONDS=${2:-2} GO_MODULE_POLL_SECONDS=1 \
    bash "$root/scripts/go-module-published.sh" "$1" > /dev/null 2>&1
}
publish v0.1.0-rc.1
check v0.1.0-rc.1 || fail 'a published module was reported missing'
started=$SECONDS
if check v0.1.0-rc.2; then fail 'a module the proxy never served was reported fetchable'; fi
((SECONDS - started <= 5)) || fail 'waiting for a missing module was not bounded'
publish v0.1.0-rc.3
sed -i '/^v0.1.0-rc.3$/d' "$fixture/proxy/$module/@v/list"
if check v0.1.0-rc.3; then fail 'a module the proxy does not list yet was reported fetchable'; fi
publish v0.1.0-rc.4
rm "$fixture/proxy/lookup/$module@v0.1.0-rc.4"
if check v0.1.0-rc.4; then fail 'a module the checksum database does not record was reported fetchable'; fi
publish v0.1.0-rc.5
printf '{"Version":"v0.1.0-rc.4"}' > "$fixture/proxy/$module/@v/v0.1.0-rc.5.info"
if check v0.1.0-rc.5; then fail 'an info file naming another version was accepted'; fi
(sleep 2 && publish v0.1.0-rc.6) &
check v0.1.0-rc.6 10 || fail 'a module that appears while waiting was reported missing'
for tag in v1.0.0 v0.1.0-rc.0 0.1.0 invalid; do
  if check "$tag"; then fail "accepted invalid tag $tag"; fi
done
echo 'go module published: ok'
