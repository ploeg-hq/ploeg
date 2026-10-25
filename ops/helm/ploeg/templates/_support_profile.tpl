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
{{- end -}}
{{- if $e.enabled -}}
{{- if eq ($e.type | default "keda") "sandbox" -}}
{{- $out = append $out "executor.type is sandbox, which is experimental; P1 needs keda or cronjob" -}}
{{- end -}}
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
{{- if and $team.executorType (eq (include "ploeg.teamExecutorType" (dict "root" $root "team" $team)) "sandbox") -}}
{{- $out = append $out (printf "team %s: executorType is sandbox, which is experimental; P1 needs keda or cronjob" $team.name) -}}
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
