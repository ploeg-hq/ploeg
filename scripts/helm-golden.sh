#!/bin/sh
# Golden renders of the Helm chart.
#
# `check` (CI) fails when a rendered manifest differs from the committed
# golden; `update` regenerates them after an intended change.
#
# Why a chart has goldens at all: the worker pod is a security boundary. Its
# privileged DinD sidecar is waived by a Kyverno PolicyException in
# webgrip/homelab-cluster that matches the `ploeg.webgrip.dev/privileged-dind`
# label THIS chart emits. A template edit that put that label on a workload
# which takes no privilege — or dropped it from one that does — would widen or
# void a security waiver with nothing in the diff to review. The goldens make
# every rendered byte show up in the pull request.
#
# They also pin the property the Shift rollout depends on: a team with no plan
# renders exactly what it rendered before Roles existed.
set -eu

cd "$(dirname "$0")/.."
GOLDEN=ops/helm/ploeg/ci/golden
REQUIRED=ops/helm/ploeg/ci/required-values.yaml
mode="${1:-check}"

# The chart version is substituted out before the diff. semantic-release rewrites
# Chart.yaml version/appVersion on every rc, and those strings land in three labels
# on every object plus the ploegd image tag — so a release nobody reviewed stales all
# three goldens, and the next pull request fails a check with nothing to say. That
# happened on rc.9→rc.10 and again on rc.10→rc.11. Substituting keeps the goldens
# about the templates, which is the only thing here anyone can get wrong. Nothing is
# given up: an image tag that stopped tracking .Chart.AppVersion no longer matches
# the substitution and still lands in the diff.
chart_version=$(sed -n 's/^version:[[:space:]]*//p' ops/helm/ploeg/Chart.yaml)
chart_app_version=$(sed -n 's/^appVersion:[[:space:]]*//p' ops/helm/ploeg/Chart.yaml)
if [ -z "$chart_version" ] || [ -z "$chart_app_version" ]; then
	echo "cannot read version/appVersion from ops/helm/ploeg/Chart.yaml" >&2
	exit 1
fi
# Escape the two regex metacharacters a semver can contain (`.` separators, `+`
# build metadata), so 0.2.0 does not also match 0x2y0.
escape() { printf '%s' "$1" | sed 's/[.+]/\\&/g'; }
version_re=$(escape "$chart_version")
app_version_re=$(escape "$chart_app_version")

drop_blank_lines_before_separators() {
	awk '/^$/ { blank++; next } $0 != "---" { while (blank > 0) { print ""; blank-- } } { blank = 0; print } END { while (blank > 0) { print ""; blank-- } }'
}

render() { # <values-file-or-empty>
	if [ -n "$1" ]; then
		helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$1"
	else
		helm template ploeg ops/helm/ploeg -f "$REQUIRED"
	fi | sed -e "s/$version_re/CHART-VERSION/g" -e "s/$app_version_re/CHART-VERSION/g" | drop_blank_lines_before_separators
}

status=0
for case in ":default" \
	"ops/helm/ploeg/ci/executor-values.yaml:executor" \
	"ops/helm/ploeg/ci/executor-cronjob-values.yaml:executor-cronjob" \
	"ops/helm/ploeg/ci/executor-cronjob-runtimeclass-values.yaml:executor-cronjob-runtimeclass" \
	"ops/helm/ploeg/ci/executor-runtimeclass-values.yaml:executor-runtimeclass" \
	"ops/helm/ploeg/ci/executor-gitlab-values.yaml:executor-gitlab" \
	"ops/helm/ploeg/ci/executor-github-values.yaml:executor-github" \
	"ops/helm/ploeg/ci/executor-sandbox-values.yaml:executor-sandbox" \
	"ops/helm/ploeg/ci/executor-sandbox-runtimeclass-values.yaml:executor-sandbox-runtimeclass" \
	"ops/helm/ploeg/ci/executor-sandbox-team-values.yaml:executor-sandbox-team" \
	"ops/helm/ploeg/ci/executor-isolation-values.yaml:executor-isolation" \
	"ops/helm/ploeg/ci/executor-isolation-disabled-values.yaml:executor-isolation-disabled" \
	"ops/helm/ploeg/ci/executor-default-worker-values.yaml:executor-default-worker" \
	"ops/helm/ploeg/ci/executor-public-endpoints-values.yaml:executor-public-endpoints" \
	"ops/helm/ploeg/ci/executor-worker-admission-values.yaml:executor-worker-admission" \
	"ops/helm/ploeg/ci/executor-acknowledged-dind-values.yaml:executor-acknowledged-dind" \
	"ops/helm/ploeg/ci/executor-harness-container-shared-values.yaml:executor-harness-container-shared" \
	"ops/helm/ploeg/ci/executor-harness-selection-values.yaml:executor-harness-selection" \
	"ops/helm/ploeg/ci/monitoring-values.yaml:monitoring" \
	"ops/helm/ploeg/ci/knowledge-graph-values.yaml:knowledge-graph" \
	"ops/helm/ploeg/ci/mcp-values.yaml:mcp" \
	"ops/helm/ploeg/ci/claude-runner-values.yaml:claude-runner" \
	"ops/helm/ploeg/ci/name-overrides-values.yaml:name-overrides"; do
	values=${case%%:*}
	name=${case##*:}
	if [ "$mode" = "update" ]; then
		render "$values" >"$GOLDEN/$name.yaml"
		echo "updated $GOLDEN/$name.yaml"
		continue
	fi
	if ! render "$values" | diff -u "$GOLDEN/$name.yaml" - >/tmp/golden-$name.diff; then
		echo "chart render '$name' differs from its golden:"
		cat /tmp/golden-$name.diff
		status=1
	fi
done

if [ "$mode" = "update" ]; then
	mise run docs-configuration
	echo "updated docs/reference/configuration.md"
fi

for case in "ops/helm/ploeg/ci/reject-reader-without-read-token-values.yaml:readTokenSecret is not set" \
	"ops/helm/ploeg/ci/reject-team-executor-type-under-cronjob-values.yaml:executorType is only honoured" \
	"ops/helm/ploeg/ci/reject-forge-admin-token-values.yaml:adminTokenSecret is not supported" \
	"ops/helm/ploeg/ci/reject-runtimeclass-non-string-values.yaml:runtimeClassName" \
	"ops/helm/ploeg/ci/reject-runtimeclass-invalid-name-values.yaml:runtimeClassName" \
	"ops/helm/ploeg/ci/reject-telemetry-without-endpoint-values.yaml:endpoint" \
	"ops/helm/ploeg/ci/reject-telemetry-unknown-opt-in-values.yaml:allowHarnesses" \
	"ops/helm/ploeg/ci/reject-claude-plugin-ploeg-secret-values.yaml:holds a Ploeg credential" \
	"ops/helm/ploeg/ci/reject-claude-plugin-run-identity-key-values.yaml:holds a Ploeg credential" \
	"ops/helm/ploeg/ci/reject-claude-plugin-transaction-token-key-values.yaml:holds a Ploeg credential" \
	"ops/helm/ploeg/ci/reject-hmac-off-without-transaction-token-values.yaml:hmacCapability off needs transactionToken.signingKeySecret" \
	"ops/helm/ploeg/ci/reject-transaction-token-team-without-sponsor-values.yaml:sponsor is required with transactionToken.signingKeySecret" \
	"ops/helm/ploeg/ci/reject-claude-plugin-moving-ref-values.yaml:commit" \
	"ops/helm/ploeg/ci/reject-missing-database-values.yaml:database.existingSecret.name is required" \
	"ops/helm/ploeg/ci/reject-missing-litellm-admin-url-values.yaml:executor.litellm.adminUrl is required" \
	"ops/helm/ploeg/ci/reject-missing-gateway-values.yaml:executor.litellm.baseUrl is required" \
	"ops/helm/ploeg/ci/reject-missing-forge-url-values.yaml:executor.forgejo.url is required" \
	"ops/helm/ploeg/ci/reject-missing-scaler-host-values.yaml:executor.scaler.host is required" \
	"ops/helm/ploeg/ci/reject-missing-agent-image-values.yaml:no agent image" \
	"ops/helm/ploeg/ci/reject-mcp-groups-without-team-values.yaml:mcpAccessGroups needs" \
	"ops/helm/ploeg/ci/reject-mcp-login-replicas-values.yaml:mcp.replicas must be 1" \
	"ops/helm/ploeg/ci/reject-harnesses-without-plan-values.yaml:harnesses needs a plan" \
	"ops/helm/ploeg/ci/reject-models-without-default-values.yaml:lists models without its default model" \
	"ops/helm/ploeg/ci/reject-claude-environment-without-login-values.yaml:requires harness.claudeEnvironment.environmentId and loginSecret or loginVolume" \
	"ops/helm/ploeg/ci/reject-client-carrier-without-environment-values.yaml:carrier client requires harness.claudeEnvironment.environmentId" \
	"ops/helm/ploeg/ci/reject-claude-runner-without-environment-secret-values.yaml:claudeRunner.environmentSecret.name is required" \
	"ops/helm/ploeg/ci/reject-dind-without-acknowledgement-values.yaml:harness.dind is true without harness.acknowledgePrivilegedDind: true" \
	"ops/helm/ploeg/ci/reject-worker-admission-with-dind-values.yaml:security.workerAdmission forbids privileged DinD" \
	"ops/helm/ploeg/ci/reject-separate-harness-container-with-dind-values.yaml:a separate harness container needs both credential proxies and no DinD" \
	"ops/helm/ploeg/ci/reject-unknown-harness-container-values.yaml:value must be one of 'separate', 'shared'"; do
	values=${case%%:*}
	expected=${case#*:}
	if out=$(helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$values" 2>&1); then
		echo "chart rendered '$values', which it must refuse"
		status=1
	elif ! printf '%s' "$out" | grep -qF "$expected"; then
		echo "chart refused '$values' for the wrong reason (want '$expected'):"
		echo "$out"
		status=1
	fi
done

P1_VALUES=ops/helm/ploeg/ci/executor-default-worker-values.yaml
P1_CHART=ops/helm/ploeg
leaves_p1() {
	expected=$1
	shift
	if out=$(helm template ploeg "$P1_CHART" -f "$REQUIRED" -f "$P1_VALUES" "$@" 2>&1); then
		echo "chart rendered $*, which leaves profile P1 under supportProfile: P1"
		status=1
	elif ! printf '%s' "$out" | grep -qF -- "$expected"; then
		echo "chart refused $* for the wrong reason (want '$expected'):"
		echo "$out"
		status=1
	fi
	if ! helm template ploeg "$P1_CHART" -f "$REQUIRED" -f "$P1_VALUES" "$@" --set supportProfile=unsupported >/dev/null 2>&1; then
		echo "chart refused $* with supportProfile: unsupported, which must render it"
		status=1
	fi
}

leaves_p1 'networkPolicy.enabled is false' --set networkPolicy.enabled=false
leaves_p1 'executor.workerAuth.mode is legacy' --set executor.workerAuth.mode=legacy
leaves_p1 'claudeRunner.enabled is true' --set claudeRunner.enabled=true --set claudeRunner.environmentSecret.name=claude-runner-environment
leaves_p1 'team defaults: the sandbox executor resolves no runtimeClassName' --set executor.type=sandbox
leaves_p1 'team defaults: the sandbox executor resolves no runtimeClassName' --set 'executor.teams[0].executorType=sandbox'
leaves_p1 'team defaults/reviewer: the sandbox executor resolves no runtimeClassName' --set 'executor.teams[0].executorType=sandbox' --set 'executor.teams[0].plan[0].roles[0].name=builder' --set 'executor.teams[0].plan[0].roles[0].writes=true' --set 'executor.teams[0].plan[0].roles[0].sandbox.runtimeClassName=kata' --set 'executor.teams[0].plan[1].roles[0].name=reviewer' --set 'executor.teams[0].plan[1].roles[0].writes=false' --set executor.forgejo.readTokenSecret.name=agent-reader-token --set executor.forgejo.readTokenSecret.key=FORGEJO_TOKEN
leaves_p1 'security.workerAdmission.enabled is false; P1 needs the chart' --set security.workerAdmission.enabled=false
leaves_p1 'team defaults: the sandbox executor runs while security.workerAdmission.runtimeClassNames is empty' --set executor.type=sandbox --set executor.sandbox.runtimeClassName=kata
leaves_p1 'team defaults: the sandbox executor runs while security.workerAdmission.runtimeClassNames is empty' --set 'executor.teams[0].executorType=sandbox' --set 'executor.teams[0].sandbox.runtimeClassName=kata'
leaves_p1 'team defaults: runtimeClassName "kata" is not in security.workerAdmission.runtimeClassNames ["gvisor"]' --set executor.type=sandbox --set executor.sandbox.runtimeClassName=kata --set 'security.workerAdmission.runtimeClassNames={gvisor}'
leaves_p1 'team defaults: runtimeClassName "" is not in security.workerAdmission.runtimeClassNames ["kata"]' --set 'security.workerAdmission.runtimeClassNames={kata}'
leaves_p1 'executor.litellm.keyIsolation is not proxy' --set executor.litellm.keyIsolation=
leaves_p1 'executor.forgeTokenIsolation is not proxy' --set executor.forgeTokenIsolation=
leaves_p1 'harness.credentialIsolation is set to "proxy"; P1 has no credentialIsolation override' --set 'executor.teams[0].harness.credentialIsolation=proxy'
leaves_p1 'team defaults harness claude-code: the harness is not qualified with both credential proxies on' --set 'executor.teams[0].harness.name=claude-code'
leaves_p1 'team defaults harness acp/opencode: the harness is not qualified with both credential proxies on' --set 'executor.teams[0].harness.name=acp' --set 'executor.teams[0].harness.acp.profile=opencode'
leaves_p1 'team defaults harness openhands: harness.dind is true' --set 'executor.teams[0].harness.dind=true' --set 'executor.teams[0].harness.acknowledgePrivilegedDind=true'
leaves_p1 'team defaults harness openhands: harnessContainer resolves to shared' --set executor.harnessContainer=shared
leaves_p1 'executor.workerSecurityContext (runAsNonRoot is not true, runAsUser is 0' --set executor.workerSecurityContext.runAsNonRoot=false --set executor.workerSecurityContext.runAsUser=0
leaves_p1 'executor.workerSecurityContext (allowPrivilegeEscalation is not false, capabilities.drop lacks ALL, seccompProfile.type is not RuntimeDefault or Localhost)' --set executor.workerSecurityContext.allowPrivilegeEscalation=true --set 'executor.workerSecurityContext.capabilities.drop=null' --set executor.workerSecurityContext.seccompProfile.type=Unconfined
leaves_p1 'workerContainerSecurityContext) (privileged is true, capabilities.add is set, readOnlyRootFilesystem is not true)' --set executor.workerContainerSecurityContext.privileged=true --set 'executor.workerContainerSecurityContext.capabilities.add={NET_ADMIN}' --set executor.workerContainerSecurityContext.readOnlyRootFilesystem=false
leaves_p1 'team defaults harness openhands: the harness image registry.example/agent-runner:1.3.0 (executor.harness.image) is not pinned by digest' --set executor.harness.image=registry.example/agent-runner:1.3.0
leaves_p1 'team defaults harness openhands: the harness image registry.example/agent-runner:latest (executor.teams[defaults].harness.image) is not pinned by digest' --set 'executor.teams[0].harness.image=registry.example/agent-runner:latest'
leaves_p1 'team defaults/builder harness openhands: the harness image registry.example/builder:2.0.0 (executor.teams[defaults].plan[].roles[builder].harness.image) is not pinned by digest' --set 'executor.teams[0].plan[0].roles[0].name=builder' --set 'executor.teams[0].plan[0].roles[0].writes=true' --set 'executor.teams[0].plan[0].roles[0].harness.image=registry.example/builder:2.0.0'
leaves_p1 'the harness image registry.example/agent-runner:1.3.0@sha256:db06 (executor.harness.image) is not pinned by digest' --set executor.harness.image=registry.example/agent-runner:1.3.0@sha256:db06
leaves_p1 'team defaults harness openhands: the go toolchain image docker.io/library/golang:1.27-alpine (executor.harness.toolchains[go].image) is not pinned by digest' --set 'executor.harness.toolchains[0].name=go' --set 'executor.harness.toolchains[0].image=docker.io/library/golang:1.27-alpine'
leaves_p1 '(worker.image) is not pinned by digest' --set worker.image.digest=
leaves_p1 'the worker image registry.example/ploeg-worker:0.2.0 (executor.workerImage) is not pinned by digest' --set executor.workerImage=registry.example/ploeg-worker:0.2.0
leaves_p1 'the worker-bin image registry.example/ploegd:0.2.0 (executor.workerImage) is not pinned by digest' --set executor.harnessContainer=shared --set executor.workerImage=registry.example/ploegd:0.2.0
leaves_p1 'the sandbox launcher image ghcr.io/ploeg-hq/ploegd:' --set executor.type=sandbox --set executor.sandbox.runtimeClassName=kata --set image.digest= --set 'security.workerAdmission.runtimeClassNames={kata}'
leaves_p1 'the dind image docker.io/library/docker:29.8.2-dind (executor.dindImage) is not pinned by digest' --set 'executor.teams[0].harness.dind=true' --set 'executor.teams[0].harness.acknowledgePrivilegedDind=true' --set executor.dindImage=docker.io/library/docker:29.8.2-dind
leaves_p1 'the claude-runner image ghcr.io/ploeg-hq/claude-runner:latest (claudeRunner.image) is not pinned by digest' --set claudeRunner.enabled=true --set claudeRunner.environmentSecret.name=claude-runner-environment --set claudeRunner.image.tag=latest
leaves_p1 'team defaults resolves no spend cap (executor.spendCap, executor.teams[].spendCap or config.teams.defaults.spendCap)' --set executor.spendCap.usd=0
leaves_p1 'team defaults resolves no spend cap' --set 'executor.teams[0].spendCap.usd=0'
leaves_p1 'team defaults resolves no spend cap' --set config.teams.defaults.spendCap.usd=0
leaves_p1 'team defaults resolves no spend cap' --set executor.spendCap=null
leaves_p1 'PLOEG_MCP_ALLOW_LOOPBACK_HTTP is true on ploeg-mcp; P1 needs an https issuer' -f ops/helm/ploeg/ci/mcp-values.yaml --set-string mcp.env.PLOEG_MCP_ALLOW_LOOPBACK_HTTP=true
leaves_p1 'PLOEG_MCP_ALLOW_LOOPBACK_HTTP is true on ploeg-mcp; P1 needs an https issuer' -f ops/helm/ploeg/ci/mcp-values.yaml --set 'mcp.extraEnv[0].name=PLOEG_MCP_ALLOW_LOOPBACK_HTTP' --set-string 'mcp.extraEnv[0].value=true'
if out=$(helm template ploeg ops/helm/ploeg -f "$REQUIRED" --set supportProfile=P2 2>&1) || ! printf '%s' "$out" | grep -qF "value must be one of 'P1', 'unsupported'"; then
	echo "chart did not refuse supportProfile P2 by its schema:"
	echo "$out"
	status=1
fi
renders_p1() {
	if ! out=$(helm template ploeg "$P1_CHART" -f "$REQUIRED" -f "$P1_VALUES" "$@" 2>&1); then
		echo "chart refused $* under supportProfile: P1, which it must render:"
		echo "$out"
		status=1
	fi
}

renders_p1 --set executor.type=sandbox --set executor.sandbox.runtimeClassName=kata --set 'security.workerAdmission.runtimeClassNames={kata}'
renders_p1 --set executor.type=sandbox --set executor.runtimeClassName=kata --set 'security.workerAdmission.runtimeClassNames={kata}'
renders_p1 --set 'executor.teams[0].executorType=sandbox' --set 'executor.teams[0].sandbox.runtimeClassName=kata' --set 'security.workerAdmission.runtimeClassNames={kata}'
renders_p1 --set security.workerAdmission.enabled=true
renders_p1 -f ops/helm/ploeg/ci/mcp-values.yaml --set-string mcp.env.PLOEG_MCP_ALLOW_LOOPBACK_HTTP=false
renders_p1 --set executor.runtimeClassName=kata --set 'security.workerAdmission.runtimeClassNames={kata}'
renders_p1 --set 'executor.teams[0].plan[0].roles[0].name=builder' --set 'executor.teams[0].plan[0].roles[0].writes=true' --set 'executor.teams[0].plan[0].roles[0].harness.image=registry.example/builder:2.0.0@sha256:db06af1d9c0969ed594a21ee2b3369a79d6e91abb36b94d29223918581963027'
renders_p1 --set 'executor.teams[0].harness.image=registry.example/agent-runner@sha256:db06af1d9c0969ed594a21ee2b3369a79d6e91abb36b94d29223918581963027'
renders_p1 --set 'executor.harness.toolchains[0].name=go' --set 'executor.harness.toolchains[0].image=docker.io/library/golang:1.27-alpine@sha256:db06af1d9c0969ed594a21ee2b3369a79d6e91abb36b94d29223918581963027'
renders_p1 --set executor.workerImage=registry.example/ploeg-worker:0.2.0@sha256:db06af1d9c0969ed594a21ee2b3369a79d6e91abb36b94d29223918581963027
renders_p1 -f ops/helm/ploeg/ci/executor-public-endpoints-values.yaml
renders_p1 --set networkPolicy.privateCidrs=null
PLOEGD_CALLERS=ops/helm/ploeg/ci/ploegd-callers-values.yaml
renders_p1
renders_p1 -f "$PLOEGD_CALLERS"
chart_from_0_3=$(mktemp -d)
cp -R ops/helm/ploeg "$chart_from_0_3/ploeg"
sed 's/^version:.*/version: 0.3.0/' ops/helm/ploeg/Chart.yaml >"$chart_from_0_3/ploeg/Chart.yaml"
P1_CHART=$chart_from_0_3/ploeg
leaves_p1 'networkPolicy.ploegd.ingressFrom is empty, so ploegd'"'"'s NetworkPolicy admits port 8080 from every pod in every namespace'
leaves_p1 'networkPolicy.ploegd.ingressFrom is empty' --set 'networkPolicy.ploegd.ingressFrom=null'
renders_p1 -f "$PLOEGD_CALLERS"
ploegd_ingress=$(helm template ploeg "$P1_CHART" -f "$REQUIRED" -f "$P1_VALUES" -f "$PLOEGD_CALLERS" -s templates/networkpolicy.yaml 2>&1 | awk '/^  name: ploeg-ploegd$/ { found = 1 } found && /^  egress:/ { exit } found')
for caller in 'kubernetes.io/metadata.name: gateway' '- ploeg-worker' '- ploeg-sandbox-launcher' '- ploeg-mcp' '- ploeg-claude-runner'; do
	if ! printf '%s' "$ploegd_ingress" | grep -qF -- "$caller"; then
		echo "chart 0.3.0 with networkPolicy.ploegd.ingressFrom named renders a ploegd ingress without '$caller':"
		echo "$ploegd_ingress"
		status=1
	fi
done
if out=$(helm template ploeg "$P1_CHART" -f "$REQUIRED" -f "$P1_VALUES" --set networkPolicy.enabled=false 2>&1) || printf '%s' "$out" | grep -qF 'ingressFrom is empty'; then
	echo "chart 0.3.0 with networkPolicy.enabled: false names ingressFrom, which has no policy to fill:"
	echo "$out"
	status=1
fi
P1_CHART=ops/helm/ploeg
rm -rf "$chart_from_0_3"
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" 2>&1)
if ! printf '%s' "$notes" | grep -qF 'WARNING: networkPolicy.ploegd.ingressFrom is empty'; then
	echo "a chart before 0.3.0 renders an empty networkPolicy.ploegd.ingressFrom without its warning"
	status=1
fi
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" -f "$PLOEGD_CALLERS" 2>&1)
if printf '%s' "$notes" | grep -qF 'ingressFrom is empty'; then
	echo "a chart with networkPolicy.ploegd.ingressFrom named still warns that it is empty"
	status=1
fi
renders_p1 --set executor.spendCap.usd=0 --set 'executor.teams[0].spendCap.usd=5' --set 'executor.teams[0].spendCap.period=week'
renders_p1 --set executor.spendCap=null --set config.teams.defaults.spendCap.usd=50 --set config.teams.defaults.spendCap.period=month
if ! helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" --set 'executor.teams[0].spendCap.usd=5' --set 'executor.teams[0].spendCap.period=week' | grep -qF 'value: "{\"defaults\":{\"period\":\"week\",\"usd\":5}}"'; then
	echo "a team's own spendCap does not reach ploegd as PLOEG_TEAM_SPEND_CAPS"
	status=1
fi
for p1 in "" "$P1_VALUES"; do
	if ! out=$(helm template ploeg ops/helm/ploeg -f "$REQUIRED" ${p1:+-f "$p1"} 2>&1); then
		echo "chart refused the P1 values ${p1:-defaults}, which it must render:"
		echo "$out"
		status=1
	fi
done
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" --set networkPolicy.enabled=false --set supportProfile=unsupported 2>&1)
if ! printf '%s' "$notes" | grep -qF -- '- networkPolicy.enabled is false'; then
	echo "an unsupported render does not list its departures from P1 in the notes"
	status=1
fi
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" 2>&1)
if printf '%s' "$notes" | grep -qF 'UNSUPPORTED'; then
	echo "the P1 render carries the unsupported profile's note"
	status=1
fi

BEARER_VALUES=ops/helm/ploeg/ci/operator-bearer-without-claims-values.yaml
BEARER_DEPARTURE='operator consumer console has execute and accepts a person token as the bearer (audiences or clientIds) without requireClaims'
BEARER_WARNING='WARNING: operator consumers console have execute and accept'
renders_p1 -f "$BEARER_VALUES"
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" -f "$BEARER_VALUES" 2>&1)
if ! printf '%s' "$notes" | grep -qF -- "$BEARER_WARNING"; then
	echo "a P1 render before chart 0.3.0 does not warn about an execute consumer that requires no person token claim:"
	echo "$notes"
	status=1
fi
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" -f "$BEARER_VALUES" --set 'operator.consumers[0].requireClaims.groups={ploeg-operators}' 2>&1)
if printf '%s' "$notes" | grep -qF -- 'WARNING: operator consumers'; then
	echo "a consumer that requires a person token claim is still warned about"
	status=1
fi
if out=$(helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" -f "$BEARER_VALUES" --set 'operator.consumers[0].requireClaims.groups=' 2>&1); then
	echo "chart rendered requireClaims with a claim that lists no value"
	status=1
fi
static_consumer='{"operator": {"consumers": [{"name": "static", "teams": ["silver"], "execute": true, "requireClaims": {"groups": ["ploeg-operators"]}, "tokenSecret": {"name": "ploeg-static", "key": "token"}}]}}'
if out=$(printf '%s' "$static_consumer" | helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" -f - 2>&1) || ! printf '%s' "$out" | grep -qF "missing property 'audiences'"; then
	echo "chart rendered requireClaims on a consumer that names no audiences or clientIds:"
	echo "$out"
	status=1
fi
next_minor=$(mktemp -d)
for version in 0.3.0-rc.1 0.3.0; do
	rm -rf "$next_minor/ploeg"
	cp -R ops/helm/ploeg "$next_minor/ploeg"
	sed "s/^version:.*/version: $version/" ops/helm/ploeg/Chart.yaml >"$next_minor/ploeg/Chart.yaml"
	if out=$(helm template ploeg "$next_minor/ploeg" -f "$REQUIRED" -f "$P1_VALUES" -f "$PLOEGD_CALLERS" -f "$BEARER_VALUES" 2>&1); then
		echo "chart $version rendered an execute consumer that requires no person token claim under supportProfile: P1"
		status=1
	elif ! printf '%s' "$out" | grep -qF -- "$BEARER_DEPARTURE"; then
		echo "chart $version refused the bearer consumer for the wrong reason:"
		echo "$out"
		status=1
	fi
	for passing in "--set=operator.consumers[0].requireClaims.groups={ploeg-operators}" "--set=operator.consumers[0].execute=false" "--set=supportProfile=unsupported"; do
		if ! out=$(helm template ploeg "$next_minor/ploeg" -f "$REQUIRED" -f "$P1_VALUES" -f "$PLOEGD_CALLERS" -f "$BEARER_VALUES" "$passing" 2>&1); then
			echo "chart $version refused the bearer consumer with $passing, which it must render:"
			echo "$out"
			status=1
		fi
	done
	notes=$(helm install --dry-run=client ploeg "$next_minor/ploeg" -f "$REQUIRED" -f "$P1_VALUES" -f "$PLOEGD_CALLERS" -f "$BEARER_VALUES" --set supportProfile=unsupported 2>&1)
	if ! printf '%s' "$notes" | grep -qF -- "- $BEARER_DEPARTURE" || printf '%s' "$notes" | grep -qF -- "$BEARER_WARNING"; then
		echo "chart $version under supportProfile: unsupported does not list the bearer consumer as a departure in place of the warning:"
		echo "$notes"
		status=1
	fi
done
rm -rf "$next_minor"

must_render_with() {
	values=$1
	shift
	if ! out=$(helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$values" 2>&1); then
		echo "chart refused '$values', which it must render:"
		echo "$out"
		status=1
		return
	fi
	expecting=present
	for token in "$@"; do
		if [ "$token" = "--" ]; then
			expecting=absent
			continue
		fi
		if [ "$expecting" = present ] && ! printf '%s' "$out" | grep -qF -- "$token"; then
			echo "chart render '$values' lacks '$token'"
			status=1
		elif [ "$expecting" = absent ] && printf '%s' "$out" | grep -qF -- "$token"; then
			echo "chart render '$values' carries '$token'"
			status=1
		fi
	done
}

for p1 in "" "--set supportProfile=unsupported --set security.workerAdmission.enabled=true"; do
	if ! helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" $p1 | grep -qF 'kind: ValidatingAdmissionPolicyBinding'; then
		echo "the worker admission policy is missing from the render ${p1:-under the P1 defaults}"
		status=1
	fi
done
if helm template ploeg ops/helm/ploeg -f "$REQUIRED" -f "$P1_VALUES" --set supportProfile=unsupported | grep -qF 'kind: ValidatingAdmissionPolicy'; then
	echo "supportProfile: unsupported with security.workerAdmission.enabled unset renders the worker admission policy, which only P1 turns on"
	status=1
fi

must_render_with ops/helm/ploeg/ci/executor-default-worker-values.yaml \
	'name: PLOEG_LLM_KEY_ISOLATION' 'name: PLOEG_FORGE_TOKEN_ISOLATION' 'runAsNonRoot: true' \
	-- 'name: dind' 'privileged: true' 'ploeg.webgrip.dev/privileged-dind' 'PLOEG_CREDENTIAL_ISOLATION_OFF_REASON' 'DOCKER_HOST'
must_render_with ops/helm/ploeg/ci/executor-acknowledged-dind-values.yaml \
	'name: dind' 'privileged: true' 'ploeg.webgrip.dev/privileged-dind: "true"' 'name: PLOEG_CREDENTIAL_ISOLATION_OFF_REASON' 'value: "dind"' 'name: DOCKER_CERT_PATH' \
	-- 'name: PLOEG_LLM_KEY_ISOLATION' 'name: PLOEG_FORGE_TOKEN_ISOLATION'

must_render_with ops/helm/ploeg/ci/executor-cronjob-runtimeclass-values.yaml \
	'runtimeClassName: "gvisor"' 'runtimeClassName: "kata"' \
	-- 'sandbox-only'
must_render_with ops/helm/ploeg/ci/executor-runtimeclass-values.yaml \
	'runtimeClassName: "gvisor"' 'runtimeClassName: "kata"' 'runtimeClassName: "gvisor-systrap"' 'runtimeClassName: kata-legacy'

if ! uv run --no-project --with pyyaml==6.0.3 python scripts/check-harness-container.py "$GOLDEN"; then
	status=1
fi
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f ops/helm/ploeg/ci/executor-harness-container-shared-values.yaml 2>&1)
if ! printf '%s' "$notes" | grep -qF 'DEPRECATED: harnessContainer is shared'; then
	echo "the shared harness container renders without its deprecation note"
	status=1
fi
notes=$(helm install --dry-run=client ploeg ops/helm/ploeg -f "$REQUIRED" -f ops/helm/ploeg/ci/executor-default-worker-values.yaml 2>&1)
if printf '%s' "$notes" | grep -qF 'DEPRECATED: harnessContainer'; then
	echo "the default render carries the shared harness container's deprecation note"
	status=1
fi

if [ "$mode" = "check" ] && [ "$status" -ne 0 ]; then
	echo
	echo "If the change is intended, run ./scripts/helm-golden.sh update and"
	echo "commit the result — the diff is the review."
	echo
	echo "Read it carefully when it touches a securityContext, a privileged"
	echo "container, or the ploeg.webgrip.dev/privileged-dind label: that label"
	echo "is what the Kyverno PolicyException in webgrip/homelab-cluster waives."
fi
exit $status
