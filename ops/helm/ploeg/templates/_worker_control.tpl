{{- define "ploeg.workerControlEnv" -}}
- name: PLOEG_WORKER_AUTH_MODE
  value: {{ .Values.executor.workerAuth.mode | quote }}
{{- if eq .Values.executor.workerAuth.mode "managed" }}
{{- $hmac := .Values.executor.workerAuth.hmacCapability | default "issue" }}
{{- if eq $hmac "off" }}
{{- if not (dig "signingKeySecret" "" (.Values.transactionToken | default dict)) }}
{{- fail "executor.workerAuth.hmacCapability off needs transactionToken.signingKeySecret: workers would get no credential (ADR-0156)" }}
{{- end }}
- name: PLOEG_WORKER_HMAC_CAPABILITY
  value: "off"
{{- else }}
- name: PLOEG_WORKER_SIGNING_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.executor.workerAuth.signingKeySecret.name }}
      key: {{ .Values.executor.workerAuth.signingKeySecret.key }}
{{- end }}
- name: PLOEG_WORKER_BOOTSTRAPS
  valueFrom:
    secretKeyRef:
      name: {{ .Values.executor.workerAuth.bootstrapSecret.name }}
      key: {{ .Values.executor.workerAuth.bootstrapSecret.registryKey }}
{{- $root := . }}
{{- $policies := list }}
{{- range $team := .Values.executor.teams }}
{{- range $role := (include "ploeg.teamRoles" $team | fromJsonArray) }}
{{- $model := $role.model | default $team.model }}
{{- $model = regexReplaceAll "^.*/" $model "" }}
{{- $models := list }}
{{- range ($role.models | default (list $model)) }}
{{- $models = append $models (regexReplaceAll "^.*/" . "") }}
{{- end }}
{{- if not (has $model $models) }}
{{- fail (printf "team %s: role %q lists models without its default model %q (ADR-0103)" $team.name $role.name $model) }}
{{- end }}
{{- $policy := dict "team" $team.name "role" ($role.name | default "") "models" $models "defaultModel" $model "budgetUsd" ($role.cap | default $team.perRunBudget | default $team.budget | float64) "ttl" $root.Values.executor.litellm.keyDuration }}
{{- /* ADR-0078: opt-in, read-only MCP tools through the LiteLLM gateway. The
     LiteLLM team bounds the access groups, so groups without a team fail. */}}
{{- if and $team.mcpAccessGroups (not $team.litellmTeamId) }}
{{- fail (printf "executor.teams[%s].mcpAccessGroups needs executor.teams[%s].litellmTeamId: the LiteLLM team bounds the groups (ADR-0078)" $team.name $team.name) }}
{{- end }}
{{- with $team.litellmTeamId }}
{{- $_ := set $policy "litellmTeamId" . }}
{{- end }}
{{- with $team.mcpAccessGroups }}
{{- $_ := set $policy "mcpAccessGroups" . }}
{{- end }}
{{- $policies = append $policies $policy }}
{{- end }}
{{- end }}
{{- range .Values.executor.workerAuth.additionalLLMPolicies }}
{{- $policies = append $policies . }}
{{- end }}
- name: PLOEG_WORKER_LLM_POLICIES
  value: {{ $policies | toJson | quote }}
{{- end }}
{{- end -}}
