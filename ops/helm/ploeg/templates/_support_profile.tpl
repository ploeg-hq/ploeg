{{- define "ploeg.securityContextDepartures" -}}
{{- $c := .context | default dict -}}
{{- $out := list -}}
{{- if ne (toString $c.runAsNonRoot) "true" -}}{{- $out = append $out "runAsNonRoot is not true" -}}{{- end -}}
{{- if and (hasKey $c "runAsUser") (eq (int $c.runAsUser) 0) -}}{{- $out = append $out "runAsUser is 0" -}}{{- end -}}
{{- if ne (toString $c.allowPrivilegeEscalation) "false" -}}{{- $out = append $out "allowPrivilegeEscalation is not false" -}}{{- end -}}
{{- if $c.privileged -}}{{- $out = append $out "privileged is true" -}}{{- end -}}
{{- $caps := $c.capabilities | default dict -}}
{{- if not (has "ALL" ($caps.drop | default list)) -}}{{- $out = append $out "capabilities.drop lacks ALL" -}}{{- end -}}
{{- if $caps.add -}}{{- $out = append $out "capabilities.add is set" -}}{{- end -}}
{{- if not (has (($c.seccompProfile | default dict).type | default "") (list "RuntimeDefault" "Localhost")) -}}{{- $out = append $out "seccompProfile.type is not RuntimeDefault or Localhost" -}}{{- end -}}
{{- if .readOnlyRoot -}}
{{- if ne (toString $c.readOnlyRootFilesystem) "true" -}}{{- $out = append $out "readOnlyRootFilesystem is not true" -}}{{- end -}}
{{- end -}}
{{- if $out -}}
{{- printf "%s (%s); P1 needs the restricted Pod Security Standard on it (ADR-0120)" .path (join ", " $out) -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.unpinnedImageDeparture" -}}
{{- if and .source.image (not (regexMatch "@sha256:[a-f0-9]{64}$" (toString .source.image))) -}}
{{- $scope := "" -}}
{{- with .label -}}{{- $scope = printf "%s: " . -}}{{- end -}}
{{- printf "%s%s image %s (%s) is not pinned by digest; P1 needs every image the worker pod runs as <reference>@sha256:<digest>" $scope .what .source.image .source.path -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.workloadImageDepartures" -}}
{{- $root := .root -}}
{{- $label := .label -}}
{{- $out := list -}}
{{- $roleScope := dict "root" $root "team" .team "role" .role -}}
{{- with include "ploeg.unpinnedImageDeparture" (dict "label" $label "what" "the harness" "source" (include "ploeg.roleHarnessImage" $roleScope | fromJson)) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- $toolchains := include "ploeg.roleToolchains" $roleScope | fromJson -}}
{{- range $tc := $toolchains.toolchains -}}
{{- with include "ploeg.unpinnedImageDeparture" (dict "label" $label "what" (printf "the %s toolchain" $tc.name) "source" (dict "image" $tc.image "path" (printf "%s[%s].image" $toolchains.path $tc.name))) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- end -}}
{{- if .separate -}}
{{- with include "ploeg.unpinnedImageDeparture" (dict "what" "the worker" "source" (include "ploeg.workerImageSource" $root | fromJson)) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- else -}}
{{- with include "ploeg.unpinnedImageDeparture" (dict "what" "the worker-bin" "source" (include "ploeg.workerBinImageSource" (dict "root" $root "separate" false) | fromJson)) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- end -}}
{{- if .dind -}}
{{- with include "ploeg.unpinnedImageDeparture" (dict "what" "the dind" "source" (dict "image" $root.Values.executor.dindImage "path" "executor.dindImage")) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- end -}}
{{- if .sandbox -}}
{{- with include "ploeg.unpinnedImageDeparture" (dict "what" "the sandbox launcher" "source" (include "ploeg.ploegdWorkerImageSource" $root | fromJson)) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{- define "ploeg.workloadSupportProfileDepartures" -}}
{{- $root := .root -}}
{{- $team := .team -}}
{{- $role := .role | default dict -}}
{{- $gh := $root.Values.executor.harness | default dict -}}
{{- $th := $team.harness | default dict -}}
{{- $rh := $role.harness | default dict -}}
{{- $who := $team.name -}}
{{- if $role.name -}}{{- $who = printf "%s/%s" $team.name $role.name -}}{{- end -}}
{{- $identity := include "ploeg.roleHarness" (dict "root" $root "team" $team "role" $role) | fromJson -}}
{{- $key := include "ploeg.qualificationHarnessName" (dict "name" $identity.name "acpProfile" $identity.acpProfile) -}}
{{- $label := printf "team %s harness %s" $who $key -}}
{{- $dind := eq (include "ploeg.roleUsesDind" (dict "root" $root "team" $team "role" $role)) "true" -}}
{{- $override := dict -}}
{{- if hasKey $rh "credentialIsolation" -}}{{- $override = dict "value" $rh.credentialIsolation -}}{{- else if hasKey $th "credentialIsolation" -}}{{- $override = dict "value" $th.credentialIsolation -}}{{- else if hasKey $gh "credentialIsolation" -}}{{- $override = dict "value" $gh.credentialIsolation -}}{{- end -}}
{{- $isolation := include "ploeg.credentialIsolation" (dict "root" $root "harness" $key "dind" $dind "override" $override) | fromJson -}}
{{- $layoutOverride := dict -}}
{{- if hasKey $rh "harnessContainer" -}}{{- $layoutOverride = dict "value" $rh.harnessContainer -}}{{- else if hasKey $th "harnessContainer" -}}{{- $layoutOverride = dict "value" $th.harnessContainer -}}{{- end -}}
{{- $layout := include "ploeg.harnessContainer" (dict "root" $root "name" $identity.name "dind" $dind "isolation" $isolation "override" $layoutOverride "who" $who) -}}
{{- $out := list -}}
{{- $sandbox := eq (include "ploeg.teamExecutorType" (dict "root" $root "team" $team)) "sandbox" -}}
{{- if and $sandbox (not (include "ploeg.runtimeClassName" (dict "root" $root "team" $team "role" $role))) -}}
{{- $out = append $out (printf "team %s: the sandbox executor resolves no runtimeClassName; P1 runs it only under a runtime class that gives each pod its own VM, such as Kata Containers" $who) -}}
{{- end -}}
{{- if $dind -}}
{{- $out = append $out (printf "%s: harness.dind is true; P1 needs dind: false on every team and Role" $label) -}}
{{- end -}}
{{- if not (has $key (include "ploeg.isolationQualifiedHarnesses" $root | fromJsonArray)) -}}
{{- $out = append $out (printf "%s: the harness is not qualified with both credential proxies on; P1 allows %s" $label (join ", " (include "ploeg.isolationQualifiedHarnesses" $root | fromJsonArray))) -}}
{{- end -}}
{{- if hasKey $override "value" -}}
{{- $out = append $out (printf "%s: harness.credentialIsolation is set to %q; P1 has no credentialIsolation override" $label (toString $override.value)) -}}
{{- end -}}
{{- if and (eq $layout "shared") (not $dind) (not $isolation.offReason) (ne $identity.name "claude-environment") -}}
{{- $out = append $out (printf "%s: harnessContainer resolves to shared; P1 needs separate (ADR-0138)" $label) -}}
{{- end -}}
{{- $out = concat $out (include "ploeg.workloadImageDepartures" (dict "root" $root "team" $team "role" $role "label" $label "separate" (eq $layout "separate") "dind" $dind "sandbox" $sandbox) | fromJsonArray) -}}
{{- toJson $out -}}
{{- end -}}

{{- define "ploeg.supportProfileDepartures" -}}
{{- $root := . -}}
{{- $e := .Values.executor -}}
{{- $out := list -}}
{{- if not .Values.networkPolicy.enabled -}}
{{- $out = append $out "networkPolicy.enabled is false; P1 needs the chart's default-deny NetworkPolicies" -}}
{{- end -}}
{{- if eq (($e.workerAuth | default dict).mode | default "managed") "legacy" -}}
{{- $out = append $out "executor.workerAuth.mode is legacy; P1 needs managed" -}}
{{- end -}}
{{- if .Values.claudeRunner.enabled -}}
{{- $out = append $out "claudeRunner.enabled is true; P1 excludes the claude-runner, whose Runs execute outside the pod" -}}
{{- with include "ploeg.unpinnedImageDeparture" (dict "what" "the claude-runner" "source" (dict "image" (include "ploeg.claudeRunnerImage" $root) "path" "claudeRunner.image")) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- end -}}
{{- if $e.enabled -}}
{{- if ne (($e.litellm | default dict).keyIsolation | default "") "proxy" -}}
{{- $out = append $out "executor.litellm.keyIsolation is not proxy; P1 needs proxy" -}}
{{- end -}}
{{- if ne ($e.forgeTokenIsolation | default "") "proxy" -}}
{{- $out = append $out "executor.forgeTokenIsolation is not proxy; P1 needs proxy" -}}
{{- end -}}
{{- with include "ploeg.securityContextDepartures" (dict "path" "executor.workerSecurityContext" "context" $e.workerSecurityContext "readOnlyRoot" false) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- with include "ploeg.securityContextDepartures" (dict "path" "the worker container's securityContext (executor.workerContainerSecurityContext)" "context" (include "ploeg.workerContainerSecurityContext" $root | fromYaml) "readOnlyRoot" true) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- range $team := $e.teams -}}
{{- $cap := include "ploeg.teamSpendCap" (dict "root" $root "team" $team) | fromJson -}}
{{- if not (gt ($cap.usd | float64) 0.0) -}}
{{- $out = append $out (printf "team %s resolves no spend cap (executor.spendCap, executor.teams[].spendCap or config.teams.%s.spendCap); P1 needs a finite cap per period on every executor team (ADR-0162)" $team.name $team.name) -}}
{{- end -}}
{{- range $planRole := (include "ploeg.teamRoles" $team | fromJsonArray) -}}
{{- range $w := (include "ploeg.roleWorkloads" (dict "root" $root "team" $team "role" $planRole) | fromJsonArray) -}}
{{- $out = concat $out (include "ploeg.workloadSupportProfileDepartures" (dict "root" $root "team" $team "role" $w.role) | fromJsonArray) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toJson ($out | uniq) -}}
{{- end -}}

{{- define "ploeg.requireSupportProfile" -}}
{{- $profile := .Values.supportProfile | default "P1" -}}
{{- if not (has $profile (list "P1" "unsupported")) -}}
{{- fail (printf "supportProfile must be P1 or unsupported, not %q" $profile) -}}
{{- end -}}
{{- $departures := include "ploeg.supportProfileDepartures" . | fromJsonArray -}}
{{- if and (eq $profile "P1") $departures -}}
{{- fail (printf "supportProfile is P1, but these values leave profile P1, the only deployment the threat model covers (docs/security/threat-model.md, finding F-11): %s. Return each setting to its P1 value, or set supportProfile: unsupported to run outside the supported profile on your own assessment (ADR-0154)" (join "; " $departures)) -}}
{{- end -}}
{{- end -}}
