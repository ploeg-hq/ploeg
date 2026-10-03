{{- define "ploeg.operatorControlEnv" -}}
{{- $consumers := list -}}
{{- $names := dict -}}
{{- if hasKey .Values.env "PLOEG_OPERATOR_CONSUMERS" -}}
{{- fail "operator.consumers and env.PLOEG_OPERATOR_CONSUMERS cannot both be configured" -}}
{{- end -}}
{{- range .Values.operator.consumers -}}
{{- if hasKey $names .name -}}{{- fail "operator consumer names must be unique" -}}{{- end -}}
{{- $_ := set $names .name true -}}
{{- $tokenEnv := printf "PLOEG_OPERATOR_TOKEN_%s" (.name | sha256sum | upper) -}}
{{- $consumer := dict "name" .name "tokenEnv" $tokenEnv "execute" (.execute | default false) "verify" (.verify | default false) -}}
{{- if hasKey . "teams" -}}{{- $_ := set $consumer "teams" .teams -}}{{- end -}}
{{- if hasKey . "maxBudgetUsd" -}}{{- $_ := set $consumer "maxBudgetUsd" .maxBudgetUsd -}}{{- end -}}
{{- $consumers = append $consumers $consumer }}
- name: {{ $tokenEnv }}
  valueFrom:
    secretKeyRef:
      name: {{ .tokenSecret.name | quote }}
      key: {{ .tokenSecret.key | quote }}
{{- end }}
- name: PLOEG_OPERATOR_CONSUMERS
  value: {{ $consumers | toJson | quote }}
{{- if .Values.operator.deliveryPolicies }}
{{- if hasKey .Values.env "PLOEG_OPERATOR_DELIVERY_POLICIES" -}}
{{- fail "operator.deliveryPolicies and env.PLOEG_OPERATOR_DELIVERY_POLICIES cannot both be configured" -}}
{{- end }}
- name: PLOEG_OPERATOR_DELIVERY_POLICIES
  value: {{ .Values.operator.deliveryPolicies | toJson | quote }}
{{- end }}
{{- end -}}
