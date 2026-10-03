{{- define "ploeg.workerControlEnv" -}}
- name: PLOEG_WORKER_AUTH_MODE
  value: {{ .Values.executor.workerAuth.mode | quote }}
{{- if eq .Values.executor.workerAuth.mode "managed" }}
- name: PLOEG_WORKER_SIGNING_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.executor.workerAuth.signingKeySecret.name }}
      key: {{ .Values.executor.workerAuth.signingKeySecret.key }}
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
{{- $policy := dict "team" $team.name "role" ($role.name | default "") "models" (list $model) "budgetUsd" ($role.cap | default $team.perRunBudget | default $team.budget | float64) "ttl" $root.Values.executor.litellm.keyDuration }}
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
