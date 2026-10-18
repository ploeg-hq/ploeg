#!/usr/bin/env bash
set -euo pipefail
tag=${1:?expected v0.x.y or v0.x.y-rc.N}
[[ "$tag" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] || { echo "not a Ploeg release tag: $tag" >&2; exit 1; }
module=${GO_MODULE:-github.com/ploeg-hq/ploeg}
proxy=${GO_MODULE_PROXY:-https://proxy.golang.org}
sumdb=${GO_MODULE_SUMDB:-https://sum.golang.org}
wait_seconds=${GO_MODULE_WAIT_SECONDS:-600}
poll_seconds=${GO_MODULE_POLL_SECONDS:-15}
deadline=$((SECONDS + wait_seconds))
fetch() { curl --silent --show-error --fail --location --max-time 30 "$1"; }
module_is_fetchable() {
  local info
  info=$(fetch "$proxy/$module/@v/$tag.info") || return 1
  grep -q "\"Version\":\"$tag\"" <<<"$info" || { echo "$proxy answered $tag.info with $info" >&2; return 1; }
  fetch "$proxy/$module/@v/$tag.mod" >/dev/null || return 1
  fetch "$proxy/$module/@v/list" | grep -Fx "$tag" >/dev/null || { echo "$proxy does not list $tag yet" >&2; return 1; }
  fetch "$sumdb/lookup/$module@$tag" | grep -F "$module $tag/go.mod h1:" >/dev/null || return 1
}
attempt=1
until module_is_fetchable; do
  if ((SECONDS + poll_seconds > deadline)); then
    echo "$module@$tag is still not fetchable from $proxy and $sumdb after $attempt attempts in ${wait_seconds}s; consumers are not notified" >&2
    exit 1
  fi
  echo "Attempt $attempt: $module@$tag not fetchable yet, retrying in ${poll_seconds}s" >&2
  attempt=$((attempt + 1))
  sleep "$poll_seconds"
done
echo "$module@$tag is listed by $proxy and recorded in $sumdb"
[[ -z "${GITHUB_STEP_SUMMARY:-}" ]] || printf 'Go module: %s is fetchable from the module proxy\n' "\`$module@$tag\`" >> "$GITHUB_STEP_SUMMARY"
