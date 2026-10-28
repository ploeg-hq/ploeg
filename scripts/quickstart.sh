#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
mode=${1:?expected script, run, pinned, pin-check or checkout-chart}
doc=${QUICKSTART_DOC:-$root/docs/quickstart.md}
version=${QUICKSTART_VERSION:-}
chart=${QUICKSTART_CHART:-}
images=${QUICKSTART_IMAGES:-}
workers=${QUICKSTART_WORKER_NAMESPACE:-}
release_namespace=ploeg
helm_values_line='  -n ploeg -f ops/quickstart/values.yaml --wait --timeout 5m'
worker_logs_line='kubectl logs -l app.kubernetes.io/name=ploeg-worker -c worker --tail=-1 \'
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

worker_namespace_setup() {
  [[ -n "$workers" ]] || return 0
  printf 'kubectl create namespace %s\n' "$workers"
  printf 'kubectl label namespace %s pod-security.kubernetes.io/enforce=restricted\n' "$workers"
  printf 'kubectl create secret generic agent-builder-token -n %s --from-literal=FORGEJO_TOKEN="$FORGEJO_TOKEN"\n' "$workers"
  printf 'kubectl create secret generic ploeg-worker-bootstrap -n %s --from-literal=quickstart--default="$BOOTSTRAP_TOKEN"\n' "$workers"
}

worker_namespace_check() {
  [[ -n "$workers" ]] || return 0
  printf 'if [ -z "$(kubectl get jobs -n %s -o name)" ] || [ -n "$(kubectl get jobs -n %s -o name)" ]; then\n' "$workers" "$release_namespace"
  printf '  echo "quickstart: the worker Jobs did not run in namespace %s alone" >&2\n' "$workers"
  echo '  exit 1'
  echo 'fi'
  printf 'echo "quickstart workers ran in namespace %s"\n' "$workers"
}

generate() {
  local pinned cleanup loads setup
  loads=$(image_loads)
  if [[ -n "$workers" ]]; then
    [[ "$workers" =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ && "$workers" != "$release_namespace" ]] \
      || fail "QUICKSTART_WORKER_NAMESPACE must be a namespace name other than $release_namespace, not '$workers'"
    require_line "$helm_values_line"
    require_line "$worker_logs_line"
  fi
  setup=$(worker_namespace_setup)
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
  emit_diagnose
  echo 'quickstart_cleanup() {'
  echo '  quickstart_status=$?'
  echo '  set +e'
  echo '  [ "$quickstart_status" = 0 ] || quickstart_diagnose'
  doc_blocks | awk -F '\t' -v block="$cleanup" '$1 == block { print "  " $2 }'
  echo '}'
  echo 'trap quickstart_cleanup EXIT'
  doc_blocks | QS_SETUP="$setup" QS_VALUES_LINE="$helm_values_line" QS_LOGS_LINE="$worker_logs_line" \
    awk -F '\t' -v block="$cleanup" -v version="$version" -v chart="$chart" -v loads="$loads" -v workers="$workers" '
    $1 == block { next }
    loads != "" && $2 ~ /^kind create cluster --name / { print $2; print loads; next }
    $2 == "git clone --depth 1 https://github.com/ploeg-hq/ploeg.git" || $2 == "cd ploeg" { next }
    version != "" && $2 ~ /^export PLOEG_VERSION=/ { print "export PLOEG_VERSION=" version; next }
    workers != "" && $2 ~ /^helm install ploeg / { print ENVIRON["QS_SETUP"] }
    workers != "" && $2 == ENVIRON["QS_VALUES_LINE"] { sub(/ --wait /, " --set executor.namespace=" workers " --wait ") }
    workers != "" && $2 == ENVIRON["QS_LOGS_LINE"] { print "kubectl logs -n " workers " " substr($2, length("kubectl logs ") + 1); next }
    chart != "" && index($2, "oci://ghcr.io/ploeg-hq/charts/ploeg --version \"$PLOEG_VERSION\"") {
      sub(/oci:\/\/ghcr\.io\/ploeg-hq\/charts\/ploeg --version "\$PLOEG_VERSION"/, chart)
    }
    { print $2 }
  '
  worker_namespace_check
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
  [[ -z "$workers" ]] || grep -qxF "quickstart workers ran in namespace $workers" "$transcript" \
    || fail "the transcript never shows the worker Jobs running in namespace $workers"
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

emit_diagnose() {
  echo "quickstart_namespaces='$release_namespace${workers:+ $workers}'"
  cat <<'SH'
quickstart_diagnose() {
  echo "quickstart: exit status $quickstart_status; the cluster as it stood"
  kubectl describe deployment/ploeg -n ploeg --request-timeout=20s
  for namespace in $quickstart_namespaces; do
    kubectl get pods,deployments,jobs,cronjobs -n "$namespace" -o wide --request-timeout=20s
    kubectl get events -n "$namespace" --sort-by=.lastTimestamp --request-timeout=20s
    kubectl describe pods -n "$namespace" --request-timeout=20s
    for pod in $(kubectl get pods -n "$namespace" -o name --request-timeout=20s); do
      kubectl logs "$pod" --all-containers --prefix --tail=200 -n "$namespace" --request-timeout=20s
      kubectl logs "$pod" --all-containers --prefix --tail=200 --previous -n "$namespace" --request-timeout=20s 2> /dev/null
    done
  done
}
SH
}

cluster_name() {
  doc_blocks | awk -F '\t' '$2 ~ /^kind create cluster --name / { split($2, words, " "); print words[5]; exit }'
}

image_loads() {
  [[ -n "$images" ]] || return 0
  local name archive image digest
  name=$(cluster_name)
  [[ -n "$name" ]] || fail "docs/quickstart.md no longer creates its cluster with 'kind create cluster --name <name>', so the checkout's images have no cluster to load into"
  for archive in "$images"/*.tar; do
    [[ -f "$archive" ]] || fail "QUICKSTART_IMAGES names $images, which holds no image archive"
    image=$(oci_archive_name "$archive")
    digest=$(oci_archive_digest "$archive")
    [[ "$image" == *:checkout && "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] \
      || fail "$archive is not an OCI archive of one <repository>:checkout image"
    printf 'kind load image-archive --name %q %q\n' "$name" "$archive"
    printf 'docker exec %q ctr --namespace k8s.io images tag --force %q %q\n' "$name-control-plane" "$image" "${image%:checkout}@$digest"
  done
}

oci_archive_digest() {
  tar -xOf "$1" index.json 2> /dev/null | jq -r '.manifests[0].digest // empty' 2> /dev/null || true
}

oci_archive_name() {
  tar -xOf "$1" index.json 2> /dev/null | jq -r '.manifests[0].annotations["io.containerd.image.name"] // empty' 2> /dev/null || true
}

build_checkout_image() {
  local image=$1 release=$2 archive=$3 platform
  platform=linux/$(docker version --format '{{.Server.Arch}}')
  docker buildx build --builder "$builder" --file "$root/ops/docker/$image/Dockerfile" --platform "$platform" \
    --provenance=false --sbom=false --build-arg "IMAGE_VERSION=$release" \
    --output "type=oci,dest=$archive,name=ghcr.io/ploeg-hq/$image:checkout" "$root" >&2
}

checkout_chart() {
  local release=${1:?expected the release whose version the chart takes and whose claude-runner image it pins} destination=${2:?expected the destination directory}
  local archives=$destination/images ploegd_digest worker_digest runner_digest
  builder=ploeg-quickstart-$$
  docker buildx create --name "$builder" --driver docker-container > /dev/null
  trap 'docker buildx rm --force "$builder" > /dev/null 2>&1' EXIT
  mkdir -p "$archives"
  build_checkout_image ploegd "$release" "$archives/ploegd.tar"
  build_checkout_image ploeg-worker "$release" "$archives/ploeg-worker.tar"
  ploegd_digest=$(oci_archive_digest "$archives/ploegd.tar")
  worker_digest=$(oci_archive_digest "$archives/ploeg-worker.tar")
  runner_digest=$(oras resolve "ghcr.io/ploeg-hq/claude-runner:$release")
  bash "$root/scripts/package-chart.sh" "$release" "$ploegd_digest" "$runner_digest" "$worker_digest" "$destination" > /dev/null
  echo "QUICKSTART_CHART=$destination/ploeg-$release.tgz"
  echo "QUICKSTART_IMAGES=$archives"
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
