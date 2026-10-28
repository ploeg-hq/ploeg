{{- define "ploeg.executorNamespace" -}}
{{- .Values.executor.namespace | default .Release.Namespace -}}
{{- end -}}

{{- define "ploeg.executorNamespaceSeparate" -}}
{{- if ne (include "ploeg.executorNamespace" .) .Release.Namespace -}}true{{- end -}}
{{- end -}}

{{- define "ploeg.executorNamespaceMetadata" -}}
{{- if include "ploeg.executorNamespaceSeparate" . -}}
namespace: {{ include "ploeg.executorNamespace" . }}
{{- end -}}
{{- end -}}

{{- define "ploeg.workerApiUrl" -}}
{{- if or .Values.executor.apiUrl (not (include "ploeg.executorNamespaceSeparate" .)) -}}
{{ include "ploeg.apiUrl" . }}
{{- else -}}
{{ printf "http://%s.%s.svc:%v" (include "ploeg.fullname" .) .Release.Namespace .Values.service.port }}
{{- end -}}
{{- end -}}

{{- define "ploeg.executorNamespaceShared" -}}
{{- $e := .Values.executor -}}
{{- if and $e.enabled (not (include "ploeg.executorNamespaceSeparate" .)) (not $e.acknowledgeSharedNamespace) -}}
executor.namespace is empty or names the release namespace, so the worker workloads share namespace {{ .Release.Namespace }} with ploegd and its Secrets, one Pod Security level and one namespace for RBAC; P1 needs them in a namespace of their own, or executor.acknowledgeSharedNamespace: true (F-44)
{{- end -}}
{{- end -}}

{{- define "ploeg.executorNamespaceRequired" -}}
{{- if semverCompare ">=0.3.0-0" .Chart.Version -}}true{{- end -}}
{{- end -}}
