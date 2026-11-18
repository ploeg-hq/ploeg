#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
dockerfile="$root/ops/docker/agent-runner/Dockerfile"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

arg() { sed -n "s/^ARG $1=//p" "$dockerfile" | head -1; }
set_arg() { awk -v name="$1" -v value="$2" '$0 ~ "^ARG " name "=" { $0 = "ARG " name "=" value } { print }' "$dockerfile" > "$work/Dockerfile" && cat "$work/Dockerfile" > "$dockerfile"; }
newer() { [[ "$1" != "$2" && "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" == "$2" ]]; }

changes=()

current_openhands=$(arg OPENHANDS_VERSION)
latest_openhands=$(curl -fsS https://pypi.org/pypi/openhands/json | jq -r .info.version)
[[ "$latest_openhands" =~ ^[0-9]+(\.[0-9]+)+$ ]] || { echo "PyPI returned no usable OpenHands version: $latest_openhands" >&2; exit 1; }
if newer "$current_openhands" "$latest_openhands"; then
  bash "$root/ops/docker/agent-runner/openhands-constraints.sh" "$latest_openhands"
  set_arg OPENHANDS_VERSION "$latest_openhands"
  changes+=("OpenHands $current_openhands -> $latest_openhands")
fi

current_goose=$(arg GOOSE_VERSION)
latest_goose=$(curl -fsS ${GH_TOKEN:+-H "Authorization: Bearer $GH_TOKEN"} https://api.github.com/repos/aaif-goose/goose/releases/latest | jq -r .tag_name)
latest_goose=${latest_goose#v}
[[ "$latest_goose" =~ ^[0-9]+(\.[0-9]+)+$ ]] || { echo "GitHub returned no usable goose release: $latest_goose" >&2; exit 1; }
if newer "$current_goose" "$latest_goose"; then
  for pair in AMD64:x86_64 ARM64:aarch64; do
    curl -fsSL --retry 3 -o "$work/goose.tar.gz" "https://github.com/aaif-goose/goose/releases/download/v$latest_goose/goose-${pair#*:}-unknown-linux-musl.tar.gz"
    set_arg "GOOSE_SHA256_${pair%%:*}" "$(sha256sum "$work/goose.tar.gz" | cut -d' ' -f1)"
  done
  set_arg GOOSE_VERSION "$latest_goose"
  changes+=("goose $current_goose -> $latest_goose")
fi

if [[ ${#changes[@]} -eq 0 ]]; then
  echo "The agent image's OpenHands $current_openhands and goose $current_goose are the newest releases"
  exit 0
fi
printf '%s\n' "${changes[@]}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    summary=$(printf '%s, ' "${changes[@]}")
    echo "summary=${summary%, }"
    echo "branch=agent-runner-pins/openhands-$(arg OPENHANDS_VERSION)-goose-$(arg GOOSE_VERSION)"
  } >> "$GITHUB_OUTPUT"
fi
