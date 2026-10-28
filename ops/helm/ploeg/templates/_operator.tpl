{{- define "ploeg.consumersActingForAnyPerson" -}}
{{- $names := list -}}
{{- range .Values.operator.consumers -}}
{{- if and .execute (or .clientIds .audiences) (not .requireClaims) -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- end -}}
{{- toJson $names -}}
{{- end -}}

{{- define "ploeg.personClaimsRequiredByP1" -}}
{{- if semverCompare ">=0.3.0-0" .Chart.Version -}}true{{- end -}}
{{- end -}}

{{- define "ploeg.operatorAttribution" -}}
{{- if .Values.operator.attribution -}}
{{- .Values.operator.attribution -}}
{{- else if hasKey .Values.env "PLOEG_OPERATOR_ATTRIBUTION" -}}
{{- toString (index .Values.env "PLOEG_OPERATOR_ATTRIBUTION") | default "asserted" -}}
{{- else if semverCompare ">=0.3.0-0" .Chart.Version -}}
proven
{{- else -}}
asserted
{{- end -}}
{{- end -}}

{{- define "ploeg.operatorControlEnv" -}}
{{- $consumers := list -}}
{{- $names := dict -}}
{{- $clientIds := false -}}
{{- if hasKey .Values.env "PLOEG_OPERATOR_CONSUMERS" -}}
{{- fail "operator.consumers and env.PLOEG_OPERATOR_CONSUMERS cannot both be configured" -}}
{{- end -}}
{{- range .Values.operator.consumers -}}
{{- if hasKey $names .name -}}{{- fail "operator consumer names must be unique" -}}{{- end -}}
{{- $_ := set $names .name true -}}
{{- $tokenEnv := printf "PLOEG_OPERATOR_TOKEN_%s" (.name | sha256sum | upper) -}}
{{- $consumer := dict "name" .name "tokenEnv" $tokenEnv "execute" (.execute | default false) "verify" (.verify | default false) "teams" .teams -}}
{{- $_ := set $consumer "readContent" (ternary .readContent (.execute | default false) (hasKey . "readContent")) -}}
{{- if hasKey . "maxBudgetUsd" -}}{{- $_ := set $consumer "maxBudgetUsd" .maxBudgetUsd -}}{{- end -}}
{{- if .clientIds -}}{{- $_ := set $consumer "clientIds" .clientIds -}}{{- $clientIds = true -}}{{- end -}}
{{- if .audiences -}}{{- $_ := set $consumer "audiences" .audiences -}}{{- end -}}
{{- if .requireClaims -}}{{- $_ := set $consumer "requireClaims" .requireClaims -}}{{- end -}}
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
{{- if not .issuer -}}
{{- fail "operator.personTokens needs issuer" -}}
{{- end }}
{{- if and $clientIds (not .audience) -}}
{{- fail "operator.personTokens needs audience when a consumer names clientIds" -}}
{{- end }}
- name: PLOEG_OPERATOR_PERSON_ISSUER
  value: {{ .issuer | quote }}
{{- if .audience }}
- name: PLOEG_OPERATOR_PERSON_AUDIENCE
  value: {{ .audience | quote }}
{{- end }}
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
{{- $attribution := include "ploeg.operatorAttribution" . -}}
{{- if and .Values.operator.attribution (hasKey .Values.env "PLOEG_OPERATOR_ATTRIBUTION") -}}
{{- fail "operator.attribution and env.PLOEG_OPERATOR_ATTRIBUTION cannot both be configured" -}}
{{- end -}}
{{- if and (eq $attribution "proven") .Values.operator.consumers (not .Values.operator.personTokens) (not (hasKey .Values.env "PLOEG_OPERATOR_PERSON_ISSUER")) -}}
{{- fail "operator.attribution resolves to proven, which accepts an operator change only with a person token, and operator.personTokens is empty; configure operator.personTokens and each consumer's audiences or clientIds (ADR-0150), or set operator.attribution: asserted to keep header attribution (ADR-0171, threat model F-07)" -}}
{{- end -}}
{{- if and (not (hasKey .Values.env "PLOEG_OPERATOR_ATTRIBUTION")) (or .Values.operator.attribution (eq $attribution "proven")) }}
- name: PLOEG_OPERATOR_ATTRIBUTION
  value: {{ $attribution | quote }}
{{- end }}
{{- if .Values.operator.deliveryPolicies }}
{{- if hasKey .Values.env "PLOEG_OPERATOR_DELIVERY_POLICIES" -}}
{{- fail "operator.deliveryPolicies and env.PLOEG_OPERATOR_DELIVERY_POLICIES cannot both be configured" -}}
{{- end }}
- name: PLOEG_OPERATOR_DELIVERY_POLICIES
  value: {{ .Values.operator.deliveryPolicies | toJson | quote }}
{{- end }}
{{- end -}}
