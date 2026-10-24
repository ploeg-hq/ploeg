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
{{- if .clientIds -}}{{- $_ := set $consumer "clientIds" .clientIds -}}{{- end -}}
{{- $consumers = append $consumers $consumer }}
- name: {{ $tokenEnv }}
  valueFrom:
    secretKeyRef:
      name: {{ .tokenSecret.name | quote }}
      key: {{ .tokenSecret.key | quote }}
{{- end }}
- name: PLOEG_OPERATOR_CONSUMERS
  value: {{ $consumers | toJson | quote }}
{{- with .Values.operator.personTokens }}
{{- range $name := list "PLOEG_OPERATOR_PERSON_ISSUER" "PLOEG_OPERATOR_PERSON_AUDIENCE" "PLOEG_OPERATOR_PERSON_JWKS_URL" "PLOEG_OPERATOR_PERSON_MAX_LIFETIME" "PLOEG_OPERATOR_PERSON_SUBJECT_NAMESPACE" }}
{{- if hasKey $.Values.env $name -}}
{{- fail (printf "operator.personTokens and env.%s cannot both be configured" $name) -}}
{{- end -}}
{{- end }}
{{- if or (not .issuer) (not .audience) -}}
{{- fail "operator.personTokens needs issuer and audience" -}}
{{- end }}
- name: PLOEG_OPERATOR_PERSON_ISSUER
  value: {{ .issuer | quote }}
- name: PLOEG_OPERATOR_PERSON_AUDIENCE
  value: {{ .audience | quote }}
{{- if .jwksUrl }}
- name: PLOEG_OPERATOR_PERSON_JWKS_URL
  value: {{ .jwksUrl | quote }}
{{- end }}
{{- if .maxLifetime }}
- name: PLOEG_OPERATOR_PERSON_MAX_LIFETIME
  value: {{ .maxLifetime | quote }}
{{- end }}
{{- if .subjectNamespace }}
- name: PLOEG_OPERATOR_PERSON_SUBJECT_NAMESPACE
  value: {{ .subjectNamespace | quote }}
{{- end }}
{{- end }}
{{- if .Values.operator.deliveryPolicies }}
{{- if hasKey .Values.env "PLOEG_OPERATOR_DELIVERY_POLICIES" -}}
{{- fail "operator.deliveryPolicies and env.PLOEG_OPERATOR_DELIVERY_POLICIES cannot both be configured" -}}
{{- end }}
- name: PLOEG_OPERATOR_DELIVERY_POLICIES
  value: {{ .Values.operator.deliveryPolicies | toJson | quote }}
{{- end }}
{{- end -}}
