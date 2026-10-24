{{- define "ploeg.fullname" -}}
{{- .Values.fullnameOverride | default .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ploeg.instance" -}}
{{- .Values.instanceOverride | default .Release.Name -}}
{{- end -}}

{{- define "ploeg.labels" -}}
app.kubernetes.io/name: ploeg
app.kubernetes.io/instance: {{ include "ploeg.instance" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ploeg.selectorLabels" -}}
app.kubernetes.io/name: ploeg
app.kubernetes.io/instance: {{ include "ploeg.instance" . }}
{{- end -}}

{{/*
ploeg.imageRef renders one image reference from a {repository, tag, digest}
block. The digest belongs to the appVersion build, so it is appended only
while the tag is the appVersion; a tag a cluster sets wins, and a tag that
carries its own @sha256 is used as written.
*/}}
{{- define "ploeg.imageRef" -}}
{{- $image := .image | default dict -}}
{{- $tag := $image.tag | default .appVersion -}}
{{- if and $image.digest (eq $tag .appVersion) -}}
{{ $image.repository }}:{{ $tag }}@{{ $image.digest }}
{{- else -}}
{{ $image.repository }}:{{ $tag }}
{{- end -}}
{{- end -}}

{{- define "ploeg.image" -}}
{{ include "ploeg.imageRef" (dict "image" .Values.image "appVersion" .Chart.AppVersion) }}
{{- end -}}

{{- define "ploeg.claudeRunnerImage" -}}
{{ include "ploeg.imageRef" (dict "image" .Values.claudeRunner.image "appVersion" .Chart.AppVersion) }}
{{- end -}}

{{- define "ploeg.mcpName" -}}
{{- .Values.mcp.name | default (printf "%s-mcp" (include "ploeg.fullname" .)) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ploeg.claudeRunnerName" -}}
{{- .Values.claudeRunner.name | default (printf "%s-claude-runner" (include "ploeg.fullname" .)) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ploeg.componentLabels" -}}
app.kubernetes.io/part-of: ploeg
app.kubernetes.io/instance: {{ include "ploeg.instance" .root }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- end -}}

{{/*
ploeg.requireIntegrations fails the render when an external service that
ploegd or its workers need is unset.
*/}}
{{- define "ploeg.requireIntegrations" -}}
{{- $e := .Values.executor -}}
{{- $litellm := $e.litellm | default dict -}}
{{- if and (ne $e.workerAuth.mode "legacy") (not $litellm.adminUrl) -}}
{{- fail "executor.litellm.adminUrl is required while executor.workerAuth.mode is managed: ploegd refuses to start without the LiteLLM management URL, e.g. http://litellm.<namespace>.svc.cluster.local:4000" -}}
{{- end -}}
{{- if $e.enabled -}}
{{- if not $litellm.baseUrl -}}
{{- fail "executor.litellm.baseUrl is required when executor.enabled is true: the model gateway URL workers call, e.g. http://litellm.<namespace>.svc.cluster.local:4000/v1" -}}
{{- end -}}
{{- $forge := include "ploeg.forge" . | fromJson -}}
{{- if not $forge.url -}}
{{- fail (printf "executor.%s.url is required when executor.enabled is true: the forge URL workers clone from and push to" $forge.kind) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.apiUrl" -}}
{{ .Values.executor.apiUrl | default (printf "http://%s:%v" (include "ploeg.fullname" .) .Values.service.port) }}
{{- end -}}

{{- define "ploeg.forge" -}}
{{- $e := .Values.executor -}}
{{- $kind := $e.forge | default "forgejo" -}}
{{- $cfg := (index $e $kind) | default dict -}}
{{- dict
      "kind" $kind
      "url" ($cfg.url | default "")
      "tokenSecret" ($cfg.tokenSecret | default dict)
      "readTokenSecret" ($cfg.readTokenSecret | default dict)
    | toJson -}}
{{- end -}}

{{/*
ploeg.teamRoles expands one team into the distinct Roles that need their own
workload, as a JSON array. Both executors range over this, so they cannot
disagree about how many workloads a team has.

A Role recurring across Rounds is ONE workload doing another stint — the
review→fix loop depends on that — so the list is deduplicated by name. Two
definitions of the same name that differ are a configuration error and fail
the render rather than silently picking one: pod shape is fixed at render
time, and a Role cannot be a small model in round 1 and a large one in
round 3.

A team with no plan yields a single empty entry: one workload, no role, and a
pod byte-identical to the pre-Shift shape.
*/}}
{{- define "ploeg.teamRoles" -}}
{{- $roles := list }}
{{- $seen := dict }}
{{- $team := . }}
{{- range $round := ($team.plan | default list) }}
{{- range $role := ($round.roles | default list) }}
{{- $prev := get $seen $role.name }}
{{- if $prev }}
{{- if ne (toJson $prev) (toJson $role) }}
{{- fail (printf "team %s: role %q is defined twice with different settings; one role is one workload, so its shape must not change between rounds" $team.name $role.name) }}
{{- end }}
{{- else }}
{{- $_ := set $seen $role.name $role }}
{{- $roles = append $roles $role }}
{{- end }}
{{- end }}
{{- end }}
{{- if not $roles }}
{{- $roles = list dict }}
{{- end }}
{{- toJson $roles }}
{{- end -}}

{{/*
ploeg.teamMaxRunning is a team's concurrency cap, 0 when unlimited. It
resolves the way ploegd does: config.teams.<name>.maxRunning wins over
executor.teams[].maxRunning.
Context: (dict "root" $ "team" <team>).
*/}}
{{- define "ploeg.teamMaxRunning" -}}
{{- $fromConfig := dig "teams" .team.name "maxRunning" 0 (.root.Values.config | default dict) | int }}
{{- $fromConfig | default (.team.maxRunning | default 0) | int }}
{{- end -}}

{{/*
ploeg.maxReplicaCount is a workload's KEDA ceiling: the role's or team's
maxReplicaCount (default 1), never above the team's concurrency cap.
Context: (dict "root" $ "team" <team> "role" <role>).
*/}}
{{- define "ploeg.maxReplicaCount" -}}
{{- $replicas := .role.maxReplicaCount | default .team.maxReplicaCount | default 1 | int }}
{{- $cap := include "ploeg.teamMaxRunning" . | int }}
{{- if and (gt $cap 0) (gt $replicas $cap) }}{{ $cap }}{{ else }}{{ $replicas }}{{ end }}
{{- end -}}

{{/*
ploeg.workloadName is the workload's name: <fullname>-worker-<team> for a
plan-less team (unchanged), <fullname>-worker-<team>-<role> for a Role.
Context: (dict "root" $ "team" <team> "role" <role>).
*/}}
{{- define "ploeg.workloadName" -}}
{{- $name := printf "%s-worker-%s" (include "ploeg.fullname" .root) .team.name }}
{{- if .role.name }}{{- $name = printf "%s-%s" $name .role.name }}{{- end }}
{{- with .suffix }}{{- $name = printf "%s-%s" $name . }}{{- end }}
{{- if gt (len $name) 63 }}
{{- fail (printf "workload name %q exceeds 63 characters; shorten the team or role name" $name) }}
{{- end }}
{{- $name }}
{{- end -}}

{{- define "ploeg.isolationQualifiedHarnesses" -}}
{{- list "openhands" "acp/openhands" "exec" | toJson -}}
{{- end -}}

{{- define "ploeg.qualifiedHarnesses" -}}
{{- list "openhands" "acp/openhands" "exec" | toJson -}}
{{- end -}}

{{/*
ploeg.roleWorkloads expands one (team, Role) into a workload per allowed
Harness (ADR-0099), as a JSON array of {role, key, suffix, default,
qualified}. role is the Role entry the pod template renders, with the
allowlist entry as its harness block. A team without harnesses yields its one
configured Harness with an empty suffix, so its workload renders unchanged.
Context: (dict "root" $ "team" <team> "role" <Role entry or empty dict>).
*/}}
{{- define "ploeg.roleWorkloads" -}}
{{- $root := .root }}
{{- $team := .team }}
{{- $role := .role | default dict }}
{{- $qualified := include "ploeg.qualifiedHarnesses" . | fromJsonArray }}
{{- $out := list }}
{{- if not $team.harnesses }}
{{- if or $role.harnesses $role.defaultHarness }}
{{- fail (printf "team %s: role %q narrows harnesses, but the team lists none in executor.teams[].harnesses" $team.name $role.name) }}
{{- end }}
{{- $id := include "ploeg.roleHarness" (dict "root" $root "team" $team "role" $role) | fromJson }}
{{- $key := include "ploeg.qualificationHarnessName" (dict "name" $id.name "acpProfile" $id.acpProfile) }}
{{- $ce := include "ploeg.claudeEnvironment" (dict "root" $root "team" $team "role" $role) | fromJson }}
{{- $carrier := "" }}
{{- if eq $id.name "claude-environment" }}{{- $carrier = $ce.carrier }}{{- end }}
{{- if and $carrier (not $ce.environmentId) }}
{{- fail (printf "team %s: harness.claudeEnvironment.carrier client requires harness.claudeEnvironment.environmentId" ($role.name | default $team.name)) }}
{{- end }}
{{- $out = append $out (dict "role" $role "key" $key "suffix" "" "default" true "qualified" (has $key $qualified) "carrier" $carrier "environmentId" $ce.environmentId) }}
{{- else }}
{{- if not $role.name }}
{{- fail (printf "team %s: harnesses needs a plan; a team without one runs its single harness" $team.name) }}
{{- end }}
{{- if $role.harness }}
{{- fail (printf "team %s: role %q sets harness while the team lists harnesses; narrow the list with the role's harnesses instead" $team.name $role.name) }}
{{- end }}
{{- $keys := list }}
{{- $teamDefault := "" }}
{{- $entries := list }}
{{- range $entry := $team.harnesses }}
{{- $r := omit $role "harness" "harnesses" "defaultHarness" }}
{{- $_ := set $r "harness" (omit $entry "default") }}
{{- $id := include "ploeg.roleHarness" (dict "root" $root "team" $team "role" $r) | fromJson }}
{{- $key := include "ploeg.qualificationHarnessName" (dict "name" $id.name "acpProfile" $id.acpProfile) }}
{{- if eq $id.name "claude-environment" }}
{{- fail (printf "team %s: claude-environment is paid by the team's Claude plan and cannot be in harnesses (ADR-0084, ADR-0098)" $team.name) }}
{{- end }}
{{- if has $key $keys }}
{{- fail (printf "team %s: harness %q is listed twice in harnesses" $team.name $key) }}
{{- end }}
{{- $keys = append $keys $key }}
{{- if $entry.default }}
{{- if $teamDefault }}
{{- fail (printf "team %s: more than one harnesses entry is the default" $team.name) }}
{{- end }}
{{- $teamDefault = $key }}
{{- end }}
{{- if or (not $role.harnesses) (has $key $role.harnesses) }}
{{- $entries = append $entries (dict "role" $r "key" $key "suffix" (replace "/" "-" $key) "qualified" (has $key $qualified)) }}
{{- end }}
{{- end }}
{{- range $k := ($role.harnesses | default list) }}
{{- if not (has $k $keys) }}
{{- fail (printf "team %s: role %q allows harness %q, which the team's harnesses do not list" $team.name $role.name $k) }}
{{- end }}
{{- end }}
{{- $narrowed := list }}
{{- range $e := $entries }}{{- $narrowed = append $narrowed $e.key }}{{- end }}
{{- $default := $role.defaultHarness | default "" }}
{{- if and $default (not (has $default $narrowed)) }}
{{- fail (printf "team %s: role %q has defaultHarness %q outside its allowed harnesses" $team.name $role.name $default) }}
{{- end }}
{{- if not $default }}
{{- $default = ternary $teamDefault (first $narrowed) (has $teamDefault $narrowed) }}
{{- end }}
{{- range $e := $entries }}
{{- $out = append $out (merge (dict "default" (eq $e.key $default)) $e) }}
{{- end }}
{{- end }}
{{- toJson $out }}
{{- end -}}

{{/*
ploeg.workerWorkloads is ploeg.roleWorkloads without the workloads whose
Runs the person's own Claude Code carries (ADR-0112): no worker claims such
a Run, so the chart renders no ScaledJob, CronJob or sandbox for it. Same
context as ploeg.roleWorkloads.
*/}}
{{- define "ploeg.workerWorkloads" -}}
{{- $out := list }}
{{- range $w := (include "ploeg.roleWorkloads" . | fromJsonArray) }}
{{- if ne ($w.carrier | default "") "client" }}
{{- $out = append $out $w }}
{{- end }}
{{- end }}
{{- toJson $out }}
{{- end -}}

{{/*
ploeg.claudeEnvironment resolves a (team, Role) workload's claude-environment
environmentId and carrier through the role -> team -> global chain, as JSON.
Context: (dict "root" $ "team" <team entry> "role" <Role entry or empty dict>).
*/}}
{{- define "ploeg.claudeEnvironment" -}}
{{- $gce := (.root.Values.executor.harness | default dict).claudeEnvironment | default dict }}
{{- $tce := (.team.harness | default dict).claudeEnvironment | default dict }}
{{- $rce := ((.role | default dict).harness | default dict).claudeEnvironment | default dict }}
{{- dict "environmentId" ($rce.environmentId | default ($tce.environmentId | default ($gce.environmentId | default ""))) "carrier" ($rce.carrier | default ($tce.carrier | default ($gce.carrier | default ""))) | toJson }}
{{- end -}}

{{- define "ploeg.qualificationHarnessName" -}}
{{- if eq .name "acp" -}}
{{- printf "acp/%s" (.acpProfile | default "opencode") -}}
{{- else -}}
{{- .name -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.harnessContainer" -}}
{{- $value := .root.Values.executor.harnessContainer | default "separate" -}}
{{- if hasKey .override "value" -}}{{- $value = .override.value -}}{{- end -}}
{{- if not (has $value (list "separate" "shared")) -}}
{{- fail (printf "team %s: harnessContainer must be separate or shared, not %q" .who $value) -}}
{{- end -}}
{{- $legacy := "" -}}
{{- if eq .name "claude-environment" -}}
{{- $legacy = "harness claude-environment runs its claude CLI in the worker's container" -}}
{{- else if .dind -}}
{{- $legacy = "harness.dind is true" -}}
{{- else if .isolation.offReason -}}
{{- $legacy = printf "a credential proxy is off (%s)" .isolation.offReason -}}
{{- end -}}
{{- if and $legacy (eq $value "separate") -}}
{{- if hasKey .override "value" -}}
{{- fail (printf "team %s: harness.harnessContainer is separate, but %s; a separate harness container needs both credential proxies and no DinD (ADR-0138). Set harness.harnessContainer: shared for this workload" .who $legacy) -}}
{{- end -}}
{{- $value = "shared" -}}
{{- end -}}
{{- $value -}}
{{- end -}}

{{- define "ploeg.workerImage" -}}
{{ include "ploeg.imageRef" (dict "image" .Values.worker.image "appVersion" .Chart.AppVersion) }}
{{- end -}}

{{- define "ploeg.workerContainerSecurityContext" -}}
{{- $harness := .Values.executor.workerSecurityContext | default dict -}}
{{- $context := dict "runAsNonRoot" true "runAsUser" ($harness.runAsUser | default 1000) "runAsGroup" ($harness.runAsGroup | default 1000) "allowPrivilegeEscalation" false "readOnlyRootFilesystem" true "capabilities" (dict "drop" (list "ALL")) "seccompProfile" (dict "type" "RuntimeDefault") -}}
{{- toYaml (mergeOverwrite $context (.Values.executor.workerContainerSecurityContext | default dict)) -}}
{{- end -}}

{{- define "ploeg.credentialIsolation" -}}
{{- $llmKey := .root.Values.executor.litellm.keyIsolation | default "" -}}
{{- $forgeToken := .root.Values.executor.forgeTokenIsolation | default "" -}}
{{- $offReason := "" -}}
{{- if hasKey .override "value" -}}
{{- $llmKey = .override.value | default "" -}}
{{- $forgeToken = .override.value | default "" -}}
{{- else if .dind -}}
{{- $llmKey = "" -}}
{{- $forgeToken = "" -}}
{{- $offReason = "dind" -}}
{{- else if not (has .harness (include "ploeg.isolationQualifiedHarnesses" . | fromJsonArray)) -}}
{{- $llmKey = "" -}}
{{- $forgeToken = "" -}}
{{- $offReason = "unqualified" -}}
{{- end -}}
{{- if and (not $offReason) (or (not $llmKey) (not $forgeToken)) -}}
{{- $offReason = "disabled" -}}
{{- end -}}
{{- dict "llmKey" $llmKey "forgeToken" $forgeToken "offReason" $offReason | toJson -}}
{{- end -}}

{{/*
ploeg.roleHarness resolves a (team, Role) workload's Harness name and acp
profile through the role -> team -> global chain, as JSON {name, acpProfile}.
The worker pod template and ploegd's PLOEG_TEAM_HARNESSES both read it, so
what ploegd says a Role is configured with cannot drift from the worker's
PLOEG_HARNESS and PLOEG_ACP_PROFILE (ADR-0097). Context: (dict "root" $
"team" <team entry> "role" <Role entry or empty dict>).
*/}}
{{- define "ploeg.roleHarness" -}}
{{- $gh := .root.Values.executor.harness | default dict }}
{{- $th := .team.harness | default dict }}
{{- $rh := (.role | default dict).harness | default dict }}
{{- $ga := $gh.acp | default dict }}
{{- $ta := $th.acp | default dict }}
{{- $ra := $rh.acp | default dict }}
{{- dict "name" ($rh.name | default ($th.name | default ($gh.name | default "openhands"))) "acpProfile" ($ra.profile | default ($ta.profile | default $ga.profile)) | toJson }}
{{- end -}}

{{/*
ploeg.workerPodTemplate renders the worker pod template for one team —
shared by every executor (ScaledJob, CronJob). Context: (dict "root" $
"team" <team entry>). The team's optional `harness` block overrides the
global executor.harness defaults field-by-field (explicit hasKey checks, so
`dind: false` overrides correctly — sprig merge would drop it).
*/}}
{{- define "ploeg.workerPodTemplate" -}}
{{- $root := .root }}
{{- $team := .team }}
{{/* role is the (team, role) workload's Role entry; empty dict = a plan-less
     team, whose single workload is byte-identical to the pre-Shift shape. The
     override chain gains a tier: role -> team -> global. */}}
{{- $role := .role | default dict }}
{{- $gh := $root.Values.executor.harness | default dict }}
{{- $th := $team.harness | default dict }}
{{- $rh := $role.harness | default dict }}
{{- $identity := include "ploeg.roleHarness" (dict "root" $root "team" $team "role" $role) | fromJson }}
{{- $hName := $identity.name }}
{{- $hImage := $rh.image | default ($th.image | default ($gh.image | default $root.Values.executor.runnerImage)) }}
{{- if eq $hName "claude-environment" }}
{{- $globalImage := "" }}
{{- if eq ($gh.name | default "") "claude-environment" }}{{- $globalImage = $gh.image }}{{- end }}
{{- $hImage = $rh.image | default ($th.image | default ($globalImage | default (include "ploeg.claudeRunnerImage" $root))) }}
{{- end }}
{{- if not $hImage }}
{{- fail (printf "team %s: no agent image; set executor.harness.image, or harness.image on the team or Role. Ploeg publishes no agent image" $team.name) }}
{{- end }}
{{- $hEntrypoint := $rh.entrypoint | default ($th.entrypoint | default $gh.entrypoint) }}
{{- $hArgs := $rh.args | default ($th.args | default $gh.args) }}
{{- $hOutcomeFile := $rh.outcomeFile | default ($th.outcomeFile | default $gh.outcomeFile) }}
{{- $hTimeout := $rh.timeout | default ($th.timeout | default $gh.timeout) }}
{{- $hIdleTimeout := $rh.idleTimeout | default ($th.idleTimeout | default $gh.idleTimeout) }}
{{- $hToolchains := $rh.toolchains | default ($th.toolchains | default $gh.toolchains) }}
{{- $hVerify := $rh.verify | default ($th.verify | default $gh.verify) }}
{{- $hVerifyTimeout := $rh.verifyTimeout | default ($th.verifyTimeout | default $gh.verifyTimeout) }}
{{- $hTelemetry := $rh.telemetry | default ($th.telemetry | default $gh.telemetry) }}
{{- /* claude-code Team plugins (ADR-0139): the most specific list wins whole,
     rendered only into a claude-code pod. A Secret-backed variable becomes a
     PLOEG_CLAUDE_PLUGIN_ENV_<plugin>_<var> secretKeyRef, the only variables
     the worker lets a plugin declaration read. */}}
{{- $gcc := $gh.claudeCode | default dict }}
{{- $tcc := $th.claudeCode | default dict }}
{{- $rcc := $rh.claudeCode | default dict }}
{{- $ccPlugins := $rcc.plugins | default ($tcc.plugins | default $gcc.plugins) }}
{{- $ccPluginJson := list }}
{{- $ccPluginSecrets := list }}
{{- if eq $hName "claude-code" }}
{{- $ploegSecrets := list $root.Values.executor.litellm.masterKeySecret.name }}
{{- with $root.Values.executor.workerAuth.bootstrapSecret }}{{- $ploegSecrets = append $ploegSecrets .name }}{{- end }}
{{- with $root.Values.executor.workerAuth.staticInferenceSecret }}{{- $ploegSecrets = append $ploegSecrets .name }}{{- end }}
{{- $forgeCfg := include "ploeg.forge" $root | fromJson }}
{{- with $forgeCfg.tokenSecret.name }}{{- $ploegSecrets = append $ploegSecrets . }}{{- end }}
{{- with $forgeCfg.readTokenSecret.name }}{{- $ploegSecrets = append $ploegSecrets . }}{{- end }}
{{- with $root.Values.runIdentity }}{{- with .signingKeySecret }}{{- $ploegSecrets = append $ploegSecrets .name }}{{- end }}{{- end }}
{{- range $i, $p := $ccPlugins }}
{{- $envJson := list }}
{{- range $j, $e := ($p.env | default list) }}
{{- if $e.secret }}
{{- if has $e.secret.name $ploegSecrets }}
{{- fail (printf "team %s: Claude Code plugin %s reads %s from Secret %s, which holds a Ploeg credential; a plugin never receives one (ADR-0139)" $team.name $p.name $e.name $e.secret.name) }}
{{- end }}
{{- $var := printf "PLOEG_CLAUDE_PLUGIN_ENV_%d_%d" $i $j }}
{{- $ccPluginSecrets = append $ccPluginSecrets (dict "var" $var "secret" $e.secret) }}
{{- $envJson = append $envJson (dict "name" $e.name "fromEnv" $var) }}
{{- else }}
{{- $envJson = append $envJson (dict "name" $e.name "value" ($e.value | default "")) }}
{{- end }}
{{- end }}
{{- $entry := dict "name" $p.name "marketplace" $p.marketplace }}
{{- if $envJson }}{{- $_ := set $entry "env" $envJson }}{{- end }}
{{- if $p.hookTimeout }}{{- $_ := set $entry "hookTimeout" $p.hookTimeout }}{{- end }}
{{- with $p.runIdentity }}{{- $_ := set $entry "runIdentity" (dict "audience" .audience) }}{{- end }}
{{- $ccPluginJson = append $ccPluginJson $entry }}
{{- end }}
{{- end }}
{{- /* Each toolchain is an image volume the kubelet pulls, mounted read-only
     at /opt/ploeg/toolchains/<name>. The worker learns the mounted PATH
     directories from PLOEG_TOOLCHAINS, so the Run's sandbox needs no
     registry egress (Ploeg ADR-0035). */}}
{{- $toolchainEnv := list }}
{{- range $tc := $hToolchains }}
{{- $mount := printf "/opt/ploeg/toolchains/%s" $tc.name }}
{{- $paths := list }}
{{- range $p := ($tc.path | default (list "/bin")) }}
{{- $paths = append $paths (printf "%s/%s" $mount (trimPrefix "/" $p)) }}
{{- end }}
{{- $toolchainEnv = append $toolchainEnv (dict "name" $tc.name "path" $paths "env" ($tc.env | default dict)) }}
{{- end }}
{{- $hDind := eq (include "ploeg.roleUsesDind" (dict "root" $root "team" $team "role" $role)) "true" }}
{{- if and $root.Values.security.workerAdmission.enabled $hDind }}
{{- fail (printf "team %s: security.workerAdmission forbids privileged DinD; set harness.dind=false at every applicable override" $team.name) }}
{{- end }}
{{- $dt := $root.Values.executor.defaultTarget | default dict }}
{{/* acp harness: same field-by-field override, one level deeper. */}}
{{- $ga := $gh.acp | default dict }}
{{- $ta := $th.acp | default dict }}
{{- $ra := $rh.acp | default dict }}
{{- $acpProfile := $identity.acpProfile }}
{{- $acpArgv := $ra.argv | default ($ta.argv | default $ga.argv) }}
{{- $acpPerm := $ra.permissionMode | default ($ta.permissionMode | default $ga.permissionMode) }}
{{- $acpPrompt := $ra.promptTimeout | default ($ta.promptTimeout | default $ga.promptTimeout) }}
{{- $acpIdle := $ra.idleTimeout | default ($ta.idleTimeout | default $ga.idleTimeout) }}
{{- $acpConfig := $ra.configJson | default ($ta.configJson | default $ga.configJson) }}
{{- $who := $team.name }}{{- if $role.name }}{{- $who = printf "%s/%s" $team.name $role.name }}{{- end }}
{{- $isolationOverride := dict }}
{{- if hasKey $rh "credentialIsolation" }}{{- $isolationOverride = dict "value" $rh.credentialIsolation }}{{- else if hasKey $th "credentialIsolation" }}{{- $isolationOverride = dict "value" $th.credentialIsolation }}{{- else if hasKey $gh "credentialIsolation" }}{{- $isolationOverride = dict "value" $gh.credentialIsolation }}{{- end }}
{{- $isolation := include "ploeg.credentialIsolation" (dict "root" $root "harness" (include "ploeg.qualificationHarnessName" (dict "name" $hName "acpProfile" $acpProfile)) "dind" $hDind "override" $isolationOverride) | fromJson }}
{{- $layoutOverride := dict }}
{{- if hasKey $rh "harnessContainer" }}{{- $layoutOverride = dict "value" $rh.harnessContainer }}{{- else if hasKey $th "harnessContainer" }}{{- $layoutOverride = dict "value" $th.harnessContainer }}{{- end }}
{{- $separate := eq (include "ploeg.harnessContainer" (dict "root" $root "name" $hName "dind" $hDind "isolation" $isolation "override" $layoutOverride "who" $who)) "separate" }}
{{- $workerImage := $root.Values.executor.workerImage | default (include "ploeg.workerImage" $root) }}
{{- /* claude-environment harness (ADR-0084): same override chain. */}}
{{- $gce := $gh.claudeEnvironment | default dict }}
{{- $tce := $th.claudeEnvironment | default dict }}
{{- $rce := $rh.claudeEnvironment | default dict }}
{{- $ceEnvironment := $rce.environmentId | default ($tce.environmentId | default $gce.environmentId) }}
{{- $ceLoginSecret := $rce.loginSecret | default ($tce.loginSecret | default $gce.loginSecret) }}
{{- $cePoll := $rce.pollInterval | default ($tce.pollInterval | default $gce.pollInterval) }}
{{- $ceLoginVolume := $rce.loginVolume | default ($tce.loginVolume | default $gce.loginVolume) }}
{{- if and (eq $hName "claude-environment") (or (not $ceEnvironment) (and (not $ceLoginSecret) (not $ceLoginVolume))) }}
{{- fail (printf "team %s: harness claude-environment requires harness.claudeEnvironment.environmentId and loginSecret or loginVolume" $who) }}
{{- end }}
{{- if and (eq $hName "acp") (eq ($acpProfile | default "opencode") "custom") (not $acpArgv) }}
{{- fail (printf "team %s: harness.acp.profile=custom requires harness.acp.argv" $who) }}
{{- end -}}
metadata:
  labels:
    app.kubernetes.io/name: ploeg-worker
    ploeg.webgrip.dev/team: {{ $team.name }}
    {{- if $role.name }}
    ploeg.webgrip.dev/role: {{ $role.name }}
    {{- end }}
    {{- if $hDind }}
    # Names the HAZARD, not the workload: this pod carries a privileged
    # docker:dind sidecar, and the ploeg-worker PolicyException is keyed on
    # exactly this label. A pod without dind never carries it and is held to
    # the full baseline — so adding a Role costs no security-repo change, and
    # readers are not waived for a privilege they do not take.
    ploeg.webgrip.dev/privileged-dind: "true"
    {{- end }}
spec:
  restartPolicy: Never
  # The shutdown budget for ploeg-worker's SIGTERM path: abort the harness,
  # revoke the per-run credential, settle its spend, report the outcome. The
  # 30s default SIGKILLed the pod mid-report, which is the silence that made a
  # killed run indistinguishable from a hung one and cost the Round an attempt.
  terminationGracePeriodSeconds: {{ $root.Values.executor.terminationGracePeriodSeconds }}
  # An identity of the workers' own. Naming none left them on `default`, which
  # both trips require-non-default-serviceaccount and makes every workload in
  # the namespace indistinguishable in an audit log. The token stays unmounted
  # either way — a worker needs no Kubernetes API authority at all.
  serviceAccountName: {{ include "ploeg.workerServiceAccountName" $root }}
  automountServiceAccountToken: false
  {{- with $root.Values.imagePullSecrets }}
  imagePullSecrets: {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $root.Values.executor.nodeSelector }}
  nodeSelector: {{- toYaml . | nindent 4 }}
  {{- end }}
  securityContext:
    fsGroup: 1000
    seccompProfile:
      type: RuntimeDefault
  initContainers:
    # Extracts ploeg-worker from the distroless ploegd image via
    # self-copy (no shell in distroless).
    - name: worker-bin
      image: {{ ternary $workerImage ($root.Values.executor.workerImage | default (include "ploeg.image" $root)) $separate }}
      command: ["/usr/local/bin/ploeg-worker", "install", "/mnt/bin/ploeg-worker"]
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
        seccompProfile:
          type: RuntimeDefault
      volumeMounts:
        - name: worker-bin
          mountPath: /mnt/bin
      resources: {{- toYaml $root.Values.executor.workerBinResources | nindent 8 }}
    {{- if $hDind }}
    # DinD sidecar: the harness sandbox and the repo quality gates need a
    # Docker daemon. Requires the ploeg-worker PolicyException. Teams whose
    # harness needs no Docker set harness.dind: false.
    - name: dind
      image: {{ $root.Values.executor.dindImage }}
      restartPolicy: Always
      securityContext:
        privileged: true
      env:
        - name: DOCKER_TLS_CERTDIR
          value: /certs
      startupProbe:
        tcpSocket:
          port: 2376
        periodSeconds: 2
        failureThreshold: 60
        timeoutSeconds: 5
      volumeMounts:
        - name: docker-certs
          mountPath: /certs
        - name: docker-storage
          mountPath: /var/lib/docker
        - name: ci-shared
          mountPath: /mnt/ci-shared
      resources: {{- toYaml (default $root.Values.executor.dindResources $role.dindResources) | nindent 8 }}
    {{- end }}
    {{- if $separate }}
    - name: harness
      image: {{ $hImage }}
      restartPolicy: Always
      command: ["/mnt/bin/ploeg-worker", "harness-serve", "/run/ploeg/launcher/launcher.sock"]
      volumeMounts:
        - name: worker-bin
          mountPath: /mnt/bin
          readOnly: true
        - name: harness-launcher
          mountPath: /run/ploeg/launcher
          readOnly: true
        - name: ci-shared
          mountPath: /mnt/ci-shared
        {{- range $tc := $hToolchains }}
        - name: toolchain-{{ $tc.name }}
          mountPath: /opt/ploeg/toolchains/{{ $tc.name }}
          readOnly: true
        {{- end }}
      {{- with $root.Values.executor.workerSecurityContext }}
      securityContext: {{- toYaml . | nindent 8 }}
      {{- end }}
      resources: {{- toYaml (default $root.Values.executor.workerResources $role.workerResources) | nindent 8 }}
    {{- end }}
  containers:
    - name: worker
      {{- if $separate }}
      image: {{ $workerImage }}
      command: ["/usr/local/bin/ploeg-worker"]
      {{- else }}
      image: {{ $hImage }}
      command: ["/mnt/bin/ploeg-worker"]
      {{- end }}
      env:
        - name: PLOEG_API_URL
          value: {{ include "ploeg.apiUrl" $root | quote }}
        - name: PLOEG_TEAM
          value: {{ $team.name | quote }}
        {{- if $role.name }}
        - name: PLOEG_ROLE
          value: {{ $role.name | quote }}
        {{- end }}
        - name: PLOEG_HARNESS
          value: {{ $hName | quote }}
        {{- if $hEntrypoint }}
        - name: PLOEG_HARNESS_ENTRYPOINT
          value: {{ $hEntrypoint | quote }}
        {{- end }}
        {{- if $hArgs }}
        - name: PLOEG_HARNESS_ARGS
          value: {{ toJson $hArgs | quote }}
        {{- end }}
        {{- if $hOutcomeFile }}
        - name: PLOEG_OUTCOME_FILE
          value: {{ $hOutcomeFile | quote }}
        {{- end }}
        {{- if $hTimeout }}
        - name: PLOEG_HARNESS_TIMEOUT
          value: {{ $hTimeout | quote }}
        {{- end }}
        {{- if $hIdleTimeout }}
        - name: PLOEG_HARNESS_IDLE_TIMEOUT
          value: {{ $hIdleTimeout | quote }}
        {{- end }}
        {{- if $toolchainEnv }}
        - name: PLOEG_TOOLCHAINS
          value: {{ toJson $toolchainEnv | replace "$(" "$$(" | quote }}
        {{- end }}
        {{- if $hVerify }}
        {{- /* $$ keeps Kubernetes from expanding $(VAR) inside a command line. */}}
        - name: PLOEG_VERIFY_COMMANDS
          value: {{ toJson $hVerify | replace "$(" "$$(" | quote }}
        {{- end }}
        {{- if $hVerifyTimeout }}
        - name: PLOEG_VERIFY_TIMEOUT
          value: {{ $hVerifyTimeout | quote }}
        {{- end }}
        {{- if $hTelemetry }}
        - name: PLOEG_TELEMETRY
          value: {{ toJson $hTelemetry | replace "$(" "$$(" | quote }}
        {{- end }}
        {{- if $ccPluginJson }}
        - name: PLOEG_CLAUDE_PLUGINS
          value: {{ toJson $ccPluginJson | replace "$(" "$$(" | quote }}
        {{- range $ccPluginSecrets }}
        - name: {{ .var }}
          valueFrom:
            secretKeyRef: {name: {{ .secret.name | quote }}, key: {{ .secret.key | quote }}}
        {{- end }}
        {{- end }}
        {{- if eq $hName "acp" }}
        {{- if $acpProfile }}
        - name: PLOEG_ACP_PROFILE
          value: {{ $acpProfile | quote }}
        {{- end }}
        {{- if $acpArgv }}
        - name: PLOEG_ACP_ARGV
          value: {{ toJson $acpArgv | quote }}
        {{- end }}
        {{- if $acpPerm }}
        - name: PLOEG_ACP_PERMISSION_MODE
          value: {{ $acpPerm | quote }}
        {{- end }}
        {{- if $acpPrompt }}
        - name: PLOEG_ACP_PROMPT_TIMEOUT
          value: {{ $acpPrompt | quote }}
        {{- end }}
        {{- if $acpIdle }}
        - name: PLOEG_ACP_IDLE_TIMEOUT
          value: {{ $acpIdle | quote }}
        {{- end }}
        {{- if $acpConfig }}
        - name: PLOEG_ACP_CONFIG_JSON
          value: {{ $acpConfig | quote }}
        {{- end }}
        {{- end }}
        {{- if eq $hName "claude-environment" }}
        - name: PLOEG_CLAUDE_ENVIRONMENT_ID
          value: {{ $ceEnvironment | quote }}
        {{- if $cePoll }}
        - name: PLOEG_CLAUDE_POLL_INTERVAL
          value: {{ $cePoll | quote }}
        {{- end }}
        {{- if $ceLoginVolume }}
        - name: PLOEG_CLAUDE_CONFIG_DIR
          value: /var/lib/ploeg/claude-login
        {{- else }}
        - name: CLAUDE_CODE_OAUTH_REFRESH_TOKEN
          valueFrom:
            secretKeyRef: {name: {{ $ceLoginSecret | quote }}, key: refreshToken}
        - name: CLAUDE_CODE_OAUTH_SCOPES
          valueFrom:
            secretKeyRef: {name: {{ $ceLoginSecret | quote }}, key: scopes}
        {{- end }}
        {{- end }}
        - name: LLM_MODEL
          value: {{ $role.model | default $team.model | quote }}
        {{- /* For a Role, the team's `budget` is the SHIFT POOL, not a per-run
             ceiling — rendering it here would hand one Run the whole pool if
             this fallback ever applied. A planned Run is always minted at the
             claim's authorization instead (ADR-0012); this value stands only
             for a plan-less team, and for a Role it degrades to its own cap.

             `perRunBudget` exists because `budget` alone meant two different
             things depending on whether a team had a plan: the Shift pool for
             bronze, and the per-run key ceiling for silver and copper, which
             have none. One word, two ceilings, and the HelmRelease comments
             drifted from the values in both directions. Set it to say the
             per-run number out loud; `budget` remains the fallback so no
             existing values file changes meaning. */}}
        - name: LITELLM_KEY_BUDGET
          value: {{ $role.cap | default $team.perRunBudget | default $team.budget | quote }}
        - name: LITELLM_KEY_DURATION
          value: {{ $root.Values.executor.litellm.keyDuration | quote }}
        - name: LLM_BASE_URL
          value: {{ $root.Values.executor.litellm.baseUrl | quote }}
        {{- with $isolation.llmKey }}
        - name: PLOEG_LLM_KEY_ISOLATION
          value: {{ . | quote }}
        {{- end }}
        {{- with $isolation.forgeToken }}
        - name: PLOEG_FORGE_TOKEN_ISOLATION
          value: {{ . | quote }}
        {{- end }}
        {{- with $isolation.offReason }}
        - name: PLOEG_CREDENTIAL_ISOLATION_OFF_REASON
          value: {{ . | quote }}
        {{- end }}
        - name: PLOEG_LLM_CREDENTIAL_MODE
          value: {{ ternary "static-compatibility" "managed" (eq $root.Values.executor.workerAuth.mode "legacy") | quote }}
        {{- if eq $root.Values.executor.workerAuth.mode "managed" }}
        - name: PLOEG_WORKER_BOOTSTRAP_TOKEN
          valueFrom:
            secretKeyRef:
              name: {{ $root.Values.executor.workerAuth.bootstrapSecret.name }}
              key: {{ printf "%s--%s" $team.name ($role.name | default "default") | quote }}
        {{- else if $root.Values.executor.workerAuth.staticInferenceSecret }}
        - name: LLM_API_KEY
          valueFrom:
            secretKeyRef:
              name: {{ $root.Values.executor.workerAuth.staticInferenceSecret.name }}
              key: {{ $root.Values.executor.workerAuth.staticInferenceSecret.key }}
        {{- end }}
        {{- $forge := include "ploeg.forge" $root | fromJson }}
        - name: PLOEG_TARGET_FORGE
          value: {{ $forge.kind | quote }}
        - name: FORGE_URL
          value: {{ $forge.url | quote }}
        {{- /* ADR-0013 tier 1: a READING Run gets a read-only forge credential,
             so the writer/reader split is enforced by the forge and not only
             by Ploeg's scheduling. The repos are private, so "no credential at
             all" cannot clone — a read-only token is the honest tier 1. A
             reader never falls back to the read-write builder token: a team
             with a reading Role and no readTokenSecret fails the render, and
             PLOEG_FORGE_TOKEN_ACCESS lets the worker refuse a reading claim
             when the pod was built some other way. */}}
        {{- $isReader := and (ne ($role.name | default "") "") (not $role.writes) }}
        {{- $readSecret := $forge.readTokenSecret }}
        {{- if and $isReader (not (and $readSecret.name $readSecret.key)) }}
        {{- fail (printf "team %s: role %q reads but executor.%s.readTokenSecret is not set; a reading Role must not receive the read-write forge token (ADR-0013 tier 1). Set readTokenSecret.name and readTokenSecret.key to a read-only token" $team.name $role.name $forge.kind) }}
        {{- end }}
        {{- if $role.name }}
        - name: PLOEG_FORGE_TOKEN_ACCESS
          value: {{ ternary "read-only" "read-write" $isReader | quote }}
        {{- end }}
        - name: AGENT_BUILDER_TOKEN
          valueFrom:
            secretKeyRef:
              {{- if $isReader }}
              name: {{ $readSecret.name }}
              key: {{ $readSecret.key }}
              {{- else }}
              name: {{ $forge.tokenSecret.name }}
              key: {{ $forge.tokenSecret.key }}
              {{- end }}
        # FALLBACK target only. The repository belongs to the work item,
        # resolved at ingest from its tracker scope and delivered on the claim
        # (R11, ADR-0001); these render only while a team still pins a repo,
        # and a team that declares none renders no repo env at all.
        {{- $repoOwner := $team.repoOwner | default $dt.owner }}
        {{- $repoName := $team.repoName | default $dt.name }}
        {{- $baseBranch := $team.baseBranch | default $dt.baseBranch }}
        {{- if $repoOwner }}
        - name: REPO_OWNER
          value: {{ $repoOwner | quote }}
        {{- end }}
        {{- if $repoName }}
        - name: REPO_NAME
          value: {{ $repoName | quote }}
        {{- end }}
        {{- if $baseBranch }}
        # Base branch for clone/branch/PR (VIK-589). Unset = the repo's
        # default branch on clone and the worker's historical "main".
        - name: PLOEG_BASE_BRANCH
          value: {{ $baseBranch | quote }}
        {{- end }}
        {{- if $team.targetSource }}
        # env = ignore the claim's target and use this team's pinned repo.
        # The per-team lever for rolling the decoupling forward or back.
        - name: PLOEG_TARGET_SOURCE
          value: {{ $team.targetSource | quote }}
        {{- end }}
        # Downward API: node+pod identity survives pod/job cleanup for
        # run forensics (VIK-597). Logged at worker start and embedded in
        # every checkpoint.
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: POD_UID
          valueFrom:
            fieldRef:
              fieldPath: metadata.uid
        - name: WORK_DIR
          value: /mnt/ci-shared
        {{- if $separate }}
        - name: PLOEG_HARNESS_LAUNCHER
          value: /run/ploeg/launcher/launcher.sock
        {{- with include "ploeg.goMemLimitEnv" (dict "goMemLimit" "" "resources" $root.Values.executor.workerContainerResources) | trim }}{{ . | nindent 8 }}{{- end }}
        {{- end }}
        {{- if $hDind }}
        - name: DOCKER_HOST
          value: tcp://localhost:2376
        - name: DOCKER_CERT_PATH
          value: /certs/client
        - name: DOCKER_TLS_VERIFY
          value: "1"
        {{- end }}
      volumeMounts:
        {{- if $separate }}
        - name: ci-shared
          mountPath: /mnt/ci-shared
        - name: harness-launcher
          mountPath: /run/ploeg/launcher
        - name: worker-tmp
          mountPath: /tmp
      securityContext: {{- include "ploeg.workerContainerSecurityContext" $root | nindent 8 }}
      resources: {{- toYaml $root.Values.executor.workerContainerResources | nindent 8 }}
        {{- else }}
        - name: worker-bin
          mountPath: /mnt/bin
          readOnly: true
        {{- if $hDind }}
        - name: docker-certs
          mountPath: /certs
          readOnly: true
        {{- end }}
        - name: ci-shared
          mountPath: /mnt/ci-shared
        {{- if and (eq $hName "claude-environment") $ceLoginVolume }}
        - name: claude-login
          mountPath: /var/lib/ploeg/claude-login
        {{- end }}
        {{- range $tc := $hToolchains }}
        - name: toolchain-{{ $tc.name }}
          mountPath: /opt/ploeg/toolchains/{{ $tc.name }}
          readOnly: true
        {{- end }}
      {{- /* Per-Role sizing, falling back to the executor-wide default. A
           Round that fans out three readers asks the scheduler for three
           whole writer-sized pods at once, which on a one-node worker pool
           simply does not fit — and the readers do not build anything, so
           they never needed a builder's CPU. Same field-by-field override
           shape as harness above. */}}
      {{- with $root.Values.executor.workerSecurityContext }}
      securityContext: {{- toYaml . | nindent 8 }}
      {{- end }}
      resources: {{- toYaml (default $root.Values.executor.workerResources $role.workerResources) | nindent 8 }}
        {{- end }}
  volumes:
    - name: worker-bin
      emptyDir: {}
    {{- if $separate }}
    - name: harness-launcher
      emptyDir:
        medium: Memory
        sizeLimit: 1Mi
    - name: worker-tmp
      emptyDir:
        sizeLimit: 1Gi
    {{- end }}
    {{- if $hDind }}
    - name: docker-certs
      emptyDir: {}
    - name: docker-storage
      emptyDir: {}
    {{- end }}
    # Disk-backed (not tmpfs): repo checkout + gate builds are large.
    # Same absolute path in worker AND dind so `docker run -v`
    # bind-mounts resolve on the daemon's filesystem.
    - name: ci-shared
      emptyDir:
        sizeLimit: 8Gi
    {{- if and (eq $hName "claude-environment") $ceLoginVolume }}
    - name: claude-login
      persistentVolumeClaim:
        claimName: {{ $ceLoginVolume | quote }}
    {{- end }}
    {{- range $tc := $hToolchains }}
    - name: toolchain-{{ $tc.name }}
      image:
        reference: {{ $tc.image }}
        pullPolicy: IfNotPresent
    {{- end }}
{{- end -}}

{{/*
ploeg.workerServiceAccountName is the identity the worker pods run as.

One helper so the object and the reference cannot disagree. They did: the
create guard used to be `not .Values.executor.serviceAccountName`, reading a
name as "an external account exists", so naming the chart's own default
suppressed the account the pods then referenced and every Job died at
admission with `serviceaccount "ploeg-worker" not found`. create and name are
separate questions and are now separate keys.
*/}}
{{- define "ploeg.workerServiceAccountName" -}}
{{- .Values.executor.serviceAccount.name | default (printf "%s-worker" (include "ploeg.fullname" .)) -}}
{{- end -}}

{{/*
ploeg.roleUsesDind resolves the dind flag for one (team, role) through the
role -> team -> global override chain. Context: (dict "root" $ "team" <team>
"role" <role>).

Extracted so the ScaledJob and the pod template cannot disagree. They did, and
it broke production: the hazard label was stamped only on the pod TEMPLATE,
but Kyverno autogens a Job rule for pod-security-baseline-enforce and a Job
selector matches the JOB's own labels — so every DinD team was denied at
admission while the PolicyException looked correct.
*/}}
{{- define "ploeg.roleUsesDind" -}}
{{- $gh := .root.Values.executor.harness | default dict }}
{{- $th := .team.harness | default dict }}
{{- $rh := (.role | default dict).harness | default dict }}
{{- $d := false }}
{{- if hasKey $rh "dind" }}{{- $d = $rh.dind }}{{- else if hasKey $th "dind" }}{{- $d = $th.dind }}{{- else if hasKey $gh "dind" }}{{- $d = $gh.dind }}{{- end }}
{{- if $d }}
{{- $acknowledged := false }}
{{- if hasKey $rh "acknowledgePrivilegedDind" }}{{- $acknowledged = $rh.acknowledgePrivilegedDind }}{{- else if hasKey $th "acknowledgePrivilegedDind" }}{{- $acknowledged = $th.acknowledgePrivilegedDind }}{{- else if hasKey $gh "acknowledgePrivilegedDind" }}{{- $acknowledged = $gh.acknowledgePrivilegedDind }}{{- end }}
{{- if not $acknowledged }}
{{- $who := .team.name }}{{- with (.role | default dict).name }}{{- $who = printf "%s/%s" $who . }}{{- end }}
{{- fail (printf "team %s: harness.dind is true without harness.acknowledgePrivilegedDind: true. With DinD the harness container receives the client certificates of a privileged Docker daemon (DOCKER_HOST, DOCKER_CERT_PATH, /certs), which is root on the node, and the chart turns both credential proxies off for it (PLOEG_CREDENTIAL_ISOLATION_OFF_REASON=dind) unless harness.credentialIsolation is proxy, so the harness also receives the real per-Run model key in LLM_API_KEY and, when it writes, the real forge token in AGENT_BUILDER_TOKEN. Set harness.acknowledgePrivilegedDind: true in the same block to accept this, or keep harness.dind: false (the default) for the strict profile" $who) }}
{{- end -}}
true
{{- end }}
{{- end -}}

{{- define "ploeg.goMemLimitEnv" -}}
{{- $set := toString (.goMemLimit | default "") -}}
{{- if and (ne $set "off") (not (hasKey (.env | default dict) "GOMEMLIMIT")) -}}
{{- if $set }}
- name: GOMEMLIMIT
  value: {{ $set | quote }}
{{- else -}}
{{- with ((.resources | default dict).limits | default dict).memory }}
- name: GOMEMLIMIT
  value: {{ include "ploeg.ninetyPercentMiB" . | quote }}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.ninetyPercentMiB" -}}
{{- $quantity := toString . -}}
{{- $units := dict "" 1.0 "k" 1000.0 "M" 1000000.0 "G" 1000000000.0 "T" 1000000000000.0 "Ki" 1024.0 "Mi" 1048576.0 "Gi" 1073741824.0 "Ti" 1099511627776.0 -}}
{{- $number := regexFind "^[0-9]+(\\.[0-9]+)?" $quantity -}}
{{- $suffix := trimPrefix $number $quantity -}}
{{- if or (not $number) (not (hasKey $units $suffix)) -}}
{{- fail (printf "cannot derive GOMEMLIMIT from the memory limit %q: set the goMemLimit value next to it" $quantity) -}}
{{- end -}}
{{- printf "%dMiB" (int64 (floor (divf (mulf (float64 $number) (get $units $suffix) 0.9) 1048576.0))) -}}
{{- end -}}
