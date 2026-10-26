#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
mode=${1:?expected script, run, pinned, pin-check or checkout-chart}
doc=${QUICKSTART_DOC:-$root/docs/quickstart.md}
version=${QUICKSTART_VERSION:-}
chart=${QUICKSTART_CHART:-}
signed_artifacts=3
max_candidates_behind=${QUICKSTART_MAX_CANDIDATES_BEHIND:-20}
fail() { echo "quickstart: $*" >&2; exit 1; }

doc_blocks() {
  awk '
    /^```sh$/ { inside = 1; block++; next }
    /^```$/ && inside { inside = 0; next }
    inside { print block "\t" $0 }
  ' "$doc"
}

cleanup_block() {
  doc_blocks | awk -F '\t' '$2 ~ /^kind delete cluster/ { print $1; exit }'
}

pinned_version() {
  sed -n 's/^export PLOEG_VERSION=\(0\.[0-9][0-9.]*\(-rc\.[0-9][0-9]*\)\{0,1\}\)$/\1/p' "$doc"
}

require_line() {
  grep -qxF -- "$1" "$doc" || fail "docs/quickstart.md no longer has the line '$1' this runner rewrites"
}

generate() {
  local pinned cleanup
  pinned=$(pinned_version)
  [[ -n "$pinned" ]] || fail "docs/quickstart.md has no 'export PLOEG_VERSION=<version>' line"
  cleanup=$(cleanup_block)
  [[ -n "$cleanup" ]] || fail "docs/quickstart.md has no sh block that runs 'kind delete cluster'"
  require_line 'git clone --depth 1 https://github.com/ploeg-hq/ploeg.git'
  require_line 'cd ploeg'
  grep -qF 'oci://ghcr.io/ploeg-hq/charts/ploeg --version "$PLOEG_VERSION"' "$doc" \
    || fail 'docs/quickstart.md no longer installs oci://ghcr.io/ploeg-hq/charts/ploeg --version "$PLOEG_VERSION"'
  echo 'set -euo pipefail'
  printf 'cd %q\n' "$root"
  echo 'quickstart_cleanup() {'
  echo '  set +e'
  doc_blocks | awk -F '\t' -v block="$cleanup" '$1 == block { print "  " $2 }'
  echo '}'
  echo 'trap quickstart_cleanup EXIT'
  doc_blocks | awk -F '\t' -v block="$cleanup" -v version="$version" -v chart="$chart" '
    $1 == block { next }
    $2 == "git clone --depth 1 https://github.com/ploeg-hq/ploeg.git" || $2 == "cd ploeg" { next }
    version != "" && $2 ~ /^export PLOEG_VERSION=/ { print "export PLOEG_VERSION=" version; next }
    chart != "" && index($2, "oci://ghcr.io/ploeg-hq/charts/ploeg --version \"$PLOEG_VERSION\"") {
      sub(/oci:\/\/ghcr\.io\/ploeg-hq\/charts\/ploeg --version "\$PLOEG_VERSION"/, chart)
    }
    { print $2 }
  '
  cat <<'SH'
quickstart_result=$(curl -sS http://localhost:8080/api/v1/operator/work-items/$WORK_ITEM \
  -H "Authorization: Bearer $OPERATOR_TOKEN" | jq -c '{state: .item.state, outcomes: [.runs[].outcome]}')
echo "quickstart result $quickstart_result"
[ "$quickstart_result" = '{"state":"done","outcomes":["no_change_needed"]}' ]
SH
}

check_transcript() {
  local transcript=$1 line
  [[ "$(grep -c '^verified ghcr.io/ploeg-hq/' "$transcript")" == "$signed_artifacts" ]] \
    || fail "step 1 did not print $signed_artifacts verified lines"
  [[ "$(grep -cx '201' "$transcript")" == 2 ]] || fail 'step 4 did not print 201 twice'
  grep -qx 'Work Item [0-9][0-9]*' "$transcript" || fail 'step 7 admitted no Work Item'
  for line in 'registered target ready' 'ploegd ready' 'ploegd answers' 'claimed work item' 'minted per-run key' 'revoked per-run key' 'outcome reported'; do
    grep -qF "$line" "$transcript" || fail "the transcript never shows '$line'"
  done
  grep -q '"settledUsd": 0' "$transcript" || fail 'step 9 never showed the settled cost of 0'
}

pin_check() {
  local pinned newest position
  pinned=$(pinned_version)
  [[ -n "$pinned" ]] || fail "docs/quickstart.md has no 'export PLOEG_VERSION=<version>' line"
  mapfile -t published < <(grep -E '^v0\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$')
  [[ "${#published[@]}" -gt 0 ]] || fail 'no published release tag on standard input'
  newest=${published[0]#v}
  position=-1
  for index in "${!published[@]}"; do
    [[ "${published[$index]}" != "v$pinned" ]] || { position=$index; break; }
  done
  [[ "$position" -ge 0 ]] || fail "docs/quickstart.md pins $pinned, which is not a published release; pin $newest"
  [[ "$position" -lt "$max_candidates_behind" ]] \
    || fail "docs/quickstart.md pins $pinned, $position releases behind the newest, $newest; the limit is $max_candidates_behind. Run the Quickstart workflow, then pin $newest"
  echo "pinned=$pinned"
  echo "newest=$newest"
}

checkout_chart() {
  local release=${1:?expected the release whose images the checkout chart runs} destination=${2:?expected the destination directory}
  local -a digests=()
  for image in ploegd claude-runner ploeg-worker; do
    digests+=("$(oras resolve "ghcr.io/ploeg-hq/$image:$release")")
  done
  bash "$root/scripts/package-chart.sh" "$release" "${digests[@]}" "$destination" > /dev/null
  echo "$destination/ploeg-$release.tgz"
}

case "$mode" in
  checkout-chart)
    checkout_chart "${@:2}"
    ;;
  pinned)
    pinned_version
    ;;
  pin-check)
    pin_check
    ;;
  script)
    generate
    ;;
  run)
    work=$(mktemp -d)
    generate > "$work/quickstart.sh"
    status=0
    TMPDIR="$work" bash "$work/quickstart.sh" 2>&1 | tee "$work/transcript.txt" || status=$?
    [[ "$status" == 0 ]] || fail "the quickstart stopped with exit status $status; transcript: $work/transcript.txt"
    check_transcript "$work/transcript.txt"
    echo "quickstart: every step ran and the Run finished done with no_change_needed"
    ;;
  *)
    fail "unknown mode $mode; expected script, run, pinned, pin-check or checkout-chart"
    ;;
esac
