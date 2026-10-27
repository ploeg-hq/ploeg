#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
python_with_yaml=(uv run --no-project --with pyyaml==6.0.3 python)
matrix="$root/docs/reference/support-matrix.yaml"

check() {
  "${python_with_yaml[@]}" "$root/scripts/support-matrix.py" --check "$@"
}

mutate() {
  local name=$1 program=$2
  "${python_with_yaml[@]}" - "$matrix" "$fixture/$name.yaml" "$program" <<'PY'
import sys
import yaml
source, target, program = sys.argv[1:4]
matrix = yaml.safe_load(open(source))
rows = {row['id']: row for row in matrix['rows']}
exec(program, {'matrix': matrix, 'rows': rows})
yaml.safe_dump(matrix, open(target, 'w'), sort_keys=False, allow_unicode=True)
PY
  printf '%s' "$fixture/$name.yaml"
}

must_pass() {
  local name=$1
  shift
  check "$@" > /dev/null || { echo "support matrix check refused a valid case: $name" >&2; exit 1; }
}

must_fail() {
  local name=$1 expected=$2
  shift 2
  local output
  if output=$(check "$@" 2>&1); then
    echo "support matrix check accepted: $name" >&2
    exit 1
  fi
  grep -qF -- "$expected" <<< "$output" || { echo "support matrix check failed $name for another reason: $output" >&2; exit 1; }
}

rendered="$fixture/page.md"
"${python_with_yaml[@]}" "$root/scripts/support-matrix.py" --output "$rendered" > /dev/null
must_pass 'the committed matrix and page' --output "$root/docs/reference/support-matrix.md"

valid_row=$(mutate valid-row "
matrix['rows'].append({'id': 'harness-goose-hostile', 'axis': 'harness', 'item': 'goose against a hostile repository',
  'cells': {'0.2.0-rc': 'tested-in-ci', '0.2.0': 'tested-in-ci'},
  'evidence': [{'ci': {'workflow': 'ci.yml', 'job': 'hostile-repository', 'contains': ['GOOSE_VERSION'],
    'test': {'package': 'pkg/harness/adapters/acp', 'name': 'TestHostileRepository_NoRepositorySuppliedCodeStarts', 'enabledBy': 'PLOEG_HOSTILE_REPOSITORY=1'}}}]})
")
"${python_with_yaml[@]}" "$root/scripts/support-matrix.py" --matrix "$valid_row" --output "$fixture/valid-row.md" > /dev/null
must_pass 'a new tested-in-ci row backed by a job that runs it' --matrix "$valid_row" --output "$fixture/valid-row.md"

must_fail 'tested-in-ci with no CI job' 'tested-in-ci needs ci evidence' --output "$rendered" --matrix "$(mutate no-ci "
rows['kubernetes-1.31-1.36']['cells']['0.2.0-rc'] = 'tested-in-ci'
")"

must_fail 'a Kubernetes version the job does not start' "does not contain 'kindest/node:v1.29.14'" --output "$rendered" --matrix "$(mutate absent-version "
rows['kubernetes-1.30']['evidence'][0]['ci']['contains'][0] = 'kindest/node:v1.29.14'
")"

must_fail 'a job that does not exist' 'has no job worker-admission-1.29' --output "$rendered" --matrix "$(mutate absent-job "
rows['kubernetes-1.30']['evidence'][0]['ci']['job'] = 'worker-admission-1.29'
")"

must_fail 'a workflow that only runs by hand' 'never runs on a push, a pull request or a schedule' --output "$rendered" --matrix "$(mutate dispatch-only "
rows['kubernetes-1.30']['evidence'][0]['ci'] = {'workflow': 'release-consumers.yml', 'job': 'consumers'}
")"

must_fail 'an opt-in test CI never enables' 'skips unless something enables it' --output "$rendered" --matrix "$(mutate opt-in-test "
rows['forge-github-app']['evidence'][1]['ci']['test'] = {'package': 'pkg/provider/github', 'name': 'TestQualificationAGitHubRunDeliversAPullRequest'}
")"

must_fail 'a Go test that does not exist' 'has no test TestTheForgeIsQualified' --output "$rendered" --matrix "$(mutate absent-test "
rows['forge-forgejo-15']['evidence'][0]['ci']['test']['name'] = 'TestTheForgeIsQualified'
")"

must_fail 'qualified-live at a release the record does not name' "does not name '0.2.0-rc.81'" --output "$rendered" --matrix "$(mutate wrong-release "
rows['harness-acp-openhands']['evidence'][0]['live']['at'] = '0.2.0-rc.81'
")"

must_fail 'qualified-live from a page that is not a research record' 'is not a research record' --output "$rendered" --matrix "$(mutate not-research "
rows['forge-forgejo-15']['cells']['0.2.0'] = 'qualified-live'
rows['forge-forgejo-15']['evidence'].append({'live': {'record': 'docs/security/credential-flow.md', 'at': '15.0.10'}})
")"

must_fail 'qualified-live with no live record' 'qualified-live needs live evidence' --output "$rendered" --matrix "$(mutate no-live "
rows['tracker-vikunja']['cells']['0.2.0-rc'] = 'qualified-live'
")"

must_fail 'supported with no page stating it' 'supported needs doc evidence' --output "$rendered" --matrix "$(mutate no-doc "
rows['kubernetes-1.38']['cells']['0.2.0'] = 'supported'
")"

must_fail 'experimental without limits' 'experimental needs limits' --output "$rendered" --matrix "$(mutate no-limits "
del rows['helm-3']['limits']
")"

must_fail 'a quoted sentence the page does not contain' 'does not contain' --output "$rendered" --matrix "$(mutate absent-quote "
rows['gateway-litellm-1.103']['evidence'][0]['doc']['contains'] = ['The controller needs LiteLLM 1.90.0 or later.']
")"

must_fail 'an anchor the page does not have' 'has no heading for #supported-versions' --output "$rendered" --matrix "$(mutate absent-anchor "
rows['gateway-litellm-1.103']['evidence'][0]['doc']['path'] = 'docs/ops/managed-workers.md#supported-versions'
")"

must_fail 'a row missing a release line' 'do not match the release lines' --output "$rendered" --matrix "$(mutate missing-line "
del rows['helm-4']['cells']['0.2.0']
")"

stale="$fixture/stale.md"
sed 's/| 1.30 | tested-in-ci |/| 1.30 | qualified-live |/' "$rendered" > "$stale"
must_fail 'a page edited by hand' 'Stale support matrix' --output "$stale"

mirror="$fixture/repository"
mkdir -p "$mirror/.github"
for entry in "$root"/* "$root"/.github/*; do
  [[ "$entry" == "$root/.github/workflows" ]] && continue
  relative=${entry#"$root"/}
  ln -s "$entry" "$mirror/$relative"
done
cp -R "$root/.github/workflows" "$mirror/.github/workflows"
sed -i '/kubernetes: "1.30"/,/node-image:/d' "$mirror/.github/workflows/ci.yml"
must_fail 'CI drops the Kubernetes 1.30 leg the matrix cites' "does not contain 'kindest/node:v1.30.13'" --root "$mirror" --output "$rendered"
echo 'support matrix check: every case behaved'
